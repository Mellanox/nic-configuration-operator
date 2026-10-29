// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	dmsconfiguration "github.com/Mellanox/dms-lib/pkg/configuration"
	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/configuration"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func planBlockerError(blockers []dmsconfiguration.Blocker, failure error) error {
	failures := make([]error, 0, 1+len(blockers))
	failures = append(failures, failure)
	for _, blocker := range blockers {
		failures = append(failures, fmt.Errorf("%s: %s", blocker.Reason, blocker.Message))
	}
	return errors.Join(failures...)
}

// validatePlanNV checks every partition before the controller enters its existing
// maintenance/apply phase. The library validates plan projection and Bay closure.
func (r *NicDeviceReconciler) validatePlanNV(ctx context.Context, groups []*planConfigurationGroup) error {
	var failures []error
	for _, group := range groups {
		// Ordinary force historically reapplies native values even when pending
		// already matches. Keep that bounded NV compatibility path; plan Force
		// retains the library's dependency-policy semantics.
		if group.nv.Plan == nil && group.options.Force {
			for _, status := range group.members {
				failures = append(failures, r.handleConfigurationSpecValidation(ctx, status))
			}
			continue
		}
		var result dmsconfiguration.Validation
		var err error
		if group.nv.Plan == nil && group.nv.NetworkBay == nil {
			result, err = configuration.ValidateOrdinaryNV(ctx, r.PlanConfiguration, group.nv, group.options)
		} else {
			result, err = r.PlanConfiguration.ValidateNVConfiguration(ctx, group.nv, group.options)
		}
		failures = append(failures, err)
		seen := map[string]bool{}
		for _, row := range result.Devices {
			status, exists := group.members[row.DeviceID]
			if !exists || seen[row.DeviceID] {
				failures = append(failures, fmt.Errorf("invalid NV validation result identity %q", row.DeviceID))
				continue
			}
			seen[row.DeviceID] = true
			failure := planBlockerError(row.Blockers, row.Err)
			failures = append(failures, r.recordNVValidation(ctx, status, row.UpdateNeeded, row.RebootNeeded, row.UnsupportedParameters, failure))
		}
		for id, status := range group.members {
			if !seen[id] {
				failures = append(failures, r.recordNVValidation(ctx, status, false, false, nil, fmt.Errorf("missing NV validation result for %s", id)))
			}
		}
	}
	return errors.Join(failures...)
}

func (r *NicDeviceReconciler) applyPlanNV(ctx context.Context, groups []*planConfigurationGroup) (bool, error) {
	reset := false
	for _, group := range groups {
		required := false
		for _, status := range group.members {
			required = required || status.nvConfigUpdateRequired
		}
		if !required {
			continue
		}
		if group.nv.Plan == nil && group.options.Force {
			for _, observed := range group.nv.Devices {
				status := group.members[observed.ID]
				if err := r.applyNvConfig(ctx, status); err != nil {
					return reset, err
				}
				// The compatibility helper logs reset failures itself. Rediscover
				// after any attempted reset before reusing captured runtime bindings.
				reset = reset || status.nvConfigUpdateRequired && os.Getenv(consts.FEATURE_GATE_FW_RESET_AFTER_CONFIG_UPDATE) == consts.LabelValueTrue
			}
			continue
		}
		var result dmsconfiguration.Result
		var err error
		if group.nv.Plan == nil && group.nv.NetworkBay == nil {
			result, err = configuration.ApplyOrdinaryNV(ctx, r.PlanConfiguration, group.nv, group.options)
		} else {
			result, err = r.PlanConfiguration.ApplyNVConfiguration(ctx, group.nv, group.options)
		}
		if failure := r.recordPlanResults(ctx, group, result, err, false); failure != nil {
			return reset, failure
		}
		if os.Getenv(consts.FEATURE_GATE_FW_RESET_AFTER_CONFIG_UPDATE) == consts.LabelValueTrue {
			for _, observed := range group.nv.Devices {
				status := group.members[observed.ID]
				if err := r.ConfigurationManager.ResetNicFirmware(ctx, status.device); err != nil {
					log.FromContext(ctx).Error(err, "failed to reset NIC firmware before reboot", "device", status.device.Name)
				} else {
					reset = true
				}
			}
		}
	}
	return reset, nil
}

