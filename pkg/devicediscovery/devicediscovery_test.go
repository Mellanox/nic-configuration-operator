// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package devicediscovery

import (
	"context"
	"errors"
	"os"

	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Library discovery adapter", func() {
	var inventory device.Inventory
	var discoveryErr error
	var adapter *deviceDiscovery
	BeforeEach(func() {
		inventory = device.Inventory{Complete: true, Devices: []device.Device{
			{ID: "opaque-a", Type: "1023", SerialNumber: "shared", PartNumber: "pn", PSID: "psid", FirmwareVersion: "1.2.3", ModelName: "CX8", SuperNIC: true, Ports: []device.Port{
				{PCI: "0000:64:00.0", Fwctl: "/dev/fwctl0", Netdev: "eth0", RDMA: "mlx5_0"},
				{PCI: "0000:64:00.1", Fwctl: "/dev/fwctl1", Netdev: "eth1", RDMA: "mlx5_1"},
			}},
			{ID: "opaque-b", Type: "1023", SerialNumber: "shared", DPU: true, Ports: []device.Port{{PCI: "0001:64:00.0"}}},
		}}
		discoveryErr = nil
		adapter = &deviceDiscovery{nodeName: "worker", discover: func(context.Context) (device.Inventory, error) { return inventory, discoveryErr }}
		previous, present := os.LookupEnv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR)
		Expect(os.Unsetenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR)).To(Succeed())
		DeferCleanup(func() {
			if present {
				Expect(os.Setenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR, previous)).To(Succeed())
			} else {
				Expect(os.Unsetenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR)).To(Succeed())
			}
		})
	})

	It("projects metadata and PF endpoints while retaining the physical PCI CR key", func() {
		observed, err := adapter.DiscoverNicDevices()
		Expect(err).NotTo(HaveOccurred())
		Expect(observed).To(HaveLen(2))
		s := observed["0000:64:00"].Status
		Expect(s.Node).To(Equal("worker"))
		Expect(s.Type).To(Equal("1023"))
		Expect(s.SerialNumber).To(Equal("shared"))
		Expect(s.PartNumber).To(Equal("pn"))
		Expect(s.PSID).To(Equal("psid"))
		Expect(s.FirmwareVersion).To(Equal("1.2.3"))
		Expect(s.ModelName).To(Equal("CX8"))
		Expect(s.SuperNIC).To(BeTrue())
		Expect(s.Ports).To(HaveLen(2))
		Expect(s.Ports[0].PCI).To(Equal("0000:64:00.0"))
		Expect(s.Ports[0].FwctlDevice).To(Equal("/dev/fwctl0"))
		Expect(s.Ports[0].NetworkInterface).To(Equal("eth0"))
		Expect(s.Ports[0].RdmaInterface).To(Equal("mlx5_0"))
		Expect(observed["0001:64:00"].Status.DPU).To(BeTrue())
	})
	It("uses the library's explicit peer identity instead of serial grouping", func() {
		inventory.Devices[0].NetworkBay = &device.NetworkBayInfo{ASIC: 0, PeerID: "opaque-b"}
		inventory.Devices[1].NetworkBay = &device.NetworkBayInfo{ASIC: 1, PeerID: "opaque-a"}
		observed, err := adapter.DiscoverNicDevices()
		Expect(err).NotTo(HaveOccurred())
		Expect(observed["0000:64:00"].Status.NetworkBay.PeerPCI).To(Equal("0001:64:00.0"))
		Expect(observed["0001:64:00"].Status.NetworkBay.Asic).To(Equal(1))
	})
	It("retains a device whose optional VPD metadata is unavailable", func() {
		inventory.Devices[0].SerialNumber = ""
		inventory.Devices[0].PartNumber = ""
		observed, err := adapter.DiscoverNicDevices()
		Expect(err).NotTo(HaveOccurred())
		Expect(observed).To(HaveLen(2))
	})
	It("fails the scan on required discovery errors unless skipping is enabled", func() {
		inventory.Issues = []device.DiscoveryIssue{{PCI: "0000:64:00.0", Err: errors.New("firmware query failed")}}
		_, err := adapter.DiscoverNicDevices()
		Expect(err).To(MatchError("firmware query failed"))
	})
	It("returns healthy devices and reports skipped ASICs for CR retention", func() {
		Expect(os.Setenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR, consts.LabelValueTrue)).To(Succeed())
		inventory.Issues = []device.DiscoveryIssue{{DeviceID: "opaque-a", PCI: "0000:64:00.1", Err: errors.New("firmware query failed")}}
		observed, err := adapter.DiscoverNicDevices()
		Expect(err).NotTo(HaveOccurred())
		Expect(observed).To(HaveLen(1))
		Expect(observed).To(HaveKey("0001:64:00"))
		Expect(adapter.IncompleteDevices()).To(HaveKey("0000:64:00"))
		delete(adapter.SkippedDevices(), "0000:64:00")
		Expect(adapter.SkippedDevices()).To(HaveKey("0000:64:00"))
		inventory.Issues = nil
		_, err = adapter.DiscoverNicDevices()
		Expect(err).NotTo(HaveOccurred())
		Expect(adapter.IncompleteDevices()).To(BeEmpty())
	})
	It("retains healthy ASICs when an Orchid has an unresolved peer issue", func() {
		Expect(os.Setenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR, consts.LabelValueTrue)).To(Succeed())
		inventory.Devices[0].NetworkBay = &device.NetworkBayInfo{ASIC: 0, PeerID: ""}
		inventory.Issues = []device.DiscoveryIssue{{DeviceID: "opaque-a", Err: errors.New("Orchid identity unavailable")}}
		observed, err := adapter.DiscoverNicDevices()
		Expect(err).NotTo(HaveOccurred())
		Expect(observed).To(HaveLen(1))
		Expect(observed).To(HaveKey("0001:64:00"))
		Expect(adapter.IncompleteDevices()).To(HaveKey("0000:64:00"))
	})
	DescribeTable("does not turn global failures into disappearance", func(kind string) {
		Expect(os.Setenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR, consts.LabelValueTrue)).To(Succeed())
		switch kind {
		case "incomplete":
			inventory.Complete = false
		case "error":
			discoveryErr = errors.New("enumeration failed")
		case "unscoped":
			inventory.Issues = []device.DiscoveryIssue{{Err: errors.New("unknown device")}}
		case "missing ports":
			inventory.Devices[0].Ports = nil
		case "missing peer":
			inventory.Devices[0].NetworkBay = &device.NetworkBayInfo{PeerID: "absent"}
		}
		observed, err := adapter.DiscoverNicDevices()
		Expect(err).To(HaveOccurred())
		Expect(observed).To(BeNil())
	}, Entry("incomplete", "incomplete"), Entry("error", "error"), Entry("unscoped", "unscoped"), Entry("missing ports", "missing ports"), Entry("missing peer", "missing peer"))
})
