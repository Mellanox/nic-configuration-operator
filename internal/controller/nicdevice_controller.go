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
	"sync"
	"time"

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
	MaintenanceManager maintenance.MaintenanceManager
}

type nicDeviceConfigurationStatuses []*nicDeviceConfigurationStatus

type nicDeviceConfigurationStatus struct {
	device                 *v1alpha1.NicDevice
	nvConfigUpdateRequired bool
	rebootRequired         bool
	lastStageError         error
}

// Reconcile reconciles the NicConfigurationTemplate object
func (r *NicDeviceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	err = r.handleSpecValidation(ctx, configStatuses)
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

		err = r.applyNvConfig(ctx, configStatuses)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	if configStatuses.rebootRequired() {
		return r.handleReboot(ctx, configStatuses)
	}

	if !configStatuses.nvConfigReadyForAll() {
		return ctrl.Result{Requeue: true, RequeueAfter: requeueTime}, nil
	}

	err = r.applyRuntimeConfig(ctx, configStatuses)
	if err != nil {
		return ctrl.Result{}, err
	}

	if configStatuses.rebootRequired() {
		return r.handleReboot(ctx, configStatuses)
	}

	err = r.MaintenanceManager.ReleaseMaintenance(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *NicDeviceReconciler) getDevices(ctx context.Context) (nicDeviceConfigurationStatuses, error) {
	devices := &v1alpha1.NicDeviceList{}

	selectorFields := fields.OneTermEqualSelector("status.node", r.NodeName)

	err := r.Client.List(ctx, devices, &client.ListOptions{FieldSelector: selectorFields})
	if err != nil {
		log.Log.Error(err, "failed to list NicDevice CRs")
		return nil, err
	}

	if len(devices.Items) == 0 {
		err = r.MaintenanceManager.ReleaseMaintenance(ctx)
		if err != nil {
			log.Log.Error(err, "failed to release maintenance")
			return nil, err
		}
		// Nothing to reconcile
		return nil, nil
	}

	configStatuses := nicDeviceConfigurationStatuses{}

	for i, device := range devices.Items {
		if device.Spec.Configuration == nil {
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

// applyRuntimeConfig applies each device's runtime spec in parallel
// if update is successful, applies status condition UpdateSuccessful, otherwise RuntimeConfigUpdateFailed
// if rebootRequired, sets status condition PendingReboot
// if status.rebootRequired == true, skips the device
// returns nil if all devices' config updates were successful, error otherwise
func (r *NicDeviceReconciler) applyRuntimeConfig(ctx context.Context, statuses nicDeviceConfigurationStatuses) error {
	var wg sync.WaitGroup

	for i := 0; i < len(statuses); i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			status := statuses[index]
			status.lastStageError = nil
			if status.rebootRequired {
				return
			}

			lastAppliedState, found := status.device.Annotations[consts.LastAppliedStateAnnotation]
			if found {
				specJson, err := json.Marshal(status.device.Spec)
				if err != nil {
					status.lastStageError = err
					return
				}

				if string(specJson) != lastAppliedState {
					log.Log.V(2).Info("last applied state differs, reboot required", "device", status.device.Name)
					status.rebootRequired = true

					err := r.updateDeviceStatusCondition(ctx, status.device, consts.PendingRebootReason, metav1.ConditionTrue, "")
					if err != nil {
						status.lastStageError = err
						return
					}

					return
				}
			}

			err := r.HostManager.ApplyDeviceRuntimeSpec(statuses[index].device)
			if err != nil {
				statuses[index].lastStageError = err
				err = r.updateDeviceStatusCondition(ctx, status.device, consts.RuntimeConfigUpdateFailedReason, metav1.ConditionFalse, err.Error())
				if err != nil {
					log.Log.Error(err, "failed to update device status condition", "device", status.device.Name)
				}
				return
			}

			specJson, err := json.Marshal(status.device.Spec)
			if err != nil {
				status.lastStageError = err
				return
			}

			if status.device.Annotations == nil {
				status.device.SetAnnotations(make(map[string]string))
			}
			status.device.Annotations[consts.LastAppliedStateAnnotation] = string(specJson)
			err = r.Update(ctx, status.device)
			if err != nil {
				status.lastStageError = err
				return
			}

			err = r.updateDeviceStatusCondition(ctx, status.device, consts.UpdateSuccessfulReason, metav1.ConditionFalse, "")
			if err != nil {
				status.lastStageError = err
				return
			}
		}(i)
	}

	wg.Wait()

	for _, status := range statuses {
		if status.lastStageError != nil {
			return status.lastStageError
		}
	}

	return nil
}

// handleReboot schedules maintenance and reboots the node if maintenance is allowed
// Before rebooting the node, strips LastAppliedState annotations from all devices
// returns true if requeue of the reconcile request is required, false otherwise
// return err if encountered an error while performing maintenance scheduling / reboot
func (r *NicDeviceReconciler) handleReboot(ctx context.Context, statuses nicDeviceConfigurationStatuses) (ctrl.Result, error) {
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
		// Maintenance not yet allowed, waiting until then
		return ctrl.Result{RequeueAfter: requeueTime}, nil
	}

	// We need to strip last applied state annotation before reboot as it resets the runtime configuration
	err = r.stripLastAppliedStateAnnotations(ctx, statuses)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.MaintenanceManager.Reboot()
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// stripLastAppliedStateAnnotations deletes the consts.LastAppliedStateAnnotation from each device in parallel
// returns error if at least one annotation update failed
func (r *NicDeviceReconciler) stripLastAppliedStateAnnotations(ctx context.Context, statuses nicDeviceConfigurationStatuses) error {
	var wg sync.WaitGroup

	for i := 0; i < len(statuses); i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			status := statuses[index]
			status.lastStageError = nil

			annotations := status.device.GetAnnotations()
			if _, found := annotations[consts.LastAppliedStateAnnotation]; !found {
				return
			}

			delete(annotations, consts.LastAppliedStateAnnotation)
			status.device.SetAnnotations(annotations)
			status.lastStageError = r.Update(ctx, status.device)
		}(i)
	}

	wg.Wait()

	for _, status := range statuses {
		if status.lastStageError != nil {
			return status.lastStageError
		}
	}

	return nil
}

