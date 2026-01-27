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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"
)

const pciAddress = "0000:03:00.0"

var _ = Describe("HostUtils", func() {
	//nolint:dupl
	Describe("GetVPD", func() {
		It("should return part, serial and model name", func() {
			partNumber := "MCX623106AE-CDAT"
			serialNumber := "MT2235J01129"
			modelName := "ConnectX-6 Dx EN adapter card, 100GbE, Dual-port QSFP56, PCIe 4.0 x16, Crypto, No Secure Boot"

			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
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
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
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
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
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
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
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
})
