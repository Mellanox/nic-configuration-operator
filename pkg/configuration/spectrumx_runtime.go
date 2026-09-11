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

package configuration

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/spectrumx"
)

const (
	spectrumXRuntimeGroupCC             = "cc"
	spectrumXRuntimeWriteOnlyPPCCSlot15 = "/nvidia/cc/algo/slot/[15]"
	spectrumXRuntimeScopePerDevice      = "per_device"
	spectrumXRuntimeScopePerRDMABond    = "per_rdma_bond"
	spectrumXRuntimeScopePerVF          = "per_vf"
	spectrumXRuntimeTargetPFNetdevAll   = "pf_netdev_all"
	spectrumXRuntimeTargetPFRDMAScope   = "pf_rdma_scope"
	spectrumXRuntimeTargetPerESwitch    = "per_eswitch"
)

type spectrumXRuntimeOperationBatch struct {
	scope       string
	targetClass string
	operations  []dmscli.XPathOperation
	targets     []spectrumXRuntimeTarget
}

type spectrumXRuntimeTarget struct {
	name string
	port v1alpha1.NicDevicePortSpec
}

func (h configurationManager) validateSpectrumXRuntimeConfig(
	ctx context.Context,
	device *v1alpha1.NicDevice,
	plan *spectrumx.Plan,
) (bool, error) {
	if plan == nil {
		return false, fmt.Errorf("cannot validate a nil doSPCX runtime plan")
	}
	for _, group := range plan.RuntimeConfig {
		batches, err := spectrumXRuntimeOperationBatches(ctx, device, group)
		if err != nil {
			return false, err
		}
		if group.Name == spectrumXRuntimeGroupCC && len(batches) > 0 {
			if err := h.startSpectrumXCC(spectrumXRuntimeBatchPorts(batches)); err != nil {
				return false, fmt.Errorf("start DOCA SPC-X CC for device %q: %w", device.Name, err)
			}
		}
		lastWriteBatches := spectrumXRuntimeLastWriteBatches(batches)

		for batchIndex, batch := range batches {
			log.FromContext(ctx).V(2).Info("validating doSPCX runtime configuration group",
				"device", device.Name, "group", group.Name, "scope", batch.scope,
				"targetClass", batch.targetClass, "operations", len(batch.operations), "targets", len(batch.targets))
			for _, target := range batch.targets {
				operations := spectrumXRuntimeFinalBatchOperations(
					batch.operations, batchIndex, target.name, lastWriteBatches)
				queries, desiredValues := spectrumXRuntimeQueries(operations)
				if len(queries) == 0 {
					log.FromContext(ctx).V(2).Info("skipping doSPCX runtime validation batch",
						"device", device.Name, "group", group.Name, "scope", batch.scope,
						"targetClass", batch.targetClass, "target", target.name,
						"reason", "all values are shadowed by a later batch or are write-only")
					continue
				}
				matches, err := h.validateSpectrumXRuntimeTarget(
					ctx, target.name, queries, desiredValues)
				if err != nil {
					return false, fmt.Errorf(
						"validate doSPCX runtime group %q scope %q target class %q for device %q on PCI function %q: %w",
						group.Name, batch.scope, batch.targetClass, device.Name, target.port.PCI, err)
				}
				if !matches {
					return false, nil
				}
			}
		}
	}
	return true, nil
}

type spectrumXRuntimeWriteKey struct {
	target string
	path   string
	leaf   string
}

// spectrumXRuntimeLastWriteBatches records which scope/target-class batch owns the
// final desired value of every leaf on every target. Validation still issues one
// DMS command per original batch and target; it only omits values shadowed by a
// later batch that applies to the same target.
func spectrumXRuntimeLastWriteBatches(batches []spectrumXRuntimeOperationBatch) map[spectrumXRuntimeWriteKey]int {
	lastWriteBatches := make(map[spectrumXRuntimeWriteKey]int)
	for batchIndex, batch := range batches {
		for _, target := range batch.targets {
			for _, operation := range batch.operations {
				for leaf := range operation.Values {
					lastWriteBatches[spectrumXRuntimeWriteKey{
						target: target.name,
						path:   operation.Path,
						leaf:   leaf,
					}] = batchIndex
				}
			}
		}
	}
	return lastWriteBatches
}

