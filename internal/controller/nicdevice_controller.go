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
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	maintenanceoperator "github.com/Mellanox/maintenance-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	k8sTypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/host"
	"github.com/Mellanox/nic-configuration-operator/pkg/maintenance"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

const nicDeviceSyncEventName = "nic-device-sync-event-name"

var requeueTime = 1 * time.Minute

// NicDeviceReconciler reconciles a NicDevice object
type NicDeviceReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	NodeName      string
	NamespaceName string

	HostManager        host.HostManager
	HostUtils          host.HostUtils
	MaintenanceManager maintenance.MaintenanceManager

	EventRecorder record.EventRecorder
}

type nicDeviceConfigurationStatuses []*nicDeviceConfigurationStatus

type nicDeviceConfigurationStatus struct {
	device                 *v1alpha1.NicDevice
	nvConfigUpdateRequired bool
	rebootRequired         bool
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
		err = r.MaintenanceManager.ReleaseMaintenance(ctx)
		if err != nil {
			log.Log.Error(err, "failed to release maintenance")
			return ctrl.Result{}, err
		}
		// Nothing to reconcile
		return ctrl.Result{}, nil
	}

	log.Log.V(2).Info(fmt.Sprintf("reconciling %d NicDevices", len(configStatuses)))

	err = runInParallel(ctx, configStatuses, r.handleConfigurationSpecValidation)
	if err != nil {
		log.Log.Error(err, "failed to validate device's spec")
		return ctrl.Result{}, err
	}

	if configStatuses.nvConfigUpdateRequired() {
		log.Log.V(2).Info("nv config update required, scheduling maintenance")

		result, err := r.ensureMaintenance(ctx)
		if err != nil {
			log.Log.V(2).Error(err, "failed to schedule maintenance")
			return ctrl.Result{}, err
		}
		if result.Requeue || result.RequeueAfter != 0 {
			return result, nil
		}

		log.Log.V(2).Info("maintenance allowed, applying nv config")

		err = runInParallel(ctx, configStatuses, r.applyNvConfig)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	if configStatuses.rebootRequired() {
		return r.handleReboot(ctx, configStatuses)
	}

	if !configStatuses.nvConfigReadyForAll() {
		log.Log.V(2).Info("nv config not ready for some devices, requeue")
		return ctrl.Result{Requeue: true, RequeueAfter: requeueTime}, nil
	}

	log.Log.V(2).Info("applying runtime config")
	err = runInParallel(ctx, configStatuses, r.applyRuntimeConfig)
	if err != nil {
		return ctrl.Result{}, err
	}

	if configStatuses.rebootRequired() {
		return r.handleReboot(ctx, configStatuses)
	}

	log.Log.V(2).Info("all configuration are successful, releasing maintenance")
	err = r.MaintenanceManager.ReleaseMaintenance(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
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

	configStatuses := nicDeviceConfigurationStatuses{}

	for i, device := range devices.Items {
		if device.Spec.Configuration == nil {
			statusCondition := meta.FindStatusCondition(device.Status.Conditions, consts.ConfigUpdateInProgressCondition)
			if statusCondition.Reason != consts.DeviceConfigSpecEmptyReason {
				err = r.updateConfigInProgressStatusCondition(ctx, &device, consts.DeviceConfigSpecEmptyReason, metav1.ConditionFalse, "")
				if err != nil {
					log.Log.Error(err, "failed to update status condition", "device", device.Name)
					return nil, err
				}
			}
			continue
		}

		configStatuses = append(configStatuses, &nicDeviceConfigurationStatus{
			device: &devices.Items[i],
		})
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
		return ctrl.Result{RequeueAfter: requeueTime}, nil
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
	if status.rebootRequired {
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
				return err
			}

			return nil
		}
	}

	err := r.HostManager.ApplyDeviceRuntimeSpec(status.device)
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
	if !status.nvConfigUpdateRequired {
		return nil
	}

	rebootRequired, err := r.HostManager.ApplyDeviceNvSpec(ctx, status.device)
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

	status.rebootRequired = rebootRequired

	return nil
}

// handleConfigurationSpecValidation validates device's configuration spec
// if spec is correct, applies status condition UpdateStarted, otherwise IncorrectSpec
// sets nvConfigUpdateRequired and rebootRequired flags for each device's configuration status
// returns nil if specs is correct, error otherwise
func (r *NicDeviceReconciler) handleConfigurationSpecValidation(ctx context.Context, status *nicDeviceConfigurationStatus) error {
	nvConfigUpdateRequired, rebootRequired, err := r.HostManager.ValidateDeviceNvSpec(ctx, status.device)
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

func (r *NicDeviceReconciler) updateConfigInProgressStatusCondition(ctx context.Context, device *v1alpha1.NicDevice, reason string, status metav1.ConditionStatus, message string) error {
	cond := metav1.Condition{
		Type:               consts.ConfigUpdateInProgressCondition,
		Status:             status,
		ObservedGeneration: device.Generation,
		Reason:             reason,
		Message:            message,
	}
	changed := meta.SetStatusCondition(&device.Status.Conditions, cond)
	var err error
	if changed {
		err = r.Client.Status().Update(ctx, device)
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

	eventHandler := handler.Funcs{
		// We skip create event because it's always followed by a status update
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			device := e.ObjectNew.(*v1alpha1.NicDevice)

			if device.Status.Node != r.NodeName {
				// We want to skip event from devices not on the current node
				return
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
		Watches(&v1alpha1.NicDevice{}, eventHandler)

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
