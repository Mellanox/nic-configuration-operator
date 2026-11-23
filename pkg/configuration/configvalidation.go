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
	"fmt"
	"reflect"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

type configValidation interface {
	// ConstructNvParamMapFromTemplate builds the full set of desired nvconfig parameters for a device by
	// combining template parameters and rawNvConfig (raw wins on key collisions).
	// The library owns composition with compiled plan intent.
	ConstructNvParamMapFromTemplate(
		device *v1alpha1.NicDevice, nvConfigQuery types.NvConfigQuery) (map[string]string, error)
	// ValidateResetToDefault checks if device's nv config has been reset to default in current and next boots
	// returns bool - need to perform reset
	// returns bool - reboot required
	// returns error - if an error occurred during validation
	ValidateResetToDefault(nvConfig types.NvConfigQuery) (bool, bool, error)
}

type configValidationImpl struct {
	utils ConfigurationUtils
}

func nvParamLinkTypeFromName(linkType string) string {
	switch linkType {
	case consts.Infiniband:
		return consts.NvParamLinkTypeInfiniband
	case consts.Ethernet:
		return consts.NvParamLinkTypeEthernet
	default:
		return ""
	}
}

func applyDefaultNvConfigValueIfExists(
	paramName string, desiredParameters map[string]string, query types.NvConfigQuery) {
	defaultValues, found := query.DefaultConfig[paramName]
	// The param's default may be absent if it is hidden on this device (e.g. ADVANCED_PCI_SETTINGS off).
	if found {
		// Take the default numeric value
		desiredParameters[paramName] = defaultValues[len(defaultValues)-1]
	}
}

// ConstructNvParamMapFromTemplate builds the desired raw nvconfig parameters from the template and
// rawNvConfig. doSPCX operations remain typed XPaths and are handled separately.
func (v *configValidationImpl) ConstructNvParamMapFromTemplate(
	device *v1alpha1.NicDevice, query types.NvConfigQuery) (map[string]string, error) {
	desired, err := templateNVParameters(device.Name, device.Spec.Configuration.Template, device.Status.Ports, query, v.utils.GetLinkType, true)
	if err != nil {
		return desired, err
	}
	return v.mergeOverrideLayers(device, desired)
}

// templateNVParameters is NCO's template policy; native reads and plan ownership
// are supplied by the caller. Omitted optimizations reset only on the legacy path.
func templateNVParameters(name string, template *v1alpha1.ConfigurationTemplateSpec, ports []v1alpha1.NicDevicePortSpec,
	query types.NvConfigQuery, linkType func(string) string, restoreOmitted bool) (map[string]string, error) {
	if template == nil {
		// No template: nothing to translate and no rawNvConfig to merge.
		return map[string]string{}, nil
	}

	desiredParameters := map[string]string{}
	portCount := len(ports)

	desiredParameters[consts.SriovEnabledParam] = consts.NvParamFalse
	desiredParameters[consts.SriovNumOfVfsParam] = "0"
	if template.NumVfs > 0 {
		desiredParameters[consts.SriovEnabledParam] = consts.NvParamTrue
		desiredParameters[consts.SriovNumOfVfsParam] = strconv.Itoa(template.NumVfs)
	}

	// Link type is only managed when the template specifies it. For Network Bay devices the
	// template must not set linkType (enforced by CEL); the link type is owned by the system profile,
	// so the operator must not emit LINK_TYPE_P* here or it would fight the system configuration.
	if template.LinkType != "" {
		// Link type change is not allowed on some devices
		_, canChangeLinkType := query.DefaultConfig[consts.LinkTypeP1Param]
		if canChangeLinkType {
			linkType := nvParamLinkTypeFromName(string(template.LinkType))
			// Emit LINK_TYPE_P<n> for every discovered port. Firmware may expose
			// fewer slots than the device has ports (e.g. BF3 DPU mode), so gate
			// each entry on a presence check in the query output.
			for portIdx := 1; portIdx <= portCount; portIdx++ {
				name := consts.PortParam(consts.LinkTypeParamBase, portIdx)
				if _, ok := query.DefaultConfig[name]; ok {
					desiredParameters[name] = linkType
				}
			}
		} else {
			desiredLinkType := string(template.LinkType)

			for _, port := range ports {
				if port.NetworkInterface != "" && linkType(port.NetworkInterface) != desiredLinkType {
					err := types.IncorrectSpecError(
						fmt.Sprintf(
							"device does not support link type change, wrong link type provided in the template, should be: %s",
							linkType(port.NetworkInterface)))
					log.Log.Error(err, "incorrect spec", "device", name)
					return desiredParameters, err
				}
			}
		}
	}

	if template.RoceOptimized != nil && template.RoceOptimized.Enabled {
		if template.LinkType == consts.Infiniband {
			err := types.IncorrectSpecError(
				"RoceOptimized settings can only be used with link type Ethernet")
			log.Log.Error(err, "incorrect spec", "device", name)
			return desiredParameters, err
		}

		// Apply RoCE optimization to every discovered port. Matches the
		// historical P1/P2 behavior, which emits these params unconditionally.
		for portIdx := 1; portIdx <= portCount; portIdx++ {
			desiredParameters[consts.PortParam(consts.RoceCcPrioMaskParamBase, portIdx)] = "255"
			desiredParameters[consts.PortParam(consts.CnpDscpParamBase, portIdx)] = "48"
			desiredParameters[consts.PortParam(consts.Cnp802pPrioParamBase, portIdx)] = "6"
		}

		// qos settings are applied as runtime configuration
	} else if restoreOmitted || template.RoceOptimized != nil {
		for portIdx := 1; portIdx <= portCount; portIdx++ {
			applyDefaultNvConfigValueIfExists(consts.PortParam(consts.RoceCcPrioMaskParamBase, portIdx), desiredParameters, query)
			applyDefaultNvConfigValueIfExists(consts.PortParam(consts.CnpDscpParamBase, portIdx), desiredParameters, query)
			applyDefaultNvConfigValueIfExists(consts.PortParam(consts.Cnp802pPrioParamBase, portIdx), desiredParameters, query)
		}
	}

	if template.GpuDirectOptimized != nil && template.GpuDirectOptimized.Enabled {
		if template.GpuDirectOptimized.Env != consts.EnvBaremetal {
			err := types.IncorrectSpecError("GpuDirectOptimized supports only Baremetal env")
			log.Log.Error(err, "incorrect spec", "device", name)
			return desiredParameters, err
		}

		desiredParameters[consts.AtsEnabledParam] = consts.NvParamFalse
		if template.PciPerformanceOptimized == nil || !template.PciPerformanceOptimized.Enabled {
			err := types.IncorrectSpecError(
				"GpuDirectOptimized should only be enabled together with PciPerformanceOptimized")
			log.Log.Error(err, "incorrect spec", "device", name)
			return desiredParameters, err
		}
	} else if restoreOmitted || template.GpuDirectOptimized != nil {
		applyDefaultNvConfigValueIfExists(consts.AtsEnabledParam, desiredParameters, query)
	}
	return desiredParameters, nil
}

