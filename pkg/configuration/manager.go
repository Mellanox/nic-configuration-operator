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
	"strings"

	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/dms"
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

// ValidateDeviceNvSpec will validate device's non-volatile spec against already applied configuration on the host
// returns bool - nv config update required
// returns bool - reboot required
// returns []string - desired params that are hidden on the device (absent from NextBootConfig); sorted, deduped
// returns error - there are errors in device's spec
// if fully matches in current and next config, returns false, false
// if fully matched next but not current, returns false, true
// if not fully matched next boot, returns true, true
func (h configurationManager) ValidateDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, bool, []string, error) {
	log.Log.Info("configurationManager.ValidateDeviceNvSpec", "device", device.Name)

	if device.Spec.Configuration.Template != nil && device.Spec.Configuration.Template.SpectrumXOptimized != nil && device.Spec.Configuration.Template.SpectrumXOptimized.Enabled {
		configUpdate, reboot, err := h.validateSpectrumXNvSpec(ctx, device)
		return configUpdate, reboot, nil, err
	}

	nvConfigsForPorts, err := h.queryNvConfigs(ctx, device)
	if err != nil {
		log.Log.Error(err, "failed to query nv configs", "device", device.Name)
		return false, false, nil, err
	}
	firstPortPCIAddr := device.Status.Ports[0].PCI
	firstPortConfig := nvConfigsForPorts[firstPortPCIAddr]

	if device.Spec.Configuration.ResetToDefault {
		resetNeeded := false
		rebootNeeded := false
		for _, nvConfig := range nvConfigsForPorts {
			resetNeededForPort, rebootNeededForPort, err := h.configValidation.ValidateResetToDefault(nvConfig)
			if err != nil {
				log.Log.Error(err, "failed to validate reset to default", "device", device.Name)
				return false, false, nil, err
			}
			resetNeeded = resetNeeded || resetNeededForPort
			rebootNeeded = rebootNeeded || rebootNeededForPort
		}
		return resetNeeded, rebootNeeded, nil, nil
	}

	// ConstructNvParamMapFromTemplate only uses the given nvConfig for a few default values, so we can use any port's nvConfig
	desiredConfig, err := h.configValidation.ConstructNvParamMapFromTemplate(device, firstPortConfig)
	if err != nil {
		log.Log.Error(err, "failed to calculate desired nvconfig parameters", "device", device.Name)
		return false, false, nil, err
	}

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
				log.Log.V(2).Info("Parameter not in NextBootConfig, treating as unsupported",
					"device", device.Name, "param", parameter)
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

	// Layer Network Bay system_conf validation on top of the regular nv config validation.
	systemConfMismatch, err := h.systemConfMismatch(ctx, device)
	if err != nil {
		return false, false, nil, err
	}
	if systemConfMismatch {
		configUpdateNeeded = true
		rebootNeeded = true
	}

	var unsupportedParams []string
	if len(unsupportedSet) > 0 {
		unsupportedParams = make([]string, 0, len(unsupportedSet))
		for name := range unsupportedSet {
			unsupportedParams = append(unsupportedParams, name)
		}
		sort.Strings(unsupportedParams)
	}

	return configUpdateNeeded, rebootNeeded, unsupportedParams, nil
}

