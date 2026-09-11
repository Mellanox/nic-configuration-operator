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

package configuration

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/dms"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/nvconfig"
	"github.com/Mellanox/nic-configuration-operator/pkg/spectrumx"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

// ConfigurationManager contains logic for configuring NIC devices on the host
type ConfigurationManager interface {
	// ValidateDeviceNvSpec will validate device's non-volatile spec against already applied configuration on the host
	// returns bool - nv config update required
	// returns bool - reboot required
	// returns []string - sorted, deduped param names that are part of the desired spec but hidden on
	//   the device (absent from NextBootConfig). These cannot be applied and surface as PartiallyApplied
	//   in the final ConfigUpdateInProgress condition.
	// returns error - there are errors in device's spec
	ValidateDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, bool, []string, error)
	// ApplyNVConfiguration calculates device's missing nv spec configuration and applies it to the device on the host
	// returns *ConfigurationApplyResult - result of the apply operation
	// returns error - there were errors while applying nv configuration
	ApplyNVConfiguration(ctx context.Context, device *v1alpha1.NicDevice, options *types.ConfigurationOptions) (*types.ConfigurationApplyResult, error)
	// ApplyRuntimeConfiguration calculates device's missing runtime spec configuration and applies it to the device on the host
	// returns *RuntimeConfigurationApplyResult - result of the apply operation
	// returns error - there were errors while applying runtime configuration
	ApplyRuntimeConfiguration(ctx context.Context, device *v1alpha1.NicDevice) (*types.RuntimeConfigurationApplyResult, error)
	// ResetNicFirmware resets NIC's firmware
	// Operation can be long, required context to be able to terminate by timeout
	// IB devices need to communicate with other nodes for confirmation
	// return err - there were errors while resetting NIC firmware
	ResetNicFirmware(ctx context.Context, device *v1alpha1.NicDevice) error
}

type configurationManager struct {
	configurationUtils     ConfigurationUtils
	configValidation       configValidation
	nvConfigUtils          nvconfig.NVConfigUtils
	spectrumXConfigManager spectrumx.SpectrumXManager
}

// contextualNVConfigBatchSetter is an optional extension implemented by the built-in NVConfig
// utility. Keeping it separate preserves the public NVConfigUtils contract for library consumers.
type contextualNVConfigBatchSetter interface {
	SetNvConfigParametersBatchWithContext(
		ctx context.Context,
		port v1alpha1.NicDevicePortSpec,
		params map[string]string,
		withDefault bool,
		force bool,
	) (types.ApplyStatus, error)
}

