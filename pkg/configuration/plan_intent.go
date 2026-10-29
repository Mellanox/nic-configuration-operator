// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"fmt"
	"math"
	"sort"
	"strings"

	dmsconfiguration "github.com/Mellanox/dms-lib/pkg/configuration"
	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"k8s.io/utils/ptr"
)

// TranslateNVOverrides expresses NCO template policy as raw overrides. An
// omitted optimization does not reset authored plan values. Raw values win and
// retain indexed/future-PF names; the library checks their applicability.
func TranslateNVOverrides(template *v1alpha1.ConfigurationTemplateSpec, observed device.Device, defaults, linkTypes map[string]string, restoreOmitted bool) ([]dmsconfiguration.NVConfigParam, error) {
	if template == nil {
		return nil, nil
	}
	if len(observed.Ports) == 0 {
		return nil, fmt.Errorf("device %s has no PFs", observed.ID)
	}
	query := types.NvConfigQuery{DefaultConfig: map[string][]string{}}
	for name, value := range defaults {
		query.DefaultConfig[name] = []string{value}
	}
	ports := make([]v1alpha1.NicDevicePortSpec, 0, len(observed.Ports))
	_, configurableLink := defaults[consts.LinkTypeP1Param]
	for _, port := range observed.Ports {
		ports = append(ports, v1alpha1.NicDevicePortSpec{PCI: port.PCI, NetworkInterface: port.Netdev})
		if template.LinkType != "" && !configurableLink {
			if port.Netdev == "" {
				// Ordinary template validation historically skips unbound PFs.
				// Plan-backed intent still requires explicit link evidence.
				if restoreOmitted {
					continue
				}
				return nil, fmt.Errorf("device %s PF %s has no netdev for fixed link-type validation", observed.ID, port.PCI)
			}
			actual := linkTypes[port.Netdev]
			if actual == "" {
				return nil, fmt.Errorf("cannot determine link type of %s", port.Netdev)
			}
		}
	}
	params, err := templateNVParameters(observed.ID, template, ports, query, func(name string) string { return linkTypes[name] }, restoreOmitted)
	if err != nil {
		return nil, err
	}
	raw := map[string]string{}
	for _, parameter := range template.RawNvConfig {
		if restoreOmitted {
			if n, ok := consts.PortSuffixNum(parameter.Name); ok && n > len(observed.Ports) {
				continue
			}
		}
		if previous, exists := raw[parameter.Name]; exists && previous != parameter.Value {
			return nil, fmt.Errorf("conflicting raw override %s", parameter.Name)
		}
		raw[parameter.Name] = parameter.Value
		params[parameter.Name] = parameter.Value
	}
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]dmsconfiguration.NVConfigParam, 0, len(names))
	for _, name := range names {
		result = append(result, dmsconfiguration.NVConfigParam{Name: name, Value: params[name]})
	}
	return result, nil
}

// TranslateRuntime preserves the existing NCO runtime policy as explicit
// per-PCI library intent. Requested settings are not skipped for missing names.
func TranslateRuntime(template *v1alpha1.ConfigurationTemplateSpec, observed device.Device) ([]dmsconfiguration.PortRuntimeConfig, error) {
	if template == nil {
		return nil, nil
	}
	desired := templateRuntimeConfig(template)
	base := dmsconfiguration.PortRuntimeConfig{}
	if desired.MaxReadRequestSize != 0 {
		if desired.MaxReadRequestSize < 0 || uint64(desired.MaxReadRequestSize) > math.MaxUint32 {
			return nil, fmt.Errorf("negative max read request")
		}
		base.MaxReadRequestBytes = ptr.To(uint32(desired.MaxReadRequestSize))
	}
	if desired.RoceMode != 0 {
		if desired.RoceMode < 1 || desired.RoceMode > 2 {
			return nil, fmt.Errorf("invalid RoCE mode %d", desired.RoceMode)
		}
		base.RoCEMode = ptr.To(uint8(desired.RoceMode))
	}
	if err := translateRuntimeQoS(&base, desired.Qos); err != nil {
		return nil, err
	}
	if err := translateRuntimePerformance(&base, desired.RuntimePerf); err != nil {
		return nil, err
	}
	result := make([]dmsconfiguration.PortRuntimeConfig, 0, len(observed.Ports))
	for _, port := range observed.Ports {
		value := base
		value.PCI = port.PCI
		result = append(result, value)
	}
	return result, nil
}

func translateRuntimeQoS(base *dmsconfiguration.PortRuntimeConfig, qos *v1alpha1.QosSpec) error {
	if qos != nil {
		if qos.Trust != "" {
			base.Trust = ptr.To(qos.Trust)
		}
		if qos.PFC != "" {
			bits := strings.Split(qos.PFC, ",")
			if len(bits) != 8 {
				return fmt.Errorf("PFC must contain eight priorities")
			}
			var pfc [8]bool
			for i, value := range bits {
				if value != "0" && value != "1" {
					return fmt.Errorf("invalid PFC value %q", value)
				}
				pfc[i] = value == "1"
			}
			base.PFC = &pfc
		}
		if qos.ToS != 0 {
			if qos.ToS < 0 || qos.ToS > 255 {
				return fmt.Errorf("ToS must be in 0..255")
			}
			base.ToS = ptr.To(uint8(qos.ToS))
			base.TrafficClass = ptr.To(int32(qos.ToS))
		}
		if qos.CableLen != 0 {
			if qos.CableLen < 0 || uint64(qos.CableLen) > math.MaxUint32 {
				return fmt.Errorf("cable length outside uint32 range")
			}
			base.CableLengthMeters = ptr.To(uint32(qos.CableLen))
		}
		if qos.ECN != nil {
			if qos.ECN.Priority < 0 || qos.ECN.Priority > 7 {
				return fmt.Errorf("ECN priority must be in 0..7")
			}
			base.ECN = []dmsconfiguration.ECNConfig{{Priority: uint8(qos.ECN.Priority), RP: ptr.To(qos.ECN.Enabled), NP: ptr.To(qos.ECN.Enabled)}}
		}
		if qos.PauseFrames != nil {
			enabled := qos.PauseFrames.Enabled
			base.Pause = &dmsconfiguration.PauseConfig{RX: ptr.To(enabled), TX: ptr.To(enabled), Autoneg: ptr.To(enabled)}
		}
	}
	return nil
}

func translateRuntimePerformance(base *dmsconfiguration.PortRuntimeConfig, perf *v1alpha1.RuntimePerformanceOptimizedSpec) error {
	if perf != nil {
		if perf.RxRingSize < 0 || uint64(perf.RxRingSize) > math.MaxUint32 || perf.TxRingSize < 0 || uint64(perf.TxRingSize) > math.MaxUint32 || perf.CombinedChannels < 0 || uint64(perf.CombinedChannels) > math.MaxUint32 {
			return fmt.Errorf("runtime performance value outside uint32 range")
		}
		if perf.RxRingSize != 0 {
			base.RXRingSize = ptr.To(uint32(perf.RxRingSize))
		}
		if perf.TxRingSize != 0 {
			base.TXRingSize = ptr.To(uint32(perf.TxRingSize))
		}
		if perf.CombinedChannels != 0 {
			base.CombinedChannels = ptr.To(uint32(perf.CombinedChannels))
		}
		if perf.LRO != nil {
			base.LRO = ptr.To(*perf.LRO)
		}
	}
	return nil
}
