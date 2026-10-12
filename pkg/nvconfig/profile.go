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
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	execUtils "k8s.io/utils/exec"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

var breakoutLanePath = regexp.MustCompile(`^/nvidia/link/breakout/module/\[([0-9]+)\]/port/\[[0-9]+\]$`)

// FilterProfileParams removes defaults owned by typed operations before explicit
// template/raw overrides are merged. The caller's profile and operations are not mutated.
// A shared native map is safe only when ownership agrees across PCI functions.
func FilterProfileParams(ctx context.Context, executor execUtils.Interface, ports []v1alpha1.NicDevicePortSpec,
	operations []dmscli.XPathOperation, profile map[string]string,
) (map[string]string, error) {
	filtered := maps.Clone(profile)
	if len(profile) == 0 || len(operations) == 0 {
		return filtered, nil
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("cannot filter system profile without PCI functions")
	}
	direct := make([]dmscli.XPathOperation, 0, len(operations))
	for _, operation := range operations {
		values := maps.Clone(operation.Values)
		if lanes, found := values["lanes"]; found && isBreakoutLanes(operation.Path, "lanes") {
			prefix, err := laneParamPrefix(operation.Path)
			if err != nil {
				return nil, err
			}
			indices, err := laneIndices(lanes)
			if err != nil {
				return nil, fmt.Errorf("profile ownership for %q: %w", operation.Path, err)
			}
			for _, lane := range indices {
				delete(filtered, fmt.Sprintf("%s%d]", prefix, lane))
			}
			delete(values, "lanes")
		}
		if len(values) > 0 {
			direct = append(direct, dmscli.XPathOperation{Path: operation.Path, Values: values})
		}
	}
	if len(direct) == 0 {
		return filtered, nil
	}
	if executor == nil {
		return nil, fmt.Errorf("command executor must not be nil")
	}
	ownership := map[string]string{}
	batches := xpathQueryBatches(xpathQueries(direct))
	for _, port := range ports {
		result, err := queryNVConfigXPaths(ctx, executor, "pci/"+port.PCI+"?port=1", batches)
		if err != nil {
			return nil, err
		}
		for _, operation := range direct {
			for leaf := range operation.Values {
				param, err := overrideParameter(result.NVConfig, operation.Path, leaf)
				if err != nil {
					return nil, fmt.Errorf("system profile ownership on PCI %q: %w", port.PCI, err)
				}
				path := operation.Path + "/" + leaf
				if previous, found := ownership[path]; found && previous != param {
					return nil, fmt.Errorf("inconsistent system profile ownership for XPath %q on PCI %q: %q versus %q",
						path, port.PCI, previous, param)
				}
				ownership[path] = param
				delete(filtered, param)
			}
		}
	}
	return filtered, nil
}

func isBreakoutLanes(path, leaf string) bool {
	return leaf == "lanes" && strings.HasPrefix(path, "/nvidia/link/breakout/")
}

func laneParamPrefix(path string) (string, error) {
	match := breakoutLanePath.FindStringSubmatch(path)
	if match == nil {
		return "", fmt.Errorf("unsupported breakout lanes XPath %q", path)
	}
	module, err := strconv.Atoi(match[1])
	if err != nil {
		return "", fmt.Errorf("invalid breakout module in %q: %w", path, err)
	}
	return fmt.Sprintf("MODULE_SPLIT_M%d[", module), nil
}

// laneIndices accepts DMS CSV responses and typed plan/JSON arrays. Return a
// fresh, sorted set so masking cannot change either the plan or the response.
func laneIndices(value any) ([]int, error) {
	var items []string
	if text, ok := value.(string); ok {
		if strings.TrimSpace(text) != "" {
			items = strings.Split(text, ",")
		}
	} else {
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() || (reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array) {
			return nil, fmt.Errorf("invalid lane list %v", value)
		}
		items = make([]string, reflected.Len())
		for i := range items {
			items[i] = fmt.Sprint(reflected.Index(i).Interface())
		}
	}
	lanes := make([]int, 0, len(items))
	for _, item := range items {
		lane, err := strconv.Atoi(strings.TrimSpace(item))
		if err != nil || lane < 0 {
			return nil, fmt.Errorf("invalid lane index %q", item)
		}
		lanes = append(lanes, lane)
	}
	slices.Sort(lanes)
	return slices.Compact(lanes), nil
}

// matchLaneValues partitions validation: native assignments own supported lane
// indices, while typed GETs validate the remaining lane membership.
func matchLaneValues(path string, desired, current, pending any, native map[string]string,
	config types.NvConfigQuery,
) (bool, bool, error) {
	prefix, err := laneParamPrefix(path)
	if err != nil {
		return false, false, err
	}
	masked := map[int]bool{}
	update, reboot := false, false
	for param, value := range native {
		if !strings.HasPrefix(param, prefix) {
			continue
		}
		index, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(param, prefix), "]"))
		if err != nil || index < 0 || !strings.HasSuffix(param, "]") {
			return false, false, fmt.Errorf("invalid native lane parameter %q", param)
		}
		next, supported := config.NextBootConfig[param]
		if !supported {
			continue
		}
		masked[index] = true
		pendingMatches := ValueMatches(next, value)
		currentMatches := ValueMatches(config.CurrentConfig[param], value)
		update = update || !pendingMatches
		reboot = reboot || !currentMatches || !pendingMatches
	}
	lists := make([][]int, 0, 3)
	for _, value := range []any{desired, current, pending} {
		lanes, err := laneIndices(value)
		if err != nil {
			return false, false, fmt.Errorf("validate lanes for %q: %w", path, err)
		}
		lists = append(lists, slices.DeleteFunc(lanes, func(lane int) bool { return masked[lane] }))
	}
	pendingMatches := dmscli.XPathValuesEqual(lists[2], lists[0])
	currentMatches := dmscli.XPathValuesEqual(lists[1], lists[0])
	return update || !pendingMatches, reboot || !currentMatches || !pendingMatches, nil
}
