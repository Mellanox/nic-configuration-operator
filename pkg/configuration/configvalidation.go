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
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

type configValidation interface {
	// ConstructNvParamMapFromTemplate translates a configuration template into a set of nvconfig parameters
	// operates under the assumption that spec validation was already carried out
	ConstructNvParamMapFromTemplate(
		device *v1alpha1.NicDevice, nvConfigQuery types.NvConfigQuery) (map[string]string, error)
	// ValidateResetToDefault checks if device's nv config has been reset to default in current and next boots
	// returns bool - need to perform reset
	// returns bool - reboot required
	// returns error - if an error occurred during validation
	ValidateResetToDefault(nvConfig types.NvConfigQuery) (bool, bool, error)
	// AdvancedPCISettingsEnabled returns true if ADVANCED_PCI_SETTINGS param is enabled for current config
	AdvancedPCISettingsEnabled(nvConfig types.NvConfigQuery) bool
	// RuntimeConfigApplied checks if desired runtime config is applied
	RuntimeConfigApplied(device *v1alpha1.NicDevice) (bool, error)
	// CalculateDesiredRuntimeConfig returns desired values for runtime config
	// returns int - maxReadRequestSize
	// returns string - qos trust mode
	// returns string - qos pfc settings
	CalculateDesiredRuntimeConfig(device *v1alpha1.NicDevice) (int, string, string)
}

type configValidationImpl struct {
	utils         ConfigurationUtils
	eventRecorder record.EventRecorder
}

func nvParamLinkTypeFromName(linkType string) string {
	if linkType == consts.Infiniband {
		return consts.NvParamLinkTypeInfiniband
	} else if linkType == consts.Ethernet {
		return consts.NvParamLinkTypeEthernet
	}

	return ""
}

func applyDefaultNvConfigValueIfExists(
	paramName string, desiredParameters map[string]string, query types.NvConfigQuery) {
	defaultValues, found := query.DefaultConfig[paramName]
	// Default values might not yet be available if ENABLE_PCI_OPTIMIZATIONS is disabled
	if found {
		// Take the default numeric value
		desiredParameters[paramName] = defaultValues[len(defaultValues)-1]
	}
}

