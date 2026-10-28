// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DiscoveredNicDevices converts one library inventory to NCO device records for
// the legacy firmware DMS server. Required discovery failures retain the existing
// fail-whole-scan policy unless SKIP_DEVICE_ON_DISCOVERY_ERROR is enabled.
func DiscoveredNicDevices(inventory device.Inventory, node string) ([]v1alpha1.NicDevice, error) {
	observed, _, err := inventoryDevices(inventory, node, nil)
	if err != nil {
		return nil, err
	}
	devices := make([]v1alpha1.NicDevice, 0, len(observed))
	for _, d := range observed {
		devices = append(devices, d)
	}
	return devices, nil
}

var inventoryPCI = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

// inventoryDevices is a Kubernetes projection of a single observation, not a
// discovery cache. Issues are correlated before any retained record is replaced.
func inventoryDevices(inventory device.Inventory, node string, discoveryErr error) (map[string]v1alpha1.NicDevice, map[string]error, error) {
	observed := make(map[string]v1alpha1.NicDevice)
	issues := make(map[string]error)
	byID := make(map[string]string)
	var global []error
	if discoveryErr != nil {
		global = append(global, discoveryErr)
	}
	if !inventory.Complete {
		global = append(global, errors.New("device inventory is incomplete"))
	}
	for _, d := range inventory.Devices {
		key, err := inventoryDeviceKey(d)
		if err != nil {
			global = append(global, err)
			continue
		}
		if _, exists := byID[d.ID]; exists {
			global = append(global, fmt.Errorf("duplicate device identity %q", d.ID))
			continue
		}

		if _, exists := observed[key]; exists {
			global = append(global, fmt.Errorf("duplicate ASIC endpoint %s", key))
			continue
		}
		byID[d.ID] = key
		status := v1alpha1.NicDeviceStatus{
			Node: node, Type: d.Type,
			SerialNumber: d.SerialNumber, PartNumber: d.PartNumber,
			PSID: d.PSID, FirmwareVersion: d.FirmwareVersion,
			ModelName: d.ModelName, DPU: d.DPU, SuperNIC: d.SuperNIC,
		}
		for _, p := range d.Ports {
			status.Ports = append(status.Ports, v1alpha1.NicDevicePortSpec{PCI: p.PCI, FwctlDevice: p.Fwctl, NetworkInterface: p.Netdev, RdmaInterface: p.RDMA})
		}
		observed[key] = v1alpha1.NicDevice{Status: status}
	}
	for _, d := range inventory.Devices {
		key, exists := byID[d.ID]
		if !exists || d.NetworkBay == nil {
			continue
		}
		peerKey, found := byID[d.NetworkBay.PeerID]
		if !found || peerKey == key {
			issues[key] = fmt.Errorf("device %q has no usable Orchid peer", d.ID)
			continue
		}
		peer := observed[peerKey]
		value := observed[key]
		value.Status.NetworkBay = &v1alpha1.NicDeviceNetworkBayStatus{Asic: d.NetworkBay.ASIC, PeerPCI: peer.Status.Ports[0].PCI}
		observed[key] = value
	}
	for _, issue := range inventory.Issues {
		problem := issue.Err
		if problem == nil {
			problem = errors.New("device discovery failed")
		}
		key := byID[issue.DeviceID]
		if issue.PCI != "" {
			pci := strings.ToLower(issue.PCI)
			if !inventoryPCI.MatchString(pci) {
				global = append(global, fmt.Errorf("malformed discovery issue PCI %q: %w", issue.PCI, problem))
				continue
			}
			pciKey := utils.PCIDeviceAddress(pci)
			if key != "" && key != pciKey {
				global = append(global, fmt.Errorf("conflicting discovery issue identity %q and PCI %s: %w", issue.DeviceID, pci, problem))
				continue
			}
			key = pciKey
		}
		if key == "" {
			global = append(global, problem)
		} else {
			issues[key] = errors.Join(issues[key], problem)
		}
	}
	if os.Getenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR) != consts.LabelValueTrue {
		for _, key := range slices.Sorted(maps.Keys(issues)) {
			global = append(global, issues[key])
		}
	}
	for key := range issues {
		delete(observed, key)
	}
	return observed, issues, errors.Join(global...)
}

func setDiscoveryCondition(d *v1alpha1.NicDevice, err error) bool {
	condition := metav1.Condition{Type: consts.DiscoveryReadyCondition, Status: metav1.ConditionTrue, Reason: "Discovered", Message: "Device observed in the latest discovery pass", ObservedGeneration: d.Generation}
	if err != nil {
		condition.Status = metav1.ConditionFalse
		condition.Reason = "DiscoveryFailed"
		condition.Message = err.Error()
	}
	return meta.SetStatusCondition(&d.Status.Conditions, condition)
}

// sameDiscoveredDevice requires current identity and the complete observed PF set. A retained subset must
// not authorize configuration after breakout or a reset changed the topology.
func sameDiscoveredDevice(a, b v1alpha1.NicDeviceStatus) bool {
	endpoints := func(s v1alpha1.NicDeviceStatus) map[string]string {
		m := make(map[string]string, len(s.Ports))
		for _, p := range s.Ports {
			m[p.PCI] = p.FwctlDevice
		}
		return m
	}
	return len(a.Ports) == len(b.Ports) && reflect.DeepEqual(endpoints(a), endpoints(b)) &&
		a.SerialNumber == b.SerialNumber && a.PartNumber == b.PartNumber &&
		a.ModelName == b.ModelName && a.SuperNIC == b.SuperNIC && a.Type == b.Type &&
		a.PSID == b.PSID && a.FirmwareVersion == b.FirmwareVersion && a.DPU == b.DPU &&
		reflect.DeepEqual(a.NetworkBay, b.NetworkBay)
}

func inventoryDeviceKey(d device.Device) (string, error) {
	if d.ID == "" || len(d.Ports) == 0 {
		return "", fmt.Errorf("device %q has no identity or PF endpoint", d.ID)
	}
	key := ""
	seen := make(map[string]bool, len(d.Ports))
	for _, port := range d.Ports {
		if !inventoryPCI.MatchString(port.PCI) || seen[port.PCI] {
			return "", fmt.Errorf("device %q has invalid or duplicate PF endpoint %q", d.ID, port.PCI)
		}
		current := utils.PCIDeviceAddress(port.PCI)
		if key != "" && current != key {
			return "", fmt.Errorf("device %q spans different ASIC endpoints", d.ID)
		}
		key = current
		seen[port.PCI] = true
	}
	return key, nil
}
