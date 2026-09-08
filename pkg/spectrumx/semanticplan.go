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

package spectrumx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/go-logr/logr"

	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
)

const (
	semanticPathDialect = "nvidia-t1"

	targetClassPFNetdevAll   = "pf_netdev_all"
	targetClassPFRDMAScope   = "pf_rdma_scope"
	targetClassPerESwitch    = "per_eswitch"
	targetClassVFRepresentor = "vf_rep"

	rdmaTopologyPerPF       = "per_pf"
	rdmaTopologyPerRailBond = "per_rail_bond"
)

// DMSOperationGroup is one executable group or ordered phase marker.
type DMSOperationGroup struct {
	Name           string
	Order          int
	Scope          string
	DeviceView     string
	FanoutOrder    string
	RequiresReboot bool
	PhaseMarker    bool
	Targets        []DMSTargetOperations
}

// DMSTargetOperations contains the ordered SET sequence and the final desired
// values to query for one DMS target.
type DMSTargetOperations struct {
	Target     string
	Queries    []dmscli.XPathQuery
	Desired    []dmscli.XPathOperation
	Operations []dmscli.XPathOperation
}

// SkippedSemanticGroup records an intentional execution-policy exclusion.
type SkippedSemanticGroup struct {
	Name   string
	Order  int
	Reason string
}

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
		Devices        []planDevice                       `json:"devices"`
		RuntimeContext planRuntimeContext                 `json:"runtime_ctx"`
		Operations     map[string]semanticOperationRecord `json:"operations"`
		Semantic       *struct {
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

type planDevice struct {
	BDF        string `json:"bdf"`
	DeviceID   string `json:"device_id"`
	RDMADevice string `json:"rdma_dev"`
	DMSTarget  string `json:"dms_target"`
	Rail       int    `json:"rail"`
	Plane      int    `json:"plane"`
	Network    string `json:"network"`
}

type planRuntimeContext struct {
	RDMATopology string `json:"rdma_topology"`
}

type semanticGroupRecord struct {
	Name           string   `json:"name"`
	Stage          string   `json:"stage"`
	Order          int      `json:"order"`
	Scope          string   `json:"scope"`
	DeviceView     string   `json:"device_view"`
	FanoutOrder    string   `json:"fanout_order"`
	RequiresReboot bool     `json:"requires_reboot"`
	OperationRefs  []string `json:"operation_refs"`
}

type semanticOperationRecord struct {
	ID          string         `json:"-"`
	Path        string         `json:"path"`
	Values      map[string]any `json:"values"`
	Kind        string         `json:"kind"`
	TargetClass string         `json:"target_class"`
	TargetRole  string         `json:"target_role"`
	Scope       string         `json:"scope"`
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

func buildDMSPlan(ctx context.Context, document *planDocument, expectedStage PlanStage) (*Plan, error) {
	if err := validateSemanticPlan(document, expectedStage); err != nil {
		return nil, err
	}

	records := append([]semanticGroupRecord(nil), document.Plan.Semantic.Groups...)
	sort.SliceStable(records, func(left, right int) bool {
		return records[left].Order < records[right].Order
	})
	result := &Plan{
		Name:          document.Plan.Name,
		Stage:         expectedStage,
		Groups:        make([]DMSOperationGroup, 0, len(records)),
		SkippedGroups: nil,
	}
	names := make(map[string]struct{}, len(records))
	for index, group := range records {
		if err := validateSemanticGroup(group, index, expectedStage, names); err != nil {
			return nil, err
		}
		operations, err := resolveGroupOperations(group, document.Plan.Operations)
		if err != nil {
			return nil, err
		}
		disposition, reason, err := semanticGroupDisposition(expectedStage, group.Name)
		if err != nil {
			return nil, err
		}
		if disposition == groupDispositionSkip {
			result.SkippedGroups = append(result.SkippedGroups, SkippedSemanticGroup{
				Name: group.Name, Order: group.Order, Reason: reason,
			})
			logr.FromContextOrDiscard(ctx).V(2).Info("skipping doSPCX semantic group",
				"plan", document.Plan.Name, "stage", expectedStage,
				"group", group.Name, "order", group.Order, "reason", reason)
			continue
		}

		compiled := DMSOperationGroup{
			Name:           group.Name,
			Order:          group.Order,
			Scope:          group.Scope,
			DeviceView:     group.DeviceView,
			FanoutOrder:    group.FanoutOrder,
			RequiresReboot: group.RequiresReboot,
			PhaseMarker:    disposition == groupDispositionMarker,
		}
		if compiled.PhaseMarker {
			if len(operations) != 0 {
				return nil, fmt.Errorf("doSPCX semantic phase marker %q unexpectedly contains operations", group.Name)
			}
		} else {
			if err := validateExecutableOperations(group.Name, operations); err != nil {
				return nil, err
			}
			compiled.Targets, err = resolveGroupTargets(
				document.Plan.Devices, document.Plan.RuntimeContext.RDMATopology,
				operations, expectedStage)
			if err != nil {
				return nil, fmt.Errorf("resolve doSPCX semantic group %q: %w", group.Name, err)
			}
		}
		result.Groups = append(result.Groups, compiled)
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
		return fmt.Errorf("doSPCX semantic plan deployment mode is %q, expected %q", document.Plan.Params.DeploymentMode, deploymentModeHostK8s)
	}
	if document.Plan.PathDialect != semanticPathDialect {
		return fmt.Errorf("doSPCX semantic plan path dialect is %q, expected %q", document.Plan.PathDialect, semanticPathDialect)
	}
	if len(document.Plan.Devices) == 0 {
		return fmt.Errorf("doSPCX semantic plan does not contain devices")
	}
	if document.Plan.Semantic == nil || len(document.Plan.Semantic.Groups) == 0 {
		return fmt.Errorf("doSPCX semantic plan does not contain semantic groups")
	}
	return validatePlanDevices(document.Plan.Devices)
}

func validatePlanDevices(devices []planDevice) error {
	bdfs := make(map[string]struct{}, len(devices))
	targets := make(map[string]struct{}, len(devices))
	for index, device := range devices {
		if strings.TrimSpace(device.BDF) == "" {
			return fmt.Errorf("doSPCX plan device at index %d has no BDF", index)
		}
		if _, found := bdfs[device.BDF]; found {
			return fmt.Errorf("doSPCX plan device BDF %q is duplicated", device.BDF)
		}
		bdfs[device.BDF] = struct{}{}
		if device.DMSTarget != "pci/"+device.BDF {
			return fmt.Errorf("doSPCX plan device %q has invalid DMS target %q", device.BDF, device.DMSTarget)
		}
		if _, found := targets[device.DMSTarget]; found {
			return fmt.Errorf("doSPCX plan DMS target %q is duplicated", device.DMSTarget)
		}
		targets[device.DMSTarget] = struct{}{}
		if strings.TrimSpace(device.Network) == "" {
			return fmt.Errorf("doSPCX plan device %q has no network role", device.BDF)
		}
	}
	return nil
}

func validateSemanticGroup(
	group semanticGroupRecord,
	index int,
	expectedStage PlanStage,
	names map[string]struct{},
) error {
	if strings.TrimSpace(group.Name) == "" {
		return fmt.Errorf("doSPCX semantic group at index %d has no name", index)
	}
	if _, found := names[group.Name]; found {
		return fmt.Errorf("doSPCX semantic group %q is duplicated", group.Name)
	}
	names[group.Name] = struct{}{}
	if group.Stage != string(expectedStage) {
		return fmt.Errorf("doSPCX semantic group %q stage is %q, expected %q", group.Name, group.Stage, expectedStage)
	}
	return nil
}

func resolveGroupOperations(
	group semanticGroupRecord,
	operations map[string]semanticOperationRecord,
) ([]semanticOperationRecord, error) {
	result := make([]semanticOperationRecord, 0, len(group.OperationRefs))
	refs := make(map[string]struct{}, len(group.OperationRefs))
	for index, ref := range group.OperationRefs {
		if strings.TrimSpace(ref) == "" {
			return nil, fmt.Errorf("doSPCX semantic group %q operation reference at index %d is empty", group.Name, index)
		}
		if _, found := refs[ref]; found {
			return nil, fmt.Errorf("doSPCX semantic group %q operation reference %q is duplicated", group.Name, ref)
		}
		refs[ref] = struct{}{}
		operation, found := operations[ref]
		if !found {
			return nil, fmt.Errorf("doSPCX semantic group %q references missing operation %q", group.Name, ref)
		}
		if operation.Kind == "" {
			operation.Kind = "set"
		}
		if operation.TargetClass == "" {
			operation.TargetClass = targetClassPFNetdevAll
		}
		operation.ID = ref
		if err := validateSemanticOperation(ref, operation); err != nil {
			return nil, err
		}
		result = append(result, operation)
	}
	return result, nil
}

func validateSemanticOperation(id string, operation semanticOperationRecord) error {
	if operation.Kind != "set" {
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
	switch operation.TargetClass {
	case targetClassPFNetdevAll, targetClassPFRDMAScope, targetClassPerESwitch, targetClassVFRepresentor:
	default:
		return fmt.Errorf("doSPCX semantic operation %q has unsupported target class %q", id, operation.TargetClass)
	}
	return nil
}

func validateExecutableOperations(group string, operations []semanticOperationRecord) error {
	for _, operation := range operations {
		if operation.TargetClass == targetClassPerESwitch ||
			operation.TargetClass == targetClassVFRepresentor ||
			operation.Scope == "per_vf" ||
			strings.HasPrefix(operation.Path, "/nvidia/eswitch") {
			return fmt.Errorf(
				"doSPCX semantic group %q contains operation %q outside the current NCO execution scope",
				group, operation.ID)
		}
	}
	return nil
}

func validSemanticValue(value any) bool {
	if value == nil {
		return false
	}
	if _, ok := value.(json.Number); ok {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Array, reflect.Slice:
		if reflected.Len() == 0 {
			return false
		}
		for index := 0; index < reflected.Len(); index++ {
			item := reflected.Index(index).Interface()
			if item == nil {
				return false
			}
			kind := reflect.ValueOf(item).Kind()
			if kind == reflect.Array || kind == reflect.Slice || kind == reflect.Map ||
				kind == reflect.Struct || kind == reflect.Pointer {
				return false
			}
			if !validSemanticValue(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

type groupDisposition string

const (
	groupDispositionExecute groupDisposition = "execute"
	groupDispositionMarker  groupDisposition = "marker"
	groupDispositionSkip    groupDisposition = "skip"
)

func semanticGroupDisposition(stage PlanStage, name string) (groupDisposition, string, error) {
	switch stage {
	case PlanStagePrepare:
		switch name {
		case "breakout":
			return groupDispositionExecute, "", nil
		case "post-breakout":
			return groupDispositionMarker, "", nil
		}
	case PlanStageConfigure:
		switch name {
		case "link-runtime", "cc", "link-event":
			return groupDispositionExecute, "", nil
		case "eswitch":
			return groupDispositionSkip, "eSwitch lifecycle is outside the current NCO plan execution scope", nil
		case "vf-lifecycle":
			return groupDispositionSkip, "VF representor lifecycle is outside the current NCO plan execution scope", nil
		}
	}
	return "", "", fmt.Errorf("unsupported doSPCX semantic group %q for stage %q", name, stage)
}

func resolveGroupTargets(
	devices []planDevice,
	rdmaTopology string,
	operations []semanticOperationRecord,
	stage PlanStage,
) ([]DMSTargetOperations, error) {
	targetOrder := make([]string, 0, len(devices))
	operationsByTarget := make(map[string][]dmscli.XPathOperation, len(devices))
	for _, operation := range operations {
		targets, err := resolveOperationTargets(devices, rdmaTopology, operation)
		if err != nil {
			return nil, fmt.Errorf("operation %q: %w", operation.ID, err)
		}
		for _, target := range targets {
			if _, found := operationsByTarget[target]; !found {
				targetOrder = append(targetOrder, target)
			}
			operationsByTarget[target] = append(operationsByTarget[target], dmscli.XPathOperation{
				Path: operation.Path, Values: cloneValueMap(operation.Values),
			})
		}
	}
	if len(operations) > 0 && len(targetOrder) == 0 {
		return nil, fmt.Errorf("no DMS targets resolved")
	}
	result := make([]DMSTargetOperations, 0, len(targetOrder))
	for _, target := range targetOrder {
		ordered := operationsByTarget[target]
		desired := finalDesiredState(ordered)
		result = append(result, DMSTargetOperations{
			Target: target, Queries: queriesForDesiredState(desired, stage == PlanStagePrepare),
			Desired: desired, Operations: ordered,
		})
	}
	return result, nil
}

func resolveOperationTargets(
	devices []planDevice,
	rdmaTopology string,
	operation semanticOperationRecord,
) ([]string, error) {
	result := make([]string, 0, len(devices))
	for _, device := range devices {
		if operation.TargetRole != "" && device.Network != operation.TargetRole {
			continue
		}
		switch operation.TargetClass {
		case targetClassPFNetdevAll:
			result = append(result, device.DMSTarget)
		case targetClassPFRDMAScope:
			switch rdmaTopology {
			case rdmaTopologyPerPF:
				if strings.TrimSpace(device.RDMADevice) != "" {
					result = append(result, device.DMSTarget)
				}
			case rdmaTopologyPerRailBond:
				if device.Plane == 0 {
					result = append(result, device.DMSTarget)
				}
			default:
				return nil, fmt.Errorf("unsupported RDMA topology %q", rdmaTopology)
			}
		}
	}
	if len(result) == 0 {
		if operation.TargetRole != "" {
			return nil, fmt.Errorf("no plan devices match target role %q", operation.TargetRole)
		}
		return nil, fmt.Errorf("doSPCX plan does not contain eligible devices")
	}
	return result, nil
}

func finalDesiredState(operations []dmscli.XPathOperation) []dmscli.XPathOperation {
	pathOrder := make([]string, 0, len(operations))
	byPath := make(map[string]map[string]any, len(operations))
	for _, operation := range operations {
		values, found := byPath[operation.Path]
		if !found {
			values = map[string]any{}
			byPath[operation.Path] = values
			pathOrder = append(pathOrder, operation.Path)
		}
		for leaf, value := range operation.Values {
			values[leaf] = cloneSemanticValue(value)
		}
	}

	result := make([]dmscli.XPathOperation, 0, len(pathOrder))
	for _, path := range pathOrder {
		result = append(result, dmscli.XPathOperation{Path: path, Values: byPath[path]})
	}
	return result
}

func queriesForDesiredState(desired []dmscli.XPathOperation, includePending bool) []dmscli.XPathQuery {
	result := make([]dmscli.XPathQuery, 0, len(desired))
	for _, operation := range desired {
		leaves := make([]string, 0, len(operation.Values)*2)
		for leaf := range operation.Values {
			leaves = append(leaves, leaf)
			if includePending {
				leaves = append(leaves, leaf+"-pending")
			}
		}
		sort.Strings(leaves)
		result = append(result, dmscli.XPathQuery{Path: operation.Path, Leaves: leaves})
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
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Array && reflected.Kind() != reflect.Slice {
		return value
	}
	result := make([]any, reflected.Len())
	for index := range result {
		result[index] = cloneSemanticValue(reflected.Index(index).Interface())
	}
	return result
}
