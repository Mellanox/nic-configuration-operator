// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// pendingRuntimeState records an attempt, not a successful apply. Keeping it
// durable before touching hardware preserves the reboot baseline if the daemon
// stops or the API fails after the first (possibly partial) runtime apply.
const pendingRuntimeStateAnnotation = "pendingRuntimeState"

func (statuses nicDeviceConfigurationStatuses) runtimeAttemptPending() bool {
	for _, status := range statuses {
		if status.device.Annotations[pendingRuntimeStateAnnotation] != "" {
			return true
		}
	}
	return false
}

type runtimeAttempt struct {
	SpecDigest     string `json:"specDigest"`
	BootID         string `json:"bootID"`
	RebootRequired bool   `json:"rebootRequired,omitempty"`
}

func runtimeSpecDigest(spec string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(spec)))
}

func (r *NicDeviceReconciler) currentBootID() (string, error) {
	if r.readBootID != nil {
		return r.readBootID()
	}
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("read host boot ID: %w", err)
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", fmt.Errorf("host boot ID is empty")
	}
	return id, nil
}

func decodeRuntimeAttempt(value string) (*runtimeAttempt, error) {
	attempt := &runtimeAttempt{}
	if err := json.Unmarshal([]byte(value), attempt); err != nil {
		return nil, fmt.Errorf("decode pending runtime state: %w", err)
	}
	if attempt.BootID == "" {
		return nil, fmt.Errorf("pending runtime state has no boot ID")
	}
	if len(attempt.SpecDigest) != sha256.Size*2 {
		return nil, fmt.Errorf("pending runtime state has invalid spec digest")
	}
	return attempt, nil
}

// A successful shutdown request is not evidence of a reboot. Retain the attempt
// until the kernel boot ID changes, including across daemon restarts/API failures.
func (r *NicDeviceReconciler) clearRuntimeAttemptsAfterBoot(ctx context.Context, statuses nicDeviceConfigurationStatuses) error {
	for _, status := range statuses {
		expected := status.device
		value, found := expected.Annotations[pendingRuntimeStateAnnotation]
		if !found {
			continue
		}
		attempt, err := decodeRuntimeAttempt(value)
		if err != nil {
			return err
		}
		bootID, err := r.currentBootID()
		if err != nil {
			return err
		}
		if bootID == attempt.BootID {
			continue
		}
		err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
			current := &v1alpha1.NicDevice{}
			if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(expected), current); err != nil {
				return err
			}
			if !sameDeviceInputs(expected, current) || !sameRuntimeHistory(expected, current) {
				return fmt.Errorf("device %q changed while observing reboot", expected.Name)
			}
			base := current.DeepCopy()
			delete(current.Annotations, pendingRuntimeStateAnnotation)
			delete(current.Annotations, consts.LastAppliedStateAnnotation)
			if err := r.Patch(ctx, current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
				return err
			}
			// This refresh occurs before validation captures immutable inputs.
			status.device = current
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// runtimeReconcileState retains the node inputs used for NV validation. A new
// reconcile must validate again if discovery, desired state, or the prepared plan
// changes. Conditions and unrelated metadata do not invalidate hardware work.
type runtimeReconcileState struct {
	devices map[client.ObjectKey]*v1alpha1.NicDevice
	plans   map[client.ObjectKey]string
}

func (r *NicDeviceReconciler) reconcileRuntimeConfiguration(ctx context.Context, statuses nicDeviceConfigurationStatuses, planDevices []*v1alpha1.NicDevice, state *runtimeReconcileState) (ctrl.Result, error) {
	// A missing configure target must not trigger a spec-change reboot once NVConfig is ready.
	if planErr := r.validateSpectrumXConfigurePlans(planDevices); planErr != nil {
		planErr = fmt.Errorf("doSPCX configure plan is not ready: %w", planErr)
		statusErr := r.updateSpectrumXPlanFailureStatus(ctx, planDevices, consts.SpecValidationFailed, planErr)
		return ctrl.Result{}, errors.Join(planErr, statusErr)
	}
	log.Log.Info("applying runtime config")
	if _, err := r.validateRuntimeState(ctx, state); err != nil {
		return ctrl.Result{}, err
	}
	if err := runInParallel(ctx, statuses, func(ctx context.Context, status *nicDeviceConfigurationStatus) error {
		return r.applyRuntimeConfig(ctx, status, state)
	}); err != nil {
		return ctrl.Result{}, err
	}
	current, err := r.validateRuntimeState(ctx, state)
	if err != nil {
		return ctrl.Result{}, err
	}
	for _, status := range statuses {
		if status.runtimeApplied && !sameRuntimeHistory(status.device, current[client.ObjectKeyFromObject(status.device)]) {
			return ctrl.Result{}, fmt.Errorf("device %q applied history changed before completion", status.device.Name)
		}
	}
	if statuses.rebootRequired() {
		return r.handleReboot(ctx, statuses)
	}
	log.Log.Info("all configurations are successful, releasing maintenance")
	if err := r.MaintenanceManager.ReleaseMaintenance(ctx); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: longRequeueTime * 20}, nil
}