func spectrumXRuntimeFinalBatchOperations(
	operations []dmscli.XPathOperation,
	batchIndex int,
	target string,
	lastWriteBatches map[spectrumXRuntimeWriteKey]int,
) []dmscli.XPathOperation {
	finalOperations := make([]dmscli.XPathOperation, 0, len(operations))
	for _, operation := range operations {
		values := make(map[string]any, len(operation.Values))
		for leaf, value := range operation.Values {
			key := spectrumXRuntimeWriteKey{target: target, path: operation.Path, leaf: leaf}
			lastBatch, found := lastWriteBatches[key]
			if found && lastBatch == batchIndex {
				values[leaf] = value
			}
		}
		if len(values) == 0 {
			continue
		}
		operation.Values = values
		finalOperations = append(finalOperations, operation)
	}
	return finalOperations
}

func (h configurationManager) applySpectrumXRuntimeConfig(
	ctx context.Context,
	device *v1alpha1.NicDevice,
	plan *spectrumx.Plan,
) error {
	if plan == nil {
		return fmt.Errorf("cannot apply a nil doSPCX runtime plan")
	}
	for _, group := range plan.RuntimeConfig {
		batches, err := spectrumXRuntimeOperationBatches(ctx, device, group)
		if err != nil {
			return err
		}
		if group.Name == spectrumXRuntimeGroupCC && len(batches) > 0 {
			if err := h.startSpectrumXCC(spectrumXRuntimeBatchPorts(batches)); err != nil {
				return fmt.Errorf("start DOCA SPC-X CC for device %q: %w", device.Name, err)
			}
		}

		for _, batch := range batches {
			log.FromContext(ctx).Info("applying doSPCX runtime configuration group",
				"device", device.Name, "group", group.Name, "scope", batch.scope,
				"targetClass", batch.targetClass, "operations", len(batch.operations), "targets", len(batch.targets))
			for _, target := range batch.targets {
				if _, err := dmscli.SetXPaths(ctx, h.execInterface, target.name, batch.operations); err != nil {
					return fmt.Errorf(
						"apply doSPCX runtime group %q scope %q target class %q for device %q on target %q: %w",
						group.Name, batch.scope, batch.targetClass, device.Name, target.name, err)
				}
			}
		}
	}
	return nil
}

func (h configurationManager) startSpectrumXCC(ports []v1alpha1.NicDevicePortSpec) error {
	if len(ports) == 0 {
		return fmt.Errorf("no RDMA-scoped ports")
	}
	started := make(map[string]struct{}, len(ports))
	for _, port := range ports {
		rdma := strings.TrimSpace(port.RdmaInterface)
		if rdma == "" {
			return fmt.Errorf("PCI function %q has no RDMA interface", port.PCI)
		}
		if _, found := started[rdma]; found {
			continue
		}
		if err := h.spectrumXConfigManager.RunDocaSpcXCC(port); err != nil {
			return err
		}
		started[rdma] = struct{}{}
	}
	return nil
}

func spectrumXRuntimeOperationBatches(
	ctx context.Context,
	device *v1alpha1.NicDevice,
	group spectrumx.OperationGroup,
) ([]spectrumXRuntimeOperationBatch, error) {
	if device == nil {
		return nil, fmt.Errorf("cannot select ports for doSPCX runtime group %q on a nil device", group.Name)
	}
	if len(group.Operations) == 0 {
		log.FromContext(ctx).V(2).Info("skipping doSPCX runtime configuration group",
			"device", device.Name, "group", group.Name, "reason", "group has no operations")
		return nil, nil
	}
	if len(device.Status.Ports) == 0 {
		return nil, fmt.Errorf("device %q has no ports for doSPCX runtime group %q", device.Name, group.Name)
	}

	batches := make([]spectrumXRuntimeOperationBatch, 0, len(group.Operations))
	for _, operation := range group.Operations {
		operationScope := strings.TrimSpace(operation.Scope)
		if operationScope == "" {
			operationScope = strings.TrimSpace(group.Scope)
		}
		targetClass := strings.TrimSpace(operation.TargetClass)
		if reason := spectrumXRuntimeOperationFilterReason(operationScope, targetClass); reason != "" {
			log.FromContext(ctx).V(2).Info("skipping doSPCX runtime operation",
				"device", device.Name, "group", group.Name, "path", operation.Path,
				"scope", operationScope, "targetClass", targetClass, "reason", reason)
			continue
		}
		scope, targetClass, err := spectrumXRuntimeOperationTarget(group.Scope, operation)
		if err != nil {
			return nil, fmt.Errorf("doSPCX runtime group %q operation %q: %w", group.Name, operation.Path, err)
		}
		if len(batches) == 0 ||
			batches[len(batches)-1].scope != scope ||
			batches[len(batches)-1].targetClass != targetClass {
			targets, err := resolveTargets(device, scope, targetClass)
			if err != nil {
				return nil, fmt.Errorf("doSPCX runtime group %q operation %q: %w", group.Name, operation.Path, err)
			}
			batches = append(batches, spectrumXRuntimeOperationBatch{
				scope: scope, targetClass: targetClass, targets: targets,
			})
		}
		batches[len(batches)-1].operations = append(batches[len(batches)-1].operations, operation)
	}
	if len(group.Operations) > 0 && len(batches) == 0 {
		log.FromContext(ctx).V(2).Info("skipping doSPCX runtime configuration group",
			"device", device.Name, "group", group.Name,
			"reason", "all operations were filtered by scope or target class")
	}
	return batches, nil
}