// ConstructNvParamMapFromTemplate translates a configuration template into a set of nvconfig parameters
// operates under the assumption that spec validation was already carried out
func (v *configValidationImpl) ConstructNvParamMapFromTemplate(
	device *v1alpha1.NicDevice, query types.NvConfigQuery) (map[string]string, error) {
	desiredParameters := map[string]string{}

	template := device.Spec.Configuration.Template
	secondPortPresent := len(device.Status.Ports) > 1

	desiredParameters[consts.SriovEnabledParam] = consts.NvParamFalse
	desiredParameters[consts.SriovNumOfVfsParam] = "0"
	if template.NumVfs > 0 {
		desiredParameters[consts.SriovEnabledParam] = consts.NvParamTrue
		desiredParameters[consts.SriovNumOfVfsParam] = strconv.Itoa(template.NumVfs)
	}

	// Link type change is not allowed on some devices
	_, canChangeLinkType := query.DefaultConfig[consts.LinkTypeP1Param]
	if canChangeLinkType {
		linkType := nvParamLinkTypeFromName(string(template.LinkType))
		desiredParameters[consts.LinkTypeP1Param] = linkType
		if secondPortPresent {
			desiredParameters[consts.LinkTypeP2Param] = linkType
		}
	} else {
		desiredLinkType := string(device.Spec.Configuration.Template.LinkType)

		for _, port := range device.Status.Ports {
			if port.NetworkInterface != "" && v.utils.GetLinkType(port.NetworkInterface) != desiredLinkType {
				err := types.IncorrectSpecError(
					fmt.Sprintf(
						"device does not support link type change, wrong link type provided in the template, should be: %s",
						v.utils.GetLinkType(port.NetworkInterface)))
				log.Log.Error(err, "incorrect spec", "device", device.Name)
				return desiredParameters, err
			}
		}
	}

	if template.PciPerformanceOptimized != nil && template.PciPerformanceOptimized.Enabled {
		if template.PciPerformanceOptimized.MaxAccOutRead != 0 {
			desiredParameters[consts.MaxAccOutReadParam] = strconv.Itoa(template.PciPerformanceOptimized.MaxAccOutRead)
		} else {
			// MAX_ACC_OUT_READ parameter is hidden if ADVANCED_PCI_SETTINGS is disabled
			if v.AdvancedPCISettingsEnabled(query) {
				values, found := query.DefaultConfig[consts.MaxAccOutReadParam]
				if !found {
					err := types.IncorrectSpecError(
						"Device does not support pci performance nv config parameters")
					log.Log.Error(err, "incorrect spec", "device", device.Name, "parameter", consts.MaxAccOutReadParam)
					return desiredParameters, err
				}

				maxAccOutReadParamDefaultValue := values[len(values)-1]

				// According to the PRM, setting MAX_ACC_OUT_READ to zero enables the auto mode,
				// which applies the best suitable optimizations.
				// However, there is a bug in certain FW versions, where the zero value is not available.
				// In this case, until the fix is available, skipping this parameter and emitting a warning
				if maxAccOutReadParamDefaultValue == consts.NvParamZero {
					applyDefaultNvConfigValueIfExists(consts.MaxAccOutReadParam, desiredParameters, query)
				} else {
					warning := fmt.Sprintf("%s nv config parameter does not work properly on this version of FW, skipping it", consts.MaxAccOutReadParam)
					if v.eventRecorder != nil {
						v.eventRecorder.Event(device, v1.EventTypeWarning, "FirmwareError", warning)
					}
					log.Log.Error(errors.New(warning), "skipping parameter", "device", device.Name, "fw version", device.Status.FirmwareVersion)
				}
			}
		}

		// maxReadRequest is applied as runtime configuration
	}

	if template.RoceOptimized != nil && template.RoceOptimized.Enabled {
		if template.LinkType == consts.Infiniband {
			err := types.IncorrectSpecError(
				"RoceOptimized settings can only be used with link type Ethernet")
			log.Log.Error(err, "incorrect spec", "device", device.Name)
			return desiredParameters, err
		}

		desiredParameters[consts.RoceCcPrioMaskP1Param] = "255"
		desiredParameters[consts.CnpDscpP1Param] = "4"
		desiredParameters[consts.Cnp802pPrioP1Param] = "6"

		if secondPortPresent {
			desiredParameters[consts.RoceCcPrioMaskP2Param] = "255"
			desiredParameters[consts.CnpDscpP2Param] = "4"
			desiredParameters[consts.Cnp802pPrioP2Param] = "6"
		}

		// qos settings are applied as runtime configuration
	} else {
		applyDefaultNvConfigValueIfExists(consts.RoceCcPrioMaskP1Param, desiredParameters, query)
		applyDefaultNvConfigValueIfExists(consts.CnpDscpP1Param, desiredParameters, query)
		applyDefaultNvConfigValueIfExists(consts.Cnp802pPrioP1Param, desiredParameters, query)
		if secondPortPresent {
			applyDefaultNvConfigValueIfExists(consts.RoceCcPrioMaskP2Param, desiredParameters, query)
			applyDefaultNvConfigValueIfExists(consts.CnpDscpP2Param, desiredParameters, query)
			applyDefaultNvConfigValueIfExists(consts.Cnp802pPrioP2Param, desiredParameters, query)
		}
	}

	if template.GpuDirectOptimized != nil && template.GpuDirectOptimized.Enabled {
		if template.GpuDirectOptimized.Env != consts.EnvBaremetal {
			err := types.IncorrectSpecError("GpuDirectOptimized supports only Baremetal env")
			log.Log.Error(err, "incorrect spec", "device", device.Name)
			return desiredParameters, err
		}

		desiredParameters[consts.AtsEnabledParam] = consts.NvParamFalse
		if template.PciPerformanceOptimized == nil || !template.PciPerformanceOptimized.Enabled {
			err := types.IncorrectSpecError(
				"GpuDirectOptimized should only be enabled together with PciPerformanceOptimized")
			log.Log.Error(err, "incorrect spec", "device", device.Name)
			return desiredParameters, err
		}
	} else {
		applyDefaultNvConfigValueIfExists(consts.AtsEnabledParam, desiredParameters, query)
	}
	//TODO: Uncomment once we'll fix DPU mode reset procedure
	//for _, rawParam := range template.RawNvConfig {
	//	// Ignore second port params if device has a single port
	//	if strings.HasSuffix(rawParam.Name, consts.SecondPortPrefix) && !secondPortPresent {
	//		continue
	//	}
	//
	//	desiredParameters[rawParam.Name] = rawParam.Value
	//}

	return desiredParameters, nil
}

