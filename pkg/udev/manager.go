/*
2026 NVIDIA CORPORATION & AFFILIATES
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

package udev

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Mellanox/dms-lib/pkg/naming"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
)

const (
	NicIDPlaceholder   = "%nic_id%"
	PlaneIDPlaceholder = "%plane_id%"
	RailIDPlaceholder  = "%rail_id%"
)

// ExpectedInterfaceNames holds the names requested by NCO's template policy.
type ExpectedInterfaceNames struct{ NetDevice, RdmaDevice string }

// UdevManager translates NCO naming intent and delegates host operations to dms-lib.
type UdevManager interface {
	ApplyUdevRules(context.Context, []*v1alpha1.NicDevice) (map[string]ExpectedInterfaceNames, bool, error)
}

type udevManager struct {
	hostPath string
	apply    func(context.Context, []naming.Assignment, naming.Options) (bool, error)
}

func NewUdevManager() UdevManager {
	return &udevManager{hostPath: consts.HostPath, apply: naming.Apply}
}

func (m *udevManager) ApplyUdevRules(ctx context.Context, devices []*v1alpha1.NicDevice) (map[string]ExpectedInterfaceNames, bool, error) {
	assignments, expected, err := namingAssignments(devices)
	if err != nil {
		return nil, false, err
	}
	changed, err := m.apply(ctx, assignments, naming.Options{HostRoot: m.hostPath})
	return expected, changed, err
}

func ValidateInterfaceNames(devices []*v1alpha1.NicDevice) error {
	_, _, err := namingAssignments(devices)
	return err
}

func namingAssignments(devices []*v1alpha1.NicDevice) ([]naming.Assignment, map[string]ExpectedInterfaceNames, error) {
	assignments := []naming.Assignment{}
	expected := map[string]ExpectedInterfaceNames{}
	owners := map[string]string{}
	for _, device := range devices {
		if device == nil {
			return nil, nil, fmt.Errorf("nil device in interface naming request")
		}
		spec := device.Spec.InterfaceNameTemplate
		if spec == nil {
			continue
		}
		key := deviceObjectKey(device)
		if spec.NetDevicePrefix == "" && spec.RdmaDevicePrefix == "" {
			return nil, nil, fmt.Errorf("device %s has empty net and RDMA device prefixes", key)
		}
		if len(device.Status.Ports) == 0 || device.Status.Ports[0].PCI == "" {
			continue
		}
		var firstRDMA string
		// Preserve NCO's existing future-PF policy. This is naming intent, not discovery.
		for index, plane := range spec.PlaneIndices {
			var pci string
			if index < len(device.Status.Ports) && device.Status.Ports[index].PCI != "" {
				pci = device.Status.Ports[index].PCI
			} else {
				var err error
				pci, err = CalculatePCIAddressForPF(device.Status.Ports[0].PCI, index)
				if err != nil {
					return nil, nil, fmt.Errorf("calculate PCI address for device %s PF %d: %w", key, index, err)
				}
			}
			if owner, found := owners[pci]; found {
				return nil, nil, fmt.Errorf("PCI address %s is assigned by devices %s and %s", pci, owner, key)
			}
			owners[pci] = key
			names := ExpectedInterfaceNames{
				NetDevice:  substituteTemplatePlaceholders(spec.NetDevicePrefix, spec.NicIndex, plane, spec.RailIndex),
				RdmaDevice: substituteTemplatePlaceholders(spec.RdmaDevicePrefix, spec.NicIndex, plane, spec.RailIndex),
			}
			if index == 0 {
				firstRDMA = names.RdmaDevice
			} else if names.RdmaDevice == firstRDMA {
				names.RdmaDevice = ""
			}
			expected[pci] = names
			// A shared RDMA owner with no net name leaves this sibling unmanaged.
			if names.NetDevice != "" || names.RdmaDevice != "" {
				assignments = append(assignments, naming.Assignment{PCI: pci, Netdev: names.NetDevice, RDMA: names.RdmaDevice})
			}
		}
	}
	if err := naming.Validate(assignments); err != nil {
		return nil, nil, err
	}
	return assignments, expected, nil
}

func deviceObjectKey(device *v1alpha1.NicDevice) string {
	if device.Namespace == "" {
		return device.Name
	}
	return device.Namespace + "/" + device.Name
}

// CalculatePCIAddressForPF calculates the PCI address for a given PF index based on a base PCI address.
// PCI addresses are in format DDDD:BB:DD.F (domain:bus:device.function).
// For multi-function NICs, the function number increments for each PF.
func CalculatePCIAddressForPF(basePCI string, pfIndex int) (string, error) {
	// Parse the base PCI address: DDDD:BB:DD.F
	var domain, bus, device, function int
	n, err := fmt.Sscanf(basePCI, "%x:%x:%x.%x", &domain, &bus, &device, &function)
	if err != nil || n != 4 {
		return "", fmt.Errorf("failed to parse PCI address %s: %w", basePCI, err)
	}

	// Calculate the new function number
	// The function number for pfIndex N is baseFunction + N
	newFunction := function + pfIndex

	// Function number is 3 bits, so max is 7
	if newFunction > 7 {
		return "", fmt.Errorf("PCI function number %d exceeds maximum (7) for base %s with pfIndex %d", newFunction, basePCI, pfIndex)
	}

	// Format the new PCI address
	return fmt.Sprintf("%04x:%02x:%02x.%x", domain, bus, device, newFunction), nil
}

// substituteTemplatePlaceholders replaces placeholder tokens with actual values
func substituteTemplatePlaceholders(template string, nicIndex, planeIndex, railIndex int) string {
	if template == "" {
		return ""
	}

	result := template
	result = strings.ReplaceAll(result, NicIDPlaceholder, strconv.Itoa(nicIndex))
	result = strings.ReplaceAll(result, PlaneIDPlaceholder, strconv.Itoa(planeIndex))
	result = strings.ReplaceAll(result, RailIDPlaceholder, strconv.Itoa(railIndex))

	return result
}