// ValidateDeviceNvSpec will validate device's non-volatile spec against already applied configuration on the host
// returns bool - nv config update required
// returns bool - reboot required
// returns []string - desired params that are hidden on the device (absent from NextBootConfig); sorted, deduped
// returns error - there are errors in device's spec
// if fully matches in current and next config, returns false, false
// if fully matched next but not current, returns false, true
// if not fully matched next boot, returns true, true
func (h configurationManager) ValidateDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, bool, []string, error) {
	logger := log.FromContext(ctx)
	logger.Info("configurationManager.ValidateDeviceNvSpec", "device", device.Name)
	if err := validateSpectrumXNVConfigCompatibility(device); err != nil {
		return false, false, nil, err
	}

	// 1. Query current nv config for every port.
	nvConfigsForPorts, err := h.queryNvConfigs(ctx, device)
	if err != nil {
		logger.Error(err, "failed to query nv configs", "device", device.Name)
		return false, false, nil, err
	}
	firstPort := device.Status.Ports[0]
	firstPortConfig := nvConfigsForPorts[firstPort.PCI]

	// 2. Reset-to-default takes precedence over everything else.
	if device.Spec.Configuration.ResetToDefault {
		resetNeeded := false
		rebootNeeded := false
		for _, nvConfig := range nvConfigsForPorts {
			resetNeededForPort, rebootNeededForPort, err := h.configValidation.ValidateResetToDefault(nvConfig)
			if err != nil {
				logger.Error(err, "failed to validate reset to default", "device", device.Name)
				return false, false, nil, err
			}
			resetNeeded = resetNeeded || resetNeededForPort
			rebootNeeded = rebootNeeded || rebootNeededForPort
		}
		return resetNeeded, rebootNeeded, nil, nil
	}

	// 3. Network Bay system_conf: the params that don't match the requested named profile (empty for
	//    non-Network-Bay devices). set_system_conf is the lowest-priority baseline.
	systemConfMismatched, err := h.systemConfMismatchedParams(ctx, device)
	if err != nil {
		return false, false, nil, err
	}
	if len(systemConfMismatched) > 0 {
		logger.V(2).Info("system_conf params mismatched against the profile", "device", device.Name, "params", systemConfMismatched)
	}

	// 4. Existing native NVConfig validation remains independent from the doSPCX plan.
	//    Validated against the device's next boot, exactly like the apply path — so a value already staged
	//    for next boot but not yet rebooted reports reboot-required instead of looping.
	overrides, err := h.configValidation.ConstructNvParamMapFromTemplate(device, firstPortConfig)
	if err != nil {
		logger.Error(err, "failed to calculate desired nvconfig parameters", "device", device.Name)
		return false, false, nil, err
	}
	logger.V(2).Info("validating native NVConfig parameters", "device", device.Name, "params", overrides)

	configUpdateNeeded, rebootNeeded, unsupportedParams := validateTemplateParamsApplied(nvConfigsForPorts, overrides)
	logger.V(2).Info("native NVConfig validation complete",
		"device", device.Name,
		"configUpdateNeeded", configUpdateNeeded,
		"rebootNeeded", rebootNeeded,
		"unsupportedParams", unsupportedParams)
	if configUpdateNeeded {
		logger.Info("native NVConfig is not yet applied to next boot", "device", device.Name)
	}
	if len(unsupportedParams) > 0 {
		logger.Info("some native NVConfig parameters are unsupported on this device and will be skipped", "device", device.Name, "params", unsupportedParams)
	}

	// 5. system_conf coverage: a mismatched profile param not covered (range-aware) by the override config
	//    means the baseline itself drifted and set_system_conf must be re-applied (reboot-required).
	if systemConfDrifted(overrides, systemConfMismatched) {
		logger.Info("Network Bay system_conf drifted, set_system_conf re-apply required",
			"device", device.Name, "mismatched", systemConfMismatched)
		configUpdateNeeded = true
		rebootNeeded = true
	}

	// 6. Validate the prepared doSPCX NVConfig plan separately. Breakout is a
	// reboot barrier: post-breakout is considered only after breakout matches
	// both the current and pending device state.
	plan, err := h.preparedSpectrumXPlan(device, spectrumx.PlanStagePrepare)
	if err != nil {
		return false, false, unsupportedParams, err
	}
	if plan != nil {
		phase, planUpdateNeeded, planRebootNeeded, err := h.spectrumXNVConfigPhase(ctx, device, plan)
		if err != nil {
			return false, false, unsupportedParams, err
		}
		configUpdateNeeded = configUpdateNeeded || planUpdateNeeded
		rebootNeeded = rebootNeeded || planRebootNeeded
		logger.V(2).Info("doSPCX NVConfig phase validation complete",
			"device", device.Name,
			"phase", phase,
			"configUpdateNeeded", planUpdateNeeded,
			"rebootNeeded", planRebootNeeded)
	}

	logger.V(2).Info("nv spec validation result", "device", device.Name,
		"configUpdateNeeded", configUpdateNeeded, "rebootNeeded", rebootNeeded, "unsupportedParams", unsupportedParams)
	return configUpdateNeeded, rebootNeeded, unsupportedParams, nil
}

// validateTemplateParamsApplied checks the desired template + rawNvConfig params against every port.
// returns bool - nv config update required (a param's desired value is missing from next boot)
// returns bool - reboot required
// returns []string - desired params hidden on the device (absent from NextBootConfig); sorted, deduped
func validateTemplateParamsApplied(nvConfigsForPorts map[string]types.NvConfigQuery, desiredConfig map[string]string) (bool, bool, []string) {
	configUpdateNeeded := false
	rebootNeeded := false
	unsupportedSet := map[string]struct{}{}

	for _, nvConfig := range nvConfigsForPorts {
		for parameter, desiredValue := range desiredConfig {
			nextValues, foundInNextBoot := nvConfig.NextBootConfig[parameter]
			if !foundInNextBoot {
				// Param unsupported on this device (e.g. hidden because ADVANCED_PCI_SETTINGS is off,
				// or the variant simply doesn't expose it). Apply skips the same param and reports
				// ApplyStatusPartiallyApplied; record it here so the controller can surface
				// PartiallyApplied even when nothing else needs to change.
				unsupportedSet[parameter] = struct{}{}
				continue
			}
			if !slices.Contains(nextValues, strings.ToLower(desiredValue)) {
				configUpdateNeeded = true
				rebootNeeded = true
				continue
			}
			currentValues, foundInCurrent := nvConfig.CurrentConfig[parameter]
			if !foundInCurrent || !slices.Contains(currentValues, strings.ToLower(desiredValue)) {
				rebootNeeded = true
			}
		}
	}

	var unsupportedParams []string
	if len(unsupportedSet) > 0 {
		unsupportedParams = make([]string, 0, len(unsupportedSet))
		for name := range unsupportedSet {
			unsupportedParams = append(unsupportedParams, name)
		}
		sort.Strings(unsupportedParams)
	}

	return configUpdateNeeded, rebootNeeded, unsupportedParams
}