// ApplyNVConfiguration calculates device's missing nv spec configuration and applies it to the device on the host
// returns *ConfigurationApplyResult - result of the apply operation
// returns error - there were errors while applying nv configuration
func (h configurationManager) ApplyNVConfiguration(ctx context.Context, device *v1alpha1.NicDevice, options *types.ConfigurationOptions) (*types.ConfigurationApplyResult, error) {
	log.Log.Info("configurationManager.ApplyNVConfiguration", "device", device.Name)

	if device.Spec.Configuration == nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}

	if device.Spec.Configuration.Template != nil &&
		device.Spec.Configuration.Template.SpectrumXOptimized != nil &&
		device.Spec.Configuration.Template.SpectrumXOptimized.Enabled {
		result, err := h.applySpectrumXNVConfiguration(ctx, device, options)
		if err != nil || result.Status != types.ApplyStatusNothingToDo {
			return result, err
		}
		// SpectrumX nv config has fully converged; layer Network Bay system_conf on top.
		// applySystemConf is a no-op (NothingToDo) for non-Network-Bay devices.
		return h.applySystemConf(ctx, device, options)
	}

	nvConfigsForPorts, err := h.queryNvConfigs(ctx, device)
	if err != nil {
		log.Log.Error(err, "failed to query nv configs", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}
	firstPortPCIAddr := device.Status.Ports[0].PCI
	firstPortConfig := nvConfigsForPorts[firstPortPCIAddr]

	if device.Spec.Configuration.ResetToDefault {
		return h.applyResetToDefault(device, firstPortPCIAddr, firstPortConfig)
	}

	if device.Spec.Configuration.Template == nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}

	desiredConfig, err := h.configValidation.ConstructNvParamMapFromTemplate(device, firstPortConfig)
	if err != nil {
		log.Log.Error(err, "failed to calculate desired nvconfig parameters", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	anyParamsApplied := false
	hasUnsupportedParams := false

	for pciAddr, nvConfig := range nvConfigsForPorts {
		supportedParams := map[string]string{}

		for param, value := range desiredConfig {
			nextValues, found := nvConfig.NextBootConfig[param]
			if !found {
				log.Log.Info("Parameter not found in NextBootConfig, skipping", "device", device.Name, "param", param)
				hasUnsupportedParams = true
				continue
			}

			if !slices.Contains(nextValues, value) {
				supportedParams[param] = value
			}
		}

		if len(supportedParams) == 0 {
			continue
		}

		log.Log.V(2).Info("applying nv config to device", "device", device.Name, "config", supportedParams)

		err = h.nvConfigUtils.SetNvConfigParametersBatch(pciAddr, supportedParams, options.WithDefault, options.Force)
		if err != nil {
			log.Log.Error(err, "Failed to apply nv config parameters", "device", device.Name, "params", supportedParams)
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
		anyParamsApplied = true
	}

	// Layer Network Bay system_conf on top of the regular nv config apply (no-op for non-bay devices).
	systemConfResult, err := h.applySystemConf(ctx, device, options)
	if err != nil {
		return systemConfResult, err
	}
	systemConfApplied := systemConfResult.RebootRequired

	if !anyParamsApplied && !hasUnsupportedParams && !systemConfApplied {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}

	log.Log.V(2).Info("nv config successfully applied to device", "device", device.Name)

	status := types.ApplyStatusSuccess
	if hasUnsupportedParams {
		status = types.ApplyStatusPartiallyApplied
	}

	return &types.ConfigurationApplyResult{Status: status, RebootRequired: anyParamsApplied || systemConfApplied}, nil
}

// checkMlxConfigMismatch queries mlxconfig for the given params and returns true if any value doesn't match.
func (h configurationManager) checkMlxConfigMismatch(ctx context.Context, pci string, params map[string]string) (bool, error) {
	paramNames := make([]string, 0, len(params))
	for name := range params {
		paramNames = append(paramNames, name)
	}

	query, err := h.nvConfigUtils.QueryNvConfig(ctx, pci, paramNames)
	if err != nil {
		return false, err
	}

	for name, desiredValue := range params {
		currentValues := query.CurrentConfig[name]
		if !slices.Contains(currentValues, desiredValue) {
			log.Log.V(2).Info("mlxconfig parameter mismatch", "param", name, "desired", desiredValue, "current", currentValues)
			return true, nil
		}
	}
	return false, nil
}

// applySpectrumXNVConfiguration handles the Spectrum-X NV configuration path (breakout + postBreakout)
func (h configurationManager) applySpectrumXNVConfiguration(ctx context.Context, device *v1alpha1.NicDevice, options *types.ConfigurationOptions) (*types.ConfigurationApplyResult, error) {
	pci := device.Status.Ports[0].PCI

	breakoutParams, err := h.spectrumXConfigManager.GetBreakoutMlxConfig(device)
	if err != nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	postBreakoutParams, err := h.spectrumXConfigManager.GetPostBreakoutMlxConfig(device)
	if err != nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}
	// Merge rawNvConfig overrides into the appropriate phase:
	// params that overlap with breakout go into breakout, the rest into postBreakout
	postBreakoutParams = mergeRawNvConfigIntoPhases(device, breakoutParams, postBreakoutParams)

	// Check and apply breakout config — requires reboot before postBreakout can be applied
	if len(breakoutParams) > 0 {
		if mismatch, err := h.checkMlxConfigMismatch(ctx, pci, breakoutParams); err != nil {
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		} else if mismatch {
			log.Log.Info("applying breakout config", "device", device.Name)
			// SpectrumX breakout/postBreakout apply is intentionally left unchanged (force=false);
			// the SpectrumX flow is reworked in a separate FR.
			if err := h.nvConfigUtils.SetNvConfigParametersBatch(pci, breakoutParams, options.WithDefault, false); err != nil {
				return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
			}
			log.Log.Info("breakout config applied, reboot required", "device", device.Name)
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusPartiallyApplied, RebootRequired: true}, nil
		}
	}

	// Check and apply postBreakout config (includes rawNvConfig params that don't overlap with breakout)
	if len(postBreakoutParams) > 0 {
		if mismatch, err := h.checkMlxConfigMismatch(ctx, pci, postBreakoutParams); err != nil {
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		} else if mismatch {
			// Apply concatenated breakout + postBreakout params
			merged := make(map[string]string, len(breakoutParams)+len(postBreakoutParams))
			for k, v := range breakoutParams {
				merged[k] = v
			}
			for k, v := range postBreakoutParams {
				merged[k] = v
			}
			log.Log.Info("applying postBreakout config", "device", device.Name)
			if err := h.nvConfigUtils.SetNvConfigParametersBatch(pci, merged, options.WithDefault, false); err != nil {
				return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
			}
			log.Log.Info("postBreakout config applied, reboot required", "device", device.Name)
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusSuccess, RebootRequired: true}, nil
		}
	}

	return &types.ConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
}

