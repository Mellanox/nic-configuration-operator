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
	"os"
	"reflect"
	"sync"
	"time"

	maintenanceoperator "github.com/Mellanox/maintenance-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/configuration"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/firmware"
	"github.com/Mellanox/nic-configuration-operator/pkg/host"
	"github.com/Mellanox/nic-configuration-operator/pkg/maintenance"
	"github.com/Mellanox/nic-configuration-operator/pkg/spectrumx"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

const nicDeviceSyncEventName = "nic-device-sync-event-name"

var longRequeueTime = 1 * time.Minute
var shortRequeueTime = 5 * time.Second

// NicDeviceReconciler reconciles a NicDevice object
type NicDeviceReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	NodeName      string
	NamespaceName string

	FirmwareManager      firmware.FirmwareManager
	ConfigurationManager configuration.ConfigurationManager
	HostUtils            host.HostUtils
	MaintenanceManager   maintenance.MaintenanceManager
	SpectrumXManager     spectrumx.SpectrumXManager

	EventRecorder record.EventRecorder
}

type nicDeviceConfigurationStatuses []*nicDeviceConfigurationStatus

type nicDeviceConfigurationStatus struct {
	device                       *v1alpha1.NicDevice
	firmwareValidationSuccessful bool
	requestedFirmwareVersion     string
	nvConfigUpdateRequired       bool
	rebootRequired               bool
}