func (r *NicDeviceReconciler) applyPlanRuntime(ctx context.Context, groups []*planConfigurationGroup) error {
	var failures []error
	for _, group := range groups {
		if group.runtime.Plan != nil {
			failures = append(failures, r.applyRuntimeConfigurationGroup(ctx, group))
			continue
		}
		// NetworkBay keeps its paired NV scope, but ordinary runtime settings and
		// carrier/reboot gates belong to each ASIC independently.
		for _, observed := range group.runtime.Devices {
			request := group.runtime
			request.Devices = []device.Device{observed}
			request.Ports = nil
			for _, intent := range group.runtime.Ports {
				if slices.ContainsFunc(observed.Ports, func(port device.Port) bool { return port.PCI == intent.PCI }) {
					request.Ports = append(request.Ports, intent)
				}
			}
			member := &planConfigurationGroup{
				members: map[string]*nicDeviceConfigurationStatus{observed.ID: group.members[observed.ID]},
				runtime: request,
			}
			failures = append(failures, r.applyRuntimeConfigurationGroup(ctx, member))
		}
	}
	return errors.Join(failures...)
}

func (r *NicDeviceReconciler) applyRuntimeConfigurationGroup(ctx context.Context, group *planConfigurationGroup) error {
	var failures []error

	deferRuntime := false
	for _, observed := range group.runtime.Devices {
		status := group.members[observed.ID]
		if status.rebootRequired {
			deferRuntime = true
			continue
		}
		reboot, err := r.runtimeSpecReboot(ctx, status)
		failures = append(failures, err)
		deferRuntime = deferRuntime || reboot || err != nil
		if !reboot && err == nil && group.runtime.Plan == nil {
			check := r.CheckRuntimePrerequisites
			if check == nil {
				failures = append(failures, r.recordRuntimeResult(ctx, status, fmt.Errorf("runtime prerequisite observation is not initialized")))
				deferRuntime = true
				continue
			}
			if err := check(status.device); err != nil {
				failures = append(failures, r.recordRuntimeResult(ctx, status, err))
				deferRuntime = true
			}
		}
	}
	if deferRuntime {
		return errors.Join(failures...)
	}
	result, err := r.applyPlanRuntimeGroup(ctx, group)
	failures = append(failures, r.recordPlanResults(ctx, group, result, err, true))
	return errors.Join(failures...)
}

// recordPlanResults consumes every device row before returning aggregate errors.
// In particular, a partial apply never loses its activation requirement.
func (r *NicDeviceReconciler) recordPlanResults(ctx context.Context, group *planConfigurationGroup, result dmsconfiguration.Result, aggregate error, runtime bool) error {
	failures := []error{aggregate}
	seen := map[string]bool{}
	for _, row := range result.Devices {
		status, exists := group.members[row.DeviceID]
		if !exists || seen[row.DeviceID] {
			failures = append(failures, fmt.Errorf("invalid configuration result identity %q", row.DeviceID))
			continue
		}
		seen[row.DeviceID] = true
		status.rebootRequired = status.rebootRequired || row.RebootNeeded
		for _, parameter := range row.UnsupportedParameters {
			if !slices.Contains(status.unsupportedNvParams, parameter) {
				status.unsupportedNvParams = append(status.unsupportedNvParams, parameter)
			}
		}
		failure := planBlockerError(row.Blockers, row.Err)
		if failure == nil && row.Status != dmsconfiguration.Applied && row.Status != dmsconfiguration.Unchanged {
			failure = errors.Join(failure, fmt.Errorf("device %s configuration %s (reboot required: %t)", row.DeviceID, row.Status, row.RebootNeeded))
		}
		if runtime {
			failures = append(failures, r.recordRuntimeResult(ctx, status, failure))
		} else if failure != nil {
			failures = append(failures, failure, r.updateConfigInProgressStatusCondition(ctx, status.device, consts.NonVolatileConfigUpdateFailedReason, metav1.ConditionFalse, failure.Error()))
		} else if row.RebootNeeded {
			failures = append(failures, r.updateConfigInProgressStatusCondition(ctx, status.device, consts.PendingRebootReason, metav1.ConditionTrue, ""))
		}
	}
	for id, status := range group.members {
		if seen[id] {
			continue
		}
		failure := fmt.Errorf("missing configuration result for %s", id)
		reason := consts.NonVolatileConfigUpdateFailedReason
		if runtime {
			reason = consts.RuntimeConfigUpdateFailedReason
		}
		failures = append(failures, failure, r.updateConfigInProgressStatusCondition(ctx, status.device, reason, metav1.ConditionFalse, failure.Error()))
	}
	return errors.Join(failures...)
}

