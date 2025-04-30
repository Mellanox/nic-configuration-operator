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
	"github.com/Mellanox/nic-configuration-operator/pkg/helper"
	"github.com/Mellanox/nic-configuration-operator/pkg/host"
)

var deviceDiscoveryReconcileTime = time.Minute * 5

// DeviceDiscoveryController periodically reconciles devices on the host, creates CRs for new devices,
// deletes CRs for absent devices, updates the CR when device's status has changed.
type DeviceDiscoveryController struct {
	client.Client

	hostManager host.HostManager
	nodeName    string
	namespace   string
}

// Constructs a unique CR name based on the device's type and serial number
func (d *DeviceDiscoveryController) getCRName(deviceType string, serialNumber string) string {
	return strings.ToLower(d.nodeName + "-" + deviceType + "-" + serialNumber)
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
	observedDevices, err := d.hostManager.DiscoverNicDevices()
	if err != nil {
		return err
	}

	list := &v1alpha1.NicDeviceList{}

	selectorFields := fields.OneTermEqualSelector("status.node", d.nodeName)

	err = d.Client.List(ctx, list, &client.ListOptions{FieldSelector: selectorFields})
	if err != nil {
		log.Log.Error(err, "failed to list NicDevice CRs")
		return err
	}

	log.Log.V(2).Info("listed devices", "devices", list.Items)

	node := &v1.Node{}
	err = d.Client.Get(ctx, types.NamespacedName{Name: d.nodeName}, node)
	if err != nil {
		log.Log.Error(err, "failed to get node object")
		return err
	}

	for _, nicDeviceCR := range list.Items {
		observedDeviceStatus, exists := observedDevices[nicDeviceCR.Status.SerialNumber]

		if !exists {
			log.Log.V(2).Info("device doesn't exist on the node anymore, deleting", "device", nicDeviceCR.Name)
			// Need to delete this CR, it doesn't represent the observedDevice on host anymore
			err = d.Client.Delete(ctx, &nicDeviceCR)
			if err != nil {
				log.Log.Error(err, "failed  to delete NicDevice CR", "device", nicDeviceCR.Name)
			}

			continue
		}

		if changed := d.updateFwCondition(&nicDeviceCR); changed {
			log.Log.V(2).Info("FirmwareConfigMatch condition changed, updating device", "nicDeviceCR", nicDeviceCR)
			err = d.Client.Status().Update(ctx, &nicDeviceCR)
			if err != nil {
				log.Log.Error(err, "failed to update FirmwareConfigMatchCondition", "device", nicDeviceCR.Name)
				continue
			}
		}

		// Need to nullify conditions for deep equal
		observedDeviceStatus.Conditions = nicDeviceCR.Status.Conditions

		if !reflect.DeepEqual(nicDeviceCR.Status, observedDeviceStatus) {
			log.Log.V(2).Info("device status changed, updating", "device", nicDeviceCR.Name, "crStatus", nicDeviceCR.Status, "observedStatus", observedDeviceStatus)
			// Status of the device changes, need to update the CR
			nicDeviceCR.Status = observedDeviceStatus

			err := d.Client.Status().Update(ctx, &nicDeviceCR)
			if err != nil {
				log.Log.Error(err, "failed to update NicDevice CR status", "device", nicDeviceCR.Name)
			}
		}

		// Device was processed, cleaning it from the map
		delete(observedDevices, nicDeviceCR.Status.SerialNumber)
	}

	// Remaining devices don't have CR representation, need to create a CR
	for _, deviceStatus := range observedDevices {
		deviceName := d.getCRName(deviceStatus.Type, deviceStatus.SerialNumber)
		device := &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deviceName,
				Namespace: d.namespace,
			},
		}

		err := controllerutil.SetOwnerReference(node, device, d.Client.Scheme())
		if err != nil {
			log.Log.Error(err, "failed to set owner reference for device", "device", device)
			continue
		}
		err = d.Client.Create(ctx, device)
		if err != nil {
			log.Log.Error(err, "failed to create device", "device", device)
			continue
		}

		if apierrors.IsAlreadyExists(err) {
			// Device already exists but was not matched by SerialNumber, which means the status was not applied properly
			err = d.Client.Get(ctx, types.NamespacedName{Name: device.Name, Namespace: device.Namespace}, device)
			if err != nil {
				log.Log.Error(err, "failed to get NicDevice obj", "device", device)
				continue
			}
		} else if err != nil {
			log.Log.Error(err, "failed to create NicDevice obj", "device", device)
			continue
		}

		device.Status = deviceStatus
		device.Status.Node = d.nodeName
		setInitialsConditionsForDevice(device)

		d.updateFwCondition(device)
		log.Log.V(2).Info("updated device", "device", device)
		err = d.Client.Status().Update(ctx, device)
		if err != nil {
			log.Log.Error(err, "failed to update FirmwareConfigMatchCondition", "device", device.Name)
			continue
		}
	}
	return nil
}

// updateFwCondition updates the FirmwareConfigMatch status condition
// returns bool - if condition changed and needs to be updated
func (d *DeviceDiscoveryController) updateFwCondition(nicDeviceCR *v1alpha1.NicDevice) bool {
	ofedVersion := d.hostManager.DiscoverOfedVersion()
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
		err := d.reconcile(ctx)
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

// NewDeviceDiscoveryController creates a new instance of DeviceDiscoveryController with the specified parameters.
func NewDeviceDiscoveryController(client client.Client, hostManager host.HostManager, node string, namespace string) *DeviceDiscoveryController {
	return &DeviceDiscoveryController{
		Client:      client,
		hostManager: hostManager,
		nodeName:    node,
		namespace:   namespace,
	}
}