func (r *NicDeviceReconciler) captureRuntimeState(statuses nicDeviceConfigurationStatuses, planDevices []*v1alpha1.NicDevice) (*runtimeReconcileState, error) {
	state := &runtimeReconcileState{
		devices: make(map[client.ObjectKey]*v1alpha1.NicDevice, len(statuses)),
		plans:   make(map[client.ObjectKey]string, len(planDevices)),
	}
	for _, status := range statuses {
		state.devices[client.ObjectKeyFromObject(status.device)] = status.device.DeepCopy()
	}
	for _, device := range planDevices {
		plan, err := r.SpectrumXManager.GetPreparedPlan(device)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(plan)
		if err != nil {
			return nil, err
		}
		state.plans[client.ObjectKeyFromObject(device)] = string(encoded)
	}
	return state, nil
}

func sameDeviceInputs(expected, current *v1alpha1.NicDevice) bool {
	if expected.UID != current.UID || expected.Generation != current.Generation ||
		!current.DeletionTimestamp.IsZero() || !reflect.DeepEqual(expected.Spec, current.Spec) {
		return false
	}
	expectedStatus, currentStatus := expected.Status, current.Status
	expectedStatus.Conditions, currentStatus.Conditions = nil, nil
	return reflect.DeepEqual(expectedStatus, currentStatus)
}

func deviceNeedsReconcile(device *v1alpha1.NicDevice) bool {
	return device.Spec.Configuration != nil || device.Spec.Firmware != nil || device.Spec.InterfaceNameTemplate != nil ||
		device.Annotations[pendingRuntimeStateAnnotation] != ""
}

// validateRuntimeState uses the uncached reader: retrying against an informer
// snapshot would repeatedly conflict, and could acknowledge obsolete inputs.
// Listing the active set also detects added/removed peers of node-scoped plans.
func (r *NicDeviceReconciler) validateRuntimeState(ctx context.Context, state *runtimeReconcileState) (map[client.ObjectKey]*v1alpha1.NicDevice, error) {
	devices := &v1alpha1.NicDeviceList{}
	// Match getDevices across namespaces, using server-side node selection when
	// supported. Older installed CRDs require the same fallback as the daemon cache.
	err := r.APIReader.List(ctx, devices, client.MatchingFields{"status.node": r.NodeName})
	if apierrors.IsBadRequest(err) {
		err = r.APIReader.List(ctx, devices)
	}
	if err != nil {
		return nil, err
	}
	current := make(map[client.ObjectKey]*v1alpha1.NicDevice, len(state.devices))
	for i := range devices.Items {
		device := &devices.Items[i]
		if device.Status.Node != r.NodeName {
			continue
		}
		if !deviceNeedsReconcile(device) {
			continue
		}
		key := client.ObjectKeyFromObject(device)
		expected, found := state.devices[key]
		if !found || !sameDeviceInputs(expected, device) {
			return nil, fmt.Errorf("device %q inputs changed during configuration; revalidation required", device.Name)
		}
		if expectedPlan, found := state.plans[key]; found {
			plan, err := r.SpectrumXManager.GetPreparedPlan(device)
			if err != nil {
				return nil, err
			}
			if plan == nil || plan.ConfigureError != nil {
				return nil, fmt.Errorf("device %q configure plan is no longer ready", device.Name)
			}
			encoded, err := json.Marshal(plan)
			if err != nil {
				return nil, err
			}
			if string(encoded) != expectedPlan {
				return nil, fmt.Errorf("device %q prepared plan changed during configuration", device.Name)
			}
		}
		current[key] = device
	}
	if len(current) != len(state.devices) {
		return nil, fmt.Errorf("node device set changed during configuration; revalidation required")
	}
	return current, nil
}