type nvConfigApplyDiff struct {
	changed     map[string]string
	unchanged   []string
	unsupported []string
}

func buildNVConfigApplyDiff(nvConfig types.NvConfigQuery, desiredConfig map[string]string, withDefault, force bool) nvConfigApplyDiff {
	diff := nvConfigApplyDiff{
		changed:     make(map[string]string, len(desiredConfig)),
		unchanged:   []string{},
		unsupported: []string{},
	}
	for param, value := range desiredConfig {
		if force {
			diff.changed[param] = value
			continue
		}
		nextValues, found := nvConfig.NextBootConfig[param]
		if !found {
			diff.unsupported = append(diff.unsupported, param)
			continue
		}
		if !withDefault && slices.Contains(nextValues, value) {
			diff.unchanged = append(diff.unchanged, param)
			continue
		}
		diff.changed[param] = value
	}
	sort.Strings(diff.unchanged)
	sort.Strings(diff.unsupported)
	return diff
}

func buildCombinedNVConfigApplyDiff(
	nvConfigsForPorts map[string]types.NvConfigQuery,
	desiredConfig map[string]string,
	withDefault bool,
	force bool,
	includeUnchanged bool,
) (map[string]string, bool) {
	changed := make(map[string]string, len(desiredConfig))
	hasUnsupported := false
	for _, nvConfig := range nvConfigsForPorts {
		diff := buildNVConfigApplyDiff(nvConfig, desiredConfig, withDefault || includeUnchanged, force)
		for name, value := range diff.changed {
			changed[name] = value
		}
		hasUnsupported = hasUnsupported || len(diff.unsupported) > 0
	}
	return changed, hasUnsupported
}

func (h configurationManager) applySpectrumXNVConfig(
	ctx context.Context,
	device *v1alpha1.NicDevice,
	plan *spectrumx.Plan,
	utils spectrumXNVConfigUtils,
	nvConfigsForPorts map[string]types.NvConfigQuery,
	desiredParams map[string]string,
	options *types.ConfigurationOptions,
) (*types.ConfigurationApplyResult, error) {
	phase := spectrumXNVConfigPhaseBreakout
	updateNeeded := false
	rebootNeeded := false
	typedOperations := make([]dmscli.XPathOperation, 0, len(plan.Breakout)+len(plan.PostBreakout))
	if options.Force {
		typedOperations = append(typedOperations, plan.Breakout...)
		typedOperations = append(typedOperations, plan.PostBreakout...)
	} else {
		var err error
		phase, updateNeeded, rebootNeeded, err = h.spectrumXNVConfigPhase(ctx, device, plan)
		if err != nil {
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
		if options.WithDefault && phase == spectrumXNVConfigPhasePostBreakout {
			typedOperations = append(typedOperations, plan.Breakout...)
			typedOperations = append(typedOperations, plan.PostBreakout...)
		} else if updateNeeded || options.WithDefault {
			if phase == spectrumXNVConfigPhaseBreakout {
				typedOperations = append(typedOperations, plan.Breakout...)
			} else {
				typedOperations = append(typedOperations, plan.PostBreakout...)
			}
		}
	}

	// DMS expands port-scoped typed mappings for every supplied port number in
	// one primary-PF command. Include the complete native desired state whenever
	// typed operations are staged so the combined operation is atomic.
	nativeUpdateNeeded, _, _ := validateTemplateParamsApplied(nvConfigsForPorts, desiredParams)
	batch, hasUnsupported := buildCombinedNVConfigApplyDiff(
		nvConfigsForPorts,
		desiredParams,
		options.WithDefault,
		options.Force,
		len(typedOperations) > 0)
	primaryPort := device.Status.Ports[0]
	log.FromContext(ctx).V(2).Info("combined doSPCX NVConfig apply diff",
		"device", device.Name,
		"phase", phase,
		"portCount", len(device.Status.Ports),
		"withDefault", options.WithDefault,
		"force", options.Force,
		"nativeUpdateNeeded", nativeUpdateNeeded,
		"nativeApplyCount", len(batch),
		"typedOperationCount", len(typedOperations),
		"hasUnsupported", hasUnsupported)
	status := types.ApplyStatusNothingToDo
	if hasUnsupported {
		status = types.ApplyStatusPartiallyApplied
	}
	if len(batch) == 0 && len(typedOperations) == 0 {
		return &types.ConfigurationApplyResult{Status: status, RebootRequired: rebootNeeded}, nil
	}

	applyStatus, err := utils.SetNvConfigParametersBatchWithXPaths(
		ctx,
		primaryPort,
		len(device.Status.Ports),
		batch,
		typedOperations,
		options.WithDefault,
		options.Force)
	if err != nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, fmt.Errorf(
			"apply combined doSPCX %s NVConfig for device %q: %w",
			phase, device.Name, err)
	}
	if applyStatus == types.ApplyStatusNothingToDo && !updateNeeded && !nativeUpdateNeeded {
		return &types.ConfigurationApplyResult{Status: status, RebootRequired: rebootNeeded}, nil
	}
	if applyStatus == types.ApplyStatusNothingToDo {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, fmt.Errorf(
			"apply combined doSPCX %s NVConfig for device %q did not stage the mismatched configuration",
			phase, device.Name)
	}
	if applyStatus != types.ApplyStatusSuccess {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, fmt.Errorf(
			"apply combined doSPCX %s NVConfig for device %q returned status %d",
			phase, device.Name, applyStatus)
	}
	if err := h.verifySpectrumXSecondaryNVConfigStaged(
		ctx, device, utils, desiredParams, typedOperations); err != nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, fmt.Errorf(
			"verify combined doSPCX %s NVConfig for device %q: %w",
			phase, device.Name, err)
	}
	if status != types.ApplyStatusPartiallyApplied {
		status = types.ApplyStatusSuccess
	}
	return &types.ConfigurationApplyResult{Status: status, RebootRequired: true}, nil
}

