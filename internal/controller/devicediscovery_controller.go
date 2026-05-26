/*
2024 NVIDIA CORPORATION & AFFILIATES
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/devicediscovery"
	"github.com/Mellanox/nic-configuration-operator/pkg/helper"
	"github.com/Mellanox/nic-configuration-operator/pkg/host"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

var deviceDiscoveryReconcileTime = time.Minute * 5

// DeviceDiscoveryController periodically reconciles devices on the host, creates CRs for new devices,
// deletes CRs for absent devices, updates the CR when device's status has changed.
type DeviceDiscoveryController struct {
	client.Client

	deviceDiscovery devicediscovery.DeviceDiscovery
	hostUtils       host.HostUtils
	nodeName        string
	namespace       string
}

// crNameSanitizer replaces PCI-address punctuation (:, .) with DNS-1123-safe dashes.
var crNameSanitizer = strings.NewReplacer(":", "-", ".", "-")

// getCRName constructs a unique CR name based on the node name, device's type, and PCI
// device address (Domain:Bus:Device). The `:` and `.` characters in the PCI address are
// replaced with `-` so the resulting name is DNS-1123-compliant.
// Example: node=co-node-25, deviceType=101b, pciDeviceAddr=0000:04:00 → co-node-25-101b-0000-04-00.
func (d *DeviceDiscoveryController) getCRName(deviceType string, pciDeviceAddr string) string {
	return strings.ToLower(d.nodeName + "-" + deviceType + "-" + crNameSanitizer.Replace(pciDeviceAddr))
}

func setInitialsConditionsForDevice(device *v1alpha1.NicDevice) {
	condition := metav1.Condition{
		Type:    consts.ConfigUpdateInProgressCondition,
		Status:  metav1.ConditionFalse,
		Reason:  consts.DeviceConfigSpecEmptyReason,
		Message: "Device configuration spec is empty, cannot update configuration",
	}
	meta.SetStatusCondition(&device.Status.Conditions, condition)

	condition = metav1.Condition{
		Type:    consts.FirmwareUpdateInProgressCondition,
		Status:  metav1.ConditionFalse,
		Reason:  consts.DeviceFirmwareSpecEmptyReason,
		Message: "Device firmware spec is empty, cannot update firmware",
	}
	meta.SetStatusCondition(&device.Status.Conditions, condition)
}

func setFwConfigConditionsForDevice(device *v1alpha1.NicDevice, recommendedFirmware string) bool {
	currentFirmware := device.Status.FirmwareVersion
	log.Log.V(2).Info("setFwConfigConditionsForDevice()", "recommendedFirmware", recommendedFirmware, "currentFirmware", currentFirmware)
	var condition metav1.Condition
	switch recommendedFirmware {
	case currentFirmware:
		condition = metav1.Condition{
			Type:    consts.FirmwareConfigMatchCondition,
			Status:  metav1.ConditionTrue,
			Reason:  consts.DeviceFwMatchReason,
			Message: fmt.Sprintf("Device firmware '%s' matches to recommended version '%s'", currentFirmware, recommendedFirmware),
		}
	case "":
		condition = metav1.Condition{
			Type:    consts.FirmwareConfigMatchCondition,
			Status:  metav1.ConditionUnknown,
			Reason:  consts.DeviceFwMatchReason,
			Message: "Can't get OFED version to check recommended firmware version",
		}
	default:
		condition = metav1.Condition{
			Type:    consts.FirmwareConfigMatchCondition,
			Status:  metav1.ConditionFalse,
			Reason:  consts.DeviceFwMismatchReason,
			Message: fmt.Sprintf("Device firmware '%s' doesn't match to recommended version '%s'", currentFirmware, recommendedFirmware),
		}
	}
	return meta.SetStatusCondition(&device.Status.Conditions, condition)
}

// reconcile reconciles the devices on the host by comparing the observed devices with the existing NicDevice custom resources (CRs).
// It deletes CRs that do not represent observed devices, updates the CRs if the status of the device changes,
// and creates new CRs for devices that do not have a CR representation.
//
// Per-device errors (Create/Update/Delete/SetOwnerReference on individual CRs) are always logged.
// If perDeviceErrs is non-nil, they are also appended to it so callers can surface them — used by
// --discovery-only mode where a single best-effort pass must report incomplete CR state. When nil,
// per-device errors are swallowed (preserves the periodic reconcile loop's tolerance for transient
// failures between ticks).
func (d *DeviceDiscoveryController) reconcile(ctx context.Context, perDeviceErrs *[]error) error {
	trackErr := func(err error, what string, name string) {
		log.Log.Error(err, what, "device", name)
		if perDeviceErrs != nil {
			*perDeviceErrs = append(*perDeviceErrs, fmt.Errorf("%s %s: %w", what, name, err))
		}
	}

	observedDevices, err := d.deviceDiscovery.DiscoverNicDevices()
	if err != nil {
		return err
	}

	list := &v1alpha1.NicDeviceList{}

	selectorFields := fields.OneTermEqualSelector("status.node", d.nodeName)

	err = d.List(ctx, list, &client.ListOptions{FieldSelector: selectorFields})
	if err != nil {
		log.Log.Error(err, "failed to list NicDevice CRs")
		return err
	}

	log.Log.V(2).Info("listed devices", "devices", list.Items)

	node := &v1.Node{}
	err = d.Get(ctx, types.NamespacedName{Name: d.nodeName}, node)
	if err != nil {
		log.Log.Error(err, "failed to get node object")
		return err
	}

	for _, nicDeviceCR := range list.Items {
		if len(nicDeviceCR.Status.Ports) == 0 {
			log.Log.V(2).Info("NicDevice CR has no ports, skipping reconciliation", "device", nicDeviceCR.Name)
			continue
		}
		pciKey := utils.PCIDeviceAddress(nicDeviceCR.Status.Ports[0].PCI)
		observedDevice, exists := observedDevices[pciKey]

		if !exists {
			log.Log.V(2).Info("device doesn't exist on the node anymore, deleting", "device", nicDeviceCR.Name)
			// Need to delete this CR, it doesn't represent the observedDevice on host anymore
			if err := d.Delete(ctx, &nicDeviceCR); err != nil {
				trackErr(err, "failed to delete NicDevice CR", nicDeviceCR.Name)
			}

			continue
		}

		observedDeviceStatus := observedDevice.Status

		if changed := d.updateFwCondition(&nicDeviceCR); changed {
			log.Log.V(2).Info("FirmwareConfigMatch condition changed, updating device", "nicDeviceCR", nicDeviceCR)
			if err := d.Client.Status().Update(ctx, &nicDeviceCR); err != nil {
				trackErr(err, "failed to update FirmwareConfigMatchCondition", nicDeviceCR.Name)
				continue
			}
		}

		// Need to nullify conditions for deep equal
		observedDeviceStatus.Conditions = nicDeviceCR.Status.Conditions

		if !reflect.DeepEqual(nicDeviceCR.Status, observedDeviceStatus) {
			log.Log.V(2).Info("device status changed, updating", "device", nicDeviceCR.Name, "crStatus", nicDeviceCR.Status, "observedStatus", observedDeviceStatus)
			// Status of the device changes, need to update the CR
			nicDeviceCR.Status = observedDeviceStatus

			if err := d.Client.Status().Update(ctx, &nicDeviceCR); err != nil {
				trackErr(err, "failed to update NicDevice CR status", nicDeviceCR.Name)
			}
		}

		// Device was processed, cleaning it from the map
		delete(observedDevices, pciKey)
	}

	// Remaining devices don't have CR representation, need to create a CR
	for pciKey, observedDevice := range observedDevices {
		deviceStatus := observedDevice.Status
		deviceName := d.getCRName(deviceStatus.Type, pciKey)
		device := &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deviceName,
				Namespace: d.namespace,
			},
		}

		if err := controllerutil.SetOwnerReference(node, device, d.Scheme()); err != nil {
			trackErr(err, "failed to set owner reference for device", device.Name)
			continue
		}
		err := d.Create(ctx, device)
		if apierrors.IsAlreadyExists(err) {
			// Device already exists but was not matched by PCI address, which means the status was not applied properly
			// on a previous reconcile. Re-fetch so the status update below runs against the live object.
			if err := d.Get(ctx, types.NamespacedName{Name: device.Name, Namespace: device.Namespace}, device); err != nil {
				trackErr(err, "failed to get existing NicDevice obj", device.Name)
				continue
			}
		} else if err != nil {
			trackErr(err, "failed to create NicDevice obj", device.Name)
			continue
		}

		device.Status = deviceStatus
		device.Status.Node = d.nodeName
		setInitialsConditionsForDevice(device)

		d.updateFwCondition(device)
		log.Log.V(2).Info("updated device", "device", device)
		if err := d.Client.Status().Update(ctx, device); err != nil {
			trackErr(err, "failed to update NicDevice CR status", device.Name)
			continue
		}
	}
	return nil
}

// updateFwCondition updates the FirmwareConfigMatch status condition
// returns bool - if condition changed and needs to be updated
func (d *DeviceDiscoveryController) updateFwCondition(nicDeviceCR *v1alpha1.NicDevice) bool {
	ofedVersion := d.hostUtils.DiscoverOfedVersion()
	recommendedFirmware := helper.GetRecommendedFwVersion(nicDeviceCR.Status.Type, ofedVersion)
	return setFwConfigConditionsForDevice(nicDeviceCR, recommendedFirmware)
}

// Start starts the device discovery process by reconciling devices on the host.
//
// It triggers the first reconciliation manually and then runs it periodically based on the
// deviceDiscoveryReconcileTime interval until the context is done.
func (d *DeviceDiscoveryController) Start(ctx context.Context) error {
	log.Log.Info("Device discovery started")

	t := time.NewTicker(deviceDiscoveryReconcileTime)
	defer t.Stop()

	retryChan := make(chan struct{}, 1) // Channel to trigger immediate retries

	runReconcile := func() {
		// Periodic mode tolerates per-device errors between ticks; pass nil so they're only logged.
		err := d.reconcile(ctx, nil)
		if err != nil {
			log.Log.Error(err, "failed to run reconcile, requeueing")
			// Retry the request if there's an error
			retryChan <- struct{}{}
		}
	}

	runReconcile()

OUTER:
	for {
		select {
		case <-ctx.Done():
			break OUTER
		case <-t.C:
			runReconcile()
		case <-retryChan:
			runReconcile()
		}
	}

	return nil
}

// RunOnce performs a single device discovery + NicDevice CR sync cycle and returns.
// Used by the daemon's --discovery-only mode (full sync semantics: creates new CRs,
// updates changed status, deletes CRs for devices no longer present on the node).
//
// Unlike the periodic reconcile loop, RunOnce surfaces per-device errors via the returned
// error (joined with errors.Join) so the caller's retry can re-attempt incomplete CRs.
// Returns nil only if every observed device's CR sync succeeded.
func (d *DeviceDiscoveryController) RunOnce(ctx context.Context) error {
	var perDeviceErrs []error
	if err := d.reconcile(ctx, &perDeviceErrs); err != nil {
		return err
	}
	if len(perDeviceErrs) > 0 {
		return fmt.Errorf("%d per-device error(s) during reconcile: %w", len(perDeviceErrs), errors.Join(perDeviceErrs...))
	}
	return nil
}

// RunUntilSuccess calls RunOnce until it succeeds or maxAttempts is exhausted,
// waiting interval between attempts. Returns nil on success, or the last error
// wrapped with the attempt count after maxAttempts failures. Returns ctx.Err()
// if the context is cancelled during a wait between attempts.
func (d *DeviceDiscoveryController) RunUntilSuccess(ctx context.Context, maxAttempts int, interval time.Duration) error {
	return retryUntilSuccess(ctx, maxAttempts, interval, d.RunOnce)
}

// NewDeviceDiscoveryController creates a new instance of DeviceDiscoveryController with the specified parameters.
func NewDeviceDiscoveryController(client client.Client, deviceDiscovery devicediscovery.DeviceDiscovery, hostUtils host.HostUtils, node string, namespace string) *DeviceDiscoveryController {
	return &DeviceDiscoveryController{
		Client:          client,
		deviceDiscovery: deviceDiscovery,
		hostUtils:       hostUtils,
		nodeName:        node,
		namespace:       namespace,
	}
}
