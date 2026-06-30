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
	"regexp"
	"slices"
	"sort"
	"strconv"
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

	// 1. Query current nv config for every port.
	nvConfigsForPorts, err := h.queryNvConfigs(ctx, device)
	if err != nil {
		log.Log.Error(err, "failed to query nv configs", "device", device.Name)
		return false, false, nil, err
	}
	firstPortConfig := nvConfigsForPorts[device.Status.Ports[0].PCI]

	// 2. Reset-to-default takes precedence over everything else.
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

	// 3. Network Bay system_conf: the params that don't match the requested named profile (empty for
	//    non-Network-Bay devices). set_system_conf is the lowest-priority baseline.
	systemConfMismatched, err := h.systemConfMismatchedParams(ctx, device)
	if err != nil {
		return false, false, nil, err
	}
	if len(systemConfMismatched) > 0 {
		log.Log.V(2).Info("system_conf params mismatched against the profile", "device", device.Name, "params", systemConfMismatched)
	}

	// 4. Combined override config (Spectrum-X breakout + postBreakout + template + rawNvConfig, raw wins).
	//    Validated against the device's next boot, exactly like the apply path — so a value already staged
	//    for next boot but not yet rebooted reports reboot-required instead of looping.
	overrides, err := h.buildOverrideParams(device, firstPortConfig)
	if err != nil {
		return false, false, nil, err
	}
	log.Log.V(2).Info("validating combined nv config", "device", device.Name, "params", overrides)

	configUpdateNeeded, rebootNeeded, unsupportedParams := validateTemplateParamsApplied(nvConfigsForPorts, overrides)
	if configUpdateNeeded {
		log.Log.Info("nv config not yet applied to next boot", "device", device.Name)
	}
	if len(unsupportedParams) > 0 {
		log.Log.Info("some nv config params are unsupported on this device and will be skipped", "device", device.Name, "params", unsupportedParams)
	}

	// 5. system_conf coverage: a mismatched profile param not covered (range-aware) by the override config
	//    means the baseline itself drifted and set_system_conf must be re-applied (reboot-required).
	if systemConfDrifted(overrides, systemConfMismatched) {
		log.Log.Info("Network Bay system_conf drifted, set_system_conf re-apply required",
			"device", device.Name, "mismatched", systemConfMismatched)
		configUpdateNeeded = true
		rebootNeeded = true
	}

	log.Log.V(2).Info("nv spec validation result", "device", device.Name,
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

// ApplyNVConfiguration calculates device's missing nv spec configuration and applies it to the device on the host
// returns *ConfigurationApplyResult - result of the apply operation
// returns error - there were errors while applying nv configuration
func (h configurationManager) ApplyNVConfiguration(ctx context.Context, device *v1alpha1.NicDevice, options *types.ConfigurationOptions) (*types.ConfigurationApplyResult, error) {
	log.Log.Info("configurationManager.ApplyNVConfiguration", "device", device.Name)

	if device.Spec.Configuration == nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}

	// 1. Query current nv config for every port.
	nvConfigsForPorts, err := h.queryNvConfigs(ctx, device)
	if err != nil {
		log.Log.Error(err, "failed to query nv configs", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}
	firstPortPCIAddr := device.Status.Ports[0].PCI
	firstPortConfig := nvConfigsForPorts[firstPortPCIAddr]

	// 2. Reset-to-default takes precedence: reset and finish.
	if device.Spec.Configuration.ResetToDefault {
		return h.applyResetToDefault(device, firstPortPCIAddr, firstPortConfig)
	}

	// 3. Network Bay system_conf: the params that don't match the requested named profile.
	systemConfMismatched, err := h.systemConfMismatchedParams(ctx, device)
	if err != nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	// 4. Combined desired params: Spectrum-X breakout + postBreakout + template + rawNvConfig (raw wins).
	desiredParams, err := h.buildOverrideParams(device, firstPortConfig)
	if err != nil {
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}
	log.Log.V(2).Info("combined desired nv config built", "device", device.Name, "params", desiredParams, "force", options.Force)

	// 5. set_system_conf baseline: if the combined params do not cover all mismatched profile params
	//    (range-aware), the baseline itself drifted on an uncovered param — re-stage set_system_conf
	//    before the override batch so the overrides still win in the same next-boot config.
	systemConfApplied := false
	if systemConfDrifted(desiredParams, systemConfMismatched) {
		log.Log.Info("Network Bay system_conf not covered by overrides, applying set_system_conf",
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
			log.Log.Error(err, "failed to re-query nv configs after set_system_conf", "device", device.Name)
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
	}

	anyParamsApplied := false
	hasUnsupportedParams := false

	// 6 & 7. Apply the combined override params on every PF the device exposes.
	//   - force=true: apply every param (mlxconfig --force accepts params not currently visible, e.g.
	//     per-port params staged before a breakout reboot exposes their ports).
	//   - force=false: apply only the params visible on this PF whose value differs; params not yet
	//     visible are skipped this round and picked up after a reboot exposes them (which sequences
	//     breakout before postBreakout without --force).
	for pciAddr, nvConfig := range nvConfigsForPorts {
		batch := map[string]string{}
		if options.Force {
			batch = desiredParams
		} else {
			for param, value := range desiredParams {
				nextValues, found := nvConfig.NextBootConfig[param]
				if !found {
					hasUnsupportedParams = true
					continue
				}
				if !slices.Contains(nextValues, value) {
					batch[param] = value
				}
			}
		}
		if len(batch) == 0 {
			continue
		}
		log.Log.V(2).Info("applying nv config to device", "device", device.Name, "pci", pciAddr, "config", batch, "force", options.Force)
		if err := h.nvConfigUtils.SetNvConfigParametersBatch(pciAddr, batch, options.WithDefault, options.Force); err != nil {
			log.Log.Error(err, "Failed to apply nv config parameters", "device", device.Name, "params", batch)
			return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
		}
		anyParamsApplied = true
	}

	if !anyParamsApplied && !hasUnsupportedParams && !systemConfApplied {
		log.Log.V(2).Info("nv config already up to date, nothing to apply", "device", device.Name)
		return &types.ConfigurationApplyResult{Status: types.ApplyStatusNothingToDo}, nil
	}

	status := types.ApplyStatusSuccess
	if hasUnsupportedParams {
		status = types.ApplyStatusPartiallyApplied
	}
	rebootRequired := anyParamsApplied || systemConfApplied
	log.Log.Info("nv config applied", "device", device.Name, "status", status, "rebootRequired", rebootRequired)

	return &types.ConfigurationApplyResult{Status: status, RebootRequired: rebootRequired}, nil
}

// buildOverrideParams returns the combined nv params that override the Network Bay system_conf baseline:
// Spectrum-X breakout + postBreakout (when Spectrum-X is enabled) + the spec template params, with
// rawNvConfig merged on top (rawNvConfig wins). Priority: rawNvConfig > Spectrum-X / template > system_conf.
//
// Indexed-range keys are expanded into per-index entries *per layer, before merging*, so each index is
// value-validated/applied against the concrete keys QueryNvConfig / validate_system_conf expose, AND a
// higher-priority layer's range (e.g. rawNvConfig MODULE_SPLIT_M0[0..3]) deterministically overrides a
// lower-priority layer's concrete key (e.g. Spectrum-X MODULE_SPLIT_M0[2]). Merging in priority order is
// what makes the override deterministic — expanding the already-merged map would resolve collisions by
// Go map iteration order instead.
func (h configurationManager) buildOverrideParams(device *v1alpha1.NicDevice, firstPortConfig types.NvConfigQuery) (map[string]string, error) {
	combined := map[string]string{}

	if spectrumXEnabled(device) {
		breakoutParams, err := h.spectrumXConfigManager.GetBreakoutMlxConfig(device)
		if err != nil {
			return nil, err
		}
		postBreakoutParams, err := h.spectrumXConfigManager.GetPostBreakoutMlxConfig(device)
		if err != nil {
			return nil, err
		}
		if err := mergeExpandedRanges(combined, breakoutParams); err != nil {
			return nil, err
		}
		if err := mergeExpandedRanges(combined, postBreakoutParams); err != nil {
			return nil, err
		}
	}

	if device.Spec.Configuration.Template != nil {
		desiredConfig, err := h.configValidation.ConstructNvParamMapFromTemplate(device, firstPortConfig)
		if err != nil {
			log.Log.Error(err, "failed to calculate desired nvconfig parameters", "device", device.Name)
			return nil, err
		}
		if err := mergeExpandedRanges(combined, desiredConfig); err != nil {
			return nil, err
		}
	}

	// rawNvConfig has the highest priority, so it is expanded and merged last: its expanded indices
	// overwrite any lower-priority concrete key they collide with.
	if err := mergeExpandedRanges(combined, getRawNvConfigParams(device)); err != nil {
		return nil, err
	}

	return combined, nil
}

// setSystemConf stages the requested Network Bay set_system_conf for the device's ASIC (the
// lowest-priority baseline). Callers stage it before the override params.
func (h configurationManager) setSystemConf(ctx context.Context, device *v1alpha1.NicDevice, options *types.ConfigurationOptions) error {
	conf := device.Spec.Configuration.Template.NetworkBay.Conf
	asic := device.Status.NetworkBay.Asic
	pci := device.Status.Ports[0].PCI

	log.Log.Info("applying Network Bay system_conf", "device", device.Name, "conf", conf, "asic", asic)
	if err := h.nvConfigUtils.SetSystemConf(ctx, pci, conf, asic, options.Force); err != nil {
		log.Log.Error(err, "failed to apply system_conf", "device", device.Name)
		return err
	}
	return nil
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
//
// Per-param results require the optional nvconfig.SystemConfValidator extension. If the injected
// nvConfigUtils does not implement it (e.g. a library consumer's custom implementation), Network Bay
// system_conf management is skipped — there is no way to enumerate the mismatched params safely.
func (h configurationManager) systemConfMismatchedParams(ctx context.Context, device *v1alpha1.NicDevice) ([]string, error) {
	if !hasNetworkBaySpec(device) {
		return nil, nil
	}

	// A Network Bay template requires per-param results to decide drift. If the injected nvConfigUtils
	// can't provide them, fail closed rather than silently reporting convergence and never applying
	// set_system_conf.
	validator, ok := h.nvConfigUtils.(nvconfig.SystemConfValidator)
	if !ok {
		return nil, fmt.Errorf("device %s requests Network Bay system_conf but nvConfigUtils does not implement SystemConfValidator", device.Name)
	}

	conf := device.Spec.Configuration.Template.NetworkBay.Conf
	asic := device.Status.NetworkBay.Asic
	pci := device.Status.Ports[0].PCI

	result, err := validator.ValidateSystemConfDetailed(ctx, pci, conf, asic)
	if err != nil {
		log.Log.Error(err, "failed to validate system_conf", "device", device.Name)
		return nil, err
	}

	var mismatched []string
	for _, entry := range result.Entries {
		if entry.Status == nvconfig.SystemConfStatusMismatch {
			mismatched = append(mismatched, entry.Param)
		}
	}

	// Fail closed: a non-matching result with no recognized MISMATCH rows would otherwise look identical
	// to a matching profile and silently skip set_system_conf. Surface it so the reconcile retries instead.
	if !result.Matches && len(mismatched) == 0 {
		return nil, fmt.Errorf("device %s system_conf %q reports a mismatch but no mismatched params were parsed", device.Name, conf)
	}
	return mismatched, nil
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

// indexedRangeRegex matches an indexed-range nv param key like "MODULE_SPLIT_M0[0..3]".
var indexedRangeRegex = regexp.MustCompile(`^(.*)\[(\d+)\.\.(\d+)\]$`)

const (
	// maxRangeSpan caps how many individual indices a single range key may expand into. mlxconfig indices
	// are small (lanes 0..15, ports 1..8), so this is generous; it guards a single user-supplied
	// rawNvConfig range like "X[0..1000000000]" from exhausting CPU/memory.
	maxRangeSpan = 256
	// maxExpandedParams caps the *aggregate* number of expanded params across all layers. rawNvConfig has
	// no maxItems, so without this a CR full of distinct in-bounds ranges could still materialize millions
	// of entries each reconcile and OOM the daemon.
	maxExpandedParams = 4096
)

// mergeExpandedRanges expands src's indexed-range keys into one entry per index (carrying the same value)
// and merges the result into dst (src wins on collisions, so callers merge in increasing priority order).
// Non-range keys pass through unchanged. Returns an error if a single range exceeds maxRangeSpan or the
// running total in dst exceeds maxExpandedParams — both are checked as entries are added, so an abusive
// input is rejected before it is fully materialized.
func mergeExpandedRanges(dst, src map[string]string) error {
	add := func(key, val string) error {
		dst[key] = val
		if len(dst) > maxExpandedParams {
			return fmt.Errorf("expanded nv config exceeds %d params; refusing to materialize", maxExpandedParams)
		}
		return nil
	}

	for key, val := range src {
		m := indexedRangeRegex.FindStringSubmatch(key)
		if m == nil {
			if err := add(key, val); err != nil {
				return err
			}
			continue
		}
		lo, errLo := strconv.Atoi(m[2])
		hi, errHi := strconv.Atoi(m[3])
		if errLo != nil || errHi != nil || hi < lo {
			// Not a usable numeric range (e.g. indices overflow int) — keep the literal key.
			if err := add(key, val); err != nil {
				return err
			}
			continue
		}
		// hi-lo is overflow-safe for non-negative lo<=hi; the loop breaks on equality so it never
		// increments past hi (which would overflow when hi == math.MaxInt).
		if hi-lo >= maxRangeSpan {
			return fmt.Errorf("nv config range %q spans %d indices, exceeds limit %d", key, hi-lo+1, maxRangeSpan)
		}
		for i := lo; ; i++ {
			if err := add(fmt.Sprintf("%s[%d]", m[1], i), val); err != nil {
				return err
			}
			if i == hi {
				break
			}
		}
	}
	return nil
}

// systemConfDrifted reports whether any mismatched system_conf param is left uncovered by the override
// config. overrides has already had range keys expanded into individual indices (see mergeExpandedRanges),
// so an accepted range override covers the individual mismatch rows validate_system_conf emits. An empty
// mismatched slice is never drift.
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
