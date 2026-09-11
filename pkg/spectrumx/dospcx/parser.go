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
	"io"
	"sort"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
)

const (
	semanticPathDialect = "nvidia-t1"

	semanticGroupBreakout     = "breakout"
	semanticGroupPostBreakout = "post-breakout"
	semanticGroupLinkRuntime  = "link-runtime"
	semanticGroupCC           = "cc"
	semanticGroupLinkEvent    = "link-event"
	semanticGroupESwitch      = "eswitch"
	semanticGroupVFLifecycle  = "vf-lifecycle"

	semanticScopePerVF             = "per_vf"
	semanticScopePerDevice         = "per_device"
	semanticTargetClassPerESwitch  = "per_eswitch"
	semanticTargetClassPFNetdevAll = "pf_netdev_all"
)

type planDocument struct {
	Plan struct {
		Name        string `json:"name"`
		Family      string `json:"family"`
		Profile     string `json:"profile"`
		Stage       string `json:"stage"`
		PathDialect string `json:"path_dialect"`
		Params      struct {
			DeploymentMode string `json:"deployment_mode"`
			Planes         int    `json:"planes"`
		} `json:"params"`
		DetectedHW struct {
			PlatformType string `json:"platform_type"`
		} `json:"detected_hw"`
		Operations map[string]semanticOperationRecord `json:"operations"`
		Semantic   *struct {
			Groups []semanticGroupRecord `json:"groups"`
		} `json:"semantic"`
		BareMetal *struct {
			Groups []json.RawMessage `json:"groups"`
		} `json:"bare_metal"`
	} `json:"plan"`
	Artifacts struct {
		Manifest []json.RawMessage `json:"manifest"`
	} `json:"artifacts"`
}

type semanticGroupRecord struct {
	Name          string   `json:"name"`
	Stage         string   `json:"stage"`
	Order         int      `json:"order"`
	Scope         string   `json:"scope"`
	OperationRefs []string `json:"operation_refs"`
}

type semanticOperationRecord struct {
	Path        string         `json:"path"`
	Values      map[string]any `json:"values"`
	Kind        string         `json:"kind"`
	Scope       string         `json:"scope"`
	TargetClass string         `json:"target_class"`
}

func decodePlanDocument(planJSON []byte) (*planDocument, error) {
	if len(bytes.TrimSpace(planJSON)) == 0 {
		return nil, fmt.Errorf("doSPCX plan must not be empty")
	}

	var document planDocument
	decoder := json.NewDecoder(bytes.NewReader(planJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode doSPCX semantic plan: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode doSPCX semantic plan: trailing JSON data")
		}
		return nil, fmt.Errorf("decode doSPCX semantic plan trailing data: %w", err)
	}
	return &document, nil
}

func buildPlan(document *planDocument, expectedStage PlanStage) (*Plan, error) {
	if err := validateSemanticPlan(document, expectedStage); err != nil {
		return nil, err
	}

	groups := append([]semanticGroupRecord(nil), document.Plan.Semantic.Groups...)
	sort.SliceStable(groups, func(left, right int) bool {
		return groups[left].Order < groups[right].Order
	})

	result := &Plan{}
	seenGroups := make(map[string]struct{}, len(groups))
	for index, group := range groups {
		if err := validateSemanticGroup(group, index, expectedStage, seenGroups); err != nil {
			return nil, err
		}
		// eSwitch mode changes are boot operations. Filtering per_eswitch parameters
		// alone is insufficient because the group also contains pf_netdev_all
		// legacy/HMFS/switchdev operations.
		if reason := skippedGroupReason(expectedStage, group.Name); reason != "" {
			log.Log.V(2).Info("skipping doSPCX semantic group",
				"group", group.Name, "stage", expectedStage, "reason", reason)
			continue
		}

		operations, err := resolveGroupOperations(group, document.Plan.Operations)
		if err != nil {
			return nil, err
		}
		// Unsupported per-VF and per-eSwitch parameters are filtered individually.
		// A group such as vf-lifecycle disappears naturally when no operations remain.
		if len(operations) == 0 {
			reason := "group has no operations"
			if len(group.OperationRefs) > 0 {
				reason = "all operations were filtered by scope or target class"
			}
			log.Log.V(2).Info("skipping doSPCX semantic group",
				"group", group.Name, "stage", expectedStage,
				"reason", reason)
			continue
		}
		if err := validateSupportedGroup(expectedStage, group.Name); err != nil {
			return nil, err
		}
		switch expectedStage {
		case PlanStagePrepare:
			switch group.Name {
			case semanticGroupBreakout:
				result.Breakout = stripXPathOperationMetadata(operations)
			case semanticGroupPostBreakout:
				result.PostBreakout = stripXPathOperationMetadata(operations)
			}
		case PlanStageConfigure:
			result.RuntimeConfig = append(result.RuntimeConfig, OperationGroup{
				Name:       group.Name,
				Scope:      strings.TrimSpace(group.Scope),
				Operations: operations,
			})
		}
	}
	return result, nil
}