// ValidateResetToDefault checks if device's nv config has been reset to default in current and next boots
// returns bool - need to perform reset
// returns bool - reboot required
// returns error - if an error occurred during validation
func (v *configValidationImpl) ValidateResetToDefault(nvConfig types.NvConfigQuery) (bool, bool, error) {
	// ResetToDefault requires us to set ADVANCED_PCI_SETTINGS=true, which is not a default value
	// Deleting this key from maps so that it doesn't interfere with comparisons
	delete(nvConfig.DefaultConfig, consts.AdvancedPCISettingsParam)

	alreadyResetInCurrent := false
	willResetInNextBoot := false

	advancedPciSettingsEnabledInCurrentConfig := false
	if values, found := nvConfig.CurrentConfig[consts.AdvancedPCISettingsParam]; found && slices.Contains(values, consts.NvParamTrue) {
		advancedPciSettingsEnabledInCurrentConfig = true
	}
	if advancedPciSettingsEnabledInCurrentConfig {
		delete(nvConfig.CurrentConfig, consts.AdvancedPCISettingsParam)
		if reflect.DeepEqual(nvConfig.CurrentConfig, nvConfig.DefaultConfig) {
			alreadyResetInCurrent = true
		}
	}

	advancedPciSettingsEnabledInNextBootConfig := false
	if values, found := nvConfig.NextBootConfig[consts.AdvancedPCISettingsParam]; found && slices.Contains(values, consts.NvParamTrue) {
		advancedPciSettingsEnabledInNextBootConfig = true
	}
	if advancedPciSettingsEnabledInNextBootConfig {
		delete(nvConfig.NextBootConfig, consts.AdvancedPCISettingsParam)
		if reflect.DeepEqual(nvConfig.NextBootConfig, nvConfig.DefaultConfig) {
			willResetInNextBoot = true
		}
	}

	// Reset complete, nothing to do for now
	if alreadyResetInCurrent && willResetInNextBoot {
		return false, false, nil
	}
	// Reset will complete after reboot
	if willResetInNextBoot {
		return false, true, nil
	}
	// Reset required
	return true, true, nil
}

// AdvancedPCISettingsEnabled returns true if ADVANCED_PCI_SETTINGS param is enabled for current config
func (v *configValidationImpl) AdvancedPCISettingsEnabled(nvConfig types.NvConfigQuery) bool {
	if values, found := nvConfig.CurrentConfig[consts.AdvancedPCISettingsParam]; found && slices.Contains(values, consts.NvParamTrue) {
		return true
	}
	return false
}

// RuntimeConfigApplied checks if desired runtime config is applied
func (v *configValidationImpl) RuntimeConfigApplied(device *v1alpha1.NicDevice) (bool, error) {
	ports := device.Status.Ports

	desiredMaxReadReqSize, desiredTrust, desiredPfc := v.CalculateDesiredRuntimeConfig(device)

	if desiredMaxReadReqSize != 0 {
		for _, port := range ports {
			actualMaxReadReqSize, err := v.utils.GetMaxReadRequestSize(port.PCI)
			if err != nil {
				log.Log.Error(err, "can't validate maxReadReqSize", "device", device.Name)
				return false, err
			}
			if actualMaxReadReqSize != desiredMaxReadReqSize {
				return false, nil
			}
		}
	}

	// Don't validate QoS settings if neither trust nor pfc changes are requested
	if desiredTrust == "" && desiredPfc == "" {
		return true, nil
	}

	for _, port := range ports {
		if port.NetworkInterface == "" {
			err := fmt.Errorf("cannot apply QoS settings for device port %s, network interface is missing", port.PCI)
			log.Log.Error(err, "cannot validate QoS settings", "device", device.Name, "port", port.PCI)
			return false, err
		}
		actualTrust, actualPfc, err := v.utils.GetTrustAndPFC(port.NetworkInterface)
		if err != nil {
			log.Log.Error(err, "cannot validate QoS settings", "device", device.Name, "port", port.PCI)
			return false, err
		}
		if actualTrust != desiredTrust || actualPfc != desiredPfc {
			return false, nil
		}
	}

	return true, nil
}

// CalculateDesiredRuntimeConfig returns desired values for runtime config
// returns int - maxReadRequestSize
// returns string - qos trust mode
// returns string - qos pfc settings
func (v *configValidationImpl) CalculateDesiredRuntimeConfig(device *v1alpha1.NicDevice) (int, string, string) {
	maxReadRequestSize := 0

	template := device.Spec.Configuration.Template

	if template.PciPerformanceOptimized != nil && template.PciPerformanceOptimized.Enabled {
		if template.PciPerformanceOptimized.MaxReadRequest != 0 {
			maxReadRequestSize = template.PciPerformanceOptimized.MaxReadRequest
		} else {
			maxReadRequestSize = 4096
		}
	}

	// QoS settings are not available for IB devices
	if template.LinkType == consts.Infiniband {
		return maxReadRequestSize, "", ""
	}

	var trust, pfc string

	if template.RoceOptimized != nil && template.RoceOptimized.Enabled {
		trust = "dscp"
		pfc = "0,0,0,1,0,0,0,0"

		if template.RoceOptimized.Qos != nil {
			trust = template.RoceOptimized.Qos.Trust
			pfc = template.RoceOptimized.Qos.PFC
		}
	}

	return maxReadRequestSize, trust, pfc
}

func newConfigValidation(utils ConfigurationUtils, eventRecorder record.EventRecorder) configValidation {
	return &configValidationImpl{utils: utils, eventRecorder: eventRecorder}
}
