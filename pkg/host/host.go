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

package host

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
)

// HostManager contains logic for managing NIC devices on the host
type HostManager interface {
	// ValidateDeviceNvSpec will validate device's non-volatile spec against already applied configuration on the host
	// returns bool - nv config update required
	// returns bool - reboot required
	// returns error - there are errors in device's spec
	ValidateDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, bool, error)
	// ApplyDeviceNvSpec calculates device's missing nv spec configuration and applies it to the device on the host
	// returns bool - reboot required
	// returns error - there were errors while applying nv configuration
	ApplyDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, error)
	// ApplyDeviceRuntimeSpec calculates device's missing runtime spec configuration and applies it to the device on the host
	// returns error - there were errors while applying nv configuration
	ApplyDeviceRuntimeSpec(device *v1alpha1.NicDevice) error
	// DiscoverOfedVersion retrieves installed OFED version
	// returns string - installed OFED version
	// returns empty string - OFED isn't installed or version couldn't be determined
	DiscoverOfedVersion() string
	// ResetNicFirmware resets NIC's firmware
	// Operation can be long, required context to be able to terminate by timeout
	// IB devices need to communicate with other nodes for confirmation
	// return err - there were errors while resetting NIC firmware
	ResetNicFirmware(ctx context.Context, device *v1alpha1.NicDevice) error
}

type hostManager struct {
	hostUtils        HostUtils
	configValidation configValidation
}

// ValidateDeviceNvSpec will validate device's non-volatile spec against already applied configuration on the host
// returns bool - nv config update required
// returns bool - reboot required
// returns error - there are errors in device's spec
// if fully matches in current and next config, returns false, false
// if fully matched next but not current, returns false, true
// if not fully matched next boot, returns true, true
func (h hostManager) ValidateDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, bool, error) {
	log.Log.Info("hostManager.ValidateDeviceNvSpec", "device", device.Name)

	nvConfig, err := h.hostUtils.QueryNvConfig(ctx, device.Status.Ports[0].PCI)
	if err != nil {
		log.Log.Error(err, "failed to query nv config", "device", device.Name)
		return false, false, err
	}

	if device.Spec.Configuration.ResetToDefault {
		return h.configValidation.ValidateResetToDefault(nvConfig)
	}

	desiredConfig, err := h.configValidation.ConstructNvParamMapFromTemplate(device, nvConfig)
	if err != nil {
		log.Log.Error(err, "failed to calculate desired nvconfig parameters", "device", device.Name)
		return false, false, err
	}

	configUpdateNeeded := false
	rebootNeeded := false

	// If ADVANCED_PCI_SETTINGS are enabled in current config, unknown parameters are treated as spec error
	advancedPciSettingsEnabled := h.configValidation.AdvancedPCISettingsEnabled(nvConfig)

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

	return configUpdateNeeded, rebootNeeded, nil
}

