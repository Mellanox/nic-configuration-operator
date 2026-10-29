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
	"sort"
	"strconv"

	dmsconfiguration "github.com/Mellanox/dms-lib/pkg/configuration"
	dmsdevice "github.com/Mellanox/dms-lib/pkg/device"
	dmsfirmware "github.com/Mellanox/dms-lib/pkg/firmware"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/nvconfig"
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

type runtimeManager interface {
	ApplyRuntimeConfiguration(context.Context, dmsconfiguration.RuntimeRequest) (dmsconfiguration.Result, error)
}
type firmwareResetter interface {
	Reset(context.Context, dmsdevice.Device, dmsfirmware.ResetOptions) error
}

type configurationManager struct {
	runtime  runtimeManager
	firmware firmwareResetter
	discover func(context.Context) (dmsdevice.Inventory, error)

	configurationUtils ConfigurationUtils
	configValidation   configValidation
	nvConfigUtils      nvconfig.NVConfigUtils
	systemConfParams   nvconfig.SystemConfParamsProvider
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

	// 3. Build native desired configuration with template overrides above profile defaults.
	//    Validated against the device's next boot, exactly like the apply path — so a value already staged
	//    for next boot but not yet rebooted reports reboot-required instead of looping.
	desiredParams, err := h.constructDesiredNvParams(ctx, device, firstPortConfig, false)
	if err != nil {
		logger.Error(err, "failed to calculate desired nvconfig parameters", "device", device.Name)
		return false, false, nil, err
	}
	logger.V(2).Info("validating native NVConfig parameters", "device", device.Name, "params", desiredParams)

	configUpdateNeeded, rebootNeeded, unsupportedParams := validateTemplateParamsApplied(nvConfigsForPorts, desiredParams)
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
			if !nvconfig.ValueMatches(nextValues, desiredValue) {
				configUpdateNeeded = true
				rebootNeeded = true
				continue
			}
			currentValues, foundInCurrent := nvConfig.CurrentConfig[parameter]
			if !foundInCurrent || !nvconfig.ValueMatches(currentValues, desiredValue) {
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
		if !withDefault && nvconfig.ValueMatches(nextValues, value) {
			diff.unchanged = append(diff.unchanged, param)
			continue
		}
		diff.changed[param] = value
	}
	sort.Strings(diff.unchanged)
	sort.Strings(diff.unsupported)
	return diff
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

	// 3. Build the complete native desired configuration. The Network Bay profile, when present,
	// is folded into the same map and sent through the regular DMS raw-parameter path.
	desiredParams, err := h.constructDesiredNvParams(ctx, device, firstPortConfig, options.Force)
	if err != nil {
		logger.Error(err, "failed to calculate desired nvconfig parameters", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}
	logger.V(2).Info("native NVConfig desired parameters built", "device", device.Name, "params", desiredParams, "force", options.Force)

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

	if !anyParamsApplied && !hasUnsupportedParams {
		logger.V(2).Info("nv config already up to date, nothing to apply", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}

	status := types.ApplyStatusSuccess
	if hasUnsupportedParams {
		status = types.ApplyStatusPartiallyApplied
	}
	rebootRequired := anyParamsApplied
	logger.Info("nv config applied", "device", device.Name, "status", status, "rebootRequired", rebootRequired)

	return &types.ConfigurationApplyResult{Status: status, RebootRequired: rebootRequired}, nil
}

func extrapolatePortParamsFromNumOfPF(params map[string]string) {
	extrapolatePortParams(params, params[consts.NumOfPfParam])
}

func extrapolatePortParams(params map[string]string, numPFs string) {
	portCount, err := strconv.Atoi(numPFs)
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

// hasNetworkBaySpec reports whether the device has a Network Bay template configured AND was
// detected as part of a Network Bay card. Both are required to resolve its system configuration
// profile. ResetToDefault takes precedence over profile management.
func hasNetworkBaySpec(device *v1alpha1.NicDevice) bool {
	return device.Spec.Configuration != nil &&
		!device.Spec.Configuration.ResetToDefault &&
		device.Spec.Configuration.Template != nil &&
		device.Spec.Configuration.Template.NetworkBay != nil &&
		device.Status.NetworkBay != nil &&
		len(device.Status.Ports) > 0
}

// constructDesiredNvParams merges the named Network Bay system profile below the existing desired
// parameter layers. All returned keys are concrete mlxconfig parameters, including expanded array
// ranges from the system profile.
func (h configurationManager) constructDesiredNvParams(
	ctx context.Context, device *v1alpha1.NicDevice, query types.NvConfigQuery, force bool) (map[string]string, error) {
	desiredParams := map[string]string{}

	profileParams, err := h.getSystemConfParams(ctx, device)
	if err != nil {
		return nil, err
	}
	overrides, err := h.configValidation.ConstructNvParamMapFromTemplate(device, query)
	if err != nil {
		return nil, err
	}
	mergeParams(desiredParams, profileParams)
	mergeParams(desiredParams, overrides)
	if force {
		extrapolatePortParamsFromNumOfPF(desiredParams)
	}

	return desiredParams, nil
}

func (h configurationManager) getSystemConfParams(ctx context.Context, device *v1alpha1.NicDevice) (map[string]string, error) {
	if !hasNetworkBaySpec(device) {
		return nil, nil
	}
	if h.systemConfParams == nil {
		return nil, fmt.Errorf("nvconfig utility does not support system configuration profile resolution")
	}

	conf := device.Spec.Configuration.Template.NetworkBay.Conf
	asic := device.Status.NetworkBay.Asic
	port := device.Status.Ports[0]

	params, err := h.systemConfParams.GetSystemConfParams(ctx, port, conf, asic)
	if err != nil {
		log.Log.Error(err, "failed to resolve system configuration profile", "device", device.Name, "conf", conf, "asic", asic)
		return nil, err
	}
	return params, nil
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

func NewConfigurationManager(nvConfigUtils nvconfig.NVConfigUtils, runtime dmsconfiguration.Manager, firmware dmsfirmware.Manager) ConfigurationManager {
	utils := newConfigurationUtils()
	systemConfParams, _ := nvConfigUtils.(nvconfig.SystemConfParamsProvider)
	return configurationManager{
		configurationUtils: utils, configValidation: newConfigValidation(utils),
		nvConfigUtils: nvConfigUtils, systemConfParams: systemConfParams,
		runtime: runtime, firmware: firmware,
		discover: func(ctx context.Context) (dmsdevice.Inventory, error) {
			return dmsdevice.Discover(ctx, dmsdevice.Options{})
		},
	}
}