func validateSemanticPlan(document *planDocument, expectedStage PlanStage) error {
	if document == nil {
		return fmt.Errorf("doSPCX plan must not be nil")
	}
	if err := validatePlanStage(expectedStage); err != nil {
		return err
	}
	if strings.TrimSpace(document.Plan.Name) == "" {
		return fmt.Errorf("doSPCX semantic plan name must not be empty")
	}
	if document.Plan.Family != "spcx" {
		return fmt.Errorf("doSPCX semantic plan family is %q, expected %q", document.Plan.Family, "spcx")
	}
	if strings.TrimSpace(document.Plan.Profile) == "" {
		return fmt.Errorf("doSPCX semantic plan profile must not be empty")
	}
	if document.Plan.Stage != string(expectedStage) {
		return fmt.Errorf("doSPCX semantic plan stage is %q, expected %q", document.Plan.Stage, expectedStage)
	}
	if document.Plan.Params.DeploymentMode != deploymentModeHostK8s {
		return fmt.Errorf(
			"doSPCX semantic plan deployment mode is %q, expected %q",
			document.Plan.Params.DeploymentMode, deploymentModeHostK8s)
	}
	if document.Plan.PathDialect != semanticPathDialect {
		return fmt.Errorf(
			"doSPCX semantic plan path dialect is %q, expected %q",
			document.Plan.PathDialect, semanticPathDialect)
	}
	if document.Plan.Semantic == nil || len(document.Plan.Semantic.Groups) == 0 {
		return fmt.Errorf("doSPCX semantic plan does not contain semantic groups")
	}
	return nil
}

func validateSemanticGroup(
	group semanticGroupRecord,
	index int,
	expectedStage PlanStage,
	seen map[string]struct{},
) error {
	if strings.TrimSpace(group.Name) == "" {
		return fmt.Errorf("doSPCX semantic group at index %d has no name", index)
	}
	if _, found := seen[group.Name]; found {
		return fmt.Errorf("doSPCX semantic group %q is duplicated", group.Name)
	}
	seen[group.Name] = struct{}{}
	if group.Stage != string(expectedStage) {
		return fmt.Errorf(
			"doSPCX semantic group %q stage is %q, expected %q",
			group.Name, group.Stage, expectedStage)
	}
	return nil
}

func skippedGroupReason(stage PlanStage, name string) string {
	if stage == PlanStageConfigure && name == semanticGroupESwitch {
		return "eSwitch mode changes are boot operations"
	}
	return ""
}

func validateSupportedGroup(stage PlanStage, name string) error {
	supported := false
	switch stage {
	case PlanStagePrepare:
		supported = name == semanticGroupBreakout || name == semanticGroupPostBreakout
	case PlanStageConfigure:
		supported = name == semanticGroupLinkRuntime || name == semanticGroupCC || name == semanticGroupLinkEvent
	}
	if !supported {
		return fmt.Errorf("unsupported doSPCX semantic group %q for stage %q", name, stage)
	}
	return nil
}

