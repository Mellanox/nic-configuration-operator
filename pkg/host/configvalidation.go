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
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

type configValidation interface {
	// ConstructNvParamMapFromTemplate translates a configuration template into a set of nvconfig parameters
	// operates under the assumption that spec validation was already carried out
	ConstructNvParamMapFromTemplate(
		device *v1alpha1.NicDevice, defaultValues map[string]string) (map[string]string, error)
	// ValidateResetToDefault checks if device's nv config has been reset to default in current and next boots
	// returns bool - need to perform reset
	// returns bool - reboot required
	// returns error - if an error occurred during validation
	ValidateResetToDefault(nvConfig types.NvConfigQuery) (bool, bool, error)
	// AdvancedPCISettingsEnabled returns true if ADVANCED_PCI_SETTINGS param is enabled for current config
	AdvancedPCISettingsEnabled(currentConfig map[string]string) bool
	// RuntimeConfigApplied checks if desired runtime config is applied
	RuntimeConfigApplied(device *v1alpha1.NicDevice) (bool, error)
	// CalculateDesiredRuntimeConfig returns desired values for runtime config
	// returns int - maxReadRequestSize
	// returns string - qos trust mode
	// returns string - qos pfc settings
	CalculateDesiredRuntimeConfig(device *v1alpha1.NicDevice) (int, string, string)
}

type configValidationImpl struct {
	utils HostUtils
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
	paramName string, desiredParameters map[string]string, defaultValues map[string]string) {
	defaultValue, found := defaultValues[paramName]
	// Default values might not yet be available if ENABLE_PCI_OPTIMIZATIONS is disabled
	if found {
		desiredParameters[paramName] = defaultValue
	}
}

