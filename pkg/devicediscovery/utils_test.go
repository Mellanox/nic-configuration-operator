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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"
)

const pciAddress = "0000:03:00.0"

var _ = Describe("HostUtils", func() {
	//nolint:dupl
	Describe("GetVPD", func() {
		var originalBackoff time.Duration

		BeforeEach(func() {
			originalBackoff = mlxvpdBackoff
			mlxvpdBackoff = 1 * time.Millisecond
		})

		AfterEach(func() {
			mlxvpdBackoff = originalBackoff
		})

		It("should return part, serial and model name", func() {
			partNumber := "MCX623106AE-CDAT"
			serialNumber := "MT2235J01129"
			modelName := "ConnectX-6 Dx EN adapter card, 100GbE, Dual-port QSFP56, PCIe 4.0 x16, Crypto, No Secure Boot"

			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.CombinedOutputScript = append(fakeCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("  VPD-KEYWORD    DESCRIPTION             VALUE\n" +
						"  -----------    -----------             -----\n" +
						"Read Only Section:\n" +
						"\n" +
						"  PN             Part Number             MCX623106AE-CDAT\n" +
						"  EC             Revision                AB\n" +
						"  SN             Serial Number           MT2235J01129\n" +
						"  V3             N/A                     3869145f5722ed118000b83fd2193152\n" +
						"  IDTAG          Board Id                ConnectX-6 Dx EN adapter card, 100GbE, Dual-port QSFP56, PCIe 4.0 x16, Crypto, No Secure Boot\n"),
					nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mlxvpd"))
				Expect(args[0]).To(Equal("-d"))
				Expect(args[1]).To(Equal(pciAddress))
				return fakeCmd
			})

			h := &deviceDiscoveryUtils{
				execInterface: fakeExec,
			}

			vpd, err := h.GetVPD(pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(vpd.PartNumber).To(Equal(partNumber))
			Expect(vpd.SerialNumber).To(Equal(serialNumber))
			Expect(vpd.ModelName).To(Equal(modelName))
		})
		It("should return error when PN or SN is missing", func() {
			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.CombinedOutputScript = append(fakeCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("  SN             Serial Number           MT2235J01129\n"), nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mlxvpd"))
				Expect(args[0]).To(Equal("-d"))
				Expect(args[1]).To(Equal(pciAddress))
				return fakeCmd
			})

			h := &deviceDiscoveryUtils{
				execInterface: fakeExec,
			}

			vpd, err := h.GetVPD(pciAddress)

			Expect(err).To(HaveOccurred())
			Expect(vpd).To(BeNil())

			fakeCmd = &execTesting.FakeCmd{}
			fakeCmd.CombinedOutputScript = append(fakeCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("  PN             Part Number             MCX623106AE-CDAT\n"), nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mlxvpd"))
				Expect(args[0]).To(Equal("-d"))
				Expect(args[1]).To(Equal(pciAddress))
				return fakeCmd
			})

			vpd, err = h.GetVPD(pciAddress)

			Expect(err).To(HaveOccurred())
			Expect(vpd).To(BeNil())
		})
		It("should set empty model name when IDTAG is absent", func() {
			partNumber := "MCX623106AE-CDAT"
			serialNumber := "MT2235J01129"

			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.CombinedOutputScript = append(fakeCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("  VPD-KEYWORD    DESCRIPTION             VALUE\n" +
						"  -----------    -----------             -----\n" +
						"Read Only Section:\n" +
						"\n" +
						"  PN             Part Number             " + partNumber + "\n" +
						"  SN             Serial Number           " + serialNumber + "\n" +
						"  V3             N/A                     3869145f5722ed118000b83fd2193152\n"),
					nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mlxvpd"))
				Expect(args[0]).To(Equal("-d"))
				Expect(args[1]).To(Equal(pciAddress))
				return fakeCmd
			})

			h := &deviceDiscoveryUtils{
				execInterface: fakeExec,
			}

			vpd, err := h.GetVPD(pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(vpd.PartNumber).To(Equal(partNumber))
			Expect(vpd.SerialNumber).To(Equal(serialNumber))
			Expect(vpd.ModelName).To(Equal(""))
		})
		It("should retry and succeed on the 2nd attempt", func() {
			partNumber := "MCX623106AE-CDAT"
			serialNumber := "MT2235J01129"

			fakeExec := &execTesting.FakeExec{}

			failingCmd := &execTesting.FakeCmd{}
			failingCmd.CombinedOutputScript = append(failingCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return nil, nil, errors.New("exit status 2")
			})

			successCmd := &execTesting.FakeCmd{}
			successCmd.CombinedOutputScript = append(successCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("  PN             Part Number             " + partNumber + "\n" +
						"  SN             Serial Number           " + serialNumber + "\n"),
					nil, nil
			})

			cmds := []exec.Cmd{failingCmd, successCmd}
			fakeExec.CommandScript = append(fakeExec.CommandScript,
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("mlxvpd"))
					return cmds[0]
				},
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("mlxvpd"))
					return cmds[1]
				},
			)

			h := &deviceDiscoveryUtils{
				execInterface: fakeExec,
			}

			vpd, err := h.GetVPD(pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(vpd.PartNumber).To(Equal(partNumber))
			Expect(vpd.SerialNumber).To(Equal(serialNumber))
			Expect(fakeExec.CommandCalls).To(Equal(2))
		})
		It("should retry up to maxAttempts and return an error", func() {
			fakeExec := &execTesting.FakeExec{}

			newFailingCmd := func() *execTesting.FakeCmd {
				c := &execTesting.FakeCmd{}
				c.CombinedOutputScript = append(c.CombinedOutputScript, func() ([]byte, []byte, error) {
					return []byte("mlxvpd: failed to read VPD"), nil, errors.New("exit status 2")
				})
				return c
			}

			cmds := []exec.Cmd{newFailingCmd(), newFailingCmd(), newFailingCmd()}
			for i := range cmds {
				c := cmds[i]
				fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("mlxvpd"))
					return c
				})
			}

			h := &deviceDiscoveryUtils{
				execInterface: fakeExec,
			}

			vpd, err := h.GetVPD(pciAddress)

			Expect(err).To(HaveOccurred())
			Expect(vpd).To(BeNil())
			Expect(fakeExec.CommandCalls).To(Equal(mlxvpdMaxAttempts))
		})
	})
	//nolint:dupl
	Describe("GetFirmwareVersionAndPSID", func() {
		It("should return lowercased firmware version and psid", func() {
			fwVersion := "VeRsIoN"
			PSID := "PSID"

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
			}

			part, serial, err := h.GetFirmwareVersionAndPSID(pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(part).To(Equal(strings.ToLower(fwVersion)))
			Expect(serial).To(Equal(strings.ToLower(PSID)))
		})
		It("should return empty string for both numbers if one is empty", func() {
			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("FW Version: VeRsIoN"), nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("flint"))
				Expect(args[1]).To(Equal(pciAddress))
				return fakeCmd
			})

			h := &deviceDiscoveryUtils{
				execInterface: fakeExec,
			}

			part, serial, err := h.GetFirmwareVersionAndPSID(pciAddress)

			Expect(err).To(HaveOccurred())
			Expect(part).To(Equal(""))
			Expect(serial).To(Equal(""))

			fakeCmd = &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("PSID: PSID"), nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("flint"))
				Expect(args[1]).To(Equal(pciAddress))
				return fakeCmd
			})

			part, serial, err = h.GetFirmwareVersionAndPSID(pciAddress)

			Expect(err).To(HaveOccurred())
			Expect(part).To(Equal(""))
			Expect(serial).To(Equal(""))
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
})
