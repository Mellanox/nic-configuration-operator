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
	"io"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	execUtils "k8s.io/utils/exec"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

func TestUdev(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Udev Suite")
}

// fakeCmd implements execUtils.Cmd for testing
type fakeCmd struct {
	execUtils.Cmd
	command string
	args    []string
	output  []byte
	err     error
}

func (c *fakeCmd) Output() ([]byte, error) {
	return c.output, c.err
}

func (c *fakeCmd) SetStderr(out io.Writer) {}

// fakeExec implements execUtils.Interface for testing
type fakeExec struct {
	execUtils.Interface
	commands []*fakeCmd
	pos      int
}

func (f *fakeExec) Command(cmd string, args ...string) execUtils.Cmd {
	if f.pos < len(f.commands) {
		c := f.commands[f.pos]
		c.command = cmd
		c.args = args
		f.pos++
		return c
	}
	return &fakeCmd{command: cmd, args: args}
}

func (f *fakeExec) CommandContext(ctx context.Context, cmd string, args ...string) execUtils.Cmd {
	return f.Command(cmd, args...)
}

var _ = Describe("UdevManager", func() {
	var (
		tempDir  string
		manager  *udevManager
		execFake *fakeExec
	)

	BeforeEach(func() {
		var err error
		tempDir, err = os.MkdirTemp("", "udev-test")
		Expect(err).NotTo(HaveOccurred())

		execFake = &fakeExec{
			commands: []*fakeCmd{
				{output: []byte(""), err: nil}, // udevadm control --reload-rules
				{output: []byte(""), err: nil}, // udevadm trigger --action=add --subsystem-match=infiniband
				{output: []byte(""), err: nil}, // udevadm trigger --action=add --subsystem-match=net
				{output: []byte(""), err: nil}, // udevadm settle
			},
		}

		manager = &udevManager{
			hostPath:      tempDir,
			execInterface: execFake,
		}
	})

	AfterEach(func() {
		_ = os.RemoveAll(tempDir)
	})

	Describe("ApplyUdevRules", func() {
		It("should create separate udev rules files for net and RDMA devices and reload udev", func() {
			devices := []*v1alpha1.NicDevice{
				{
					Spec: v1alpha1.NicDeviceSpec{
						InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
							NicIndex:         0,
							RailIndex:        0,
							PlaneIndices:     []int{0, 1},
							RdmaDevicePrefix: "rdma%nic_id%p%plane_id%",
							NetDevicePrefix:  "net%nic_id%p%plane_id%",
						},
					},
					Status: v1alpha1.NicDeviceStatus{
						Ports: []v1alpha1.NicDevicePortSpec{
							{PCI: "0000:1a:00.0", NetworkInterface: "eth0", RdmaInterface: "mlx5_0"},
							{PCI: "0000:1a:00.1", NetworkInterface: "eth1", RdmaInterface: "mlx5_1"},
						},
					},
				},
			}

			expectedNames, updated, err := manager.ApplyUdevRules(context.Background(), devices)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated).To(BeTrue())

			// Verify expected names are returned
			Expect(expectedNames).To(HaveLen(2))
			Expect(expectedNames["0000:1a:00.0"]).To(Equal(ExpectedInterfaceNames{NetDevice: "net0p0", RdmaDevice: "rdma0p0"}))
			Expect(expectedNames["0000:1a:00.1"]).To(Equal(ExpectedInterfaceNames{NetDevice: "net0p1", RdmaDevice: "rdma0p1"}))

			// Verify net rules file was created
			netRulesPath := filepath.Join(tempDir, UdevNetRulesFile)
			netContent, err := os.ReadFile(netRulesPath)
			Expect(err).NotTo(HaveOccurred())

			netContentStr := string(netContent)
			Expect(netContentStr).To(ContainSubstring(`SUBSYSTEM=="net", ACTION=="add", KERNELS=="0000:1a:00.0", NAME="net0p0"`))
			Expect(netContentStr).To(ContainSubstring(`SUBSYSTEM=="net", ACTION=="add", KERNELS=="0000:1a:00.1", NAME="net0p1"`))
			Expect(netContentStr).NotTo(ContainSubstring(`infiniband`))

			// Verify RDMA rules file was created
			rdmaRulesPath := filepath.Join(tempDir, UdevRdmaRulesFile)
			rdmaContent, err := os.ReadFile(rdmaRulesPath)
			Expect(err).NotTo(HaveOccurred())

			rdmaContentStr := string(rdmaContent)
			Expect(rdmaContentStr).To(ContainSubstring(`ACTION=="add", KERNELS=="0000:1a:00.0", SUBSYSTEM=="infiniband", RUN+="/usr/bin/rdma dev set %k name rdma0p0"`))
			Expect(rdmaContentStr).To(ContainSubstring(`ACTION=="add", KERNELS=="0000:1a:00.1", SUBSYSTEM=="infiniband", RUN+="/usr/bin/rdma dev set %k name rdma0p1"`))
			Expect(rdmaContentStr).NotTo(ContainSubstring(`SUBSYSTEM=="net"`))

			// Verify udevadm commands were called (4 commands: reload, trigger infiniband, trigger net, settle)
			Expect(execFake.pos).To(Equal(4))
		})

		It("should not update file or reload udev if content is the same", func() {
			devices := []*v1alpha1.NicDevice{
				{
					Spec: v1alpha1.NicDeviceSpec{
						InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
							NicIndex:         1,
							RailIndex:        1,
							PlaneIndices:     []int{1},
							RdmaDevicePrefix: "rdma%nic_id%",
							NetDevicePrefix:  "net%nic_id%",
						},
					},
					Status: v1alpha1.NicDeviceStatus{
						Ports: []v1alpha1.NicDevicePortSpec{
							{PCI: "0000:1a:00.0"},
						},
					},
				},
			}

			// First apply - should call udevadm (4 commands: reload, trigger infiniband, trigger net, settle)
			expectedNames1, updated1, err := manager.ApplyUdevRules(context.Background(), devices)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated1).To(BeTrue())
			Expect(expectedNames1).To(HaveLen(1))
			Expect(execFake.pos).To(Equal(4))

			// Reset exec position for second apply
			execFake.pos = 0

			// Second apply with same content - should NOT call udevadm
			expectedNames2, updated2, err := manager.ApplyUdevRules(context.Background(), devices)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated2).To(BeFalse())
			Expect(expectedNames2).To(HaveLen(1)) // Still returns expected names
			Expect(execFake.pos).To(Equal(0))     // No commands should have been called
		})

		It("should handle multiple devices", func() {
			// Note: With numPFs derived from PlaneIndices, this generates rules for all PFs
			// even if not all PFs are currently in device.Status.Ports
			devices := []*v1alpha1.NicDevice{
				{
					Spec: v1alpha1.NicDeviceSpec{
						InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
							NicIndex:         0,
							RailIndex:        0,
							PlaneIndices:     []int{0, 1}, // 2 PFs -> generates rules for .0 and .1
							RdmaDevicePrefix: "rdma_r%rail_id%_n%nic_id%_p%plane_id%",
							NetDevicePrefix:  "net_r%rail_id%_n%nic_id%_p%plane_id%",
						},
					},
					Status: v1alpha1.NicDeviceStatus{
						Ports: []v1alpha1.NicDevicePortSpec{
							{PCI: "0000:1a:00.0"}, // Only 1 port in status, but 2 rules generated
						},
					},
				},
				{
					Spec: v1alpha1.NicDeviceSpec{
						InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
							NicIndex:         1,
							RailIndex:        0,
							PlaneIndices:     []int{2, 3}, // 2 PFs -> generates rules for .0 and .1
							RdmaDevicePrefix: "rdma_r%rail_id%_n%nic_id%_p%plane_id%",
							NetDevicePrefix:  "net_r%rail_id%_n%nic_id%_p%plane_id%",
						},
					},
					Status: v1alpha1.NicDeviceStatus{
						Ports: []v1alpha1.NicDevicePortSpec{
							{PCI: "0000:2a:00.0"}, // Only 1 port in status, but 2 rules generated
						},
					},
				},
			}

			expectedNames, updated, err := manager.ApplyUdevRules(context.Background(), devices)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated).To(BeTrue())

			// Verify expected names from both devices - 2 PFs each = 4 total
			Expect(expectedNames).To(HaveLen(4))
			Expect(expectedNames["0000:1a:00.0"]).To(Equal(ExpectedInterfaceNames{NetDevice: "net_r0_n0_p0", RdmaDevice: "rdma_r0_n0_p0"}))
			Expect(expectedNames["0000:1a:00.1"]).To(Equal(ExpectedInterfaceNames{NetDevice: "net_r0_n0_p1", RdmaDevice: "rdma_r0_n0_p1"}))
			Expect(expectedNames["0000:2a:00.0"]).To(Equal(ExpectedInterfaceNames{NetDevice: "net_r0_n1_p2", RdmaDevice: "rdma_r0_n1_p2"}))
			Expect(expectedNames["0000:2a:00.1"]).To(Equal(ExpectedInterfaceNames{NetDevice: "net_r0_n1_p3", RdmaDevice: "rdma_r0_n1_p3"}))

			// Check net rules file
			netRulesPath := filepath.Join(tempDir, UdevNetRulesFile)
			netContent, err := os.ReadFile(netRulesPath)
			Expect(err).NotTo(HaveOccurred())

			netContentStr := string(netContent)
			Expect(netContentStr).To(ContainSubstring(`KERNELS=="0000:1a:00.0", NAME="net_r0_n0_p0"`))
			Expect(netContentStr).To(ContainSubstring(`KERNELS=="0000:1a:00.1", NAME="net_r0_n0_p1"`))
			Expect(netContentStr).To(ContainSubstring(`KERNELS=="0000:2a:00.0", NAME="net_r0_n1_p2"`))
			Expect(netContentStr).To(ContainSubstring(`KERNELS=="0000:2a:00.1", NAME="net_r0_n1_p3"`))

			// Check RDMA rules file
			rdmaRulesPath := filepath.Join(tempDir, UdevRdmaRulesFile)
			rdmaContent, err := os.ReadFile(rdmaRulesPath)
			Expect(err).NotTo(HaveOccurred())

			rdmaContentStr := string(rdmaContent)
			Expect(rdmaContentStr).To(ContainSubstring(`KERNELS=="0000:1a:00.0"`))
			Expect(rdmaContentStr).To(ContainSubstring(`rdma_r0_n0_p0`))
			Expect(rdmaContentStr).To(ContainSubstring(`KERNELS=="0000:1a:00.1"`))
			Expect(rdmaContentStr).To(ContainSubstring(`rdma_r0_n0_p1`))
			Expect(rdmaContentStr).To(ContainSubstring(`KERNELS=="0000:2a:00.0"`))
			Expect(rdmaContentStr).To(ContainSubstring(`rdma_r0_n1_p2`))
			Expect(rdmaContentStr).To(ContainSubstring(`KERNELS=="0000:2a:00.1"`))
			Expect(rdmaContentStr).To(ContainSubstring(`rdma_r0_n1_p3`))
		})

		It("should skip devices without InterfaceNameTemplate", func() {
			devices := []*v1alpha1.NicDevice{
				{
					Spec: v1alpha1.NicDeviceSpec{
						InterfaceNameTemplate: nil,
					},
					Status: v1alpha1.NicDeviceStatus{
						Ports: []v1alpha1.NicDevicePortSpec{
							{PCI: "0000:1a:00.0"},
						},
					},
				},
			}

			expectedNames, updated, err := manager.ApplyUdevRules(context.Background(), devices)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated).To(BeTrue())
			Expect(expectedNames).To(BeEmpty())

			// Both files should only contain the header
			netRulesPath := filepath.Join(tempDir, UdevNetRulesFile)
			netContent, err := os.ReadFile(netRulesPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(netContent)).To(Equal(UdevRulesHeader))

			rdmaRulesPath := filepath.Join(tempDir, UdevRdmaRulesFile)
			rdmaContent, err := os.ReadFile(rdmaRulesPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(rdmaContent)).To(Equal(UdevRulesHeader))
		})

		It("should skip ports without PCI address", func() {
			devices := []*v1alpha1.NicDevice{
				{
					Spec: v1alpha1.NicDeviceSpec{
						InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
							NicIndex:         1,
							RailIndex:        1,
							PlaneIndices:     []int{1},
							RdmaDevicePrefix: "rdma%nic_id%",
							NetDevicePrefix:  "net%nic_id%",
						},
					},
					Status: v1alpha1.NicDeviceStatus{
						Ports: []v1alpha1.NicDevicePortSpec{
							{PCI: ""}, // Empty PCI address
						},
					},
				},
			}

			expectedNames, updated, err := manager.ApplyUdevRules(context.Background(), devices)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated).To(BeTrue())
			Expect(expectedNames).To(BeEmpty())

			// Both files should only contain the header
			netRulesPath := filepath.Join(tempDir, UdevNetRulesFile)
			netContent, err := os.ReadFile(netRulesPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(netContent)).To(Equal(UdevRulesHeader))

			rdmaRulesPath := filepath.Join(tempDir, UdevRdmaRulesFile)
			rdmaContent, err := os.ReadFile(rdmaRulesPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(rdmaContent)).To(Equal(UdevRulesHeader))
		})

		It("should handle empty device list", func() {
			devices := []*v1alpha1.NicDevice{}

			expectedNames, updated, err := manager.ApplyUdevRules(context.Background(), devices)
			Expect(err).NotTo(HaveOccurred())
			Expect(updated).To(BeTrue())
			Expect(expectedNames).To(BeEmpty())

			// Both files should only contain the header
			netRulesPath := filepath.Join(tempDir, UdevNetRulesFile)
			netContent, err := os.ReadFile(netRulesPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(netContent)).To(Equal(UdevRulesHeader))

			rdmaRulesPath := filepath.Join(tempDir, UdevRdmaRulesFile)
			rdmaContent, err := os.ReadFile(rdmaRulesPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(rdmaContent)).To(Equal(UdevRulesHeader))
		})

		It("should return error if udevadm control --reload-rules fails", func() {
			// Set up exec to fail on first command
			execFake.commands = []*fakeCmd{
				{output: []byte(""), err: fmt.Errorf("udevadm failed")},
			}
			execFake.pos = 0

			devices := []*v1alpha1.NicDevice{
				{
					Spec: v1alpha1.NicDeviceSpec{
						InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
							NicIndex:         1,
							RailIndex:        1,
							PlaneIndices:     []int{1},
							RdmaDevicePrefix: "rdma%nic_id%",
							NetDevicePrefix:  "net%nic_id%",
						},
					},
					Status: v1alpha1.NicDeviceStatus{
						Ports: []v1alpha1.NicDevicePortSpec{
							{PCI: "0000:1a:00.0"},
						},
					},
				},
			}

			_, _, err := manager.ApplyUdevRules(context.Background(), devices)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to reload udev rules"))
		})

		It("should return error if udevadm trigger for infiniband fails", func() {
			// Set up exec to fail on second command (infiniband trigger)
			execFake.commands = []*fakeCmd{
				{output: []byte(""), err: nil},                          // reload-rules succeeds
				{output: []byte(""), err: fmt.Errorf("trigger failed")}, // infiniband trigger fails
			}
			execFake.pos = 0

			devices := []*v1alpha1.NicDevice{
				{
					Spec: v1alpha1.NicDeviceSpec{
						InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
							NicIndex:         1,
							RailIndex:        1,
							PlaneIndices:     []int{1},
							RdmaDevicePrefix: "rdma%nic_id%",
							NetDevicePrefix:  "net%nic_id%",
						},
					},
					Status: v1alpha1.NicDeviceStatus{
						Ports: []v1alpha1.NicDevicePortSpec{
							{PCI: "0000:1a:00.0"},
						},
					},
				},
			}

			_, _, err := manager.ApplyUdevRules(context.Background(), devices)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to trigger infiniband subsystem"))
		})

		It("should return error if udevadm trigger for net fails", func() {
			// Set up exec to fail on third command (net trigger)
			execFake.commands = []*fakeCmd{
				{output: []byte(""), err: nil},                              // reload-rules succeeds
				{output: []byte(""), err: nil},                              // infiniband trigger succeeds
				{output: []byte(""), err: fmt.Errorf("net trigger failed")}, // net trigger fails
			}
			execFake.pos = 0

			devices := []*v1alpha1.NicDevice{
				{
					Spec: v1alpha1.NicDeviceSpec{
						InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
							NicIndex:         1,
							RailIndex:        1,
							PlaneIndices:     []int{1},
							RdmaDevicePrefix: "rdma%nic_id%",
							NetDevicePrefix:  "net%nic_id%",
						},
					},
					Status: v1alpha1.NicDeviceStatus{
						Ports: []v1alpha1.NicDevicePortSpec{
							{PCI: "0000:1a:00.0"},
						},
					},
				},
			}

			_, _, err := manager.ApplyUdevRules(context.Background(), devices)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to trigger net subsystem"))
		})

		It("should return error if udevadm settle fails", func() {
			// Set up exec to fail on fourth command (settle)
			execFake.commands = []*fakeCmd{
				{output: []byte(""), err: nil},                         // reload-rules succeeds
				{output: []byte(""), err: nil},                         // infiniband trigger succeeds
				{output: []byte(""), err: nil},                         // net trigger succeeds
				{output: []byte(""), err: fmt.Errorf("settle failed")}, // settle fails
			}
			execFake.pos = 0

			devices := []*v1alpha1.NicDevice{
				{
					Spec: v1alpha1.NicDeviceSpec{
						InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
							NicIndex:         1,
							RailIndex:        1,
							PlaneIndices:     []int{1},
							RdmaDevicePrefix: "rdma%nic_id%",
							NetDevicePrefix:  "net%nic_id%",
						},
					},
					Status: v1alpha1.NicDeviceStatus{
						Ports: []v1alpha1.NicDevicePortSpec{
							{PCI: "0000:1a:00.0"},
						},
					},
				},
			}

			_, _, err := manager.ApplyUdevRules(context.Background(), devices)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to wait for udev events to settle"))
		})
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

	Describe("generateNetDeviceRule", func() {
		It("should generate correct net device rule", func() {
			rule := generateNetDeviceRule("0000:1a:00.0", "eth_nic1")
			Expect(rule).To(Equal(`SUBSYSTEM=="net", ACTION=="add", KERNELS=="0000:1a:00.0", NAME="eth_nic1"`))
		})
	})

	Describe("generateRdmaDeviceRule", func() {
		It("should generate correct rdma device rule", func() {
			rule := generateRdmaDeviceRule("0000:1a:00.0", "rdma_nic1")
			Expect(rule).To(Equal(`ACTION=="add", KERNELS=="0000:1a:00.0", SUBSYSTEM=="infiniband", RUN+="/usr/bin/rdma dev set %k name rdma_nic1"`))
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