// applyPlanRuntimeGroup retains the existing CC process owner and startup order.
// Apply owns native observation setup and convergence. Only an absent process
// requires ordered replay and startup at the compiled CC group boundary.
func (r *NicDeviceReconciler) applyPlanRuntimeGroup(ctx context.Context, group *planConfigurationGroup) (dmsconfiguration.Result, error) {
	request := group.runtime
	if request.Plan == nil {
		return r.PlanConfiguration.ApplyRuntimeConfiguration(ctx, request)
	}
	ports, err := request.Plan.RuntimeGroupPorts("cc", request.Devices)
	if err != nil {
		return dmsconfiguration.Result{}, err
	}
	if len(ports) == 0 {
		return r.PlanConfiguration.ApplyRuntimeConfiguration(ctx, request)
	}
	if r.SpectrumXManager == nil {
		return dmsconfiguration.Result{}, fmt.Errorf("Spectrum-X CC process manager is not initialized")
	}
	running := true
	for _, port := range ports {
		if strings.TrimSpace(port.RDMA) == "" {
			return dmsconfiguration.Result{}, fmt.Errorf("CC target %s has no RDMA interface", port.PCI)
		}
		running = running && r.SpectrumXManager.IsDocaSpcXCCRunning(port.RDMA)
	}
	if !running {
		request.BeforeGroup = map[string]dmsconfiguration.RuntimeGroupHook{"cc": r.startPlanCC}
	}
	return r.PlanConfiguration.ApplyRuntimeConfiguration(ctx, request)
}

func (r *NicDeviceReconciler) startPlanCC(ctx context.Context, _ string, ports []device.Port) error {
	started := map[string]bool{}
	for _, port := range ports {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.TrimSpace(port.RDMA) == "" {
			return fmt.Errorf("CC target %s has no RDMA interface", port.PCI)
		}
		if started[port.RDMA] {
			continue
		}
		if err := r.SpectrumXManager.RunDocaSpcXCC(v1alpha1.NicDevicePortSpec{PCI: port.PCI, NetworkInterface: port.Netdev, RdmaInterface: port.RDMA}); err != nil {
			return err
		}
		started[port.RDMA] = true
	}
	return ctx.Err()
}

func (r *NicDeviceReconciler) validatePlanConfigureReady(ctx context.Context, groups []*planConfigurationGroup) error {
	var failures []error
	for _, group := range groups {
		if group.runtime.Plan == nil || group.runtime.Plan.Info().RuntimeReady {
			continue
		}
		failure := fmt.Errorf("doSPCX configure plan is not ready; activate preparation and rebuild")
		failures = append(failures, failure)
		for _, status := range group.members {
			failures = append(failures, r.updateConfigInProgressStatusCondition(ctx, status.device, consts.SpecValidationFailed, metav1.ConditionFalse, failure.Error()))
		}
	}
	return errors.Join(failures...)
}
