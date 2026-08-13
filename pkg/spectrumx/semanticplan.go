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

// PlanDevice is one DMS-addressable device emitted by the doSPCX planner.
type PlanDevice struct {
	BDF            string   `json:"bdf"`
	DeviceID       string   `json:"device_id"`
	HCAType        string   `json:"hca_type"`
	Netdev         string   `json:"netdev"`
	RDMADevice     string   `json:"rdma_dev"`
	DMSTarget      string   `json:"dms_target"`
	PFIndex        int      `json:"pf_index"`
	Rail           int      `json:"rail"`
	Plane          int      `json:"plane"`
	PlaneExplicit  bool     `json:"plane_explicit"`
	Network        string   `json:"network"`
	PhysicalLabel  string   `json:"physical_label"`
	ResetDomain    string   `json:"reset_domain"`
	EndpointLabels []string `json:"endpoint_labels"`
	TargetID       string   `json:"target_id"`
}

// ExpectedRDMA identifies the control target selected for one rail.
type ExpectedRDMA struct {
	Rail          int    `json:"rail"`
	RDMADevice    string `json:"rdma_dev"`
	ControlBDF    string `json:"control_bdf"`
	ControlTarget string `json:"control_target"`
	Source        string `json:"source"`
}

// SemanticRuntimeContext contains topology needed to resolve semantic target
// classes without interpreting rendered bare-metal artifacts.
type SemanticRuntimeContext struct {
	DeploymentMode   string         `json:"deployment_mode"`
	MultiplaneMode   string         `json:"multiplane_mode"`
	ESwitchMultiport bool           `json:"esw_multiport"`
	NumVFs           int            `json:"num_vfs"`
	PostBreakout     bool           `json:"post_breakout"`
	RDMATopology     string         `json:"rdma_topology"`
	ExpectedRDMA     []ExpectedRDMA `json:"expected_rdma"`
}

// ResetPolicy records the reset boundary associated with a semantic operation.
type ResetPolicy struct {
	Action        string `json:"action"`
	Owner         string `json:"owner"`
	Postcondition string `json:"postcondition"`
}

// SemanticOperation is one referenced typed operation from plan.operations.
type SemanticOperation struct {
	ID             string
	Path           string
	Values         map[string]any
	SourceFeature  string
	Kind           string
	TargetClass    string
	TargetRole     string
	ExecutionGroup string
	Scope          string
	Lifecycle      string
	Reset          *ResetPolicy
	Condition      json.RawMessage
	Context        json.RawMessage
}

// SemanticGroup preserves the planner's ordered semantic execution boundary.
type SemanticGroup struct {
	Name           string
	Stage          PlanStage
	Order          int
	Scope          string
	DeviceView     string
	FanoutOrder    string
	RequiresReboot bool
	Operations     []SemanticOperation
}

// SemanticPlan is the NCO-facing doSPCX plan surface. It intentionally omits
// bare-metal steps, services, and rendered artifacts.
type SemanticPlan struct {
	Name           string
	Profile        string
	Stage          PlanStage
	PathDialect    string
	Devices        []PlanDevice
	RuntimeContext SemanticRuntimeContext
	Groups         []SemanticGroup
}

// DMSOperationPlan contains ordered, target-resolved operations ready for the
// generic dms-cli query/set transport. It does not execute any operation.
type DMSOperationPlan struct {
	Stage         PlanStage
	Groups        []DMSOperationGroup
	SkippedGroups []SkippedSemanticGroup
}

