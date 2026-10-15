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
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

// splitBreakoutLanes bypasses DMS's shell-quoted MODULE_LANES template compiler
// (DMS bug 5345051).
// Keep other typed leaves intact and never mutate the cached plan.
func splitBreakoutLanes(operations []dmscli.XPathOperation) ([]dmscli.XPathOperation, map[string]string, error) {
	direct := make([]dmscli.XPathOperation, 0, len(operations))
	assignments := map[string]string{}
	for _, operation := range operations {
		lanes, found := operation.Values["lanes"]
		if !found || !isBreakoutLanes(operation.Path, "lanes") {
			direct = append(direct, operation)
			continue
		}
		prefix, owner, err := breakoutLaneMapping(operation.Path)
		if err != nil {
			return nil, nil, err
		}
		indices, err := laneIndices(lanes)
		if err != nil {
			return nil, nil, fmt.Errorf("breakout lanes for %q: %w", operation.Path, err)
		}
		for _, lane := range indices {
			if lane > 255 {
				return nil, nil, fmt.Errorf("breakout lane index %d exceeds uint8 range", lane)
			}
			param := fmt.Sprintf("%s%d]", prefix, lane)
			if previous, exists := assignments[param]; exists && previous != owner {
				return nil, nil, fmt.Errorf("conflicting breakout assignments for %s: %s and %s", param, previous, owner)
			}
			assignments[param] = owner
		}
		values := maps.Clone(operation.Values)
		delete(values, "lanes")
		if len(values) > 0 {
			direct = append(direct, dmscli.XPathOperation{Path: operation.Path, Values: values})
		}
	}
	return direct, assignments, nil
}

func breakoutLaneMapping(path string) (prefix, owner string, err error) {
	prefix, err = laneParamPrefix(path)
	if err != nil {
		return "", "", err
	}
	match := breakoutLanePath.FindStringSubmatch(path)
	if _, err := strconv.ParseUint(match[1], 10, 8); err != nil {
		return "", "", fmt.Errorf("invalid breakout module in %q: %w", path, err)
	}
	port, err := strconv.ParseUint(match[2], 10, 8)
	if err != nil {
		return "", "", fmt.Errorf("invalid breakout port in %q: %w", path, err)
	}
	if port == 255 {
		return prefix, "0xff", nil
	}
	return prefix, strconv.FormatUint(port, 10), nil
}

// nativeBreakoutState recreates lane membership from the native array, including
// unexpected members. Reuse only explicitly unfiltered caller state; otherwise query
// each entire module array so checking only desired lanes cannot hide drift.
func (h *nvConfigUtils) nativeBreakoutState(ctx context.Context, port v1alpha1.NicDevicePortSpec,
	operations []dmscli.XPathOperation, config types.NvConfigQuery,
	result *dmscli.QueryXPathsResult,
) (types.NvConfigQuery, error) {
	modules := map[string]struct{}{}
	for _, operation := range operations {
		if _, hasLanes := operation.Values["lanes"]; !hasLanes || !isBreakoutLanes(operation.Path, "lanes") {
			continue
		}
		prefix, _, err := breakoutLaneMapping(operation.Path)
		if err != nil {
			return config, err
		}
		modules[strings.TrimSuffix(prefix, "[")] = struct{}{}
	}
	if len(modules) == 0 {
		return config, nil
	}
	if !config.AllParameters {
		parameters := slices.Sorted(maps.Keys(modules))
		queried, err := h.QueryNvConfig(ctx, port, parameters)
		if err != nil {
			return config, fmt.Errorf("query breakout lane arrays on PCI %q: %w", port.PCI, err)
		}
		// Replace the module slice rather than overlaying it: partial snapshots
		// can contain stale extra members. Preserve unrelated native overrides and
		// keep the caller's maps immutable.
		config = types.NvConfigQuery{
			DefaultConfig:  replaceModuleValues(config.DefaultConfig, queried.DefaultConfig, modules),
			CurrentConfig:  replaceModuleValues(config.CurrentConfig, queried.CurrentConfig, modules),
			NextBootConfig: replaceModuleValues(config.NextBootConfig, queried.NextBootConfig, modules),
			AllParameters:  false,
		}
	}
	for _, operation := range operations {
		desired, hasLanes := operation.Values["lanes"]
		if !hasLanes || !isBreakoutLanes(operation.Path, "lanes") {
			continue
		}
		prefix, owner, err := breakoutLaneMapping(operation.Path)
		if err != nil {
			return config, err
		}
		indices, err := laneIndices(desired)
		if err != nil {
			return config, err
		}
		for _, lane := range indices {
			param := fmt.Sprintf("%s%d]", prefix, lane)
			if len(config.CurrentConfig[param]) == 0 || len(config.NextBootConfig[param]) == 0 {
				return config, fmt.Errorf("native breakout state is missing current or next-boot value for %q on PCI %q", param, port.PCI)
			}
		}
		current, err := nativeLaneMembers(prefix, owner, config.CurrentConfig)
		if err != nil {
			return config, err
		}
		pending, err := nativeLaneMembers(prefix, owner, config.NextBootConfig)
		if err != nil {
			return config, err
		}
		if result.Values[operation.Path] == nil {
			result.Values[operation.Path] = map[string]any{}
		}
		result.Values[operation.Path]["lanes"] = current
		result.Values[operation.Path]["lanes-pending"] = pending
	}
	return config, nil
}

func nativeLaneMembers(prefix, owner string, state map[string][]string) ([]int, error) {
	members := []int{}
	entries := 0
	for param, values := range state {
		if !strings.HasPrefix(param, prefix) {
			continue
		}
		entries++
		if len(values) == 0 {
			return nil, fmt.Errorf("native breakout state is missing value for %q", param)
		}
		index := strings.TrimSuffix(strings.TrimPrefix(param, prefix), "]")
		lane, err := strconv.ParseUint(index, 10, 8)
		if err != nil || !strings.HasSuffix(param, "]") {
			return nil, fmt.Errorf("invalid native lane parameter %q", param)
		}
		if ValueMatches(values, owner) {
			members = append(members, int(lane))
		}
	}
	if entries == 0 {
		return nil, fmt.Errorf("native breakout state does not contain array %q", strings.TrimSuffix(prefix, "["))
	}
	slices.Sort(members)
	return members, nil
}

func replaceModuleValues(existing, queried map[string][]string, modules map[string]struct{}) map[string][]string {
	result := make(map[string][]string, len(existing)+len(queried))
	for param, values := range existing {
		module, _, _ := strings.Cut(param, "[")
		if _, replaced := modules[module]; !replaced {
			result[param] = values
		}
	}
	maps.Copy(result, queried)
	return result
}