// applyResetToDefault resets NV config to defaults, preserving BF3 operation mode
func (h configurationManager) applyResetToDefault(device *v1alpha1.NicDevice, pciAddr string, portConfig types.NvConfigQuery) (*types.ConfigurationApplyResult, error) {
	log.Log.Info("resetting nv config to default", "device", device.Name)
	bf3OperationModeValue, isBF3device := portConfig.CurrentConfig[consts.BF3OperationModeParam]

	err := h.nvConfigUtils.ResetNvConfig(pciAddr)
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
		err = h.nvConfigUtils.SetNvConfigParameter(pciAddr, consts.BF3OperationModeParam, val)
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

// validateSpectrumXNvSpec validates the Spectrum-X nv config (breakout + postBreakout) and, once it
// has converged, the Network Bay system_conf. The Spectrum-X validation itself is unchanged.
func (h configurationManager) validateSpectrumXNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, bool, error) {
	pci := device.Status.Ports[0].PCI

	breakoutParams, err := h.spectrumXConfigManager.GetBreakoutMlxConfig(device)
	if err != nil {
		return false, false, err
	}
	postBreakoutParams, err := h.spectrumXConfigManager.GetPostBreakoutMlxConfig(device)
	if err != nil {
		return false, false, err
	}
	// Merge rawNvConfig overrides into the appropriate phase:
	// params that overlap with breakout go into breakout, the rest into postBreakout
	postBreakoutParams = mergeRawNvConfigIntoPhases(device, breakoutParams, postBreakoutParams)

	if len(breakoutParams) > 0 {
		if mismatch, err := h.checkMlxConfigMismatch(ctx, pci, breakoutParams); err != nil {
			return false, false, err
		} else if mismatch {
			log.Log.V(2).Info("breakout config not applied, update and reboot required", "device", device.Name)
			return true, true, nil
		}
	}

	if len(postBreakoutParams) > 0 {
		if mismatch, err := h.checkMlxConfigMismatch(ctx, pci, postBreakoutParams); err != nil {
			return false, false, err
		} else if mismatch {
			log.Log.V(2).Info("postBreakout config not applied, update and reboot required", "device", device.Name)
			return true, true, nil
		}
	}

	// SpectrumX nv config is fully applied; layer Network Bay system_conf validation on top.
	systemConfMismatch, err := h.systemConfMismatch(ctx, device)
	if err != nil {
		return false, false, err
	}
	if systemConfMismatch {
		return true, true, nil
	}

	return false, false, nil
}