// ApplyDeviceNvSpec calculates device's missing nv spec configuration and applies it to the device on the host
// returns bool - reboot required
// returns error - there were errors while applying nv configuration
func (h hostManager) ApplyDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, error) {
	log.Log.Info("hostManager.ApplyDeviceNvSpec", "device", device.Name)

	pciAddr := device.Status.Ports[0].PCI

	nvConfig, err := h.hostUtils.QueryNvConfig(ctx, device.Status.Ports[0].PCI)
	if err != nil {
		log.Log.Error(err, "failed to query nv config", "device", device.Name)
		return false, err
	}

	if device.Spec.Configuration.ResetToDefault {
		log.Log.Info("resetting nv config to default", "device", device.Name)
		bf3OperationModeValue, isBF3device := nvConfig.CurrentConfig[consts.BF3OperationModeParam]

		err := h.hostUtils.ResetNvConfig(pciAddr)
		if err != nil {
			log.Log.Error(err, "Failed to reset nv config", "device", device.Name)
			return false, err
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
			err = h.hostUtils.SetNvConfigParameter(pciAddr, consts.BF3OperationModeParam, val)
			if err != nil {
				log.Log.Error(err, "Failed to restore the BlueField device mode of operation", "device", device.Name, "mode", mode, "param", consts.BF3OperationModeParam, "value", val)
				return false, err
			}
		}

		err = h.hostUtils.SetNvConfigParameter(pciAddr, consts.AdvancedPCISettingsParam, consts.NvParamTrue)
		if err != nil {
			log.Log.Error(err, "Failed to apply nv config parameter", "device", device.Name, "param", consts.AdvancedPCISettingsParam, "value", consts.NvParamTrue)
			return false, err
		}

		return true, err
	}

	// if ADVANCED_PCI_SETTINGS == 0, not all nv config parameters are available for configuration
	// we enable this parameter first to unlock them
	if !h.configValidation.AdvancedPCISettingsEnabled(nvConfig) {
		log.Log.V(2).Info("AdvancedPciSettings not enabled, fw reset required", "device", device.Name)
		err = h.hostUtils.SetNvConfigParameter(pciAddr, consts.AdvancedPCISettingsParam, consts.NvParamTrue)
		if err != nil {
			log.Log.Error(err, "Failed to apply nv config parameter", "device", device.Name, "param", consts.AdvancedPCISettingsParam, "value", consts.NvParamTrue)
			return false, err
		}

		err := h.ResetNicFirmware(ctx, device)
		if err != nil {
			log.Log.Error(err, "Failed to reset NIC firmware, reboot required to apply ADVANCED_PCI_SETTINGS", "device", device.Name)
			// We try to perform FW reset after setting the ADVANCED_PCI_SETTINGS to save us a reboot
			// However, if the soft FW reset fails for some reason, we need to perform a reboot to unlock
			// all the nv config parameters
			return true, nil
		}

		// Query nv config again, additional options could become available
		nvConfig, err = h.hostUtils.QueryNvConfig(ctx, device.Status.Ports[0].PCI)
		if err != nil {
			log.Log.Error(err, "failed to query nv config", "device", device.Name)
			return false, err
		}
	}

	desiredConfig, err := h.configValidation.ConstructNvParamMapFromTemplate(device, nvConfig)
	if err != nil {
		log.Log.Error(err, "failed to calculate desired nvconfig parameters", "device", device.Name)
		return false, err
	}

	paramsToApply := map[string]string{}

	for param, value := range desiredConfig {
		nextValues, found := nvConfig.NextBootConfig[param]
		if !found {
			err = types.IncorrectSpecError(fmt.Sprintf("Parameter %s unsupported for device %s", param, device.Name))
			log.Log.Error(err, "can't set nv config parameter for device")
			return false, err
		}

		if !slices.Contains(nextValues, value) {
			paramsToApply[param] = value
		}
	}

	log.Log.V(2).Info("applying nv config to device", "device", device.Name, "config", paramsToApply)

	for param, value := range paramsToApply {
		err = h.hostUtils.SetNvConfigParameter(pciAddr, param, value)
		if err != nil {
			log.Log.Error(err, "Failed to apply nv config parameter", "device", device.Name, "param", param, "value", value)
			return false, err
		}
	}

	log.Log.V(2).Info("nv config successfully applied to device", "device", device.Name)

	return true, nil
}

// ApplyDeviceRuntimeSpec calculates device's missing runtime spec configuration and applies it to the device on the host
// returns error - there were errors while applying nv configuration
func (h hostManager) ApplyDeviceRuntimeSpec(device *v1alpha1.NicDevice) error {
	log.Log.Info("hostManager.ApplyDeviceRuntimeSpec", "device", device.Name)

	alreadyApplied, err := h.configValidation.RuntimeConfigApplied(device)
	if err != nil {
		log.Log.Error(err, "failed to verify runtime configuration", "device", device)
	}

	if alreadyApplied {
		log.Log.V(2).Info("runtime config already applied", "device", device)
		return nil
	}

	desiredMaxReadReqSize, desiredTrust, desiredPfc := h.configValidation.CalculateDesiredRuntimeConfig(device)

	ports := device.Status.Ports

	if desiredMaxReadReqSize != 0 {
		for _, port := range ports {
			err = h.hostUtils.SetMaxReadRequestSize(port.PCI, desiredMaxReadReqSize)
			if err != nil {
				log.Log.Error(err, "failed to apply runtime configuration", "device", device)
				return err
			}
		}
	}

	for _, port := range ports {
		err = h.hostUtils.SetTrustAndPFC(port.NetworkInterface, desiredTrust, desiredPfc)
		if err != nil {
			log.Log.Error(err, "failed to apply runtime configuration", "device", device)
			return err
		}
	}

	return nil
}

// DiscoverOfedVersion retrieves installed OFED version
// returns string - installed OFED version
// returns error - OFED isn't installed or version couldn't be determined
func (h hostManager) DiscoverOfedVersion() string {
	return h.hostUtils.GetOfedVersion()
}

// ResetNicFirmware resets NIC's firmware
// Operation can be long, required context to be able to terminate by timeout
// IB devices need to communicate with other nodes for confirmation
// return err - there were errors while resetting NIC firmware
func (h hostManager) ResetNicFirmware(ctx context.Context, device *v1alpha1.NicDevice) error {
	log.Log.Info("hostManager.ResetNicFirmware", "device", device.Name)
	err := h.hostUtils.ResetNicFirmware(ctx, device.Status.Ports[0].PCI)
	if err != nil {
		log.Log.Error(err, "Failed to reset NIC firmware", "device", device.Name)
		return err
	}

	return nil
}

func NewHostManager(hostUtils HostUtils, eventRecorder record.EventRecorder) HostManager {
	return hostManager{hostUtils: hostUtils, configValidation: newConfigValidation(hostUtils, eventRecorder)}
}
