// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"context"
	"errors"

	dmsconfiguration "github.com/Mellanox/dms-lib/pkg/configuration"
	"github.com/Mellanox/dms-lib/pkg/device"
	dmsfirmware "github.com/Mellanox/dms-lib/pkg/firmware"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/configuration/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type runtimeApplyFunc func(context.Context, dmsconfiguration.RuntimeRequest) (dmsconfiguration.Result, error)

func (f runtimeApplyFunc) ApplyRuntimeConfiguration(ctx context.Context, request dmsconfiguration.RuntimeRequest) (dmsconfiguration.Result, error) {
	return f(ctx, request)
}

type resetFunc func(context.Context, device.Device, dmsfirmware.ResetOptions) error

func (f resetFunc) Reset(ctx context.Context, observed device.Device, options dmsfirmware.ResetOptions) error {
	return f(ctx, observed, options)
}

var _ = Describe("library runtime delegation", func() {
	var (
		manager         configurationManager
		nic             *v1alpha1.NicDevice
		inventory       device.Inventory
		linkUtils       *mocks.ConfigurationUtils
		calls           int
		observedRequest dmsconfiguration.RuntimeRequest
		row             dmsconfiguration.DeviceResult
	)
	BeforeEach(func() {
		nic = &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "asic"}, Spec: v1alpha1.NicDeviceSpec{Configuration: &v1alpha1.NicDeviceConfigurationSpec{Template: &v1alpha1.ConfigurationTemplateSpec{LinkType: "Ethernet", PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{Enabled: true}}}}, Status: v1alpha1.NicDeviceStatus{Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:03:00.0", NetworkInterface: "eth0"}}}}
		inventory = device.Inventory{Complete: true, Devices: []device.Device{{ID: "observed", Ports: []device.Port{{PCI: "0000:03:00.0", Netdev: "eth0", RDMA: "mlx5_actual"}}}}}
		linkUtils = &mocks.ConfigurationUtils{}
		linkUtils.On("HasNoCarrier", "eth0").Return(false, nil)
		calls = 0
		observedRequest = dmsconfiguration.RuntimeRequest{}
		row = dmsconfiguration.DeviceResult{DeviceID: "observed", Status: dmsconfiguration.Unchanged}
		manager = configurationManager{configurationUtils: linkUtils, discover: func(context.Context) (device.Inventory, error) { return inventory, nil }, runtime: runtimeApplyFunc(func(_ context.Context, request dmsconfiguration.RuntimeRequest) (dmsconfiguration.Result, error) {
			calls++
			observedRequest = request
			return dmsconfiguration.Result{Devices: []dmsconfiguration.DeviceResult{row}}, nil
		})}
	})
	It("uses real discovered endpoint identity and preserves caller PF selection", func() {
		inventory.Devices[0].Ports = append(inventory.Devices[0].Ports, device.Port{PCI: "0000:03:00.1", Netdev: "eth1", RDMA: "mlx5_other"})
		result, err := manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
		Expect(observedRequest.Devices).To(HaveLen(1))
		Expect(observedRequest.Devices[0].Ports).To(HaveLen(1))
		Expect(observedRequest.Devices[0].Ports[0].RDMA).To(Equal("mlx5_actual"))
		Expect(observedRequest.Ports).To(HaveLen(1))
		Expect(*observedRequest.Ports[0].MaxReadRequestBytes).To(Equal(uint32(4096)))
	})
	It("keeps nil-template reset requests as no-op without discovery", func() {
		nic.Spec.Configuration.ResetToDefault = true
		nic.Spec.Configuration.Template = nil
		manager.discover = nil
		result, err := manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
		Expect(calls).To(BeZero())
	})
	It("still applies a supplied runtime template during reset-to-default", func() {
		nic.Spec.Configuration.ResetToDefault = true
		_, err := manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal(1))
	})
	It("skips unbound per-port extras while preserving PCI intent", func() {
		nic.Status.Ports[0].NetworkInterface = ""
		inventory.Devices[0].Ports[0].Netdev = ""
		inventory.Devices[0].Ports[0].RDMA = ""
		nic.Spec.Configuration.Template.RuntimePerformanceOptimized = &v1alpha1.RuntimePerformanceOptimizedSpec{Enabled: true, RxRingSize: 1024}
		_, err := manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(err).NotTo(HaveOccurred())
		Expect(observedRequest.Ports[0].RXRingSize).To(BeNil())
		Expect(observedRequest.Ports[0].MaxReadRequestBytes).NotTo(BeNil())
	})
	It("rejects carrier failure before discovery or writes", func() {
		linkUtils.ExpectedCalls = nil
		linkUtils.On("HasNoCarrier", "eth0").Return(true, nil)
		manager.discover = func(context.Context) (device.Inventory, error) {
			Fail("discovery must follow carrier gate")
			return device.Inventory{}, nil
		}
		_, err := manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(err).To(MatchError(ContainSubstring("NO-CARRIER")))
		Expect(calls).To(BeZero())
	})
	It("allows runtime when one named PF has carrier and reports observation errors", func() {
		linkUtils.ExpectedCalls = nil
		linkUtils.On("HasNoCarrier", "eth0").Return(true, nil)
		linkUtils.On("HasNoCarrier", "eth1").Return(false, nil)
		nic.Status.Ports = append(nic.Status.Ports, v1alpha1.NicDevicePortSpec{PCI: "0000:03:00.1", NetworkInterface: "eth1"})
		inventory.Devices[0].Ports = append(inventory.Devices[0].Ports, device.Port{PCI: "0000:03:00.1", Netdev: "eth1"})
		_, err := manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal(1))
		linkUtils.ExpectedCalls = nil
		linkUtils.On("HasNoCarrier", "eth0").Return(false, errors.New("link observation failed"))
		_, err = manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(err).To(MatchError("link observation failed"))
		Expect(calls).To(Equal(1))
	})
	It("allows unrelated discovery issues but rejects selected issues", func() {
		inventory.Complete = false
		inventory.Issues = []device.DiscoveryIssue{{PCI: "0000:09:00.0", Err: errors.New("unrelated")}}
		_, err := manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(err).NotTo(HaveOccurred())
		Expect(calls).To(Equal(1))
		inventory.Issues = []device.DiscoveryIssue{{DeviceID: "observed", Err: errors.New("selected")}}
		_, err = manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(err).To(MatchError(ContainSubstring("selected")))
		Expect(calls).To(Equal(1))
	})
	It("rejects missing PFs and mixed-ASIC requests without writes", func() {
		nic.Status.Ports = append(nic.Status.Ports, v1alpha1.NicDevicePortSpec{PCI: "0000:09:00.0"})
		_, err := manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(err).To(HaveOccurred())
		Expect(calls).To(BeZero())
		inventory.Devices = append(inventory.Devices, device.Device{ID: "other", Ports: []device.Port{{PCI: "0000:09:00.0"}}})
		_, err = manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(err).To(HaveOccurred())
		Expect(calls).To(BeZero())
	})
	DescribeTable("preserves delegated status", func(status dmsconfiguration.ApplyStatus, expected types.ApplyStatus, failure bool) {
		row.Status = status
		if failure {
			row.Err = errors.New("backend")
		}
		result, err := manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(result.Status).To(Equal(expected))
		if failure {
			Expect(err).To(HaveOccurred())
		} else {
			Expect(err).NotTo(HaveOccurred())
		}
	}, Entry("applied", dmsconfiguration.Applied, types.ApplyStatusSuccess, false), Entry("partial", dmsconfiguration.Partial, types.ApplyStatusPartiallyApplied, true), Entry("failed", dmsconfiguration.Failed, types.ApplyStatusFailed, true), Entry("applied with error", dmsconfiguration.Applied, types.ApplyStatusPartiallyApplied, true))
	It("does not report partial effects as a nil-error success", func() {
		row.Status = dmsconfiguration.Partial
		row.Err = nil
		result, err := manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(result.Status).To(Equal(types.ApplyStatusPartiallyApplied))
		Expect(err).To(MatchError(ContainSubstring("runtime configuration")))
	})
	It("rejects malformed result identities without claiming no-op", func() {
		row.DeviceID = "wrong"
		result, err := manager.ApplyRuntimeConfiguration(context.Background(), nic)
		Expect(err).To(HaveOccurred())
		Expect(result.Status).To(Equal(types.ApplyStatusPartiallyApplied))
	})
	It("delegates first-PF reset without adding VM-skip policy", func() {
		manager.firmware = resetFunc(func(_ context.Context, observed device.Device, options dmsfirmware.ResetOptions) error {
			Expect(observed.Ports).To(Equal([]device.Port{{PCI: "0000:03:00.0"}}))
			Expect(options.SkipVMCheck).To(BeFalse())
			return errors.New("reset failed")
		})
		Expect(manager.ResetNicFirmware(context.Background(), nic)).To(MatchError("reset failed"))
	})
})