// DMSOperationGroup is one executable group or ordered phase marker.
type DMSOperationGroup struct {
	Name           string
	Order          int
	Scope          string
	DeviceView     string
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

type semanticPlanDocument struct {
	Plan struct {
		Name        string `json:"name"`
		Family      string `json:"family"`
		Profile     string `json:"profile"`
		Stage       string `json:"stage"`
		PathDialect string `json:"path_dialect"`
		Params      struct {
			DeploymentMode string `json:"deployment_mode"`
		} `json:"params"`
		Devices        []PlanDevice                       `json:"devices"`
		RuntimeContext SemanticRuntimeContext             `json:"runtime_ctx"`
		Operations     map[string]semanticOperationRecord `json:"operations"`
		Semantic       *struct {
			Groups []semanticGroupRecord `json:"groups"`
		} `json:"semantic"`
	} `json:"plan"`
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
	Path           string          `json:"path"`
	Values         map[string]any  `json:"values"`
	SourceFeature  string          `json:"source_feature"`
	Kind           string          `json:"kind"`
	TargetClass    string          `json:"target_class"`
	TargetRole     string          `json:"target_role"`
	ExecutionGroup string          `json:"execution_group"`
	Scope          string          `json:"scope"`
	Lifecycle      string          `json:"lifecycle"`
	Reset          *ResetPolicy    `json:"reset"`
	Condition      json.RawMessage `json:"condition"`
	Context        json.RawMessage `json:"context"`
}

// ParseSemanticPlan parses and validates the semantic consumer surface of a
// generated host-k8s doSPCX plan.
func ParseSemanticPlan(planJSON json.RawMessage, expectedStage PlanStage) (*SemanticPlan, error) {
	if err := validatePlanStage(expectedStage); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(planJSON)) == 0 {
		return nil, fmt.Errorf("doSPCX plan must not be empty")
	}

	var document semanticPlanDocument
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

	if err := validateSemanticPlanHeader(&document, expectedStage); err != nil {
		return nil, err
	}
	if err := validatePlanDevices(document.Plan.Devices); err != nil {
		return nil, err
	}

	groups, err := resolveSemanticGroups(
		document.Plan.Semantic.Groups,
		document.Plan.Operations,
		expectedStage,
	)
	if err != nil {
		return nil, err
	}

	return &SemanticPlan{
		Name:           document.Plan.Name,
		Profile:        document.Plan.Profile,
		Stage:          expectedStage,
		PathDialect:    document.Plan.PathDialect,
		Devices:        append([]PlanDevice(nil), document.Plan.Devices...),
		RuntimeContext: document.Plan.RuntimeContext,
		Groups:         groups,
	}, nil
}

// BuildDMSOperationPlan applies NCO execution policy and resolves semantic
// target classes. The eswitch and vf-lifecycle groups are intentionally skipped
// in this phase; unknown groups fail closed.
func (p *SemanticPlan) BuildDMSOperationPlan(ctx context.Context) (*DMSOperationPlan, error) {
	if p == nil {
		return nil, fmt.Errorf("semantic plan must not be nil")
	}
	if err := validatePlanStage(p.Stage); err != nil {
		return nil, err
	}

	result := &DMSOperationPlan{
		Stage:         p.Stage,
		Groups:        make([]DMSOperationGroup, 0, len(p.Groups)),
		SkippedGroups: nil,
	}
	for _, group := range p.Groups {
		disposition, reason, err := semanticGroupDisposition(p.Stage, group.Name)
		if err != nil {
			return nil, err
		}
		switch disposition {
		case groupDispositionSkip:
			skipped := SkippedSemanticGroup{Name: group.Name, Order: group.Order, Reason: reason}
			result.SkippedGroups = append(result.SkippedGroups, skipped)
			logr.FromContextOrDiscard(ctx).V(2).Info("skipping doSPCX semantic group",
				"plan", p.Name,
				"stage", p.Stage,
				"group", group.Name,
				"order", group.Order,
				"reason", reason)
			continue
		case groupDispositionMarker:
			if len(group.Operations) != 0 {
				return nil, fmt.Errorf("doSPCX semantic phase marker %q unexpectedly contains operations", group.Name)
			}
			result.Groups = append(result.Groups, DMSOperationGroup{
				Name:           group.Name,
				Order:          group.Order,
				Scope:          group.Scope,
				DeviceView:     group.DeviceView,
				RequiresReboot: group.RequiresReboot,
				PhaseMarker:    true,
				Targets:        nil,
			})
			continue
		case groupDispositionExecute:
			targets, err := p.resolveGroupTargets(group)
			if err != nil {
				return nil, fmt.Errorf("resolve doSPCX semantic group %q: %w", group.Name, err)
			}
			result.Groups = append(result.Groups, DMSOperationGroup{
				Name:           group.Name,
				Order:          group.Order,
				Scope:          group.Scope,
				DeviceView:     group.DeviceView,
				RequiresReboot: group.RequiresReboot,
				PhaseMarker:    false,
				Targets:        targets,
			})
		default:
			return nil, fmt.Errorf("unsupported doSPCX semantic group disposition %q", disposition)
		}
	}
	return result, nil
}

func validateSemanticPlanHeader(document *semanticPlanDocument, expectedStage PlanStage) error {
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
	return nil
}