// applyNvConfig applies each device's non-volatile spec in parallel
// if update is correct, applies status condition PendingReboot, otherwise NonVolatileConfigUpdateFailed
// sets rebootRequired flags for each device's configuration status
// if status.nvConfigUpdateRequired == false, skips the device
// returns nil if all devices' config updates were successful, error otherwise
func (r *NicDeviceReconciler) applyNvConfig(ctx context.Context, statuses nicDeviceConfigurationStatuses) error {
	var wg sync.WaitGroup

	for i := 0; i < len(statuses); i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			status := statuses[index]
			status.lastStageError = nil
			if !status.nvConfigUpdateRequired {
				return
			}

			rebootRequired, err := r.HostManager.ApplyDeviceNvSpec(ctx, statuses[index].device)
			if err != nil {
				statuses[index].lastStageError = err
				if types.IsIncorrectSpecError(err) {
					err = r.updateDeviceStatusCondition(ctx, status.device, consts.IncorrectSpecReason, metav1.ConditionFalse, err.Error())
					if err != nil {
						log.Log.Error(err, "failed to update device status condition", "device", status.device.Name)
					}
				} else {
					err = r.updateDeviceStatusCondition(ctx, status.device, consts.NonVolatileConfigUpdateFailedReason, metav1.ConditionFalse, err.Error())
					if err != nil {
						log.Log.Error(err, "failed to update device status condition", "device", status.device.Name)
					}
				}
			}
			err = r.updateDeviceStatusCondition(ctx, status.device, consts.PendingRebootReason, metav1.ConditionTrue, "")
			if err != nil {
				status.lastStageError = err
			}

			statuses[index].rebootRequired = rebootRequired
		}(i)
	}

	wg.Wait()

	for _, status := range statuses {
		if status.lastStageError != nil {
			return status.lastStageError
		}
	}

	return nil
}

// handleSpecValidation validates each device's spec in parallel
// if spec is correct, applies status condition UpdateStarted, otherwise IncorrectSpec
// sets nvConfigUpdateRequired and rebootRequired flags for each device's configuration status
// returns nil if all devices' specs are correct, error otherwise
func (r *NicDeviceReconciler) handleSpecValidation(ctx context.Context, statuses nicDeviceConfigurationStatuses) error {
	var wg sync.WaitGroup

	for i := 0; i < len(statuses); i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			status := statuses[index]

			nvConfigUpdateRequired, rebootRequired, err := r.HostManager.ValidateDeviceNvSpec(ctx, status.device)
			log.Log.V(2).Info("nv spec validation complete for device", "device", status.device.Name, "nvConfigUpdateRequired", nvConfigUpdateRequired, "rebootRequired", rebootRequired)
			if err != nil {
				log.Log.Error(err, "failed to validate spec for device", "device", status.device.Name)
				status.lastStageError = err
				if types.IsIncorrectSpecError(err) {
					err = r.updateDeviceStatusCondition(ctx, status.device, consts.IncorrectSpecReason, metav1.ConditionFalse, err.Error())
					if err != nil {
						log.Log.Error(err, "failed to update device status condition", "device", status.device.Name)
					}
				} else {
					err = r.updateDeviceStatusCondition(ctx, status.device, consts.SpecValidationFailed, metav1.ConditionFalse, err.Error())
					if err != nil {
						log.Log.Error(err, "failed to update device status condition", "device", status.device.Name)
					}
				}
			}
			if nvConfigUpdateRequired {
				log.Log.V(2).Info("update started for device", "device", status.device.Name)
				err = r.updateDeviceStatusCondition(ctx, status.device, consts.UpdateStartedReason, metav1.ConditionTrue, "")
				if err != nil {
					status.lastStageError = err
				}
			}

			status.nvConfigUpdateRequired = nvConfigUpdateRequired
			status.rebootRequired = rebootRequired
		}(i)
	}

	wg.Wait()

	for _, status := range statuses {
		if status.lastStageError != nil {
			return status.lastStageError
		}
	}

	return nil
}

func (r *NicDeviceReconciler) updateDeviceStatusCondition(ctx context.Context, device *v1alpha1.NicDevice, reason string, status metav1.ConditionStatus, message string) error {
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
		maintenanceEventHandler := handler.Funcs{
			// We only want status update events
			UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				nm := e.ObjectNew.(*maintenanceoperator.NodeMaintenance)

				if nm.Spec.RequestorID != consts.MaintenanceRequestor || nm.Spec.NodeName != r.NodeName {
					// We want to skip event from maintenance not on the current node or not scheduled by us
					return
				}

				log.Log.Info("Enqueuing sync for maintenance update event", "resource", e.ObjectNew.GetName())
				qHandler(q)
			},
		}

		controller.Watches(&maintenanceoperator.NodeMaintenance{}, maintenanceEventHandler)
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

	log.Log.V(2).Info("nv config change required for some devices")
	return nvConfigUpdateRequiredForSome
}
