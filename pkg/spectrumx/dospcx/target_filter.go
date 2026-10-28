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

package dospcx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
)

// Compiled plans retain selection constraints until GetPreparedPlan knows the
// requesting NIC. They are immutable after publication in the manager cache.
type compiledPlan struct {
	Breakout       []compiledOperation
	PostBreakout   []compiledOperation
	RuntimeConfig  []compiledOperationGroup
	ConfigureError error
}

type compiledOperation struct {
	dmscli.XPathOperation
	filter targetFilter
}

type compiledOperationGroup struct {
	Name       string
	Scope      string
	Operations []compiledOperation
}

type targetFilter struct {
	hcaTypes []string
	// nil is unrestricted; a non-nil empty slice matches no NIC.
	nicIndexes []int
}

func parseTargetFilter(raw json.RawMessage) (targetFilter, error) {
	var result targetFilter
	if len(raw) == 0 {
		return result, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return result, fmt.Errorf("target_filter must be an object")
	}
	for key, value := range fields {
		switch key {
		case "hca_type":
			if err := decodeFilterList(value, &result.hcaTypes); err != nil {
				return result, fmt.Errorf("target_filter.hca_type: %w", err)
			}
			for _, hca := range result.hcaTypes {
				if strings.TrimSpace(hca) == "" {
					return result, fmt.Errorf("target_filter.hca_type must contain nonempty names")
				}
			}
		case "nic_index_in_rail":
			if err := decodeFilterList(value, &result.nicIndexes); err != nil {
				return result, fmt.Errorf("target_filter.nic_index_in_rail: %w", err)
			}
			for _, index := range result.nicIndexes {
				if index < 0 {
					return result, fmt.Errorf("target_filter.nic_index_in_rail must contain nonnegative integers")
				}
			}
		default:
			// Plane filters need per-port projection; a NIC-wide plan cannot
			// safely represent them. Unknown selectors must never widen scope.
			return result, fmt.Errorf("unsupported target_filter field %q", key)
		}
	}
	return result, nil
}

func decodeFilterList[T any](raw json.RawMessage, result *[]T) error {
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil || elements == nil {
		return fmt.Errorf("must be an array, not null")
	}
	*result = make([]T, len(elements))
	for i, element := range elements {
		if bytes.Equal(bytes.TrimSpace(element), []byte("null")) {
			return fmt.Errorf("filter array must not contain null")
		}
		if err := json.Unmarshal(element, &(*result)[i]); err != nil {
			return fmt.Errorf("invalid filter array element: %w", err)
		}
	}
	return nil
}

func (filter targetFilter) matches(target preBreakoutTarget, platform string) (bool, error) {
	if len(filter.hcaTypes) > 0 {
		hca, err := targetHCAType(target.DeviceID)
		if err != nil {
			return false, err
		}
		if !slices.Contains(filter.hcaTypes, hca) {
			return false, nil
		}
	}
	if filter.nicIndexes == nil {
		return true, nil
	}
	if len(filter.nicIndexes) == 0 {
		return false, nil
	}
	index := 0 // The planner defaults single-NIC rails to index zero.
	if target.NicIndexInRail != nil {
		index = *target.NicIndexInRail
	} else if platform == "vr" {
		return false, fmt.Errorf("target %q is missing nic_index_in_rail", target.BDF)
	}
	if index < 0 {
		return false, fmt.Errorf("target %q has negative nic_index_in_rail", target.BDF)
	}
	return slices.Contains(filter.nicIndexes, index), nil
}

// Match the doSPCX HCA names for the device IDs accepted by blueprintDeviceID.
func targetHCAType(deviceID string) (string, error) {
	switch deviceID {
	case "0x1021":
		return "ConnectX-7", nil
	case "0x1023":
		return "ConnectX-8", nil
	case "0x1025":
		return "ConnectX-9", nil
	case "0xa2dc":
		return "BlueField-3", nil
	default:
		return "", fmt.Errorf("unsupported target device ID %q for HCA filtering", deviceID)
	}
}

func projectPlan(source *compiledPlan, target preBreakoutTarget, platform string) (*Plan, error) {
	result := &Plan{ConfigureError: source.ConfigureError}
	var err error
	result.Breakout, err = projectOperations(source.Breakout, target, platform, false)
	if err != nil {
		return nil, err
	}
	result.PostBreakout, err = projectOperations(source.PostBreakout, target, platform, false)
	if err != nil {
		return nil, err
	}
	for _, group := range source.RuntimeConfig {
		operations, err := projectOperations(group.Operations, target, platform, true)
		if err != nil {
			return nil, err
		}
		if len(operations) > 0 {
			result.RuntimeConfig = append(result.RuntimeConfig, OperationGroup{
				Name: group.Name, Scope: group.Scope, Operations: operations,
			})
		}
	}
	return result, nil
}

func projectOperations(source []compiledOperation, target preBreakoutTarget, platform string, runtime bool) ([]dmscli.XPathOperation, error) {
	result := make([]dmscli.XPathOperation, 0, len(source))
	for _, operation := range source {
		matches, err := operation.filter.matches(target, platform)
		if err != nil {
			return nil, fmt.Errorf("select doSPCX operation %q: %w", operation.Path, err)
		}
		if !matches {
			continue
		}
		projected := dmscli.XPathOperation{Path: operation.Path, Values: cloneValueMap(operation.Values)}
		if runtime {
			projected.Scope = operation.Scope
			projected.TargetClass = operation.TargetClass
		}
		result = append(result, projected)
	}
	return result, nil
}