func validatePlanDevices(devices []PlanDevice) error {
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
		if !strings.HasPrefix(device.DMSTarget, "pci/") || strings.TrimPrefix(device.DMSTarget, "pci/") != device.BDF {
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

func resolveSemanticGroups(
	records []semanticGroupRecord,
	operations map[string]semanticOperationRecord,
	expectedStage PlanStage,
) ([]SemanticGroup, error) {
	groups := make([]SemanticGroup, 0, len(records))
	names := make(map[string]struct{}, len(records))
	for groupIndex, record := range records {
		if strings.TrimSpace(record.Name) == "" {
			return nil, fmt.Errorf("doSPCX semantic group at index %d has no name", groupIndex)
		}
		if _, found := names[record.Name]; found {
			return nil, fmt.Errorf("doSPCX semantic group %q is duplicated", record.Name)
		}
		names[record.Name] = struct{}{}
		if record.Stage != string(expectedStage) {
			return nil, fmt.Errorf("doSPCX semantic group %q stage is %q, expected %q", record.Name, record.Stage, expectedStage)
		}

		group := SemanticGroup{
			Name:           record.Name,
			Stage:          expectedStage,
			Order:          record.Order,
			Scope:          record.Scope,
			DeviceView:     record.DeviceView,
			FanoutOrder:    record.FanoutOrder,
			RequiresReboot: record.RequiresReboot,
			Operations:     make([]SemanticOperation, 0, len(record.OperationRefs)),
		}
		refs := make(map[string]struct{}, len(record.OperationRefs))
		for refIndex, ref := range record.OperationRefs {
			if strings.TrimSpace(ref) == "" {
				return nil, fmt.Errorf("doSPCX semantic group %q operation reference at index %d is empty", record.Name, refIndex)
			}
			if _, found := refs[ref]; found {
				return nil, fmt.Errorf("doSPCX semantic group %q operation reference %q is duplicated", record.Name, ref)
			}
			refs[ref] = struct{}{}
			operation, found := operations[ref]
			if !found {
				return nil, fmt.Errorf("doSPCX semantic group %q references missing operation %q", record.Name, ref)
			}
			if operation.TargetClass == "" {
				operation.TargetClass = targetClassPFNetdevAll
			}
			if err := validateSemanticOperation(ref, record.Name, operation); err != nil {
				return nil, err
			}
			group.Operations = append(group.Operations, SemanticOperation{
				ID:             ref,
				Path:           operation.Path,
				Values:         cloneValueMap(operation.Values),
				SourceFeature:  operation.SourceFeature,
				Kind:           operation.Kind,
				TargetClass:    operation.TargetClass,
				TargetRole:     operation.TargetRole,
				ExecutionGroup: operation.ExecutionGroup,
				Scope:          operation.Scope,
				Lifecycle:      operation.Lifecycle,
				Reset:          operation.Reset,
				Condition:      append(json.RawMessage(nil), operation.Condition...),
				Context:        append(json.RawMessage(nil), operation.Context...),
			})
		}
		groups = append(groups, group)
	}

	sort.SliceStable(groups, func(left, right int) bool {
		return groups[left].Order < groups[right].Order
	})
	return groups, nil
}

func validateSemanticOperation(id, group string, operation semanticOperationRecord) error {
	if operation.Kind != "set" {
		return fmt.Errorf("doSPCX semantic operation %q has unsupported kind %q", id, operation.Kind)
	}
	if operation.ExecutionGroup != group {
		return fmt.Errorf("doSPCX semantic operation %q belongs to group %q, not %q", id, operation.ExecutionGroup, group)
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
	if operation.TargetClass != targetClassVFRepresentor && strings.TrimSpace(operation.TargetRole) == "" {
		return fmt.Errorf("doSPCX semantic operation %q has no target role", id)
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
		for index := 0; index < reflected.Len(); index++ {
			item := reflected.Index(index).Interface()
			if item == nil {
				return false
			}
			kind := reflect.ValueOf(item).Kind()
			if kind == reflect.Array || kind == reflect.Slice || kind == reflect.Map || kind == reflect.Struct || kind == reflect.Pointer {
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

func (p *SemanticPlan) resolveGroupTargets(group SemanticGroup) ([]DMSTargetOperations, error) {
	targetOrder := make([]string, 0, len(p.Devices))
	operationsByTarget := make(map[string][]dmscli.XPathOperation, len(p.Devices))
	for _, operation := range group.Operations {
		targets, err := p.resolveOperationTargets(operation)
		if err != nil {
			return nil, fmt.Errorf("operation %q: %w", operation.ID, err)
		}
		for _, target := range targets {
			if _, found := operationsByTarget[target]; !found {
				targetOrder = append(targetOrder, target)
			}
			operationsByTarget[target] = append(operationsByTarget[target], dmscli.XPathOperation{
				Path:   operation.Path,
				Values: cloneValueMap(operation.Values),
			})
		}
	}

	if len(group.Operations) > 0 && len(targetOrder) == 0 {
		return nil, fmt.Errorf("no DMS targets resolved")
	}
	result := make([]DMSTargetOperations, 0, len(targetOrder))
	for _, target := range targetOrder {
		operations := operationsByTarget[target]
		desired := finalDesiredState(operations)
		result = append(result, DMSTargetOperations{
			Target:     target,
			Queries:    queriesForDesiredState(desired, p.Stage == PlanStagePrepare),
			Desired:    desired,
			Operations: operations,
		})
	}
	return result, nil
}

func (p *SemanticPlan) resolveOperationTargets(operation SemanticOperation) ([]string, error) {
	eligible := make([]PlanDevice, 0, len(p.Devices))
	for _, device := range p.Devices {
		if device.Network != operation.TargetRole {
			continue
		}
		eligible = append(eligible, device)
	}
	if len(eligible) == 0 {
		return nil, fmt.Errorf("no plan devices match target role %q", operation.TargetRole)
	}

	switch operation.TargetClass {
	case targetClassPFNetdevAll:
		result := make([]string, 0, len(eligible))
		for _, device := range eligible {
			result = append(result, device.DMSTarget)
		}
		return result, nil
	case targetClassPFRDMAScope:
		switch p.RuntimeContext.RDMATopology {
		case rdmaTopologyPerPF:
			result := make([]string, 0, len(eligible))
			for _, device := range eligible {
				result = append(result, device.DMSTarget)
			}
			return result, nil
		case rdmaTopologyPerRailBond:
			expected := append([]ExpectedRDMA(nil), p.RuntimeContext.ExpectedRDMA...)
			sort.SliceStable(expected, func(left, right int) bool {
				return expected[left].Rail < expected[right].Rail
			})
			if len(expected) == 0 {
				return nil, fmt.Errorf("per-rail-bond topology does not define expected RDMA controls")
			}
			seenTargets := make(map[string]struct{}, len(expected))
			seenRails := make(map[int]struct{}, len(expected))
			eligibleDevices := make(map[string]PlanDevice, len(eligible))
			for _, device := range eligible {
				eligibleDevices[device.DMSTarget] = device
			}
			result := make([]string, 0, len(expected))
			for _, control := range expected {
				if _, found := seenRails[control.Rail]; found {
					return nil, fmt.Errorf("RDMA control rail %d is duplicated", control.Rail)
				}
				seenRails[control.Rail] = struct{}{}
				if strings.TrimSpace(control.ControlTarget) == "" {
					return nil, fmt.Errorf("rail %d has no RDMA control target", control.Rail)
				}
				device, found := eligibleDevices[control.ControlTarget]
				if !found {
					return nil, fmt.Errorf("RDMA control target %q is not a plan device for role %q", control.ControlTarget, operation.TargetRole)
				}
				if control.ControlBDF == "" || control.ControlTarget != "pci/"+control.ControlBDF {
					return nil, fmt.Errorf("RDMA control target %q does not match control BDF %q", control.ControlTarget, control.ControlBDF)
				}
				if device.Rail != control.Rail {
					return nil, fmt.Errorf("RDMA control target %q belongs to rail %d, not rail %d", control.ControlTarget, device.Rail, control.Rail)
				}
				if _, found := seenTargets[control.ControlTarget]; found {
					return nil, fmt.Errorf("RDMA control target %q is duplicated", control.ControlTarget)
				}
				seenTargets[control.ControlTarget] = struct{}{}
				result = append(result, control.ControlTarget)
			}
			return result, nil
		default:
			return nil, fmt.Errorf("unsupported RDMA topology %q", p.RuntimeContext.RDMATopology)
		}
	default:
		return nil, fmt.Errorf("target class %q is not executable in this phase", operation.TargetClass)
	}
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
		leafCapacity := len(operation.Values)
		if includePending {
			leafCapacity *= 2
		}
		leaves := make([]string, 0, leafCapacity)
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
	if values == nil {
		return nil
	}
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = cloneSemanticValue(value)
	}
	return result
}

func cloneSemanticValue(value any) any {
	if value == nil {
		return nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Array && reflected.Kind() != reflect.Slice {
		return value
	}
	result := make([]any, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		result[index] = cloneSemanticValue(reflected.Index(index).Interface())
	}
	return result
}
