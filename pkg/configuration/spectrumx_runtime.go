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
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/spectrumx"
)

const spectrumXRuntimeGroupCC = "cc"

func (h configurationManager) validateSpectrumXRuntimeConfig(
	ctx context.Context,
	device *v1alpha1.NicDevice,
	plan *spectrumx.Plan,
) (bool, error) {
	if plan == nil {
		return false, fmt.Errorf("cannot validate a nil doSPCX runtime plan")
	}
	for _, group := range plan.RuntimeConfig {
		ports, err := spectrumXRuntimeGroupPorts(device, group.Name)
		if err != nil {
			return false, err
		}
		if group.Name == spectrumXRuntimeGroupCC {
			if err := h.startSpectrumXCC(ports); err != nil {
				return false, fmt.Errorf("start DOCA SPC-X CC for device %q: %w", device.Name, err)
			}
		}

		queries, desiredValues := spectrumXRuntimeQueries(group.Operations)
		if len(queries) == 0 {
			continue
		}
		log.FromContext(ctx).V(2).Info("validating doSPCX runtime configuration group",
			"device", device.Name, "group", group.Name, "operations", len(group.Operations), "targets", len(ports))
		for _, port := range ports {
			matches, err := h.validateSpectrumXRuntimeTarget(
				ctx, spectrumXRuntimeTarget(port), queries, desiredValues)
			if err != nil {
				return false, fmt.Errorf(
					"validate doSPCX runtime group %q for device %q on PCI function %q: %w",
					group.Name, device.Name, port.PCI, err)
			}
			if !matches {
				return false, nil
			}
		}
	}
	return true, nil
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
		if len(group.Operations) == 0 {
			continue
		}
		ports, err := spectrumXRuntimeGroupPorts(device, group.Name)
		if err != nil {
			return err
		}
		if group.Name == spectrumXRuntimeGroupCC {
			if err := h.startSpectrumXCC(ports); err != nil {
				return fmt.Errorf("start DOCA SPC-X CC for device %q: %w", device.Name, err)
			}
		}

		log.FromContext(ctx).Info("applying doSPCX runtime configuration group",
			"device", device.Name, "group", group.Name, "operations", len(group.Operations), "targets", len(ports))
		for _, port := range ports {
			target := spectrumXRuntimeTarget(port)
			if _, err := dmscli.SetXPaths(ctx, h.execInterface, target, group.Operations); err != nil {
				return fmt.Errorf(
					"apply doSPCX runtime group %q for device %q on target %q: %w",
					group.Name, device.Name, target, err)
			}
		}
	}
	return nil
}

func (h configurationManager) startSpectrumXCC(ports []v1alpha1.NicDevicePortSpec) error {
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

func spectrumXRuntimeGroupPorts(
	device *v1alpha1.NicDevice,
	groupName string,
) ([]v1alpha1.NicDevicePortSpec, error) {
	if device == nil {
		return nil, fmt.Errorf("cannot select ports for doSPCX runtime group %q on a nil device", groupName)
	}
	if len(device.Status.Ports) == 0 {
		return nil, fmt.Errorf("device %q has no ports for doSPCX runtime group %q", device.Name, groupName)
	}
	if groupName == spectrumXRuntimeGroupCC &&
		device.Spec.Configuration != nil &&
		device.Spec.Configuration.Template != nil &&
		device.Spec.Configuration.Template.SpectrumXOptimized != nil &&
		device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode == consts.MultiplaneModeHwplb {
		return device.Status.Ports[:1], nil
	}
	return device.Status.Ports, nil
}

func spectrumXRuntimeTarget(port v1alpha1.NicDevicePortSpec) string {
	return "pci/" + port.PCI
}

// spectrumXRuntimeQueries reduces an authored sequence to its final desired state. Repeated
// writes remain ordered during apply, but validation compares the last value written to each leaf.
func spectrumXRuntimeQueries(
	operations []dmscli.XPathOperation,
) ([]dmscli.XPathQuery, map[string]map[string]any) {
	valuesByPath := make(map[string]map[string]any, len(operations))
	for _, operation := range operations {
		values, found := valuesByPath[operation.Path]
		if !found {
			values = map[string]any{}
			valuesByPath[operation.Path] = values
		}
		for leaf, value := range operation.Values {
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
