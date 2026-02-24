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
	// returns error - there are errors in device's spec
	ValidateDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, bool, error)
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
// returns error - there are errors in device's spec
// if fully matches in current and next config, returns false, false
// if fully matched next but not current, returns false, true
// if not fully matched next boot, returns true, true
func (h configurationManager) ValidateDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, bool, error) {
	log.Log.Info("configurationManager.ValidateDeviceNvSpec", "device", device.Name)

	if device.Spec.Configuration.Template != nil && device.Spec.Configuration.Template.SpectrumXOptimized != nil && device.Spec.Configuration.Template.SpectrumXOptimized.Enabled {
		// First check breakout config (from breakout section) - requires reboot to apply
		breakoutConfigApplied, err := h.spectrumXConfigManager.BreakoutConfigApplied(ctx, device)
		if err != nil {
			log.Log.Error(err, "failed to check spectrumx breakout config", "device", device.Name)
			return false, false, err
		}

		if !breakoutConfigApplied {
			log.Log.V(2).Info("breakout config not applied, update and reboot required", "device", device.Name)
			return true, true, nil
		}

		// Then check nvconfig (from nvConfig section) - may require reboot
		nvConfigApplied, err := h.spectrumXConfigManager.NvConfigApplied(ctx, device)
		if err != nil {
			log.Log.Error(err, "failed to check spectrumx nvconfig", "device", device.Name)
			return false, false, err
		}

		if !nvConfigApplied {
			log.Log.V(2).Info("nvconfig not applied, update and reboot required", "device", device.Name)
			return true, true, nil
		}

		return false, false, nil
	}

	nvConfigsForPorts, err := h.queryNvConfigs(ctx, device)
	if err != nil {
		log.Log.Error(err, "failed to query nv configs", "device", device.Name)
		return false, false, err
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
				return false, false, err
			}
			resetNeeded = resetNeeded || resetNeededForPort
			rebootNeeded = rebootNeeded || rebootNeededForPort
		}
		return resetNeeded, rebootNeeded, nil
	}

	// ConstructNvParamMapFromTemplate only uses the given nvConfig for a few default values, so we can use any port's nvConfig
	desiredConfig, err := h.configValidation.ConstructNvParamMapFromTemplate(device, firstPortConfig)
	if err != nil {
		log.Log.Error(err, "failed to calculate desired nvconfig parameters", "device", device.Name)
		return false, false, err
	}

	configUpdateNeeded := false
	rebootNeeded := false

	// If ADVANCED_PCI_SETTINGS are enabled in current config, unknown parameters are treated as spec error
	// ADVANCED_PCI_SETTINGS param value is shared across all ports, so we can use any port's nvConfig for validation
	advancedPciSettingsEnabled := h.configValidation.AdvancedPCISettingsEnabled(firstPortConfig)

	for _, nvConfig := range nvConfigsForPorts {
		for parameter, desiredValue := range desiredConfig {
			currentValues, foundInCurrent := nvConfig.CurrentConfig[parameter]
			nextValues, foundInNextBoot := nvConfig.NextBootConfig[parameter]
			if advancedPciSettingsEnabled && !foundInCurrent {
				err = types.IncorrectSpecError(fmt.Sprintf("Parameter %s unsupported for device %s", parameter, device.Name))
				log.Log.Error(err, "can't set nv config parameter for device")
				return false, false, err
			}

			if foundInNextBoot && slices.Contains(nextValues, strings.ToLower(desiredValue)) {
				if !foundInCurrent || !slices.Contains(currentValues, strings.ToLower(desiredValue)) {
					rebootNeeded = true
				}
			} else {
				configUpdateNeeded = true
				rebootNeeded = true
			}
		}
	}

	return configUpdateNeeded, rebootNeeded, nil
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
		return h.applySpectrumXNVConfiguration(ctx, device)
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

	// if ADVANCED_PCI_SETTINGS == 0, not all nv config parameters are available for configuration
	// we enable this parameter first to unlock them
	if !h.configValidation.AdvancedPCISettingsEnabled(firstPortConfig) {
		log.Log.V(2).Info("AdvancedPciSettings not enabled, fw reset required", "device", device.Name)
		err := h.nvConfigUtils.SetNvConfigParameter(firstPortPCIAddr, consts.AdvancedPCISettingsParam, consts.NvParamTrue)
		if err != nil {
			log.Log.Error(err, "Failed to apply nv config parameter", "device", device.Name, "param", consts.AdvancedPCISettingsParam, "value", consts.NvParamTrue)
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}

		if !options.SkipReset {
			err = h.ResetNicFirmware(ctx, device)
			if err != nil {
				log.Log.Error(err, "Failed to reset NIC firmware, reboot required to apply ADVANCED_PCI_SETTINGS", "device", device.Name)
				// We try to perform FW reset after setting the ADVANCED_PCI_SETTINGS to save us a reboot
				// However, if the soft FW reset fails for some reason, we need to perform a reboot to unlock
				// all the nv config parameters
				return &types.ConfigurationApplyResult{Status: types.ApplyStatusSuccess, RebootRequired: true}, nil
			}
		} else {
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusSuccess, RebootRequired: true}, nil
		}

		// Query nv config again, additional options could become available
		nvConfigsForPorts, err = h.queryNvConfigs(ctx, device)
		if err != nil {
			log.Log.Error(err, "failed to query nv configs", "device", device.Name)
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
		firstPortConfig = nvConfigsForPorts[firstPortPCIAddr]
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

		err = h.nvConfigUtils.SetNvConfigParametersBatch(pciAddr, supportedParams, options.WithDefault)
		if err != nil {
			log.Log.Error(err, "Failed to apply nv config parameters", "device", device.Name, "params", supportedParams)
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
		anyParamsApplied = true
	}

	if !anyParamsApplied && !hasUnsupportedParams {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}

	log.Log.V(2).Info("nv config successfully applied to device", "device", device.Name)

	status := types.ApplyStatusSuccess
	if hasUnsupportedParams {
		status = types.ApplyStatusPartiallyApplied
	}

	return &types.ConfigurationApplyResult{Status: status, RebootRequired: true}, nil
}