// mergeOverrideLayers combines template-derived params with rawNvConfig. Raw values are merged last
// and therefore win on key collisions. doSPCX plan XPaths are intentionally excluded.
func (v *configValidationImpl) mergeOverrideLayers(device *v1alpha1.NicDevice, templateParams map[string]string) (map[string]string, error) {
	combined := map[string]string{}
	mergeParams(combined, templateParams)
	// rawNvConfig has the highest priority, so it is merged last. getRawNvConfigParams already drops
	// _Pn params whose port is beyond the device's port count.
	mergeParams(combined, getRawNvConfigParams(device))

	return combined, nil
}

// ValidateResetToDefault checks if device's nv config has been reset to default in current and next boots
// returns bool - need to perform reset
// returns bool - reboot required
// returns error - if an error occurred during validation
func (v *configValidationImpl) ValidateResetToDefault(nvConfig types.NvConfigQuery) (bool, bool, error) {
	// We want to retain the BF3 operation mode after reset, so not taking it into consideration
	delete(nvConfig.DefaultConfig, consts.BF3OperationModeParam)
	delete(nvConfig.CurrentConfig, consts.BF3OperationModeParam)
	delete(nvConfig.NextBootConfig, consts.BF3OperationModeParam)

	alreadyResetInCurrent := reflect.DeepEqual(nvConfig.CurrentConfig, nvConfig.DefaultConfig)
	willResetInNextBoot := reflect.DeepEqual(nvConfig.NextBootConfig, nvConfig.DefaultConfig)

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

func (v *configValidationImpl) validatePortCarrierState(device *v1alpha1.NicDevice) error {
	checkedPort := false
	for _, port := range device.Status.Ports {
		if port.NetworkInterface == "" {
			continue
		}

		checkedPort = true
		noCarrier, err := v.utils.HasNoCarrier(port.NetworkInterface)
		if err != nil {
			log.Log.Error(err, "can't check link state", "device", device.Name, "port", port.PCI)
			return err
		}
		if !noCarrier {
			return nil
		}
	}

	if checkedPort {
		return fmt.Errorf("all network interfaces have NO-CARRIER")
	}
	return nil
}

func templateRuntimeConfig(template *v1alpha1.ConfigurationTemplateSpec) types.DesiredRuntimeConfig {
	result := types.DesiredRuntimeConfig{}
	if template == nil {
		return result
	}

	if template.PciPerformanceOptimized != nil && template.PciPerformanceOptimized.Enabled {
		if template.PciPerformanceOptimized.MaxReadRequest != 0 {
			result.MaxReadRequestSize = template.PciPerformanceOptimized.MaxReadRequest
		} else {
			result.MaxReadRequestSize = 4096
		}
	}

	// QoS, RoCE, and runtime perf settings are not available for IB devices
	if template.LinkType == consts.Infiniband {
		return result
	}

	if template.RuntimePerformanceOptimized != nil && template.RuntimePerformanceOptimized.Enabled {
		result.RuntimePerf = template.RuntimePerformanceOptimized
	}

	if template.RoceOptimized != nil && template.RoceOptimized.Enabled {
		trust := consts.TrustModeDscp
		pfc := "0,0,0,1,0,0,0,0"
		tos := 0

		if template.RoceOptimized.Qos != nil {
			trust = template.RoceOptimized.Qos.Trust
			pfc = template.RoceOptimized.Qos.PFC
			tos = template.RoceOptimized.Qos.ToS
		}

		qos := &v1alpha1.QosSpec{
			Trust: trust,
			PFC:   pfc,
			ToS:   tos,
		}

		// Copy over extended QoS fields
		if template.RoceOptimized.Qos != nil {
			qos.CableLen = template.RoceOptimized.Qos.CableLen
			qos.ECN = template.RoceOptimized.Qos.ECN
			qos.PauseFrames = template.RoceOptimized.Qos.PauseFrames
		}

		result.Qos = qos
		result.RoceMode = template.RoceOptimized.RoceMode
	}

	return result
}

func newConfigValidation(utils ConfigurationUtils) configValidation {
	return &configValidationImpl{utils: utils}
}
