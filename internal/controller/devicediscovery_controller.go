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
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/Mellanox/dms-lib/pkg/device"

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
	"github.com/Mellanox/nic-configuration-operator/pkg/helper"
	"github.com/Mellanox/nic-configuration-operator/pkg/host"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

var deviceDiscoveryReconcileTime = time.Minute * 5

// DeviceDiscoveryController periodically reconciles devices on the host, creates CRs for new devices,
// deletes CRs for absent devices, updates the CR when device's status has changed.
type DeviceDiscoveryController struct {
	client.Client
	// apiReader reads NicDevices that the node-scoped cache cannot see because their
	// status.node is not set yet.
	apiReader client.Reader

	discover  func(context.Context) (device.Inventory, error)
	hostUtils host.HostUtils
	nodeName  string
	namespace string
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
func (d *DeviceDiscoveryController) reconcile(ctx context.Context) error {
	inventory, discoveryErr := d.discover(ctx)
	observedDevices, incompleteDevices, discoveryErr := inventoryDevices(inventory, d.nodeName, discoveryErr)
	var failures []error
	if discoveryErr != nil {
		failures = append(failures, discoveryErr)
	}
	// Scoped failures still need a prompt retry, but must not suppress the
	// healthy device/status work performed below when the skip switch is set.
	if discoveryErr == nil {
		for _, key := range slices.Sorted(maps.Keys(incompleteDevices)) {
			failures = append(failures, incompleteDevices[key])
		}
	}

	list := &v1alpha1.NicDeviceList{}

	selectorFields := fields.OneTermEqualSelector("status.node", d.nodeName)

	err := d.List(ctx, list, client.InNamespace(d.namespace), &client.ListOptions{FieldSelector: selectorFields})
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
			if setDiscoveryCondition(&nicDeviceCR, errors.New("device has no observed PF endpoints")) {
				if err := d.Status().Update(ctx, &nicDeviceCR); err != nil {
					failures = append(failures, err)
				}
			}
			continue
		}
		pciKey := utils.PCIDeviceAddress(nicDeviceCR.Status.Ports[0].PCI)
		observedDevice, exists := observedDevices[pciKey]

		issue := errors.Join(discoveryErr, incompleteDevices[pciKey])
		if issue != nil {
			delete(observedDevices, pciKey)
			if setDiscoveryCondition(&nicDeviceCR, issue) {
				if err := d.Status().Update(ctx, &nicDeviceCR); err != nil {
					failures = append(failures, err)
				}
			}
			continue
		}
		if !exists {
			if err := d.Delete(ctx, &nicDeviceCR); err != nil {
				failures = append(failures, err)
			}
			continue
		}

		observedDeviceStatus := observedDevice.Status

		if changed := d.updateFwCondition(&nicDeviceCR); changed {
			log.Log.V(2).Info("FirmwareConfigMatch condition changed, updating device", "nicDeviceCR", nicDeviceCR)
			err = d.Client.Status().Update(ctx, &nicDeviceCR)
			if err != nil {
				failures = append(failures, err)
				log.Log.Error(err, "failed to update FirmwareConfigMatchCondition", "device", nicDeviceCR.Name)
				continue
			}
		}

		// Need to nullify conditions for deep equal
		observedDeviceStatus.Conditions = append([]metav1.Condition(nil), nicDeviceCR.Status.Conditions...)
		observedDevice.ObjectMeta = nicDeviceCR.ObjectMeta
		observedDevice.Status = observedDeviceStatus
		setDiscoveryCondition(&observedDevice, nil)
		observedDeviceStatus = observedDevice.Status

		if !reflect.DeepEqual(nicDeviceCR.Status, observedDeviceStatus) {
			log.Log.V(2).Info("device status changed, updating", "device", nicDeviceCR.Name, "crStatus", nicDeviceCR.Status, "observedStatus", observedDeviceStatus)
			// Status of the device changes, need to update the CR
			nicDeviceCR.Status = observedDeviceStatus

			err := d.Client.Status().Update(ctx, &nicDeviceCR)
			if err != nil {
				failures = append(failures, err)
				log.Log.Error(err, "failed to update NicDevice CR status", "device", nicDeviceCR.Name)
			}
		}

		// Device was processed, cleaning it from the map
		delete(observedDevices, pciKey)
	}

	// A failed host observation cannot create authoritative device records.
	if discoveryErr != nil {
		return errors.Join(failures...)
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

		err := controllerutil.SetOwnerReference(node, device, d.Scheme())
		if err != nil {
			log.Log.Error(err, "failed to set owner reference for device", "device", device)
			continue
		}
		err = d.Create(ctx, device)
		if apierrors.IsAlreadyExists(err) {
			// Device already exists but was not matched by PCI address, which means the status was not applied properly
			// on a previous reconcile. Re-fetch so the status update below runs against the live object.
			err = d.apiReader.Get(ctx, types.NamespacedName{Name: device.Name, Namespace: device.Namespace}, device)
			if err != nil {
				log.Log.Error(err, "failed to get existing NicDevice obj", "device", device)
				continue
			}
		} else if err != nil {
			log.Log.Error(err, "failed to create NicDevice obj", "device", device)
			continue
		}

		device.Status = deviceStatus
		device.Status.Node = d.nodeName
		setInitialsConditionsForDevice(device)
		setDiscoveryCondition(device, nil)

		d.updateFwCondition(device)
		log.Log.V(2).Info("updated device", "device", device)
		err = d.Client.Status().Update(ctx, device)
		if err != nil {
			log.Log.Error(err, "failed to update FirmwareConfigMatchCondition", "device", device.Name)
			continue
		}
	}
	return errors.Join(failures...)
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

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			interval := deviceDiscoveryReconcileTime
			if err := d.reconcile(ctx); err != nil {
				log.Log.Error(err, "failed to run discovery, retrying after delay")
				interval = shortRequeueTime
			}
			timer.Reset(interval)
		}
	}

}

// NewDeviceDiscoveryController creates a new instance of DeviceDiscoveryController with the specified parameters.
func NewDeviceDiscoveryController(client client.Client, apiReader client.Reader, discover func(context.Context) (device.Inventory, error), hostUtils host.HostUtils, node string, namespace string) *DeviceDiscoveryController {
	return &DeviceDiscoveryController{
		Client:    client,
		apiReader: apiReader,
		discover:  discover,
		hostUtils: hostUtils,
		nodeName:  node,
		namespace: namespace,
	}
}