// applySpectrumXNVConfiguration handles the Spectrum-X NV configuration path (breakout + nvconfig)
func (h configurationManager) applySpectrumXNVConfiguration(ctx context.Context, device *v1alpha1.NicDevice) (*types.ConfigurationApplyResult, error) {
	// First check and apply breakout config (from breakout section)
	// Breakout config must be applied first and requires a reboot before nvconfig can be applied
	breakoutConfigApplied, err := h.spectrumXConfigManager.BreakoutConfigApplied(ctx, device)
	if err != nil {
		log.Log.Error(err, "failed to check spectrumx breakout config", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	if !breakoutConfigApplied {
		log.Log.Info("applying breakout config", "device", device.Name)
		result, err := h.spectrumXConfigManager.ApplyBreakoutConfig(ctx, device)
		if err != nil {
			log.Log.Error(err, "failed to apply spectrumx breakout config", "device", device.Name)
			return result, err
		}
		// Always require reboot after applying breakout config to ensure the device is in correct state
		// before applying nvconfig
		log.Log.Info("breakout config applied, reboot required", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusPartiallyApplied, RebootRequired: true}, nil
	}

	// Then check and apply nvconfig (from nvConfig section)
	nvConfigApplied, err := h.spectrumXConfigManager.NvConfigApplied(ctx, device)
	if err != nil {
		log.Log.Error(err, "failed to check spectrumx nvconfig", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	if !nvConfigApplied {
		log.Log.Info("applying nvconfig", "device", device.Name)
		result, err := h.spectrumXConfigManager.ApplyNvConfig(ctx, device)
		if err != nil {
			log.Log.Error(err, "failed to apply spectrumx nvconfig", "device", device.Name)
			return result, err
		}
		log.Log.Info("nvconfig applied, reboot required", "device", device.Name)
		return result, nil
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
		if val == consts.NvParamBF3DpuMode {
			mode = "DPU"
		} else if val == consts.NvParamBF3NicMode {
			mode = "NIC"
		}
		log.Log.Info(fmt.Sprintf("The device %s is the BlueField-3, restoring the previous mode of operation (%s mode) after configuration reset", device.Name, mode))
		err = h.nvConfigUtils.SetNvConfigParameter(pciAddr, consts.BF3OperationModeParam, val)
		if err != nil {
			log.Log.Error(err, "Failed to restore the BlueField device mode of operation", "device", device.Name, "mode", mode, "param", consts.BF3OperationModeParam, "value", val)
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
	}

	err = h.nvConfigUtils.SetNvConfigParameter(pciAddr, consts.AdvancedPCISettingsParam, consts.NvParamTrue)
	if err != nil {
		log.Log.Error(err, "Failed to apply nv config parameter", "device", device.Name, "param", consts.AdvancedPCISettingsParam, "value", consts.NvParamTrue)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
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

	desiredMaxReadReqSize, desiredQoS := h.configValidation.CalculateDesiredRuntimeConfig(device)

	ports := device.Status.Ports

	if desiredMaxReadReqSize != 0 {
		for _, port := range ports {
			err = h.configurationUtils.SetMaxReadRequestSize(port.PCI, desiredMaxReadReqSize)
			if err != nil {
				log.Log.Error(err, "failed to apply runtime configuration", "device", device)
				return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
			}
		}
	}

	// Don't apply QoS settings if neither trust nor pfc changes are requested
	if desiredQoS != nil {
		err = h.configurationUtils.SetQoSSettings(device, desiredQoS)
		if err != nil {
			log.Log.Error(err, "failed to apply runtime configuration", "device", device)
			return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
	}

	return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusSuccess}, nil
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

func (h configurationManager) queryNvConfigs(ctx context.Context, device *v1alpha1.NicDevice) (map[string]types.NvConfigQuery, error) {
	nvConfigs := make(map[string]types.NvConfigQuery)
	for _, port := range device.Status.Ports {
		pciAddr := port.PCI
		nvConfig, err := h.nvConfigUtils.QueryNvConfig(ctx, pciAddr, "")
		if err != nil {
			return nil, err
		}
		nvConfigs[pciAddr] = nvConfig
	}
	return nvConfigs, nil
}

func NewConfigurationManager(eventRecorder record.EventRecorder, dmsManager dms.DMSManager, nvConfigUtils nvconfig.NVConfigUtils, spectrumXConfigManager spectrumx.SpectrumXManager) ConfigurationManager {
	utils := newConfigurationUtils(dmsManager)
	return configurationManager{configurationUtils: utils, configValidation: newConfigValidation(utils, eventRecorder), nvConfigUtils: nvConfigUtils, spectrumXConfigManager: spectrumXConfigManager}
}
