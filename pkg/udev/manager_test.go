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
	"errors"
	"testing"

	"github.com/Mellanox/dms-lib/pkg/naming"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestUdev(t *testing.T) { RegisterFailHandler(Fail); RunSpecs(t, "Udev Suite") }

func namingDevice(name string, pcis ...string) *v1alpha1.NicDevice {
	ports := make([]v1alpha1.NicDevicePortSpec, len(pcis))
	planes := make([]int, len(pcis))
	for i, pci := range pcis {
		ports[i] = v1alpha1.NicDevicePortSpec{PCI: pci}
		planes[i] = i
	}
	return &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test"},
		Spec: v1alpha1.NicDeviceSpec{InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex: 1, RailIndex: 2, PlaneIndices: planes, NetDevicePrefix: "n%nic_id%r%rail_id%p%plane_id%", RdmaDevicePrefix: "r%nic_id%p%plane_id%",
		}}, Status: v1alpha1.NicDeviceStatus{Ports: ports},
	}
}

var _ = Describe("UdevManager", func() {
	It("delegates complete explicit assignments and host root to the generic library", func() {
		d := namingDevice("nic", "0000:03:00.0", "0002:03:00.0")
		called := false
		manager := &udevManager{hostPath: "/host", apply: func(ctx context.Context, a []naming.Assignment, options naming.Options) (bool, error) {
			called = true
			Expect(ctx).To(Equal(context.Background()))
			Expect(options.HostRoot).To(Equal("/host"))
			Expect(a).To(Equal([]naming.Assignment{{PCI: "0000:03:00.0", Netdev: "n1r2p0", RDMA: "r1p0"}, {PCI: "0002:03:00.0", Netdev: "n1r2p1", RDMA: "r1p1"}}))
			return true, nil
		}}
		expected, changed, err := manager.ApplyUdevRules(context.Background(), []*v1alpha1.NicDevice{d})
		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		Expect(called).To(BeTrue())
		Expect(expected["0002:03:00.0"]).To(Equal(ExpectedInterfaceNames{NetDevice: "n1r2p1", RdmaDevice: "r1p1"}))
		Expect(expected).NotTo(HaveKey("0000:03:00.1"))
	})
	It("retains existing future-PF naming policy without treating it as discovery", func() {
		d := namingDevice("nic", "0000:03:00.0")
		d.Spec.InterfaceNameTemplate.PlaneIndices = []int{2, 3}
		expected, err := ExpectedNames([]*v1alpha1.NicDevice{d})
		Expect(err).NotTo(HaveOccurred())
		Expect(expected["0000:03:00.1"]).To(Equal(ExpectedInterfaceNames{NetDevice: "n1r2p3", RdmaDevice: "r1p3"}))
		Expect(d.Status.Ports).To(HaveLen(1))
	})
	It("assigns shared RDMA naming only to the first requested PF", func() {
		d := namingDevice("nic", "0000:03:00.0", "0000:03:00.1")
		d.Spec.InterfaceNameTemplate.RdmaDevicePrefix = "rdma%nic_id%"
		assignments, expected, err := namingAssignments([]*v1alpha1.NicDevice{d})
		Expect(err).NotTo(HaveOccurred())
		Expect(assignments[0].RDMA).To(Equal("rdma1"))
		Expect(assignments[1].RDMA).To(BeEmpty())
		Expect(expected["0000:03:00.1"].RdmaDevice).To(BeEmpty())
		d.Spec.InterfaceNameTemplate.NetDevicePrefix = ""
		assignments, expected, err = namingAssignments([]*v1alpha1.NicDevice{d})
		Expect(err).NotTo(HaveOccurred())
		Expect(assignments).To(HaveLen(1))
		Expect(expected).To(HaveLen(2))
	})
	It("allows omitted RDMA naming and keeps net/RDMA namespaces distinct", func() {
		d := namingDevice("nic", "0000:03:00.0")
		d.Spec.InterfaceNameTemplate.NetDevicePrefix = "same"
		d.Spec.InterfaceNameTemplate.RdmaDevicePrefix = "same"
		Expect(ValidateInterfaceNames([]*v1alpha1.NicDevice{d})).To(Succeed())
		d.Spec.InterfaceNameTemplate.RdmaDevicePrefix = ""
		expected, err := ExpectedNames([]*v1alpha1.NicDevice{d})
		Expect(err).NotTo(HaveOccurred())
		Expect(expected["0000:03:00.0"].RdmaDevice).To(BeEmpty())
	})
	It("rejects duplicate names and PCI ownership before invoking Apply", func() {
		a, b := namingDevice("a", "0000:03:00.0"), namingDevice("b", "0000:04:00.0")
		manager := &udevManager{hostPath: "/host", apply: func(context.Context, []naming.Assignment, naming.Options) (bool, error) {
			Fail("must not apply invalid assignments")
			return false, nil
		}}
		_, _, err := manager.ApplyUdevRules(context.Background(), []*v1alpha1.NicDevice{a, b})
		Expect(err).To(HaveOccurred())
		b.Spec.InterfaceNameTemplate.NetDevicePrefix = "other"
		_, _, err = manager.ApplyUdevRules(context.Background(), []*v1alpha1.NicDevice{a, b})
		Expect(err).To(HaveOccurred()) // RDMA collision remains.
		b.Spec.InterfaceNameTemplate.RdmaDevicePrefix = "other"
		b.Status.Ports[0].PCI = a.Status.Ports[0].PCI
		_, _, err = manager.ApplyUdevRules(context.Background(), []*v1alpha1.NicDevice{a, b})
		Expect(err).To(HaveOccurred())
	})
	It("rejects empty prefixes, nil devices and unsafe final names", func() {
		Expect(ValidateInterfaceNames([]*v1alpha1.NicDevice{nil})).NotTo(Succeed())
		d := namingDevice("nic", "0000:03:00.0")
		d.Spec.InterfaceNameTemplate.NetDevicePrefix = ""
		d.Spec.InterfaceNameTemplate.RdmaDevicePrefix = ""
		Expect(ValidateInterfaceNames([]*v1alpha1.NicDevice{d})).NotTo(Succeed())
		d.Spec.InterfaceNameTemplate.NetDevicePrefix = "bad name"
		Expect(ValidateInterfaceNames([]*v1alpha1.NicDevice{d})).NotTo(Succeed())
	})
	It("forwards empty desired sets so the library can clear owned files", func() {
		called := false
		manager := &udevManager{hostPath: "/host", apply: func(_ context.Context, a []naming.Assignment, _ naming.Options) (bool, error) {
			called = true
			Expect(a).To(BeEmpty())
			return false, nil
		}}
		d := namingDevice("nic", "0000:03:00.0")
		d.Spec.InterfaceNameTemplate = nil
		expected, changed, err := manager.ApplyUdevRules(context.Background(), []*v1alpha1.NicDevice{d})
		Expect(err).NotTo(HaveOccurred())
		Expect(expected).To(BeEmpty())
		Expect(changed).To(BeFalse())
		Expect(called).To(BeTrue())
	})
	It("preserves changed and errors from failed library activation", func() {
		failure := errors.New("udev activation failed")
		manager := &udevManager{hostPath: "/host", apply: func(context.Context, []naming.Assignment, naming.Options) (bool, error) { return true, failure }}
		expected, changed, err := manager.ApplyUdevRules(context.Background(), []*v1alpha1.NicDevice{namingDevice("nic", "0000:03:00.0")})
		Expect(err).To(MatchError(failure))
		Expect(changed).To(BeTrue())
		Expect(expected).To(HaveLen(1))
	})
	Describe("substituteTemplatePlaceholders", func() {
		It("should replace all placeholders", func() {
			result := substituteTemplatePlaceholders("net%nic_id%p%plane_id%r%rail_id%", 0, 1, 2)
			Expect(result).To(Equal("net0p1r2"))
		})

		It("should handle template without placeholders", func() {
			result := substituteTemplatePlaceholders("static_name", 0, 1, 2)
			Expect(result).To(Equal("static_name"))
		})

		It("should handle empty template", func() {
			result := substituteTemplatePlaceholders("", 0, 1, 2)
			Expect(result).To(Equal(""))
		})

		It("should handle multiple occurrences of same placeholder", func() {
			result := substituteTemplatePlaceholders("nic%nic_id%_nic%nic_id%", 4, 0, 0)
			Expect(result).To(Equal("nic4_nic4"))
		})
	})

	Describe("CalculatePCIAddressForPF", func() {
		It("should return same address for pfIndex 0", func() {
			pciAddr, err := CalculatePCIAddressForPF("0000:1a:00.0", 0)
			Expect(err).NotTo(HaveOccurred())
			Expect(pciAddr).To(Equal("0000:1a:00.0"))
		})

		It("should increment function number for pfIndex 1", func() {
			pciAddr, err := CalculatePCIAddressForPF("0000:1a:00.0", 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(pciAddr).To(Equal("0000:1a:00.1"))
		})

		It("should increment function number for multiple PFs", func() {
			pciAddr, err := CalculatePCIAddressForPF("0000:1a:00.0", 3)
			Expect(err).NotTo(HaveOccurred())
			Expect(pciAddr).To(Equal("0000:1a:00.3"))
		})

		It("should handle base address with non-zero function", func() {
			pciAddr, err := CalculatePCIAddressForPF("0000:1a:00.2", 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(pciAddr).To(Equal("0000:1a:00.3"))
		})

		It("should handle maximum function number (7)", func() {
			pciAddr, err := CalculatePCIAddressForPF("0000:1a:00.0", 7)
			Expect(err).NotTo(HaveOccurred())
			Expect(pciAddr).To(Equal("0000:1a:00.7"))
		})

		It("should return error when function number exceeds 7", func() {
			_, err := CalculatePCIAddressForPF("0000:1a:00.0", 8)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exceeds maximum"))
		})

		It("should return error when base + pfIndex exceeds 7", func() {
			_, err := CalculatePCIAddressForPF("0000:1a:00.5", 3)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exceeds maximum"))
		})

		It("should return error for invalid PCI address format", func() {
			_, err := CalculatePCIAddressForPF("invalid", 0)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to parse PCI address"))
		})

		It("should handle different domains and buses", func() {
			pciAddr, err := CalculatePCIAddressForPF("0001:3b:02.0", 2)
			Expect(err).NotTo(HaveOccurred())
			Expect(pciAddr).To(Equal("0001:3b:02.2"))
		})

		It("should preserve leading zeros in formatted output", func() {
			pciAddr, err := CalculatePCIAddressForPF("0000:01:00.0", 1)
			Expect(err).NotTo(HaveOccurred())
			Expect(pciAddr).To(Equal("0000:01:00.1"))
		})
	})

})