// hasNetworkBaySpec reports whether the device has a Network Bay template configured AND was
// detected as part of a Network Bay card. Both are required to apply / validate set_system_conf.
func hasNetworkBaySpec(device *v1alpha1.NicDevice) bool {
	return device.Spec.Configuration != nil &&
		device.Spec.Configuration.Template != nil &&
		device.Spec.Configuration.Template.NetworkBay != nil &&
		device.Status.NetworkBay != nil &&
		len(device.Status.Ports) > 0
}

// systemConfMismatch returns true if the device's applied configuration does not match the
// requested Network Bay system_conf. Returns false for non-Network-Bay devices.
func (h configurationManager) systemConfMismatch(ctx context.Context, device *v1alpha1.NicDevice) (bool, error) {
	if !hasNetworkBaySpec(device) {
		return false, nil
	}

	conf := device.Spec.Configuration.Template.NetworkBay.Conf
	asic := device.Status.NetworkBay.Asic
	pci := device.Status.Ports[0].PCI

	matches, err := h.nvConfigUtils.ValidateSystemConf(ctx, pci, conf, asic)
	if err != nil {
		log.Log.Error(err, "failed to validate system_conf", "device", device.Name)
		return false, err
	}
	return !matches, nil
}

// applySystemConf applies the requested Network Bay system_conf for the device's ASIC if it is not
// already applied. It is a no-op (ApplyStatusNothingToDo) for non-Network-Bay devices. set_system_conf
// is persistent and reboot-required, so a successful apply reports RebootRequired.
func (h configurationManager) applySystemConf(ctx context.Context, device *v1alpha1.NicDevice, options *types.ConfigurationOptions) (*types.ConfigurationApplyResult, error) {
	if !hasNetworkBaySpec(device) {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}

	conf := device.Spec.Configuration.Template.NetworkBay.Conf
	asic := device.Status.NetworkBay.Asic
	pci := device.Status.Ports[0].PCI

	matches, err := h.nvConfigUtils.ValidateSystemConf(ctx, pci, conf, asic)
	if err != nil {
		log.Log.Error(err, "failed to validate system_conf", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}
	if matches {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}

	log.Log.Info("applying Network Bay system_conf", "device", device.Name, "conf", conf, "asic", asic)
	if err := h.nvConfigUtils.SetSystemConf(ctx, pci, conf, asic, options.Force); err != nil {
		log.Log.Error(err, "failed to apply system_conf", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	return &types.ConfigurationApplyResult{Status: types.ApplyStatusSuccess, RebootRequired: true}, nil
}

func (h configurationManager) queryNvConfigs(ctx context.Context, device *v1alpha1.NicDevice) (map[string]types.NvConfigQuery, error) {
	nvConfigs := make(map[string]types.NvConfigQuery)
	for _, port := range device.Status.Ports {
		pciAddr := port.PCI
		nvConfig, err := h.nvConfigUtils.QueryNvConfig(ctx, pciAddr, nil)
		if err != nil {
			return nil, err
		}
		nvConfigs[pciAddr] = nvConfig
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

// mergeRawNvConfigIntoPhases merges rawNvConfig overrides into the appropriate Spectrum-X phase.
// Params that overlap with breakout keys are overridden in breakoutParams;
// all other raw params are merged into postBreakoutParams.
func mergeRawNvConfigIntoPhases(device *v1alpha1.NicDevice, breakoutParams map[string]string, postBreakoutParams map[string]string) map[string]string {
	for k, v := range getRawNvConfigParams(device) {
		if _, exists := breakoutParams[k]; exists {
			breakoutParams[k] = v
		} else {
			if postBreakoutParams == nil {
				postBreakoutParams = make(map[string]string)
			}
			postBreakoutParams[k] = v
		}
	}
	return postBreakoutParams
}

func NewConfigurationManager(eventRecorder record.EventRecorder, dmsManager dms.DMSManager, nvConfigUtils nvconfig.NVConfigUtils, spectrumXConfigManager spectrumx.SpectrumXManager) ConfigurationManager {
	utils := newConfigurationUtils(dmsManager)
	return configurationManager{configurationUtils: utils, configValidation: newConfigValidation(utils, eventRecorder), nvConfigUtils: nvConfigUtils, spectrumXConfigManager: spectrumXConfigManager}
}