// ConstructNvParamMapFromTemplate translates a configuration template into a set of nvconfig parameters
// operates under the assumption that spec validation was already carried out
func (v *configValidationImpl) ConstructNvParamMapFromTemplate(
	device *v1alpha1.NicDevice, defaultValues map[string]string) (map[string]string, error) {
	desiredParameters := map[string]string{}

	template := device.Spec.Configuration.Template
	secondPortPresent := len(device.Status.Ports) > 1
	pciAddr := device.Status.Ports[0].PCI

	desiredParameters[consts.SriovEnabledParam] = consts.NvParamFalse
	desiredParameters[consts.SriovNumOfVfsParam] = "0"
	if template.NumVfs > 0 {
		desiredParameters[consts.SriovEnabledParam] = consts.NvParamTrue
		desiredParameters[consts.SriovNumOfVfsParam] = strconv.Itoa(template.NumVfs)
	}

	if template.LinkType != "" {
		// Link type change is not allowed on some devices
		if _, found := defaultValues[consts.LinkTypeP1Param]; found {
			linkType := nvParamLinkTypeFromName(string(template.LinkType))
			desiredParameters[consts.LinkTypeP1Param] = linkType
			if secondPortPresent {
				desiredParameters[consts.LinkTypeP2Param] = linkType
			}
		}
		if device.Status.Ports[0].NetworkInterface != "" &&
			v.utils.GetLinkType(device.Status.Ports[0].NetworkInterface) !=
				string(device.Spec.Configuration.Template.LinkType) {
			err := types.IncorrectSpecError(
				fmt.Sprintf(
					"device doesn't support link type change, wrong link type provided in the template, should be: %s",
					v.utils.GetLinkType(pciAddr)))
			log.Log.Error(err, "incorrect spec", "device", device.Name)
			return desiredParameters, err
		}
	}

	if template.PciPerformanceOptimized != nil && template.PciPerformanceOptimized.Enabled {
		if template.PciPerformanceOptimized.MaxAccOutRead != 0 {
			desiredParameters[consts.MaxAccOutReadParam] = strconv.Itoa(template.PciPerformanceOptimized.MaxAccOutRead)
		} else {
			// If not specified, use the best parameters for specific PCI gen
			pciLinkSpeed, err := v.utils.GetPCILinkSpeed(pciAddr)
			if err != nil {
				log.Log.Error(err, "failed to get PCI link speed", "pciAddr", pciAddr)
				return desiredParameters, err
			}
			if pciLinkSpeed == 16 { // Gen4
				desiredParameters[consts.MaxAccOutReadParam] = "44"
			} else if pciLinkSpeed > 16 { // Gen5
				desiredParameters[consts.MaxAccOutReadParam] = "0"
			}
		}

		// maxReadRequest is applied as runtime configuration
	} else {
		applyDefaultNvConfigValueIfExists(consts.MaxAccOutReadParam, desiredParameters, defaultValues)
	}

	if template.RoceOptimized != nil && template.RoceOptimized.Enabled {
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
		applyDefaultNvConfigValueIfExists(consts.RoceCcPrioMaskP1Param, desiredParameters, defaultValues)
		applyDefaultNvConfigValueIfExists(consts.CnpDscpP1Param, desiredParameters, defaultValues)
		applyDefaultNvConfigValueIfExists(consts.Cnp802pPrioP1Param, desiredParameters, defaultValues)
		if secondPortPresent {
			applyDefaultNvConfigValueIfExists(consts.RoceCcPrioMaskP2Param, desiredParameters, defaultValues)
			applyDefaultNvConfigValueIfExists(consts.CnpDscpP2Param, desiredParameters, defaultValues)
			applyDefaultNvConfigValueIfExists(consts.Cnp802pPrioP2Param, desiredParameters, defaultValues)
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
		applyDefaultNvConfigValueIfExists(consts.AtsEnabledParam, desiredParameters, defaultValues)
	}

	for _, rawParam := range template.RawNvConfig {
		// Ignore second port params if device has a single port
		if strings.HasSuffix(rawParam.Name, consts.SecondPortPrefix) && !secondPortPresent {
			continue
		}

		desiredParameters[rawParam.Name] = rawParam.Value
	}

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
	if value, found := nvConfig.CurrentConfig[consts.AdvancedPCISettingsParam]; found && value == consts.NvParamTrue {
		advancedPciSettingsEnabledInCurrentConfig = true
	}
	if advancedPciSettingsEnabledInCurrentConfig {
		delete(nvConfig.CurrentConfig, consts.AdvancedPCISettingsParam)
		if reflect.DeepEqual(nvConfig.CurrentConfig, nvConfig.DefaultConfig) {
			alreadyResetInCurrent = true
		}
	}

	advancedPciSettingsEnabledInNextBootConfig := false
	if value, found := nvConfig.NextBootConfig[consts.AdvancedPCISettingsParam]; found && value == consts.NvParamTrue {
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
func (v *configValidationImpl) AdvancedPCISettingsEnabled(currentConfig map[string]string) bool {
	if value, found := currentConfig[consts.AdvancedPCISettingsParam]; found && value == consts.NvParamTrue {
		return true
	}
	return false
}

// RuntimeConfigApplied checks if desired runtime config is applied
func (v *configValidationImpl) RuntimeConfigApplied(device *v1alpha1.NicDevice) (bool, error) {
	ports := device.Status.Ports

	// TODO uncomment after a fix to mlnx_qos command
	//desiredMaxReadReqSize, desiredTrust, desiredPfc := v.CalculateDesiredRuntimeConfig(device)
	desiredMaxReadReqSize, _, _ := v.CalculateDesiredRuntimeConfig(device)

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

	// TODO uncomment after a fix to mlnx_qos command
	//for _, port := range ports {
	//	actualTrust, actualPfc, err := v.utils.GetTrustAndPFC(port.NetworkInterface)
	//	if err != nil {
	//		log.Log.Error(err, "can't validate QoS settings", "device", device.Name)
	//		return false, err
	//	}
	//	if actualTrust != desiredTrust || actualPfc != desiredPfc {
	//		return false, nil
	//	}
	//}

	return true, nil
}

// CalculateDesiredRuntimeConfig returns desired values for runtime config
// returns int - maxReadRequestSize
// returns string - qos trust mode
// returns string - qos pfc settings
func (v *configValidationImpl) CalculateDesiredRuntimeConfig(device *v1alpha1.NicDevice) (int, string, string) {
	maxReadRequestSize := 0
	trust := "pcp"
	pfc := "0,0,0,0,0,0,0,0"

	template := device.Spec.Configuration.Template

	if template.PciPerformanceOptimized != nil && template.PciPerformanceOptimized.Enabled {
		if template.PciPerformanceOptimized.MaxReadRequest != 0 {
			maxReadRequestSize = template.PciPerformanceOptimized.MaxReadRequest
		} else {
			maxReadRequestSize = 4096
		}
	}

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

func newConfigValidation(utils HostUtils) configValidation {
	return &configValidationImpl{utils: utils}
}
