/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES
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

package nvconfig

import (
	"context"
	"fmt"
	"slices"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

const (
	roceDefault  = "default"
	roceDisabled = "disabled"
)

// Keep these encodings aligned with DMS tools/configuration/_value_maps.sh.
// Legacy DMS GET reads every sibling in /nvidia/roce, including parameters
// unsupported by the device even when the plan did not request them.
var roceNVConfig = map[string]struct {
	parameter string
	values    map[string]string
}{
	"adaptive-routing": {"ROCE_ADAPTIVE_ROUTING_EN", map[string]string{"true": "1", "false": "0"}},
	"cc-steering-ext":  {"ROCE_CC_STEERING_EXT", map[string]string{roceDefault: "0", roceDisabled: "1", "enabled": "2"}},
	"tx-sched-locality-mode": {"TX_SCHEDULER_LOCALITY_MODE", map[string]string{
		roceDefault: "0", "static": "1", "accumulative": "2", roceDisabled: "3",
	}},
	"multipath-dscp": {"MULTIPATH_DSCP", map[string]string{
		roceDefault: "0", roceDisabled: "1", "dscp-0": "2", "dscp-1": "3", "dscp-2": "4",
	}},
}

func roceParameter(path, leaf string) string {
	if path != "/nvidia/roce" {
		return ""
	}
	return roceNVConfig[leaf].parameter
}

// splitRoceValidation only partitions reads; apply retains the original typed operations.
func splitRoceValidation(operations []dmscli.XPathOperation) (direct, native []dmscli.XPathOperation) {
	for _, operation := range operations {
		directValues, nativeValues := map[string]any{}, map[string]any{}
		for leaf, value := range operation.Values {
			if roceParameter(operation.Path, leaf) != "" {
				nativeValues[leaf] = value
			} else {
				directValues[leaf] = value
			}
		}
		if len(directValues) > 0 {
			direct = append(direct, dmscli.XPathOperation{Path: operation.Path, Values: directValues})
		}
		if len(nativeValues) > 0 {
			native = append(native, dmscli.XPathOperation{Path: operation.Path, Values: nativeValues})
		}
	}
	return direct, native
}

func (h *nvConfigUtils) validateNativeRoce(ctx context.Context, port v1alpha1.NicDevicePortSpec,
	operations []dmscli.XPathOperation, overrides map[string]string, config types.NvConfigQuery,
) (updateNeeded, rebootNeeded bool, err error) {
	if len(operations) == 0 {
		return false, false, nil
	}
	parameters := []string{}
	for _, operation := range operations {
		for leaf := range operation.Values {
			parameters = append(parameters, roceParameter(operation.Path, leaf))
		}
	}
	slices.Sort(parameters)
	parameters = slices.Compact(parameters)
	if !config.AllParameters {
		config, err = h.QueryNvConfig(ctx, port, parameters)
		if err != nil {
			return false, false, fmt.Errorf("query RoCE NVConfig on PCI %q: %w", port.PCI, err)
		}
	}
	for _, operation := range operations {
		for leaf, desired := range operation.Values {
			mapping := roceNVConfig[leaf]
			current, pending := config.CurrentConfig[mapping.parameter], config.NextBootConfig[mapping.parameter]
			if len(current) == 0 || len(pending) == 0 {
				return false, false, fmt.Errorf("RoCE NVConfig %q on PCI %q is missing current or next-boot state for XPath %q/%s",
					mapping.parameter, port.PCI, operation.Path, leaf)
			}
			expected, overridden := overrides[mapping.parameter]
			if !overridden {
				var supported bool
				expected, supported = mapping.values[fmt.Sprint(desired)]
				if !supported {
					return false, false, fmt.Errorf("unsupported value %v for RoCE XPath %q/%s", desired, operation.Path, leaf)
				}
			}
			currentMatches, pendingMatches := ValueMatches(current, expected), ValueMatches(pending, expected)
			updateNeeded = updateNeeded || !pendingMatches
			rebootNeeded = rebootNeeded || !currentMatches || !pendingMatches
		}
	}
	return updateNeeded, rebootNeeded, nil
}
