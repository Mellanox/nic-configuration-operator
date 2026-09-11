/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES
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

package configuration

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/spectrumx"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

const (
	spectrumXNVConfigPhaseBreakout     = "breakout"
	spectrumXNVConfigPhasePostBreakout = "post-breakout"
)

type spectrumXNVConfigUtils interface {
	ValidateNvConfigXPaths(
		ctx context.Context,
		ports []v1alpha1.NicDevicePortSpec,
		operations []dmscli.XPathOperation,
	) (updateNeeded, rebootNeeded bool, err error)
	SetNvConfigParametersBatchWithXPaths(
		ctx context.Context,
		primaryPort v1alpha1.NicDevicePortSpec,
		portCount int,
		params map[string]string,
		operations []dmscli.XPathOperation,
		withDefault bool,
		force bool,
	) (types.ApplyStatus, error)
}

func (h configurationManager) preparedSpectrumXPlan(
	device *v1alpha1.NicDevice,
	stage spectrumx.PlanStage,
) (*spectrumx.Plan, error) {
	if !spectrumXEnabled(device) {
		return nil, nil
	}
	if h.spectrumXConfigManager == nil {
		return nil, fmt.Errorf(
			"matching doSPCX %s plan is required for device %q: Spectrum-X manager is not configured",
			stage, device.Name)
	}
	plan, err := h.spectrumXConfigManager.GetPreparedPlan(device, stage)
	if err != nil {
		return nil, fmt.Errorf("matching doSPCX %s plan is required for device %q: %w", stage, device.Name, err)
	}
	return plan, nil
}

func (h configurationManager) spectrumXNVConfigUtils(device *v1alpha1.NicDevice) (spectrumXNVConfigUtils, error) {
	utils, ok := h.nvConfigUtils.(spectrumXNVConfigUtils)
	if !ok {
		return nil, fmt.Errorf(
			"doSPCX NVConfig is not supported by the configured NVConfig utility for device %q", device.Name)
	}
	return utils, nil
}

func (h configurationManager) preparedSpectrumXNVConfig(
	device *v1alpha1.NicDevice,
) (*spectrumx.Plan, spectrumXNVConfigUtils, error) {
	plan, err := h.preparedSpectrumXPlan(device, spectrumx.PlanStagePrepare)
	if err != nil || plan == nil {
		return plan, nil, err
	}
	utils, err := h.spectrumXNVConfigUtils(device)
	return plan, utils, err
}

// validateSpectrumXNVConfigCompatibility rejects combinations whose native
// NVConfig ownership cannot yet be reconciled with typed doSPCX operations.
//
// TODO(dospcx-nvconfig): HIGH PRIORITY -- restore rawNvConfig and Network Bay
// support ASAP once DMS can validate their combined native/typed state.
func validateSpectrumXNVConfigCompatibility(device *v1alpha1.NicDevice) error {
	if !spectrumXEnabled(device) || device.Spec.Configuration.ResetToDefault {
		return nil
	}
	template := device.Spec.Configuration.Template
	if len(template.RawNvConfig) > 0 {
		return types.IncorrectSpecError(
			"rawNvConfig cannot currently be combined with spectrumXOptimized")
	}
	if template.NetworkBay != nil {
		return types.IncorrectSpecError(
			"networkBay cannot currently be combined with spectrumXOptimized")
	}
	return nil
}

func (h configurationManager) spectrumXNVConfigPhase(
	ctx context.Context,
	device *v1alpha1.NicDevice,
	plan *spectrumx.Plan,
) (phase string, updateNeeded, rebootNeeded bool, err error) {
	utils, err := h.spectrumXNVConfigUtils(device)
	if err != nil {
		return "", false, false, err
	}
	validate := func(phase string, operations []dmscli.XPathOperation) (string, bool, bool, error) {
		log.FromContext(ctx).V(2).Info("validating doSPCX NVConfig phase",
			"device", device.Name,
			"phase", phase,
			"operations", len(operations),
			"ports", len(device.Status.Ports))
		updateNeeded, rebootNeeded, err := utils.ValidateNvConfigXPaths(
			ctx, device.Status.Ports, operations)
		if err != nil {
			return "", false, false, fmt.Errorf(
				"validate doSPCX %s NVConfig for device %q: %w", phase, device.Name, err)
		}
		return phase, updateNeeded, rebootNeeded, nil
	}

	if len(plan.Breakout) > 0 {
		phase, updateNeeded, rebootNeeded, err = validate(spectrumXNVConfigPhaseBreakout, plan.Breakout)
		if err != nil || rebootNeeded {
			return phase, updateNeeded, rebootNeeded, err
		}
	}
	return validate(spectrumXNVConfigPhasePostBreakout, plan.PostBreakout)
}

// verifySpectrumXSecondaryNVConfigStaged prevents a successful primary-target
// apply from hiding drift on split PCI functions. DMS currently executes one
// mlxconfig command on the primary BDF, so its success response alone cannot
// prove that every secondary function's pending state converged.
func (h configurationManager) verifySpectrumXSecondaryNVConfigStaged(
	ctx context.Context,
	device *v1alpha1.NicDevice,
	utils spectrumXNVConfigUtils,
	desiredParams map[string]string,
	typedOperations []dmscli.XPathOperation,
) error {
	if len(device.Status.Ports) < 2 {
		return nil
	}
	secondaryPorts := device.Status.Ports[1:]
	logger := log.FromContext(ctx)
	logger.V(2).Info("verifying doSPCX NVConfig pending state on secondary PCI functions",
		"device", device.Name,
		"secondaryFunctions", len(secondaryPorts),
		"nativeParameters", len(desiredParams),
		"typedOperations", len(typedOperations))

	if len(typedOperations) > 0 {
		updateNeeded, _, err := utils.ValidateNvConfigXPaths(ctx, secondaryPorts, typedOperations)
		if err != nil {
			return fmt.Errorf("verify typed NVConfig on secondary PCI functions: %w", err)
		}
		if updateNeeded {
			return fmt.Errorf("DMS primary-target apply did not stage typed NVConfig on every secondary PCI function")
		}
	}
	if len(desiredParams) == 0 {
		return nil
	}

	for _, port := range secondaryPorts {
		nvConfig, err := h.nvConfigUtils.QueryNvConfig(ctx, port, nil)
		if err != nil {
			return fmt.Errorf("verify native NVConfig on secondary PCI function %q: %w", port.PCI, err)
		}
		updateNeeded, _, _ := validateTemplateParamsApplied(
			map[string]types.NvConfigQuery{port.PCI: nvConfig}, desiredParams)
		if updateNeeded {
			return fmt.Errorf(
				"DMS primary-target apply did not stage native NVConfig on secondary PCI function %q", port.PCI)
		}
	}
	return nil
}
