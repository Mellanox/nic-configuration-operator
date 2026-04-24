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
	"slices"
	"strconv"

	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

type configValidation interface {
	// ConstructNvParamMapFromTemplate translates a configuration template into a set of
	// nvconfig parameters. Returns only what the spec explicitly asks to set — no
	// "resurrected" defaults. Callers apply the returned map with --with_default so
	// unmanaged params are normalized to factory default.
	// Operates under the assumption that spec validation was already carried out.
	ConstructNvParamMapFromTemplate(device *v1alpha1.NicDevice) (map[string]string, error)
	// ResolveFactoryDefaults replaces sentinel (consts.NvParamFactoryDefault) entries
	// in `desired` with the firmware's factory default from `portConfig.DefaultConfig`,
	// returning a new map. Entries whose names aren't in DefaultConfig are dropped.
	// Callers MUST invoke this after queryNvConfigs and before the compare/apply loop.
	ResolveFactoryDefaults(desired map[string]string, portConfig types.NvConfigQuery) map[string]string
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
	// returns *v1alpha1.QosSpec - qos settings
	CalculateDesiredRuntimeConfig(device *v1alpha1.NicDevice) (int, *v1alpha1.QosSpec)
}

type configValidationImpl struct {
	utils         ConfigurationUtils
	eventRecorder record.EventRecorder
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

// ConstructNvParamMapFromTemplate translates a configuration template into the full
// set of NV-config parameters this operator manages for the device. The returned map
// is what we ask mlxconfig to write after sentinel resolution. Every managed param
// appears either as a concrete value OR as the sentinel `consts.NvParamFactoryDefault`
// meaning "this parameter should be at the firmware's factory default" — callers MUST
// run the result through `ResolveFactoryDefaults` against a scoped mlxconfig query
// before the compare/apply loop.
//
// This function is intentionally query-independent: it decides what's managed based on
// spec + port count alone. Sentinels are how we keep former-feature params (e.g. RoCE
// params after RoceOptimized goes false) in scope so the scoped query picks them up
// and drift is detected.
//
// For BF devices we always emit INTERNAL_CPU_OFFLOAD_ENGINE=NIC mode — applying a
// NicConfigurationTemplate to a BF implies NIC mode by design; DPU mode is out of
// scope for this operator.
//
// Operates under the assumption that spec-level sanity validation was already done.
func (v *configValidationImpl) ConstructNvParamMapFromTemplate(device *v1alpha1.NicDevice) (map[string]string, error) {
	desiredParameters := map[string]string{}

	template := device.Spec.Configuration.Template
	portCount := len(device.Status.Ports)

	desiredParameters[consts.SriovEnabledParam] = consts.NvParamFalse
	desiredParameters[consts.SriovNumOfVfsParam] = "0"
	if template.NumVfs > 0 {
		desiredParameters[consts.SriovEnabledParam] = consts.NvParamTrue
		desiredParameters[consts.SriovNumOfVfsParam] = strconv.Itoa(template.NumVfs)
	}

	// Emit LINK_TYPE_P<n> for every discovered port. mlxconfig rejects the set on
	// devices that don't support link type change, surfacing a clear error upstream.
	linkType := nvParamLinkTypeFromName(string(template.LinkType))
	for portIdx := 1; portIdx <= portCount; portIdx++ {
		desiredParameters[consts.PortParam(consts.LinkTypeParamBase, portIdx)] = linkType
	}

	// BF device implies NIC mode. Applies to BF2/BF3/BF4 — see utils.IsBlueFieldDevice.
	if utils.IsBlueFieldDevice(device.Status.Type) {
		desiredParameters[consts.BF3OperationModeParam] = consts.NvParamBF3NicMode
	}

	// PciPerformanceOptimized: only emit MAX_ACC_OUT_READ when the user asked for a
	// specific non-zero value (explicit). Otherwise (disabled or auto/0) treat it as
	// "should be at factory default" via the sentinel. ADVANCED_PCI_SETTINGS is only
	// forced on when we need the hidden param visible for a concrete value.
	if template.PciPerformanceOptimized != nil &&
		template.PciPerformanceOptimized.Enabled &&
		template.PciPerformanceOptimized.MaxAccOutRead != 0 {
		desiredParameters[consts.AdvancedPCISettingsParam] = consts.NvParamTrue
		desiredParameters[consts.MaxAccOutReadParam] = strconv.Itoa(template.PciPerformanceOptimized.MaxAccOutRead)
	} else {
		desiredParameters[consts.MaxAccOutReadParam] = consts.NvParamFactoryDefault
	}

	if template.RoceOptimized != nil && template.RoceOptimized.Enabled {
		if template.LinkType == consts.Infiniband {
			err := types.IncorrectSpecError(
				"RoceOptimized settings can only be used with link type Ethernet")
			log.Log.Error(err, "incorrect spec", "device", device.Name)
			return desiredParameters, err
		}

		// Apply RoCE optimization to every discovered port.
		for portIdx := 1; portIdx <= portCount; portIdx++ {
			desiredParameters[consts.PortParam(consts.RoceCcPrioMaskParamBase, portIdx)] = "255"
			desiredParameters[consts.PortParam(consts.CnpDscpParamBase, portIdx)] = "4"
			desiredParameters[consts.PortParam(consts.Cnp802pPrioParamBase, portIdx)] = "6"
		}

		// qos settings are applied as runtime configuration.
	} else {
		// RoCE disabled → emit the sentinel for every port so the resolver replaces
		// it with the FW default. This ensures cleanup when the user previously had
		// RoCE enabled and then disabled it.
		for portIdx := 1; portIdx <= portCount; portIdx++ {
			desiredParameters[consts.PortParam(consts.RoceCcPrioMaskParamBase, portIdx)] = consts.NvParamFactoryDefault
			desiredParameters[consts.PortParam(consts.CnpDscpParamBase, portIdx)] = consts.NvParamFactoryDefault
			desiredParameters[consts.PortParam(consts.Cnp802pPrioParamBase, portIdx)] = consts.NvParamFactoryDefault
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
		// GpuDirect disabled → ATS_ENABLED should be at FW default.
		desiredParameters[consts.AtsEnabledParam] = consts.NvParamFactoryDefault
	}

	for _, rawParam := range template.RawNvConfig {
		// Drop _Pn params whose port is beyond the device's port count.
		if n, ok := consts.PortSuffixNum(rawParam.Name); ok && n > portCount {
			continue
		}
		desiredParameters[rawParam.Name] = rawParam.Value
	}

	return desiredParameters, nil
}

// ResolveFactoryDefaults materializes sentinel-valued entries in `desired` against
// the firmware's DefaultConfig from a scoped mlxconfig query, returning a new map.
// Entries whose value is `consts.NvParamFactoryDefault` are replaced with the
// numeric default value. Entries whose names don't appear in `portConfig.DefaultConfig`
// are dropped (firmware doesn't expose the param — matches the existing "unsupported
// param" tolerance in manager.go). Concrete values pass through unchanged.
//
// Callers MUST call this method after `queryNvConfigs` and before the compare/apply
// loop — sentinels are not a valid mlxconfig value and will be rejected by the
// firmware if leaked to `SetNvConfigParametersBatch`.
func (v *configValidationImpl) ResolveFactoryDefaults(
	desired map[string]string, portConfig types.NvConfigQuery,
) map[string]string {
	resolved := make(map[string]string, len(desired))
	for name, value := range desired {
		if value != consts.NvParamFactoryDefault {
			resolved[name] = value
			continue
		}
		defaults, found := portConfig.DefaultConfig[name]
		if !found || len(defaults) == 0 {
			// FW doesn't expose this param. Drop from desired so the compare loop
			// doesn't count it as drift; downstream "param not supported" handling
			// picks it up if ADVANCED_PCI_SETTINGS is enabled.
			log.Log.V(2).Info("sentinel not resolvable, dropping from desired", "param", name)
			continue
		}
		// DefaultConfig stores [alias, numeric] for bracketed values or [numeric] for
		// plain scalars; the numeric slot is always the last element.
		resolved[name] = defaults[len(defaults)-1]
	}
	return resolved
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

	desiredMaxReadReqSize, desiredQoSSpec := v.CalculateDesiredRuntimeConfig(device)

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
	if desiredQoSSpec == nil || (desiredQoSSpec.Trust == "" && desiredQoSSpec.PFC == "" && desiredQoSSpec.ToS == 0) {
		return true, nil
	}

	for _, port := range ports {
		if port.NetworkInterface == "" {
			err := fmt.Errorf("cannot apply QoS settings for device port %s, network interface is missing", port.PCI)
			log.Log.Error(err, "cannot validate QoS settings", "device", device.Name, "port", port.PCI)
			return false, err
		}
		actualSpec, err := v.utils.GetQoSSettings(device, port.NetworkInterface)
		if err != nil {
			log.Log.Error(err, "cannot validate QoS settings", "device", device.Name, "port", port.PCI)
			return false, err
		}
		if desiredQoSSpec != nil && (actualSpec.Trust != desiredQoSSpec.Trust || actualSpec.PFC != desiredQoSSpec.PFC || actualSpec.ToS != desiredQoSSpec.ToS) {
			return false, nil
		}
	}

	return true, nil
}

// CalculateDesiredRuntimeConfig returns desired values for runtime config
// returns int - maxReadRequestSize
// returns string - qos trust mode
// returns string - qos pfc settings
func (v *configValidationImpl) CalculateDesiredRuntimeConfig(device *v1alpha1.NicDevice) (int, *v1alpha1.QosSpec) {
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
		return maxReadRequestSize, nil
	}

	var qos *v1alpha1.QosSpec = nil

	if template.RoceOptimized != nil && template.RoceOptimized.Enabled {
		trust := "dscp"
		pfc := "0,0,0,1,0,0,0,0"
		tos := 0

		if template.RoceOptimized.Qos != nil {
			trust = template.RoceOptimized.Qos.Trust
			pfc = template.RoceOptimized.Qos.PFC
			tos = template.RoceOptimized.Qos.ToS
		}
		qos = &v1alpha1.QosSpec{
			Trust: trust,
			PFC:   pfc,
			ToS:   tos,
		}
	}

	return maxReadRequestSize, qos
}

func newConfigValidation(utils ConfigurationUtils, eventRecorder record.EventRecorder) configValidation {
	return &configValidationImpl{utils: utils, eventRecorder: eventRecorder}
}
