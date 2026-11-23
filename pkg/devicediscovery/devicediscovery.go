// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package devicediscovery

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"

	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

type DeviceDiscovery interface {
	// DiscoverNicDevices returns NCO records keyed by PCI domain:bus:device.
	DiscoverNicDevices() (map[string]v1alpha1.NicDevice, error)
}

type SkippedDeviceReporter interface {
	SkippedDevices() map[string]error
}

type IncompleteDeviceReporter interface {
	IncompleteDevices() map[string]error
}

type deviceDiscovery struct {
	nodeName          string
	discover          func(context.Context) (device.Inventory, error)
	incompleteDevices map[string]error
}

// DiscoverNicDevices adapts library discovery to the existing NCO record and
// partial-discovery contracts. Hardware discovery and ASIC pairing belong to dms-lib.
func (d *deviceDiscovery) DiscoverNicDevices() (map[string]v1alpha1.NicDevice, error) {
	d.incompleteDevices = make(map[string]error)
	inventory, err := d.discover(context.Background())
	if err != nil {
		return nil, err
	}
	if !inventory.Complete {
		return nil, errors.New("device inventory is incomplete")
	}

	devices := make(map[string]v1alpha1.NicDevice, len(inventory.Devices))
	firstPFByID := make(map[string]string, len(inventory.Devices))
	for _, discovered := range inventory.Devices {
		if len(discovered.Ports) == 0 {
			return nil, fmt.Errorf("discovered device %q has no PF endpoint", discovered.ID)
		}
		key := utils.PCIDeviceAddress(discovered.Ports[0].PCI)
		firstPFByID[discovered.ID] = discovered.Ports[0].PCI
		status := v1alpha1.NicDeviceStatus{
			Node: d.nodeName, Type: discovered.Type,
			SerialNumber: discovered.SerialNumber, PartNumber: discovered.PartNumber,
			PSID: discovered.PSID, FirmwareVersion: discovered.FirmwareVersion,
			ModelName: discovered.ModelName, DPU: discovered.DPU, SuperNIC: discovered.SuperNIC,
		}
		for _, port := range discovered.Ports {
			status.Ports = append(status.Ports, v1alpha1.NicDevicePortSpec{
				PCI: port.PCI, FwctlDevice: port.Fwctl,
				NetworkInterface: port.Netdev, RdmaInterface: port.RDMA,
			})
		}
		devices[key] = v1alpha1.NicDevice{Status: status}
	}
	for _, issue := range inventory.Issues {
		problem := issue.Err
		if problem == nil {
			problem = errors.New("device discovery failed")
		}
		pci := firstPFByID[issue.DeviceID]
		if issue.PCI != "" {
			pci = issue.PCI
		}
		key := ""
		if pci != "" {
			key = utils.PCIDeviceAddress(pci)
		}
		if key == "" || os.Getenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR) != consts.LabelValueTrue {
			return nil, problem
		}
		d.incompleteDevices[key] = errors.Join(d.incompleteDevices[key], problem)
		delete(devices, key)
	}
	for _, discovered := range inventory.Devices {
		key := utils.PCIDeviceAddress(discovered.Ports[0].PCI)
		value, retained := devices[key]
		if !retained || discovered.NetworkBay == nil {
			continue
		}
		peerPCI, found := firstPFByID[discovered.NetworkBay.PeerID]
		if !found || utils.PCIDeviceAddress(peerPCI) == key {
			return nil, fmt.Errorf("discovered device %q has no distinct Orchid peer", discovered.ID)
		}
		value.Status.NetworkBay = &v1alpha1.NicDeviceNetworkBayStatus{
			Asic: discovered.NetworkBay.ASIC, PeerPCI: peerPCI,
		}
		devices[key] = value
	}
	return devices, nil
}

func (d *deviceDiscovery) SkippedDevices() map[string]error {
	return maps.Clone(d.incompleteDevices)
}

func (d *deviceDiscovery) IncompleteDevices() map[string]error {
	return maps.Clone(d.incompleteDevices)
}

// NewDeviceDiscovery creates the NCO adapter. Native discovery dependencies are
// configured by dms-lib; the adapter only needs the Kubernetes node name.
func NewDeviceDiscovery(nodeName string) DeviceDiscovery {
	return &deviceDiscovery{
		nodeName: nodeName,
		discover: func(ctx context.Context) (device.Inventory, error) {
			return device.Discover(ctx, device.Options{})
		},
	}
}