func spectrumXRuntimeOperationFilterReason(scope, targetClass string) string {
	reasons := make([]string, 0, 2)
	if scope == spectrumXRuntimeScopePerVF {
		reasons = append(reasons, "per-VF scope is managed outside NCO runtime execution")
	}
	if targetClass == spectrumXRuntimeTargetPerESwitch {
		reasons = append(reasons, "per-eSwitch target class is managed by boot configuration")
	}
	return strings.Join(reasons, "; ")
}

func spectrumXRuntimeOperationTarget(
	groupScope string,
	operation dmscli.XPathOperation,
) (string, string, error) {
	scope := strings.TrimSpace(operation.Scope)
	if scope == "" {
		scope = strings.TrimSpace(groupScope)
	}
	if scope == "" {
		scope = spectrumXRuntimeScopePerDevice
	}
	if scope != spectrumXRuntimeScopePerDevice && scope != spectrumXRuntimeScopePerRDMABond {
		return "", "", fmt.Errorf("unsupported scope %q", scope)
	}

	targetClass := strings.TrimSpace(operation.TargetClass)
	if targetClass == "" {
		if scope != spectrumXRuntimeScopePerDevice {
			return "", "", fmt.Errorf("target class is required for scope %q", scope)
		}
		targetClass = spectrumXRuntimeTargetPFNetdevAll
	}
	if targetClass != spectrumXRuntimeTargetPFNetdevAll && targetClass != spectrumXRuntimeTargetPFRDMAScope {
		return "", "", fmt.Errorf("unsupported target class %q", targetClass)
	}
	return scope, targetClass, nil
}

func resolveTargets(
	device *v1alpha1.NicDevice,
	scope string,
	targetGroup string,
) ([]spectrumXRuntimeTarget, error) {
	if device == nil {
		return nil, fmt.Errorf("cannot resolve doSPCX runtime targets for a nil device")
	}
	if len(device.Status.Ports) == 0 {
		return nil, fmt.Errorf("device %q has no ports", device.Name)
	}

	targets := make([]spectrumXRuntimeTarget, 0, len(device.Status.Ports))
	seenRDMABonds := make(map[string]struct{}, len(device.Status.Ports))
	for _, port := range device.Status.Ports {
		rdma := strings.TrimSpace(port.RdmaInterface)
		if scope == spectrumXRuntimeScopePerRDMABond || targetGroup == spectrumXRuntimeTargetPFRDMAScope {
			if rdma == "" {
				log.Log.V(2).Info("skipping doSPCX runtime target",
					"device", device.Name, "pci", port.PCI, "scope", scope, "targetGroup", targetGroup,
					"reason", "PCI function has no RDMA interface required by scope or target group")
				continue
			}
		}
		if scope == spectrumXRuntimeScopePerRDMABond {
			if _, found := seenRDMABonds[rdma]; found {
				log.Log.V(2).Info("skipping doSPCX runtime target",
					"device", device.Name, "pci", port.PCI, "scope", scope, "targetGroup", targetGroup,
					"rdma", rdma, "reason", "RDMA bond already has a representative target")
				continue
			}
			seenRDMABonds[rdma] = struct{}{}
		}
		targets = append(targets, spectrumXRuntimeTarget{name: "pci/" + port.PCI, port: port})
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf(
			"device %q has no targets for scope %q and target group %q", device.Name, scope, targetGroup)
	}
	return targets, nil
}