// Reconcile reconciles the NicConfigurationTemplate object
func (r *NicDeviceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Only handle node-policy-sync-event
	if req.Name != nicDeviceSyncEventName || req.Namespace != "" {
		return reconcile.Result{}, nil
	}

	configStatuses, err := r.getDevices(ctx)
	if err != nil {
		log.Log.Error(err, "failed to get devices to reconcile")
		return ctrl.Result{}, err
	}

	if len(configStatuses) == 0 {
		log.Log.V(2).Info("no devices from this node to reconcile")

		err = r.MaintenanceManager.ReleaseMaintenance(ctx)
		if err != nil {
			log.Log.Error(err, "failed to release maintenance")
			return ctrl.Result{}, err
		}
		// Nothing to reconcile
		return ctrl.Result{}, nil
	}

	log.Log.V(2).Info(fmt.Sprintf("reconciling %d NicDevices", len(configStatuses)))

	// First we need to validate the firmware spec of all devices
	// NicDevices with empty FW specs are skipped during this step
	// 1. If the NicFirmwareSource object, referenced in the spec, is not ready or has failed, update the status and proceed with other devices
	// 2. If the updatePolicy is set to Validate and the installed FW version doesn't match the spec, update the status and requeue
	// 3. If the updatePolicy is set to Update and the installed FW version doesn't match the spec, update the firmware to the version from NicFirmwareSource
	// 4. If FW update failed, update the status and return an error to try again.
	// 5. If after the FW update FW is still not ready on some devices, requeue to try again later
	// Unavailable NicFirmwareSource for one device shouldn't block the FW update for all of them
	// If FW failed the validation / to update on at least one device, NIC configuration won't start until the error is resolved

	err = runInParallel(ctx, configStatuses, r.handleFirmwareValidation)
	if err != nil {
		log.Log.Error(err, "failed to validate device's firmware source")
		return ctrl.Result{}, err
	}

	// If Firmware is not ready, update NIC configuration status to "Pending Firmware Update"
	if !configStatuses.firmwareReady() {
		log.Log.V(2).Info("firmware is not ready for some devices, set PendingFirmwareUpdate status to ConfigUpdateInProgress condition")

		err = runInParallel(ctx, configStatuses, r.handleConfigurationPendingFirmwareUpdate)
		if err != nil {
			log.Log.Error(err, "failed to update device's status")
			return ctrl.Result{}, err
		}
	}

	if configStatuses.firmwareUpdateRequired() {
		log.Log.Info("firmware update is required for some devices, scheduling maintenance to perform update")

		result, err := r.ensureMaintenance(ctx)
		if err != nil {
			log.Log.V(2).Error(err, "failed to schedule maintenance")
			return ctrl.Result{}, err
		}
		if result.Requeue || result.RequeueAfter != 0 {
			return result, nil
		}

		err = runInParallel(ctx, configStatuses, r.handleFirmwareUpdate)
		if err != nil {
			log.Log.Error(err, "failed to update device's firmware")
			return ctrl.Result{RequeueAfter: shortRequeueTime}, err
		}

		if configStatuses.rebootRequired() {
			return r.handleReboot(ctx, configStatuses)
		}
	}

	// If FW is not ready for all devices after update, requeue the request
	if !configStatuses.firmwareReady() {
		log.Log.Info("firmware is not ready for some devices, requeue to try again later")
		// If FW is not ready but FW update is not required, we can release maintenance
		// If FW update is required but failed, the reconcile request would be rescheduled and won't reach this point
		if !configStatuses.firmwareUpdateRequired() {
			err = r.MaintenanceManager.ReleaseMaintenance(ctx)
			if err != nil {
				log.Log.Error(err, "failed to release maintenance")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{Requeue: true, RequeueAfter: longRequeueTime}, nil
	}

	log.Log.Info("firmware is ready for all devices, proceeding with NIC configuration")

	// If FW is ready for all devices, proceed with NIC configuration
	err = runInParallel(ctx, configStatuses, r.handleConfigurationSpecValidation)
	if err != nil {
		log.Log.Error(err, "failed to validate device's spec")
		return ctrl.Result{}, err
	}

	if configStatuses.nvConfigUpdateRequired() {
		log.Log.Info("nv config update required for some devices, scheduling maintenance")

		result, err := r.ensureMaintenance(ctx)
		if err != nil {
			log.Log.V(2).Error(err, "failed to schedule maintenance")
			return ctrl.Result{}, err
		}
		if result.Requeue || result.RequeueAfter != 0 {
			return result, nil
		}

		log.Log.Info("maintenance allowed, applying nv config")

		err = runInParallel(ctx, configStatuses, r.applyNvConfig)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	if configStatuses.rebootRequired() {
		return r.handleReboot(ctx, configStatuses)
	}

	if !configStatuses.nvConfigReadyForAll() {
		log.Log.Info("nv config not ready for some devices, requeue")
		return ctrl.Result{Requeue: true, RequeueAfter: longRequeueTime}, nil
	}

	log.Log.Info("applying runtime config")
	err = runInParallel(ctx, configStatuses, r.applyRuntimeConfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	if configStatuses.rebootRequired() {
		return r.handleReboot(ctx, configStatuses)
	}

	log.Log.Info("all configuration are successful, releasing maintenance")
	err = r.MaintenanceManager.ReleaseMaintenance(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	log.Log.V(2).Info("requeue the request with a big interval to check if the configuration has drifter")
	return ctrl.Result{RequeueAfter: longRequeueTime * 20}, nil
}

// getDevices lists all the NicDevice objects from this node and filters those with non-empty specs,
// updates relevant status conditions if configuration or firmware specs are empty
func (r *NicDeviceReconciler) getDevices(ctx context.Context) (nicDeviceConfigurationStatuses, error) {
	devices := &v1alpha1.NicDeviceList{}

	selectorFields := fields.OneTermEqualSelector("status.node", r.NodeName)

	err := r.Client.List(ctx, devices, &client.ListOptions{FieldSelector: selectorFields})
	if err != nil {
		log.Log.Error(err, "failed to list NicDevice CRs")
		return nil, err
	}

	log.Log.V(2).Info("listed devices from this node", "count", len(devices.Items))

	configStatuses := nicDeviceConfigurationStatuses{}

	for i, device := range devices.Items {
		if device.Spec.Firmware == nil {
			log.Log.V(2).Info("device has no firmware spec", "name", device.Name)
			statusCondition := meta.FindStatusCondition(device.Status.Conditions, consts.FirmwareUpdateInProgressCondition)
			if statusCondition == nil || statusCondition.Reason != consts.DeviceFirmwareSpecEmptyReason {
				err = r.updateFirmwareUpdateInProgressStatusCondition(ctx, &device, consts.DeviceFirmwareSpecEmptyReason, metav1.ConditionFalse, "")
				if err != nil {
					log.Log.Error(err, "failed to update status condition", "device", device.Name)
					return nil, err
				}
			}
		}

		if device.Spec.Configuration == nil {
			log.Log.V(2).Info("device has no configuration spec", "name", device.Name)

			statusCondition := meta.FindStatusCondition(device.Status.Conditions, consts.ConfigUpdateInProgressCondition)
			if statusCondition == nil || statusCondition.Reason != consts.DeviceConfigSpecEmptyReason {
				err = r.updateConfigInProgressStatusCondition(ctx, &device, consts.DeviceConfigSpecEmptyReason, metav1.ConditionFalse, "")
				if err != nil {
					log.Log.Error(err, "failed to update status condition", "device", device.Name)
					return nil, err
				}
			}
		}

		if device.Spec.Firmware != nil || device.Spec.Configuration != nil {
			log.Log.V(2).Info("device has non-nil spec, adding it to the list to reconcile", "name", device.Name)
			configStatuses = append(configStatuses, &nicDeviceConfigurationStatus{
				device: &devices.Items[i],
			})
		}
	}

	return configStatuses, nil
}

// ensureMaintenance schedules maintenance if required and requests reschedule if it's not ready yet
func (r *NicDeviceReconciler) ensureMaintenance(ctx context.Context) (ctrl.Result, error) {
	err := r.MaintenanceManager.ScheduleMaintenance(ctx)
	if err != nil {
		log.Log.Error(err, "failed to schedule maintenance for node")
		return ctrl.Result{}, err
	}

	maintenanceAllowed, err := r.MaintenanceManager.MaintenanceAllowed(ctx)
	if err != nil {
		log.Log.Error(err, "failed to get maintenance status")
		return ctrl.Result{}, err
	}
	if !maintenanceAllowed {
		log.Log.V(2).Info("maintenance not allowed yet, exiting for now")
		// Maintenance not yet allowed, waiting until then
		return ctrl.Result{RequeueAfter: longRequeueTime}, nil
	}

	return ctrl.Result{}, nil
}

// runInParallel runs a given function in parallel for each of the nicDevice statuses
// returns an error if at least one status holds an error, nil otherwise
func runInParallel(ctx context.Context, statuses nicDeviceConfigurationStatuses, f func(ctx context.Context, status *nicDeviceConfigurationStatus) error) error {
	var wg sync.WaitGroup

	observedErrors := make([]error, len(statuses))

	for i := 0; i < len(statuses); i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			err := f(ctx, statuses[index])
			observedErrors[index] = err
		}(i)
	}

	wg.Wait()

	for _, observedErr := range observedErrors {
		if observedErr != nil {
			return observedErr
		}
	}

	return nil
}

// applyRuntimeConfig applies device's runtime spec
// if update is successful, applies status condition UpdateSuccessful, otherwise RuntimeConfigUpdateFailed
// if rebootRequired, sets status condition PendingReboot
// if status.rebootRequired == true, skips the device
// returns nil if device's config update was successful, error otherwise
func (r *NicDeviceReconciler) applyRuntimeConfig(ctx context.Context, status *nicDeviceConfigurationStatus) error {
	if status.device.Spec.Configuration == nil || status.rebootRequired {
		return nil
	}

	lastAppliedState, found := status.device.Annotations[consts.LastAppliedStateAnnotation]
	if found {
		specJson, err := json.Marshal(status.device.Spec)
		if err != nil {
			return err
		}

		if string(specJson) != lastAppliedState {
			log.Log.V(2).Info("last applied state differs, reboot required", "device", status.device.Name)
			status.rebootRequired = true

			err := r.updateConfigInProgressStatusCondition(ctx, status.device, consts.PendingRebootReason, metav1.ConditionTrue, "")
			if err != nil {
				updateErr := r.updateConfigInProgressStatusCondition(ctx, status.device, consts.RuntimeConfigUpdateFailedReason, metav1.ConditionFalse, err.Error())
				if updateErr != nil {
					log.Log.Error(err, "failed to update device status condition", "device", status.device.Name)
				}
				return err
			}

			return nil
		}
	}

	targetVersion, err := r.SpectrumXManager.GetDocaCCTargetVersion(status.device)
	if err != nil {
		return err
	}

	if targetVersion != "" {
		err = r.FirmwareManager.InstallDocaSpcXCC(ctx, status.device, targetVersion)
		if err != nil {
			return err
		}
	}

	err = r.ConfigurationManager.ApplyDeviceRuntimeSpec(status.device)
	if err != nil {
		updateErr := r.updateConfigInProgressStatusCondition(ctx, status.device, consts.RuntimeConfigUpdateFailedReason, metav1.ConditionFalse, err.Error())
		if updateErr != nil {
			log.Log.Error(err, "failed to update device status condition", "device", status.device.Name)
		}
		return err
	}

	specJson, err := json.Marshal(status.device.Spec)
	if err != nil {
		return err
	}

	if status.device.Annotations == nil {
		status.device.SetAnnotations(make(map[string]string))
	}
	status.device.Annotations[consts.LastAppliedStateAnnotation] = string(specJson)
	err = r.Update(ctx, status.device)
	if err != nil {
		return err
	}

	err = r.updateConfigInProgressStatusCondition(ctx, status.device, consts.UpdateSuccessfulReason, metav1.ConditionFalse, "")
	if err != nil {
		return err
	}

	return nil
}

// handleReboot schedules maintenance and reboots the node if maintenance is allowed
// Before rebooting the node, strips LastAppliedState annotations from all devices
// returns true if requeue of the reconcile request is required, false otherwise
// return err if encountered an error while performing maintenance scheduling / reboot
func (r *NicDeviceReconciler) handleReboot(ctx context.Context, statuses nicDeviceConfigurationStatuses) (ctrl.Result, error) {
	log.Log.V(2).Info("reboot required")

	if result, err := r.ensureMaintenance(ctx); result.RequeueAfter != 0 || err != nil {
		return result, err
	}

	// We need to strip last applied state annotation before reboot as it resets the runtime configuration
	err := runInParallel(ctx, statuses, r.stripLastAppliedStateAnnotation)
	if err != nil {
		return ctrl.Result{}, err
	}

	log.Log.V(2).Info("maintenance allowed, scheduling reboot")

	err = r.MaintenanceManager.Reboot()
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// stripLastAppliedStateAnnotation deletes the consts.LastAppliedStateAnnotation from device
// returns error if annotation update failed
func (r *NicDeviceReconciler) stripLastAppliedStateAnnotation(ctx context.Context, status *nicDeviceConfigurationStatus) error {
	annotations := status.device.GetAnnotations()
	if _, found := annotations[consts.LastAppliedStateAnnotation]; !found {
		return nil
	}

	delete(annotations, consts.LastAppliedStateAnnotation)
	status.device.SetAnnotations(annotations)
	return r.Update(ctx, status.device)
}

// applyNvConfig applies device's non-volatile spec
// if update is correct, applies status condition PendingReboot, otherwise NonVolatileConfigUpdateFailed
// sets rebootRequired flags for each device's configuration status
// if status.nvConfigUpdateRequired == false, skips the device
// returns nil if config update was successful, error otherwise
func (r *NicDeviceReconciler) applyNvConfig(ctx context.Context, status *nicDeviceConfigurationStatus) error {
	if status.device.Spec.Configuration == nil || !status.nvConfigUpdateRequired {
		return nil
	}

	rebootRequired, err := r.ConfigurationManager.ApplyDeviceNvSpec(ctx, status.device)
	if err != nil {
		if types.IsIncorrectSpecError(err) {
			updateErr := r.updateConfigInProgressStatusCondition(ctx, status.device, consts.IncorrectSpecReason, metav1.ConditionFalse, err.Error())
			if updateErr != nil {
				log.Log.Error(err, "failed to update device status condition", "device", status.device.Name)
			}
		} else {
			updateErr := r.updateConfigInProgressStatusCondition(ctx, status.device, consts.NonVolatileConfigUpdateFailedReason, metav1.ConditionFalse, err.Error())
			if updateErr != nil {
				log.Log.Error(err, "failed to update device status condition", "device", status.device.Name)
			}
		}
		return err
	}
	err = r.updateConfigInProgressStatusCondition(ctx, status.device, consts.PendingRebootReason, metav1.ConditionTrue, "")
	if err != nil {
		return err
	}

	// On some platforms, explicit FW reset is required before reboot to apply the changes to NV spec
	featureGate := os.Getenv(consts.FEATURE_GATE_FW_RESET_AFTER_CONFIG_UPDATE)
	if featureGate == consts.LabelValueTrue {
		log.Log.Info("Feature gate FW_RESET_AFTER_CONFIG_UPDATE is enabled, resetting NIC firmware before reboot", "device", status.device.Name)
		err = r.ConfigurationManager.ResetNicFirmware(ctx, status.device)
		if err != nil {
			log.Log.Error(err, "failed to reset NIC firmware before reboot", "device", status.device.Name)
			return err
		}
		log.Log.Info("NIC firmware reset successful", "device", status.device.Name)
	}

	status.rebootRequired = rebootRequired

	return nil
}

// handleConfigurationSpecValidation validates device's configuration spec
// if spec is correct, applies status condition UpdateStarted, otherwise IncorrectSpec
// sets nvConfigUpdateRequired and rebootRequired flags for each device's configuration status
// returns nil if specs is correct, error otherwise
func (r *NicDeviceReconciler) handleConfigurationSpecValidation(ctx context.Context, status *nicDeviceConfigurationStatus) error {
	if status.device.Spec.Configuration == nil {
		status.nvConfigUpdateRequired = false
		status.rebootRequired = false
		return nil
	}

	nvConfigUpdateRequired, rebootRequired, err := r.ConfigurationManager.ValidateDeviceNvSpec(ctx, status.device)
	log.Log.V(2).Info("nv spec validation complete for device", "device", status.device.Name, "nvConfigUpdateRequired", nvConfigUpdateRequired, "rebootRequired", rebootRequired)
	if err != nil {
		log.Log.Error(err, "failed to validate spec for device", "device", status.device.Name)
		if types.IsIncorrectSpecError(err) {
			updateError := r.updateConfigInProgressStatusCondition(ctx, status.device, consts.IncorrectSpecReason, metav1.ConditionFalse, err.Error())
			if updateError != nil {
				log.Log.Error(err, "failed to update device status condition", "device", status.device.Name)
			}
		} else {
			updateError := r.updateConfigInProgressStatusCondition(ctx, status.device, consts.SpecValidationFailed, metav1.ConditionFalse, err.Error())
			if updateError != nil {
				log.Log.Error(err, "failed to update device status condition", "device", status.device.Name)
			}
		}

		return err
	}

	status.nvConfigUpdateRequired = nvConfigUpdateRequired
	status.rebootRequired = rebootRequired

	if nvConfigUpdateRequired {
		log.Log.V(2).Info("update started for device", "device", status.device.Name)
		err = r.updateConfigInProgressStatusCondition(ctx, status.device, consts.UpdateStartedReason, metav1.ConditionTrue, "")
		if err != nil {
			return err
		}
	} else if rebootRequired {
		// There might be a case where FW config didn't apply after a reboot because of some error in FW. In this case
		// we don't want the node to be kept in a reboot loop (FW configured -> reboot -> Config was not applied -> FW configured -> etc.).
		// To break the reboot loop, we should compare the last time the status was changed to PendingReboot to the node's uptime.
		// If the node started after the status was changed, we assume the node was rebooted and the config couldn't apply.
		// In this case, we indicate the error to the user with the status change and emit an error event.
		statusCondition := meta.FindStatusCondition(status.device.Status.Conditions, consts.ConfigUpdateInProgressCondition)
		if statusCondition == nil {
			return nil
		}

		switch statusCondition.Reason {
		case consts.PendingRebootReason:
			// We need to determine, whether a reboot has happened since the PendingReboot status has been set
			uptime, err := r.HostUtils.GetHostUptimeSeconds()
			if err != nil {
				return err
			}

			sinceStatusUpdate := time.Since(statusCondition.LastTransitionTime.Time)

			// If more time has passed since boot than since the status update, the reboot hasn't happened yet
			if uptime > sinceStatusUpdate {
				return nil
			}

			// If configuration reset was applied (nvConfigUpdateRequired == false) and reboot has happened (uptime > sinceStatusUpdate),
			// there might still be discrepancies in the mlxconfig query that might lead to a boot loop.
			// In this case, consider the reset successful
			if status.device.Spec.Configuration.ResetToDefault {
				status.rebootRequired = false
				return nil
			}

			log.Log.Info("nv config failed to update after reboot for device", "device", status.device.Name)
			r.EventRecorder.Event(status.device, v1.EventTypeWarning, consts.FirmwareError, consts.FwConfigNotAppliedAfterRebootErrorMsg)
			err = r.updateConfigInProgressStatusCondition(ctx, status.device, consts.FirmwareError, metav1.ConditionFalse, consts.FwConfigNotAppliedAfterRebootErrorMsg)
			if err != nil {
				return err
			}

			fallthrough
		case consts.FirmwareError:
			status.nvConfigUpdateRequired = false
			status.rebootRequired = false
			return errors.New(consts.FwConfigNotAppliedAfterRebootErrorMsg)
		default:
			// If reboot hasn't happened yet, proceed as normal and set PendingReboot status
			log.Log.V(2).Info("reboot pending for device", "device", status.device.Name)
			err = r.updateConfigInProgressStatusCondition(ctx, status.device, consts.PendingRebootReason, metav1.ConditionTrue, "")
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// handleFirmwareValidation validates device's firmware against the referenced NicFirmwareSource
// Sets fwSourceReady flag for each device's configuration status
// If an error occurred, applies either the FirmwareSourceNotReady or FirmwareSourceFailed status condition based on the error type
func (r *NicDeviceReconciler) handleFirmwareValidation(ctx context.Context, status *nicDeviceConfigurationStatus) error {
	if status.device.Spec.Firmware == nil {
		status.firmwareValidationSuccessful = true
		return nil
	}

	requestedFirmwareVersion, err := r.FirmwareManager.ValidateRequestedFirmwareSource(ctx, status.device)
	log.Log.V(2).Info("firmware source validation complete for device", "device", status.device.Name, "err", err)

	status.requestedFirmwareVersion = requestedFirmwareVersion
	status.firmwareValidationSuccessful = err == nil

	if err != nil {
		if types.IsFirmwareSourceNotReadyError(err) {
			log.Log.Error(err, "firmware source not ready", "device", status.device.Name)
			return r.updateFirmwareUpdateInProgressStatusCondition(ctx, status.device, consts.FirmwareSourceNotReadyReason, metav1.ConditionFalse, err.Error())
		} else {
			log.Log.Error(err, "firmware source failed", "device", status.device.Name)
			return r.updateFirmwareUpdateInProgressStatusCondition(ctx, status.device, consts.FirmwareSourceFailedReason, metav1.ConditionFalse, err.Error())
		}
	}

	_, runningFirmwareVersion, err := r.FirmwareManager.GetFirmwareVersionsFromDevice(status.device)
	if err != nil {
		log.Log.Error(err, "failed to get firmware versions from device", "device", status.device.Name)
		return err
	}

	if runningFirmwareVersion != status.device.Status.FirmwareVersion {
		log.Log.Info("Running firmware version is different from the last observed, updating the device status first", "device", status.device.Name, "new version", status.requestedFirmwareVersion)
		status.device.Status.FirmwareVersion = runningFirmwareVersion
		err = r.Client.Status().Update(ctx, status.device)
		if err != nil {
			log.Log.Error(err, "failed to update device status", "device", status.device.Name)
			return err
		}
	}

	if status.firmwareUpdateRequired() {
		return r.updateFirmwareUpdateInProgressStatusCondition(ctx, status.device, consts.PendingNodeMaintenanceReason, metav1.ConditionTrue, "")
	} else if !status.firmwareUpToDate() && status.device.Spec.Firmware.UpdatePolicy == consts.FirmwareUpdatePolicyValidate {
		log.Log.Info("firmware doesn't match the requested version, update policy set to Validate", "device", status.device.Name)
		return r.updateFirmwareUpdateInProgressStatusCondition(ctx, status.device, consts.DeviceFwMismatchReason, metav1.ConditionFalse, consts.DeviceFwMismatchMessage)
	} else if status.firmwareUpToDate() {
		log.Log.Info("firmware matches the requested version", "device", status.device.Name)
		return r.updateFirmwareUpdateInProgressStatusCondition(ctx, status.device, consts.DeviceFwMatchReason, metav1.ConditionFalse, consts.DeviceFwMatchMessage)
	}

	return nil
}

// handleFirmwareUpdate performs firmware update according to the given update policy
// If fw version matched the requested, applies the status condition DeviceFwMatchReason, FirmwareUpdateStarted otherwise
// If not, performs the firmware update
// If an error occurred, applies FirmwareUpdateFailed status condition
func (r *NicDeviceReconciler) handleFirmwareUpdate(ctx context.Context, status *nicDeviceConfigurationStatus) error {
	if status.device.Spec.Firmware == nil || !status.firmwareValidationSuccessful || status.device.Spec.Firmware.UpdatePolicy == consts.FirmwareUpdatePolicyValidate {
		return nil
	}

	if status.firmwareUpToDate() {
		log.Log.V(2).Info("firmware matches the requested version", "device", status.device.Name)
		return r.updateFirmwareUpdateInProgressStatusCondition(ctx, status.device, consts.DeviceFwMatchReason, metav1.ConditionFalse, consts.DeviceFwMatchMessage)
	}

	log.Log.Info("firmware update started for device", "device", status.device.Name)

	err := r.updateFirmwareUpdateInProgressStatusCondition(ctx, status.device, consts.FirmwareUpdateStartedReason, metav1.ConditionTrue, "")
	if err != nil {
		return err
	}

	err = r.FirmwareManager.BurnNicFirmware(ctx, status.device, status.requestedFirmwareVersion)
	if err != nil {
		log.Log.Error(err, "failed to update device firmware", "device", status.device.Name)
		_ = r.updateFirmwareUpdateInProgressStatusCondition(ctx, status.device, consts.FirmwareUpdateFailedReason, metav1.ConditionFalse, err.Error())
		return err
	}

	if utils.IsBlueFieldDevice(status.device.Status.Type) {
		log.Log.Info("BlueField BFB installed, reboot is required to apply changes", "device", status.device.Name)
		status.rebootRequired = true
		return nil
	}

	log.Log.Info("New firmware image was successfully burned. Firmware reset is required to apply changes", "device", status.device.Name)

	err = r.ConfigurationManager.ResetNicFirmware(ctx, status.device)
	if err != nil {
		log.Log.Error(err, "failed to reset NIC firmware", "device", status.device.Name)
		_ = r.updateFirmwareUpdateInProgressStatusCondition(ctx, status.device, consts.FirmwareUpdateFailedReason, metav1.ConditionFalse, err.Error())
		return err
	}

	log.Log.Info("update fw version for device after update", "device", status.device.Name, "new version", status.requestedFirmwareVersion)
	status.device.Status.FirmwareVersion = status.requestedFirmwareVersion
	err = r.Client.Status().Update(ctx, status.device)
	if err != nil {
		log.Log.Error(err, "failed to update device firmware version", "device", status.device.Name)
	}
	return r.updateFirmwareUpdateInProgressStatusCondition(ctx, status.device, consts.DeviceFwMatchReason, metav1.ConditionFalse, consts.DeviceFwMatchMessage)
}

// handleConfigurationPendingFirmwareUpdate updates the device's configuration status condition to PendingFirmware if configuration spec is not empty
func (r *NicDeviceReconciler) handleConfigurationPendingFirmwareUpdate(ctx context.Context, status *nicDeviceConfigurationStatus) error {
	if status.device.Spec.Configuration == nil {
		return nil
	}

	if status.firmwareReady() {
		log.Log.V(2).Info("device's NIC configuration is pending FW update on other devices", "device", status.device.Name)
		return r.updateConfigInProgressStatusCondition(ctx, status.device, consts.PendingFirmwareUpdateReason, metav1.ConditionFalse, consts.PendingOtherFirmwareUpdateMessage)
	} else {
		log.Log.V(2).Info("device's NIC configuration is pending own FW update", "device", status.device.Name)
		return r.updateConfigInProgressStatusCondition(ctx, status.device, consts.PendingFirmwareUpdateReason, metav1.ConditionFalse, consts.PendingOwnFirmwareUpdateMessage)
	}
}

func (r *NicDeviceReconciler) updateConfigInProgressStatusCondition(ctx context.Context, device *v1alpha1.NicDevice, reason string, status metav1.ConditionStatus, message string) error {
	return r.updateStatusCondition(ctx, device, consts.ConfigUpdateInProgressCondition, reason, status, message)
}

func (r *NicDeviceReconciler) updateFirmwareUpdateInProgressStatusCondition(ctx context.Context, device *v1alpha1.NicDevice, reason string, status metav1.ConditionStatus, message string) error {
	return r.updateStatusCondition(ctx, device, consts.FirmwareUpdateInProgressCondition, reason, status, message)
}

func (r *NicDeviceReconciler) updateStatusCondition(ctx context.Context, device *v1alpha1.NicDevice, conditionType string, reason string, status metav1.ConditionStatus, message string) error {
	cond := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		ObservedGeneration: device.Generation,
		Reason:             reason,
		Message:            message,
	}
	changed := meta.SetStatusCondition(&device.Status.Conditions, cond)
	var err error
	if changed {
		err = r.Client.Status().Update(ctx, device)
		log.Log.V(2).Info("updating status condition for device", "device", device.Name, "type", conditionType, "reason", reason, "err", err)
	}

	return err
}

// SetupWithManager sets up the controller with the Manager.
func (r *NicDeviceReconciler) SetupWithManager(mgr ctrl.Manager, watchForMaintenance bool) error {
	qHandler := func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		q.Add(reconcile.Request{NamespacedName: k8sTypes.NamespacedName{
			Namespace: "",
			Name:      nicDeviceSyncEventName,
		}})
	}

	nicDeviceEventHandler := handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			device := e.Object.(*v1alpha1.NicDevice)

			if device.Status.Node != r.NodeName {
				return
			}

			log.Log.Info("Enqueuing sync for create event", "resource", e.Object.GetName())
			qHandler(q)
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			oldDevice := e.ObjectOld.(*v1alpha1.NicDevice)
			newDevice := e.ObjectNew.(*v1alpha1.NicDevice)

			if newDevice.Status.Node != r.NodeName {
				// We want to skip event from devices not on the current node
				return
			}
			if reflect.DeepEqual(oldDevice.Spec, newDevice.Spec) {
				oldStatus := oldDevice.Status.DeepCopy()
				newStatus := newDevice.Status.DeepCopy()

				oldStatus.Conditions = nil
				newStatus.Conditions = nil

				// No need to reconcile if only status conditions got updated
				if reflect.DeepEqual(oldStatus, newStatus) {
					return
				}
			}

			log.Log.Info("Enqueuing sync for update event", "resource", e.ObjectNew.GetName())
			qHandler(q)
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			device := e.Object.(*v1alpha1.NicDevice)

			if device.Status.Node != r.NodeName {
				return
			}

			log.Log.Info("Enqueuing sync for delete event", "resource", e.Object.GetName())
			qHandler(q)
		},
		GenericFunc: func(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			device := e.Object.(*v1alpha1.NicDevice)

			if device.Status.Node != r.NodeName {
				return
			}

			log.Log.Info("Enqueuing sync for generic event", "resource", e.Object.GetName())
			qHandler(q)
		},
	}

	controller := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.NicDevice{}).
		Watches(&v1alpha1.NicDevice{}, nicDeviceEventHandler)

	if watchForMaintenance {
		controller.Watches(&maintenanceoperator.NodeMaintenance{}, maintenance.GetMaintenanceRequestEventHandler(r.NodeName, qHandler))
	}

	return controller.
		Named("nicDeviceReconciler").
		Complete(r)
}

// nvConfigReadyForAll returns true if nv config is ready for ALL devices, false if not ready for at least one device
func (p nicDeviceConfigurationStatuses) nvConfigReadyForAll() bool {
	nvConfigReadyForAll := true
	for _, result := range p {
		if result.nvConfigUpdateRequired || result.rebootRequired {
			nvConfigReadyForAll = false
			log.Log.V(2).Info("nv config not ready for device", "device", result.device)
		}
	}

	log.Log.V(2).Info("nv config ready for all devices", "ready", nvConfigReadyForAll)
	return nvConfigReadyForAll
}

// rebootRequired returns true if reboot required for at least one device, false if not required for any device
func (p nicDeviceConfigurationStatuses) rebootRequired() bool {
	rebootRequiredForSome := false
	for _, result := range p {
		if result.rebootRequired {
			rebootRequiredForSome = true
			log.Log.V(2).Info("reboot required for device", "device", result.device)
		}
	}

	return rebootRequiredForSome
}

// nvConfigUpdateRequired returns true if nv config update required for at least one device, false if not required for any device
func (p nicDeviceConfigurationStatuses) nvConfigUpdateRequired() bool {
	nvConfigUpdateRequiredForSome := false
	for _, result := range p {
		if result.nvConfigUpdateRequired {
			nvConfigUpdateRequiredForSome = true
		}
	}

	if nvConfigUpdateRequiredForSome {
		log.Log.V(2).Info("nv config change required for some devices")
	}

	return nvConfigUpdateRequiredForSome
}

// firmwareUpToDate returns true if fw version matches the requested
func (s nicDeviceConfigurationStatus) firmwareUpToDate() bool {
	return s.requestedFirmwareVersion == "" || s.device.Status.FirmwareVersion == s.requestedFirmwareVersion
}

// firmwareUpdateRequired returns true if fw validation was successful and fw is not up to date
func (s nicDeviceConfigurationStatus) firmwareUpdateRequired() bool {
	return s.firmwareValidationSuccessful && !s.firmwareUpToDate() && s.device.Spec.Firmware.UpdatePolicy == consts.FirmwareUpdatePolicyUpdate
}

// firmwareReady returns true if fw validation was successful and fw update is not required
func (s nicDeviceConfigurationStatus) firmwareReady() bool {
	return s.firmwareValidationSuccessful && s.firmwareUpToDate()
}

// firmwareUpdateRequired returns true if firmware update is required for at least one device, false if not required for all devices
func (p nicDeviceConfigurationStatuses) firmwareUpdateRequired() bool {
	for _, result := range p {
		if result.firmwareUpdateRequired() {
			log.Log.V(2).Info("firmware update required for device", "device", result.device)
			return true
		}
	}

	log.Log.V(2).Info("firmware update not required for all devices")
	return false
}

// firmwareUpdateRequired returns true if firmware is ready for all device, false if not ready for at least one device
func (p nicDeviceConfigurationStatuses) firmwareReady() bool {
	for _, result := range p {
		if !result.firmwareReady() {
			log.Log.V(2).Info("firmware not ready for device", "device", result.device)
			return false
		}
	}

	log.Log.V(2).Info("firmware ready for all devices")
	return true
}