// ApplyNVConfiguration calculates device's missing nv spec configuration and applies it to the device on the host
// returns *ConfigurationApplyResult - result of the apply operation
// returns error - there were errors while applying nv configuration
func (h configurationManager) ApplyNVConfiguration(ctx context.Context, device *v1alpha1.NicDevice, options *types.ConfigurationOptions) (*types.ConfigurationApplyResult, error) {
	logger := log.FromContext(ctx)
	logger.Info("configurationManager.ApplyNVConfiguration", "device", device.Name)

	if device.Spec.Configuration == nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}
	if options == nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, fmt.Errorf("configuration options must not be nil")
	}
	if err := validateSpectrumXNVConfigCompatibility(device); err != nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}
	var plan *spectrumx.Plan
	var spectrumXUtils spectrumXNVConfigUtils
	if !device.Spec.Configuration.ResetToDefault {
		var err error
		plan, spectrumXUtils, err = h.preparedSpectrumXNVConfig(device)
		if err != nil {
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
	}

	// 1. Query current nv config for every port.
	nvConfigsForPorts, err := h.queryNvConfigs(ctx, device)
	if err != nil {
		logger.Error(err, "failed to query nv configs", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}
	firstPort := device.Status.Ports[0]
	firstPortConfig := nvConfigsForPorts[firstPort.PCI]

	// 2. Reset-to-default takes precedence: reset and finish.
	if device.Spec.Configuration.ResetToDefault {
		return h.applyResetToDefault(device, firstPort, firstPortConfig)
	}

	// 3. Network Bay system_conf: the params that don't match the requested named profile.
	systemConfMismatched, err := h.systemConfMismatchedParams(ctx, device)
	if err != nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	// 4. Build the existing template-derived native NVConfig layer independently
	// from the doSPCX typed plan.
	desiredParams, err := h.configValidation.ConstructNvParamMapFromTemplate(device, firstPortConfig)
	if err != nil {
		logger.Error(err, "failed to calculate desired nvconfig parameters", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}
	if options.Force {
		extrapolatePortParamsFromNumOfPF(desiredParams)
	}
	logger.V(2).Info("native NVConfig desired parameters built", "device", device.Name, "params", desiredParams, "force", options.Force)

	// 5. set_system_conf baseline: if the native params do not cover all mismatched profile params
	//    (range-aware), the baseline itself drifted on an uncovered param — re-stage set_system_conf
	//    before the override batch so the overrides still win in the same next-boot config.
	systemConfApplied := false
	if systemConfDrifted(desiredParams, systemConfMismatched) {
		logger.Info("Network Bay system_conf not covered by overrides, applying set_system_conf",
			"device", device.Name, "mismatched", systemConfMismatched)
		if err := h.setSystemConf(ctx, device, options); err != nil {
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
		systemConfApplied = true

		// set_system_conf restages the whole profile, so the pre-call query is now stale. Re-query so the
		// override batch below diffs against the restaged next-boot config and does not skip an override
		// the baseline just overwrote.
		nvConfigsForPorts, err = h.queryNvConfigs(ctx, device)
		if err != nil {
			logger.Error(err, "failed to re-query nv configs after set_system_conf", "device", device.Name)
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
	}

	// Spectrum-X combines template-derived native and typed NVConfig into one
	// primary-PF DMS action.
	if plan != nil {
		result, err := h.applySpectrumXNVConfig(
			ctx, device, plan, spectrumXUtils, nvConfigsForPorts, desiredParams, options)
		if err != nil {
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
		if systemConfApplied {
			result.RebootRequired = true
			if result.Status == types.ApplyStatusNothingToDo {
				result.Status = types.ApplyStatusSuccess
			}
		}
		return result, nil
	}

	// Non-Spectrum-X devices keep the existing per-PF raw apply behavior.
	anyParamsApplied := false
	hasUnsupportedParams := false
	for _, port := range device.Status.Ports {
		nvConfig := nvConfigsForPorts[port.PCI]
		diff := buildNVConfigApplyDiff(nvConfig, desiredParams, options.WithDefault, options.Force)
		batch := diff.changed
		hasUnsupportedParams = hasUnsupportedParams || len(diff.unsupported) > 0
		target := "pci/" + port.PCI
		logger.V(2).Info("nv config apply diff",
			"device", device.Name,
			"target", target,
			"withDefault", options.WithDefault,
			"force", options.Force,
			"comparedToNextBoot", !options.WithDefault && !options.Force,
			"desiredCount", len(desiredParams),
			"changedCount", len(batch),
			"changedParams", batch,
			"unchangedCount", len(diff.unchanged),
			"unchangedParams", diff.unchanged,
			"unsupportedCount", len(diff.unsupported),
			"unsupportedParams", diff.unsupported)
		if len(batch) == 0 {
			continue
		}
		logger.V(2).Info("applying nv config batch", "device", device.Name, "target", target, "params", batch, "force", options.Force)
		applyStatus, err := h.setNvConfigParametersBatch(ctx, port, batch, options.WithDefault, options.Force)
		if err != nil {
			logger.Error(err, "Failed to apply nv config parameters", "device", device.Name, "params", batch)
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
		if applyStatus == types.ApplyStatusSuccess {
			anyParamsApplied = true
		}
	}

	if !anyParamsApplied && !hasUnsupportedParams && !systemConfApplied {
		logger.V(2).Info("nv config already up to date, nothing to apply", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}

	status := types.ApplyStatusSuccess
	if hasUnsupportedParams {
		status = types.ApplyStatusPartiallyApplied
	}
	rebootRequired := anyParamsApplied || systemConfApplied
	logger.Info("nv config applied", "device", device.Name, "status", status, "rebootRequired", rebootRequired)

	return &types.ConfigurationApplyResult{Status: status, RebootRequired: rebootRequired}, nil
}

func extrapolatePortParamsFromNumOfPF(params map[string]string) {
	value, ok := params[consts.NumOfPfParam]
	if !ok {
		return
	}
	portCount, err := strconv.Atoi(value)
	if err != nil || portCount < 2 {
		return
	}

	baseValues := map[string]string{}
	for param, value := range params {
		portNum, ok := consts.PortSuffixNum(param)
		if !ok || portNum != 1 {
			continue
		}
		base := param[:len(param)-len(strconv.Itoa(portNum))-2]
		baseValues[base] = value
	}
	for base, baseValue := range baseValues {
		for portNum := 1; portNum <= portCount; portNum++ {
			param := consts.PortParam(base, portNum)
			if _, exists := params[param]; !exists {
				params[param] = baseValue
			}
		}
	}
}

func (h configurationManager) setNvConfigParametersBatch(
	ctx context.Context,
	port v1alpha1.NicDevicePortSpec,
	params map[string]string,
	withDefault bool,
	force bool,
) (types.ApplyStatus, error) {
	if contextual, ok := h.nvConfigUtils.(contextualNVConfigBatchSetter); ok {
		return contextual.SetNvConfigParametersBatchWithContext(ctx, port, params, withDefault, force)
	}
	return h.nvConfigUtils.SetNvConfigParametersBatch(port, params, withDefault, force)
}

// setSystemConf stages the requested Network Bay set_system_conf for the device's ASIC (the
// lowest-priority baseline). Callers stage it before the override params.
func (h configurationManager) setSystemConf(ctx context.Context, device *v1alpha1.NicDevice, options *types.ConfigurationOptions) error {
	conf := device.Spec.Configuration.Template.NetworkBay.Conf
	asic := device.Status.NetworkBay.Asic
	port := device.Status.Ports[0]

	log.Log.Info("applying Network Bay system_conf", "device", device.Name, "conf", conf, "asic", asic)
	if err := h.nvConfigUtils.SetSystemConf(ctx, port, conf, asic, options.Force); err != nil {
		log.Log.Error(err, "failed to apply system_conf", "device", device.Name)
		return err
	}
	return nil
}

// applyResetToDefault resets NV config to defaults, preserving BF3 operation mode
func (h configurationManager) applyResetToDefault(device *v1alpha1.NicDevice, port v1alpha1.NicDevicePortSpec, portConfig types.NvConfigQuery) (*types.ConfigurationApplyResult, error) {
	log.Log.Info("resetting nv config to default", "device", device.Name)
	bf3OperationModeValue, isBF3device := portConfig.CurrentConfig[consts.BF3OperationModeParam]

	err := h.nvConfigUtils.ResetNvConfig(port)
	if err != nil {
		log.Log.Error(err, "Failed to reset nv config", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	// We need to restore the previous mode of operation for the BlueField devices, otherwise they might become unavailable if the mode changes
	if isBF3device {
		val := bf3OperationModeValue[0]
		mode := ""
		switch val {
		case consts.NvParamBF3DpuMode:
			mode = "DPU"
		case consts.NvParamBF3NicMode:
			mode = "NIC"
		}
		log.Log.Info(fmt.Sprintf("The device %s is the BlueField-3, restoring the previous mode of operation (%s mode) after configuration reset", device.Name, mode))
		err = h.nvConfigUtils.SetNvConfigParameter(port, consts.BF3OperationModeParam, val)
		if err != nil {
			log.Log.Error(err, "Failed to restore the BlueField device mode of operation", "device", device.Name, "mode", mode, "param", consts.BF3OperationModeParam, "value", val)
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
	}

	return &types.ConfigurationApplyResult{Status: types.ApplyStatusSuccess, RebootRequired: true}, nil
}

// ApplyRuntimeConfiguration calculates device's missing runtime spec configuration and applies it to the device on the host
// returns *RuntimeConfigurationApplyResult - result of the apply operation
// returns error - there were errors while applying runtime configuration
func (h configurationManager) ApplyRuntimeConfiguration(ctx context.Context, device *v1alpha1.NicDevice) (*types.RuntimeConfigurationApplyResult, error) {
	log.Log.Info("configurationManager.ApplyRuntimeConfiguration", "device", device.Name)

	if device.Spec.Configuration == nil || device.Spec.Configuration.Template == nil {
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}
	if _, err := h.preparedSpectrumXPlan(device, spectrumx.PlanStageConfigure); err != nil {
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	alreadyApplied, err := h.configValidation.RuntimeConfigApplied(device)
	if err != nil {
		log.Log.Error(err, "failed to verify runtime configuration", "device", device)
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	if device.Spec.Configuration.Template.SpectrumXOptimized != nil && device.Spec.Configuration.Template.SpectrumXOptimized.Enabled {
		spectrumXConfigApplied, err := h.spectrumXConfigManager.RuntimeConfigApplied(device)
		if err != nil {
			log.Log.Error(err, "failed to verify spectrumx runtime configuration", "device", device.Name)
			return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}

		if !spectrumXConfigApplied {
			log.Log.V(2).Info("spectrumx runtime config not applied yet", "device", device.Name)

			result, err := h.spectrumXConfigManager.ApplyRuntimeConfig(device)
			if err != nil {
				log.Log.Error(err, "failed to apply spectrumx config", "device", device.Name)
				return result, err
			}
		}

		spectrumXConfigApplied, err = h.spectrumXConfigManager.RuntimeConfigApplied(device)
		if err != nil {
			log.Log.Error(err, "failed to verify spectrumx runtime configuration", "device", device.Name)
			return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}

		if !spectrumXConfigApplied {
			err = fmt.Errorf("spectrumx runtime config failed to apply")
			log.Log.Error(err, "", "device", device.Name)
			return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
	}

	if alreadyApplied {
		log.Log.V(2).Info("runtime config already applied", "device", device)
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}

	desired := h.configValidation.CalculateDesiredRuntimeConfig(device)

	ports := device.Status.Ports

	if desired.MaxReadRequestSize != 0 {
		for _, port := range ports {
			err = h.configurationUtils.SetMaxReadRequestSize(port.PCI, desired.MaxReadRequestSize)
			if err != nil {
				log.Log.Error(err, "failed to apply maxReadRequestSize", "device", device)
				return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
			}
		}
	}

	// Apply QoS settings (trust, PFC, ToS) via DMS
	if desired.Qos != nil && (desired.Qos.Trust != "" || desired.Qos.PFC != "" || desired.Qos.ToS != 0) {
		err = h.configurationUtils.SetQoSSettings(device, desired.Qos)
		if err != nil {
			log.Log.Error(err, "failed to apply QoS settings", "device", device)
			return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
	}

	// Apply per-port runtime settings
	for _, port := range ports {
		if port.NetworkInterface == "" {
			log.Log.V(2).Info("skipping runtime config apply for port with empty NetworkInterface", "device", device.Name, "port", port.PCI)
			continue
		}
		if err = h.applyPortRuntimeConfig(port.NetworkInterface, device, desired); err != nil {
			return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
	}

	return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusSuccess}, nil
}

// applyPortRuntimeConfig applies per-port runtime settings (RoCE mode, QoS extended, runtime perf)
func (h configurationManager) applyPortRuntimeConfig(iface string, device *v1alpha1.NicDevice, desired types.DesiredRuntimeConfig) error {
	if desired.RoceMode != 0 {
		if err := h.configurationUtils.SetRoceMode(iface, desired.RoceMode); err != nil {
			log.Log.Error(err, "failed to apply roceMode", "device", device, "interface", iface)
			return err
		}
	}

	if desired.Qos != nil {
		if err := h.applyPortExtendedQoS(iface, device, desired.Qos); err != nil {
			return err
		}
	}

	if desired.RuntimePerf != nil && desired.RuntimePerf.Enabled {
		if err := h.applyPortRuntimePerf(iface, device, desired.RuntimePerf); err != nil {
			return err
		}
	}

	return nil
}

// applyPortExtendedQoS applies per-port extended QoS settings (CableLen, ECN, PauseFrames)
func (h configurationManager) applyPortExtendedQoS(iface string, device *v1alpha1.NicDevice, qos *v1alpha1.QosSpec) error {
	if qos.CableLen != 0 {
		if err := h.configurationUtils.SetCableLen(iface, qos.CableLen); err != nil {
			log.Log.Error(err, "failed to apply cableLen", "device", device, "interface", iface)
			return err
		}
	}

	if qos.ECN != nil {
		if err := h.configurationUtils.SetECNEnabled(iface, qos.ECN.Priority, qos.ECN.Enabled, qos.ECN.Enabled); err != nil {
			log.Log.Error(err, "failed to apply ECN", "device", device, "interface", iface)
			return err
		}
	}

	if qos.PauseFrames != nil {
		if err := h.configurationUtils.SetPauseFrames(iface, qos.PauseFrames.Enabled); err != nil {
			log.Log.Error(err, "failed to apply pauseFrames", "device", device, "interface", iface)
			return err
		}
	}

	return nil
}

// applyPortRuntimePerf applies per-port runtime performance settings (ring size, channels, LRO)
func (h configurationManager) applyPortRuntimePerf(iface string, device *v1alpha1.NicDevice, perf *v1alpha1.RuntimePerformanceOptimizedSpec) error {
	if perf.RxRingSize != 0 || perf.TxRingSize != 0 {
		if err := h.configurationUtils.SetRingSize(iface, perf.RxRingSize, perf.TxRingSize); err != nil {
			log.Log.Error(err, "failed to apply ringSize", "device", device, "interface", iface)
			return err
		}
	}

	if perf.CombinedChannels != 0 {
		// Check if the driver exposes combined channels before attempting to set
		current, err := h.configurationUtils.GetCombinedChannels(iface)
		if err != nil {
			log.Log.Error(err, "failed to get combinedChannels", "device", device, "interface", iface)
			return err
		}
		if current != 0 {
			if err = h.configurationUtils.SetCombinedChannels(iface, perf.CombinedChannels); err != nil {
				log.Log.Error(err, "failed to apply combinedChannels", "device", device, "interface", iface)
				return err
			}
		} else {
			log.Log.V(2).Info("skipping combinedChannels apply, driver does not expose combined channels", "device", device, "interface", iface)
		}
	}

	if perf.LRO != nil {
		if err := h.configurationUtils.SetLRO(iface, *perf.LRO); err != nil {
			log.Log.Error(err, "failed to apply LRO", "device", device, "interface", iface)
			return err
		}
	}

	return nil
}

// ResetNicFirmware resets NIC's firmware
// Operation can be long, required context to be able to terminate by timeout
// IB devices need to communicate with other nodes for confirmation
// return err - there were errors while resetting NIC firmware
func (h configurationManager) ResetNicFirmware(ctx context.Context, device *v1alpha1.NicDevice) error {
	log.Log.Info("configurationManager.ResetNicFirmware", "device", device.Name)
	err := h.configurationUtils.ResetNicFirmware(ctx, device.Status.Ports[0].PCI)
	if err != nil {
		log.Log.Error(err, "Failed to reset NIC firmware", "device", device.Name)
		return err
	}

	return nil
}

// spectrumXEnabled reports whether the device's template requests Spectrum-X optimization.
func spectrumXEnabled(device *v1alpha1.NicDevice) bool {
	return device.Spec.Configuration != nil &&
		device.Spec.Configuration.Template != nil &&
		device.Spec.Configuration.Template.SpectrumXOptimized != nil &&
		device.Spec.Configuration.Template.SpectrumXOptimized.Enabled
}

// hasNetworkBaySpec reports whether the device has a Network Bay template configured AND was
// detected as part of a Network Bay card. Both are required to apply / validate set_system_conf.
// ResetToDefault takes precedence: a reset wipes nv config, so we must not also manage set_system_conf
// for the same device — otherwise apply would stage set_system_conf and the reset would wipe it on
// every reconcile, looping forever.
func hasNetworkBaySpec(device *v1alpha1.NicDevice) bool {
	return device.Spec.Configuration != nil &&
		!device.Spec.Configuration.ResetToDefault &&
		device.Spec.Configuration.Template != nil &&
		device.Spec.Configuration.Template.NetworkBay != nil &&
		device.Status.NetworkBay != nil &&
		len(device.Status.Ports) > 0
}

// systemConfMismatchedParams returns the names of params whose applied value does not match the
// requested Network Bay system_conf (the MISMATCH rows of validate_system_conf), in source order.
// Returns nil for non-Network-Bay devices. SKIPPED rows are informational and excluded.
func (h configurationManager) systemConfMismatchedParams(ctx context.Context, device *v1alpha1.NicDevice) ([]string, error) {
	if !hasNetworkBaySpec(device) {
		return nil, nil
	}

	conf := device.Spec.Configuration.Template.NetworkBay.Conf
	asic := device.Status.NetworkBay.Asic
	port := device.Status.Ports[0]

	matches, mismatched, err := h.nvConfigUtils.ValidateSystemConf(ctx, port, conf, asic)
	if err != nil {
		log.Log.Error(err, "failed to validate system_conf", "device", device.Name)
		return nil, err
	}

	// Fail closed: a non-matching result with no recognized MISMATCH rows would otherwise look identical
	// to a matching profile and silently skip set_system_conf. Surface it so the reconcile retries instead.
	if !matches && len(mismatched) == 0 {
		return nil, fmt.Errorf("device %s system_conf %q reports a mismatch but no mismatched params were parsed", device.Name, conf)
	}
	return mismatched, nil
}

func (h configurationManager) queryNvConfigs(ctx context.Context, device *v1alpha1.NicDevice) (map[string]types.NvConfigQuery, error) {
	nvConfigs := make(map[string]types.NvConfigQuery)
	for _, port := range device.Status.Ports {
		nvConfig, err := h.nvConfigUtils.QueryNvConfig(ctx, port, nil)
		if err != nil {
			return nil, err
		}
		nvConfigs[port.PCI] = nvConfig
	}
	return nvConfigs, nil
}

// getRawNvConfigParams extracts rawNvConfig params from the device template,
// dropping _Pn params whose port index is beyond the device's port count.
func getRawNvConfigParams(device *v1alpha1.NicDevice) map[string]string {
	template := device.Spec.Configuration.Template
	if template == nil || len(template.RawNvConfig) == 0 {
		return nil
	}
	portCount := len(device.Status.Ports)
	params := make(map[string]string, len(template.RawNvConfig))
	for _, rawParam := range template.RawNvConfig {
		if n, ok := consts.PortSuffixNum(rawParam.Name); ok && n > portCount {
			continue
		}
		params[rawParam.Name] = rawParam.Value
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

// systemConfDrifted reports whether any mismatched system_conf param is left uncovered by the override
// config. overrides and the mismatched names are both concrete per-index keys (e.g. MODULE_SPLIT_M0[2]),
// so coverage is an exact name lookup — an override of a profile param suppresses its mismatch row. An
// empty mismatched slice is never drift.
func systemConfDrifted(overrides map[string]string, mismatched []string) bool {
	for _, param := range mismatched {
		if _, ok := overrides[param]; !ok {
			return true
		}
	}
	return false
}

func NewConfigurationManager(eventRecorder record.EventRecorder, dmsManager dms.DMSManager, nvConfigUtils nvconfig.NVConfigUtils, spectrumXConfigManager spectrumx.SpectrumXManager) ConfigurationManager {
	utils := newConfigurationUtils(dmsManager)
	return configurationManager{configurationUtils: utils, configValidation: newConfigValidation(utils, eventRecorder), nvConfigUtils: nvConfigUtils, spectrumXConfigManager: spectrumXConfigManager}
}
