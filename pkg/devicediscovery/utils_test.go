/*
2025 NVIDIA CORPORATION & AFFILIATES
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

package devicediscovery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"
)

const (
	pciAddress   = "0000:03:00.0"
	partNumber   = "MCX623106AE-CDAT"
	serialNumber = "MT2235J01129"
)

func writeRDMASysfsFixture(root, rdmaDevice, firmwareVersion, psid string) {
	GinkgoHelper()
	rdmaDevicePath := filepath.Join(root, pciAddress, "infiniband", rdmaDevice)
	Expect(os.MkdirAll(rdmaDevicePath, 0o755)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(rdmaDevicePath, "fw_ver"), []byte(firmwareVersion), 0o644)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(rdmaDevicePath, "board_id"), []byte(psid), 0o644)).To(Succeed())
}

var _ = Describe("HostUtils", func() {
	Describe("getFwctlDeviceFromPath", func() {
		It("returns the dev path for a discovered fwctl entry", func() {
			root := GinkgoT().TempDir()
			fwctlDir := filepath.Join(root, pciAddress, "fwctl")
			Expect(os.MkdirAll(fwctlDir, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(fwctlDir, "fwctl2"), []byte{}, 0o644)).To(Succeed())

			fwctlDevice, err := getFwctlDeviceFromPath(root, pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(fwctlDevice).To(Equal("/dev/fwctl/fwctl2"))
		})

		It("returns the first fwctl entry sorted by name", func() {
			root := GinkgoT().TempDir()
			fwctlDir := filepath.Join(root, pciAddress, "fwctl")
			Expect(os.MkdirAll(fwctlDir, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(fwctlDir, "fwctl7"), []byte{}, 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(fwctlDir, "fwctl3"), []byte{}, 0o644)).To(Succeed())

			fwctlDevice, err := getFwctlDeviceFromPath(root, pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(fwctlDevice).To(Equal("/dev/fwctl/fwctl3"))
		})

		It("returns an error when the fwctl directory is missing", func() {
			root := GinkgoT().TempDir()

			fwctlDevice, err := getFwctlDeviceFromPath(root, pciAddress)

			Expect(err).To(HaveOccurred())
			Expect(fwctlDevice).To(BeEmpty())
		})

		It("returns an error when the fwctl directory has no fwctl entries", func() {
			root := GinkgoT().TempDir()
			fwctlDir := filepath.Join(root, pciAddress, "fwctl")
			Expect(os.MkdirAll(fwctlDir, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(fwctlDir, "not-fwctl"), []byte{}, 0o644)).To(Succeed())

			fwctlDevice, err := getFwctlDeviceFromPath(root, pciAddress)

			Expect(err).To(HaveOccurred())
			Expect(fwctlDevice).To(BeEmpty())
		})
	})
	//nolint:dupl
	Describe("GetFirmwareVersionAndPSID", func() {
		It("should return lowercased firmware version and PSID from flint", func() {
			fwVersion := "VeRsIoN"
			psid := "PSID"

			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("irrelevant line\n" +
						"FW Version: VeRsIoN\n" +
						"PSID: PSID\n" +
						"another irrelevant line"),
					nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("flint"))
				Expect(args[1]).To(Equal(pciAddress))
				return fakeCmd
			})

			h := &deviceDiscoveryUtils{
				execInterface: fakeExec,
				pciSysfsRoot:  filepath.Join(GinkgoT().TempDir(), "missing"),
			}

			firmwareVersion, actualPSID, err := h.GetFirmwareVersionAndPSID(pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(firmwareVersion).To(Equal(strings.ToLower(fwVersion)))
			Expect(actualPSID).To(Equal(strings.ToLower(psid)))
		})

		It("should fall back to RDMA sysfs when flint fails on the affected ConnectX-7 function", func() {
			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return nil, []byte("-E- Cannot open Device: 0000:a3:00.0. ICMD bad parameter given"), errors.New("exit status 1")
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("flint"))
				Expect(args).To(Equal([]string{"-d", pciAddress, "q"}))
				return fakeCmd
			})

			root := GinkgoT().TempDir()
			writeRDMASysfsFixture(root, "mlx5_7", "28.48.1132\n", "NVD0000000079\n")
			h := &deviceDiscoveryUtils{execInterface: fakeExec, pciSysfsRoot: root}

			firmwareVersion, actualPSID, err := h.GetFirmwareVersionAndPSID(pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(firmwareVersion).To(Equal("28.48.1132"))
			Expect(actualPSID).To(Equal("nvd0000000079"))
		})

		It("should fall back to RDMA sysfs when flint output is incomplete", func() {
			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("FW Version: ignored-without-psid"), nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("flint"))
				Expect(args).To(Equal([]string{"-d", pciAddress, "q"}))
				return fakeCmd
			})

			root := GinkgoT().TempDir()
			writeRDMASysfsFixture(root, "mlx5_0", "40.48.1132\n", "NVD0000000072\n")
			h := &deviceDiscoveryUtils{execInterface: fakeExec, pciSysfsRoot: root}

			firmwareVersion, actualPSID, err := h.GetFirmwareVersionAndPSID(pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(firmwareVersion).To(Equal("40.48.1132"))
			Expect(actualPSID).To(Equal("nvd0000000072"))
		})

		It("should use a complete RDMA device without combining partial data from another entry", func() {
			root := GinkgoT().TempDir()
			writeRDMASysfsFixture(root, "mlx5_7", "28.48.1132\n", " \n")
			writeRDMASysfsFixture(root, "mlx5_8", "28.48.1132\n", "NVD0000000079\n")

			firmwareVersion, actualPSID, err := getFirmwareVersionAndPSIDFromSysfs(root, pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(firmwareVersion).To(Equal("28.48.1132"))
			Expect(actualPSID).To(Equal("nvd0000000079"))
		})

		It("should follow the PCI function's RDMA sysfs symlink", func() {
			root := GinkgoT().TempDir()
			target := filepath.Join(root, "class", "infiniband", "mlx5_7")
			Expect(os.MkdirAll(target, 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(target, "fw_ver"), []byte("28.48.1132\n"), 0o644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(target, "board_id"), []byte("NVD0000000079\n"), 0o644)).To(Succeed())
			rdmaDir := filepath.Join(root, pciAddress, "infiniband")
			Expect(os.MkdirAll(rdmaDir, 0o755)).To(Succeed())
			Expect(os.Symlink(target, filepath.Join(rdmaDir, "mlx5_7"))).To(Succeed())

			firmwareVersion, actualPSID, err := getFirmwareVersionAndPSIDFromSysfs(root, pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(firmwareVersion).To(Equal("28.48.1132"))
			Expect(actualPSID).To(Equal("nvd0000000079"))
		})

		It("should reject complementary partial data from different RDMA devices", func() {
			root := GinkgoT().TempDir()
			writeRDMASysfsFixture(root, "mlx5_7", "28.48.1132\n", " \n")
			writeRDMASysfsFixture(root, "mlx5_8", " \n", "NVD0000000079\n")

			firmwareVersion, actualPSID, err := getFirmwareVersionAndPSIDFromSysfs(root, pciAddress)

			Expect(err).To(MatchError(ContainSubstring("no RDMA device")))
			Expect(firmwareVersion).To(BeEmpty())
			Expect(actualPSID).To(BeEmpty())
		})

		It("should return an error when both flint and RDMA sysfs fail", func() {
			fakeExec := &execTesting.FakeExec{}
			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return nil, nil, errors.New("flint failure")
			})
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(_ string, _ ...string) exec.Cmd {
				return fakeCmd
			})

			root := GinkgoT().TempDir()
			Expect(os.MkdirAll(filepath.Join(root, pciAddress, "infiniband"), 0o755)).To(Succeed())
			h := &deviceDiscoveryUtils{execInterface: fakeExec, pciSysfsRoot: root}

			firmwareVersion, actualPSID, err := h.GetFirmwareVersionAndPSID(pciAddress)

			Expect(err).To(MatchError(And(
				ContainSubstring("flint failure"),
				ContainSubstring("no RDMA devices found"),
			)))
			Expect(firmwareVersion).To(BeEmpty())
			Expect(actualPSID).To(BeEmpty())
		})

		It("should return a contextual error when a sysfs attribute cannot be read", func() {
			root := GinkgoT().TempDir()
			rdmaDevice := filepath.Join(root, pciAddress, "infiniband", "mlx5_7")
			Expect(os.MkdirAll(filepath.Join(rdmaDevice, "fw_ver"), 0o755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(rdmaDevice, "board_id"), []byte("NVD0000000079\n"), 0o644)).To(Succeed())

			firmwareVersion, actualPSID, err := getFirmwareVersionAndPSIDFromSysfs(root, pciAddress)

			Expect(err).To(MatchError(ContainSubstring("reading firmware version for RDMA device mlx5_7")))
			Expect(firmwareVersion).To(BeEmpty())
			Expect(actualPSID).To(BeEmpty())
		})
	})

	Describe("IsZeroTrust", func() {
		It("should return true if the device is in zero-trust mode", func() {
			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("level	: restricted"), nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mlxprivhost"))
				Expect(args[1]).To(Equal(pciAddress))
				return fakeCmd
			})

			h := &deviceDiscoveryUtils{
				execInterface: fakeExec,
			}

			zeroTrust, err := h.IsZeroTrust(pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(zeroTrust).To(BeTrue())
		})
		It("should return false if the device is not in zero-trust mode", func() {
			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("level	: privileged"), nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mlxprivhost"))
				Expect(args[1]).To(Equal(pciAddress))
				return fakeCmd
			})

			h := &deviceDiscoveryUtils{
				execInterface: fakeExec,
			}

			zeroTrust, err := h.IsZeroTrust(pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(zeroTrust).To(BeFalse())
		})
	})

	Describe("isPhysicalPort", func() {
		var tmpDir string

		BeforeEach(func() {
			var err error
			tmpDir, err = os.MkdirTemp("", "pci-test-*")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			Expect(os.RemoveAll(tmpDir)).To(Succeed())
		})

		createPhysPortName := func(pciAddr, ifaceName, portName string) {
			dir := filepath.Join(tmpDir, pciAddr, "net", ifaceName)
			Expect(os.MkdirAll(dir, 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "phys_port_name"), []byte(portName+"\n"), 0644)).To(Succeed())
		}

		It("should return true for physical port p0", func() {
			createPhysPortName(pciAddress, "eth_rail0", "p0")
			Expect(isPhysicalPort(tmpDir, pciAddress, "eth_rail0")).To(BeTrue())
		})

		It("should return true for physical port p1", func() {
			createPhysPortName(pciAddress, "eth_rail1", "p1")
			Expect(isPhysicalPort(tmpDir, pciAddress, "eth_rail1")).To(BeTrue())
		})

		It("should return false for VF representor pf0vf0", func() {
			createPhysPortName(pciAddress, "eth1", "pf0vf0")
			Expect(isPhysicalPort(tmpDir, pciAddress, "eth1")).To(BeFalse())
		})

		It("should return false for SF representor pf0sf0", func() {
			createPhysPortName(pciAddress, "en3f0pf0sf0", "pf0sf0")
			Expect(isPhysicalPort(tmpDir, pciAddress, "en3f0pf0sf0")).To(BeFalse())
		})

		It("should return true when phys_port_name file does not exist", func() {
			dir := filepath.Join(tmpDir, pciAddress, "net", "eth0")
			Expect(os.MkdirAll(dir, 0755)).To(Succeed())
			Expect(isPhysicalPort(tmpDir, pciAddress, "eth0")).To(BeTrue())
		})

		It("should return true when phys_port_name is empty", func() {
			createPhysPortName(pciAddress, "eth0", "")
			Expect(isPhysicalPort(tmpDir, pciAddress, "eth0")).To(BeTrue())
		})
	})

	Describe("getNetNamesFromPath", func() {
		var tmpDir string

		BeforeEach(func() {
			var err error
			tmpDir, err = os.MkdirTemp("", "pci-test-*")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			Expect(os.RemoveAll(tmpDir)).To(Succeed())
		})

		createIface := func(pciAddr, ifaceName, physPortName string) {
			dir := filepath.Join(tmpDir, pciAddr, "net", ifaceName)
			Expect(os.MkdirAll(dir, 0755)).To(Succeed())
			if physPortName != "" {
				Expect(os.WriteFile(filepath.Join(dir, "phys_port_name"), []byte(physPortName+"\n"), 0644)).To(Succeed())
			}
		}

		It("should return single interface", func() {
			createIface(pciAddress, "eth0", "p0")
			names, err := getNetNamesFromPath(tmpDir, pciAddress)
			Expect(err).NotTo(HaveOccurred())
			Expect(names).To(ConsistOf("eth0"))
		})

		It("should filter out VF representors and return only PF", func() {
			createIface(pciAddress, "eth1", "pf0vf0")
			createIface(pciAddress, "eth_rail1", "p0")
			names, err := getNetNamesFromPath(tmpDir, pciAddress)
			Expect(err).NotTo(HaveOccurred())
			Expect(names).To(ConsistOf("eth_rail1"))
		})

		It("should filter out multiple representors", func() {
			createIface(pciAddress, "eth1", "pf0vf0")
			createIface(pciAddress, "eth2", "pf0vf1")
			createIface(pciAddress, "eth_rail1", "p0")
			names, err := getNetNamesFromPath(tmpDir, pciAddress)
			Expect(err).NotTo(HaveOccurred())
			Expect(names).To(ConsistOf("eth_rail1"))
		})

		It("should return all interfaces when phys_port_name is not available", func() {
			// No phys_port_name file — isPhysicalPort returns true for backward compat
			createIface(pciAddress, "eth0", "")
			createIface(pciAddress, "eth1", "")
			names, err := getNetNamesFromPath(tmpDir, pciAddress)
			Expect(err).NotTo(HaveOccurred())
			Expect(names).To(ConsistOf("eth0", "eth1"))
		})

		It("should return empty list when all interfaces are representors", func() {
			createIface(pciAddress, "eth1", "pf0vf0")
			createIface(pciAddress, "eth2", "pf0vf1")
			names, err := getNetNamesFromPath(tmpDir, pciAddress)
			Expect(err).NotTo(HaveOccurred())
			Expect(names).To(BeEmpty())
		})

		It("should return error when net directory does not exist", func() {
			names, err := getNetNamesFromPath(tmpDir, pciAddress)
			Expect(err).To(HaveOccurred())
			Expect(names).To(BeNil())
		})
	})

	Describe("parseMGIRGa", func() {
		It("parses orchid ASIC #0", func() {
			output := "ga_valid | 0x00000001\nga       | 0x00000000\n"
			gaValid, ga, ok := parseMGIRGa([]byte(output))
			Expect(ok).To(BeTrue())
			Expect(gaValid).To(BeTrue())
			Expect(ga).To(Equal(0))
		})

		It("parses orchid ASIC #1", func() {
			output := "ga_valid | 0x00000001\nga       | 0x00000001\n"
			gaValid, ga, ok := parseMGIRGa([]byte(output))
			Expect(ok).To(BeTrue())
			Expect(gaValid).To(BeTrue())
			Expect(ga).To(Equal(1))
		})

		It("reports non-orchid when ga_valid is 0", func() {
			output := "ga_valid | 0x00000000\nga       | 0x00000000\n"
			gaValid, _, ok := parseMGIRGa([]byte(output))
			Expect(ok).To(BeTrue())
			Expect(gaValid).To(BeFalse())
		})

		It("reports not-ok when fields are missing", func() {
			_, _, ok := parseMGIRGa([]byte("some unrelated output\n"))
			Expect(ok).To(BeFalse())
		})
	})

	Describe("GetNetworkBayASIC", func() {
		newUtils := func(output []byte, cmdErr error) *deviceDiscoveryUtils {
			fakeExec := &execTesting.FakeExec{}
			cmd := &execTesting.FakeCmd{}
			cmd.CombinedOutputScript = append(cmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return output, nil, cmdErr
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(name string, args ...string) exec.Cmd {
					Expect(name).To(Equal("mlxreg"))
					Expect(args).To(Equal([]string{"-d", pciAddress, "--reg_name", "MGIR", "-g"}))
					return cmd
				},
			}
			return &deviceDiscoveryUtils{execInterface: fakeExec}
		}

		It("returns the ASIC index for an orchid device", func() {
			h := newUtils([]byte("ga_valid | 0x00000001\nga | 0x00000001\n"), nil)
			asic, isOrchid := h.GetNetworkBayASIC(pciAddress)
			Expect(isOrchid).To(BeTrue())
			Expect(asic).To(Equal(1))
		})

		It("returns not-orchid when ga_valid is 0", func() {
			h := newUtils([]byte("ga_valid | 0x00000000\nga | 0x00000000\n"), nil)
			_, isOrchid := h.GetNetworkBayASIC(pciAddress)
			Expect(isOrchid).To(BeFalse())
		})

		It("returns not-orchid when mlxreg fails", func() {
			h := newUtils([]byte("error"), errors.New("mlxreg not found"))
			_, isOrchid := h.GetNetworkBayASIC(pciAddress)
			Expect(isOrchid).To(BeFalse())
		})
	})
})