func spectrumXRuntimeBatchPorts(batches []spectrumXRuntimeOperationBatch) []v1alpha1.NicDevicePortSpec {
	ports := make([]v1alpha1.NicDevicePortSpec, 0)
	seen := make(map[string]struct{})
	for _, batch := range batches {
		for _, target := range batch.targets {
			if _, found := seen[target.port.PCI]; found {
				continue
			}
			seen[target.port.PCI] = struct{}{}
			ports = append(ports, target.port)
		}
	}
	return ports
}

// spectrumXRuntimeQueries reduces an authored sequence to its final desired state. Repeated
// writes remain ordered during apply, but validation compares the last value written to each leaf.
func spectrumXRuntimeQueries(
	operations []dmscli.XPathOperation,
) ([]dmscli.XPathQuery, map[string]map[string]any) {
	valuesByPath := make(map[string]map[string]any, len(operations))
	for _, operation := range operations {
		for leaf, value := range operation.Values {
			// PPCC slot 15 is write-only on the supported ConnectX-8 firmware: the
			// disable SET succeeds, but GET_ALGO_STATUS fails. This is an intentional
			// validation proxy: when the readable slot 0 values in the same CC group
			// match, consider the slot 15 disable successful. Querying this leaf fails,
			// while always reapplying it would make every reconciliation non-idempotent.
			if isSpectrumXRuntimeWriteOnlyPPCCSlot15Disable(operation.Path, leaf, value) {
				continue
			}
			values, found := valuesByPath[operation.Path]
			if !found {
				values = map[string]any{}
				valuesByPath[operation.Path] = values
			}
			values[leaf] = value
		}
	}

	paths := make([]string, 0, len(valuesByPath))
	for path := range valuesByPath {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	queries := make([]dmscli.XPathQuery, 0, len(paths))
	for _, path := range paths {
		leaves := make([]string, 0, len(valuesByPath[path]))
		for leaf := range valuesByPath[path] {
			leaves = append(leaves, leaf)
		}
		sort.Strings(leaves)
		queries = append(queries, dmscli.XPathQuery{Path: path, Leaves: leaves})
	}
	return queries, valuesByPath
}

func isSpectrumXRuntimeWriteOnlyPPCCSlot15Disable(path, leaf string, value any) bool {
	enabled, ok := value.(bool)
	return path == spectrumXRuntimeWriteOnlyPPCCSlot15 && leaf == "enabled" && ok && !enabled
}

func (h configurationManager) validateSpectrumXRuntimeTarget(
	ctx context.Context,
	target string,
	queries []dmscli.XPathQuery,
	desiredValues map[string]map[string]any,
) (bool, error) {
	for _, batch := range spectrumXRuntimeQueryBatches(queries) {
		result, err := dmscli.QueryXPaths(ctx, h.execInterface, target, batch)
		if err != nil {
			return false, err
		}
		for _, query := range batch {
			actualValues, found := result.Values[query.Path]
			if !found {
				return false, fmt.Errorf("DMS response does not contain XPath %q", query.Path)
			}
			for _, leaf := range query.Leaves {
				actual, found := actualValues[leaf]
				if !found {
					return false, fmt.Errorf("DMS response does not contain XPath leaf %q/%s", query.Path, leaf)
				}
				desired := desiredValues[query.Path][leaf]
				if !dmscli.XPathValuesEqual(actual, desired) {
					log.FromContext(ctx).V(2).Info("doSPCX runtime value differs",
						"target", target, "path", query.Path, "leaf", leaf, "current", actual, "desired", desired)
					return false, nil
				}
			}
		}
	}
	return true, nil
}

// TODO(dospcx-runtime): remove per-index queries once dms-cli preserves indexed XPath keys in
// batched JSON GET responses. Today indexed paths can collapse to the same response key.
func spectrumXRuntimeQueryBatches(queries []dmscli.XPathQuery) [][]dmscli.XPathQuery {
	batched := make([]dmscli.XPathQuery, 0, len(queries))
	individual := make([][]dmscli.XPathQuery, 0)
	for _, query := range queries {
		if strings.Contains(query.Path, "[") {
			individual = append(individual, []dmscli.XPathQuery{query})
			continue
		}
		batched = append(batched, query)
	}
	if len(batched) == 0 {
		return individual
	}
	return append([][]dmscli.XPathQuery{batched}, individual...)
}
