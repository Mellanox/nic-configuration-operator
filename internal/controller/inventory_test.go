// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"errors"

	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// inventoryFixture gives the existing controller lifecycle fixtures an explicit
// hardware observation. Freshness failure tests provide separate observations.
func inventoryFixture(records map[string]v1alpha1.NicDevice) device.Inventory {
	inventory := device.Inventory{Complete: true}
	for id, record := range records {
		s := record.Status
		if len(s.Ports) == 0 {
			continue
		}
		d := device.Device{ID: id, Type: s.Type, SerialNumber: s.SerialNumber, PartNumber: s.PartNumber, PSID: s.PSID, FirmwareVersion: s.FirmwareVersion, ModelName: s.ModelName, DPU: s.DPU, SuperNIC: s.SuperNIC}
		for _, p := range s.Ports {
			d.Ports = append(d.Ports, device.Port{PCI: p.PCI, Fwctl: p.FwctlDevice, Netdev: p.NetworkInterface, RDMA: p.RdmaInterface})
		}
		if s.NetworkBay != nil {
			d.NetworkBay = &device.NetworkBayInfo{ASIC: s.NetworkBay.Asic}
			for peerID, peer := range records {
				if len(peer.Status.Ports) > 0 && peer.Status.Ports[0].PCI == s.NetworkBay.PeerPCI {
					d.NetworkBay.PeerID = peerID
				}
			}
		}
		inventory.Devices = append(inventory.Devices, d)
	}
	return inventory
}

var _ = Describe("Inventory projection", func() {
	fixture := func() device.Inventory {
		return device.Inventory{Complete: true, Devices: []device.Device{
			{ID: "opaque-a", Type: "1023", Ports: []device.Port{{PCI: "0000:64:00.0"}}},
			{ID: "opaque-b", Type: "1023", Ports: []device.Port{{PCI: "0001:64:00.0"}}},
		}}
	}
	It("keeps opaque host identity separate from CR PCI identity and serial metadata", func() {
		inventory := fixture()
		inventory.Devices[0].SerialNumber = "shared"
		inventory.Devices[1].SerialNumber = "shared"
		observed, issues, err := inventoryDevices(inventory, nodeName, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(issues).To(BeEmpty())
		Expect(observed).To(HaveLen(2))
		Expect(observed).To(HaveKey("0000:64:00"))
		Expect(observed).To(HaveKey("0001:64:00"))
	})
	DescribeTable("fails globally on contradictory or uncorrelated identity", func(change func(*device.Inventory)) {
		inventory := fixture()
		change(&inventory)
		_, _, err := inventoryDevices(inventory, nodeName, nil)
		Expect(err).To(HaveOccurred())
	},
		Entry("conflicting issue identifiers", func(i *device.Inventory) {
			i.Issues = []device.DiscoveryIssue{{DeviceID: "opaque-a", PCI: "0001:64:00.0", Err: errors.New("read")}}
		}),
		Entry("malformed issue PCI", func(i *device.Inventory) {
			i.Issues = []device.DiscoveryIssue{{PCI: "invalid", Err: errors.New("read")}}
		}),
		Entry("unknown opaque issue identity", func(i *device.Inventory) {
			i.Issues = []device.DiscoveryIssue{{DeviceID: "unobserved-opaque", Err: errors.New("read")}}
		}),
		Entry("duplicate opaque IDs", func(i *device.Inventory) { i.Devices[1].ID = i.Devices[0].ID }),
		Entry("cross-ASIC PF grouping", func(i *device.Inventory) {
			i.Devices[0].Ports = append(i.Devices[0].Ports, device.Port{PCI: "0001:64:00.1"})
		}),
		Entry("duplicate PF endpoint", func(i *device.Inventory) { i.Devices[0].Ports = append(i.Devices[0].Ports, i.Devices[0].Ports[0]) }),
		Entry("malformed observed PCI", func(i *device.Inventory) { i.Devices[0].Ports[0].PCI = "bad" }),
	)
	It("does not invent an Orchid peer from duplicate serials", func() {
		inventory := fixture()
		inventory.Devices[0].NetworkBay = &device.NetworkBayInfo{ASIC: 0, PeerID: "missing"}
		_, issues, _ := inventoryDevices(inventory, nodeName, nil)
		Expect(issues).To(HaveKey("0000:64:00"))
	})
})