func resolveGroupOperations(
	group semanticGroupRecord,
	operations map[string]semanticOperationRecord,
) ([]dmscli.XPathOperation, error) {
	result := make([]dmscli.XPathOperation, 0, len(group.OperationRefs))
	seenRefs := make(map[string]struct{}, len(group.OperationRefs))
	for index, ref := range group.OperationRefs {
		if strings.TrimSpace(ref) == "" {
			return nil, fmt.Errorf(
				"doSPCX semantic group %q operation reference at index %d is empty",
				group.Name, index)
		}
		if _, found := seenRefs[ref]; found {
			return nil, fmt.Errorf(
				"doSPCX semantic group %q operation reference %q is duplicated",
				group.Name, ref)
		}
		seenRefs[ref] = struct{}{}

		record, found := operations[ref]
		if !found {
			return nil, fmt.Errorf(
				"doSPCX semantic group %q references missing operation %q",
				group.Name, ref)
		}
		scope := strings.TrimSpace(record.Scope)
		if scope == "" {
			scope = strings.TrimSpace(group.Scope)
		}
		targetClass := strings.TrimSpace(record.TargetClass)
		if reason := semanticOperationFilterReason(scope, targetClass); reason != "" {
			log.Log.V(2).Info("skipping doSPCX semantic operation",
				"group", group.Name, "operation", ref, "path", record.Path,
				"scope", scope, "targetClass", targetClass, "reason", reason)
			continue
		}
		if err := validateSemanticOperation(ref, record); err != nil {
			return nil, err
		}
		if targetClass == "" && scope == semanticScopePerDevice {
			targetClass = semanticTargetClassPFNetdevAll
		}
		result = append(result, dmscli.XPathOperation{
			Path:        record.Path,
			Values:      cloneValueMap(record.Values),
			Scope:       scope,
			TargetClass: targetClass,
		})
	}
	return result, nil
}

func semanticOperationFilterReason(scope, targetClass string) string {
	reasons := make([]string, 0, 2)
	if scope == semanticScopePerVF {
		reasons = append(reasons, "per-VF scope is managed outside NCO runtime execution")
	}
	if targetClass == semanticTargetClassPerESwitch {
		reasons = append(reasons, "per-eSwitch target class is managed by boot configuration")
	}
	return strings.Join(reasons, "; ")
}

func validateSemanticOperation(id string, operation semanticOperationRecord) error {
	if operation.Kind != "" && operation.Kind != "set" {
		return fmt.Errorf("doSPCX semantic operation %q has unsupported kind %q", id, operation.Kind)
	}
	if !strings.HasPrefix(operation.Path, "/nvidia/") || strings.ContainsAny(operation.Path, " \t\r\n;") {
		return fmt.Errorf("doSPCX semantic operation %q has invalid path %q", id, operation.Path)
	}
	if len(operation.Values) == 0 {
		return fmt.Errorf("doSPCX semantic operation %q has no values", id)
	}
	for leaf, value := range operation.Values {
		if strings.TrimSpace(leaf) == "" || strings.ContainsAny(leaf, "/=; \t\r\n") {
			return fmt.Errorf("doSPCX semantic operation %q has invalid leaf %q", id, leaf)
		}
		if !validSemanticValue(value) {
			return fmt.Errorf("doSPCX semantic operation %q leaf %q has an unsupported value", id, leaf)
		}
	}
	return nil
}

func validSemanticValue(value any) bool {
	switch typed := value.(type) {
	case string, bool, json.Number:
		return true
	case []any:
		if len(typed) == 0 {
			return false
		}
		for _, item := range typed {
			if !validSemanticValue(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func clonePlan(source *Plan) *Plan {
	if source == nil {
		return nil
	}
	result := &Plan{
		Breakout:      cloneXPathOperations(source.Breakout),
		PostBreakout:  cloneXPathOperations(source.PostBreakout),
		RuntimeConfig: make([]OperationGroup, len(source.RuntimeConfig)),
	}
	for index, group := range source.RuntimeConfig {
		result.RuntimeConfig[index] = OperationGroup{
			Name:       group.Name,
			Scope:      group.Scope,
			Operations: cloneXPathOperations(group.Operations),
		}
	}
	return result
}

func stripXPathOperationMetadata(source []dmscli.XPathOperation) []dmscli.XPathOperation {
	result := make([]dmscli.XPathOperation, len(source))
	for index, operation := range source {
		result[index] = dmscli.XPathOperation{
			Path:   operation.Path,
			Values: cloneValueMap(operation.Values),
		}
	}
	return result
}

func cloneXPathOperations(source []dmscli.XPathOperation) []dmscli.XPathOperation {
	result := make([]dmscli.XPathOperation, len(source))
	for index, operation := range source {
		result[index] = dmscli.XPathOperation{
			Path:        operation.Path,
			Values:      cloneValueMap(operation.Values),
			Scope:       operation.Scope,
			TargetClass: operation.TargetClass,
		}
	}
	return result
}

func cloneValueMap(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = cloneSemanticValue(value)
	}
	return result
}

func cloneSemanticValue(value any) any {
	typed, ok := value.([]any)
	if !ok {
		return value
	}
	result := make([]any, len(typed))
	for index := range result {
		result[index] = cloneSemanticValue(typed[index])
	}
	return result
}