func sameRuntimeHistory(expected, current *v1alpha1.NicDevice) bool {
	for _, key := range []string{consts.LastAppliedStateAnnotation, pendingRuntimeStateAnnotation} {
		previous, hadPrevious := expected.Annotations[key]
		actual, hasActual := current.Annotations[key]
		if hadPrevious != hasActual || previous != actual {
			return false
		}
	}
	return true
}

func (r *NicDeviceReconciler) recordRuntimeAttempt(ctx context.Context, applied *v1alpha1.NicDevice, spec string) error {
	bootID, err := r.currentBootID()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(runtimeAttempt{SpecDigest: runtimeSpecDigest(spec), BootID: bootID})
	if err != nil {
		return err
	}
	value := string(encoded)
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &v1alpha1.NicDevice{}
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(applied), current); err != nil {
			return err
		}
		if !sameDeviceInputs(applied, current) || !sameRuntimeHistory(applied, current) {
			return fmt.Errorf("device %q inputs or applied history changed before runtime configuration", applied.Name)
		}
		if current.Annotations[pendingRuntimeStateAnnotation] == value {
			return nil
		}
		base := current.DeepCopy()
		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		current.Annotations[pendingRuntimeStateAnnotation] = value
		return r.Patch(ctx, current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	})
	if err == nil {
		if applied.Annotations == nil {
			applied.Annotations = make(map[string]string)
		}
		applied.Annotations[pendingRuntimeStateAnnotation] = value
	}
	return err
}

// persistAppliedSpec records history even if desired state changed while runtime
// A was applied. Otherwise the first A -> B change could lose its reboot baseline.
// This write alone never acknowledges B or permits maintenance release.
func (r *NicDeviceReconciler) persistAppliedSpec(ctx context.Context, applied *v1alpha1.NicDevice, spec string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &v1alpha1.NicDevice{}
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(applied), current); err != nil {
			return err
		}
		if current.UID != applied.UID || !current.DeletionTimestamp.IsZero() {
			return fmt.Errorf("device %q was deleted or replaced during runtime configuration", applied.Name)
		}
		actual, hasActual := current.Annotations[consts.LastAppliedStateAnnotation]
		_, pending := current.Annotations[pendingRuntimeStateAnnotation]
		if hasActual && actual == spec && !pending {
			return nil
		}
		if !sameRuntimeHistory(applied, current) {
			return fmt.Errorf("device %q applied history changed during runtime configuration", applied.Name)
		}
		base := current.DeepCopy()
		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		current.Annotations[consts.LastAppliedStateAnnotation] = spec
		delete(current.Annotations, pendingRuntimeStateAnnotation)
		return r.Patch(ctx, current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	})
}

func (r *NicDeviceReconciler) persistRuntimeCondition(ctx context.Context, applied *v1alpha1.NicDevice, state *runtimeReconcileState, condition metav1.Condition, completedSpec string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		devices, err := r.validateRuntimeState(ctx, state)
		if err != nil {
			return err
		}
		current := devices[client.ObjectKeyFromObject(applied)]
		if completedSpec == "" && !sameRuntimeHistory(applied, current) {
			return fmt.Errorf("device %q applied history changed before reboot", applied.Name)
		}
		if completedSpec != "" && (current.Annotations[consts.LastAppliedStateAnnotation] != completedSpec ||
			current.Annotations[pendingRuntimeStateAnnotation] != "") {
			return fmt.Errorf("device %q applied history changed before completion", applied.Name)
		}
		if !meta.SetStatusCondition(&current.Status.Conditions, condition) {
			return nil
		}
		return r.Status().Update(ctx, current)
	})
}

func (r *NicDeviceReconciler) prepareRuntimeStateForReboot(ctx context.Context, status *nicDeviceConfigurationStatus) error {
	expected := status.device
	bootID, err := r.currentBootID()
	if err != nil {
		return err
	}
	spec, err := json.Marshal(expected.Spec)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(runtimeAttempt{SpecDigest: runtimeSpecDigest(string(spec)), BootID: bootID, RebootRequired: true})
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current := &v1alpha1.NicDevice{}
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(expected), current); err != nil {
			return err
		}
		if !sameDeviceInputs(expected, current) {
			return fmt.Errorf("device %q inputs changed before reboot", expected.Name)
		}
		if !sameRuntimeHistory(expected, current) {
			return fmt.Errorf("device %q applied history changed before reboot", expected.Name)
		}
		base := current.DeepCopy()
		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		delete(current.Annotations, consts.LastAppliedStateAnnotation)
		current.Annotations[pendingRuntimeStateAnnotation] = string(encoded)
		return r.Patch(ctx, current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	})
}
