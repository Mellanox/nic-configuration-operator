/*
2024 NVIDIA CORPORATION & AFFILIATES
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

package configuration

import (
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dms/mocks"
)

var _ = Describe("ConfigurationUtils", func() {
	Describe("GetPCILinkSpeed", func() {
		var (
			h        *configurationUtils
			fakeExec *execTesting.FakeExec
			pciAddr  string
		)

		BeforeEach(func() {
			fakeExec = &execTesting.FakeExec{}

			h = &configurationUtils{
				execInterface: fakeExec,
			}
		})

		It("should return the correct PCI link speed when lspci output is valid", func() {
			lspciOutput := `
03:00.0 Ethernet controller: Mellanox Technologies MT27800 Family [ConnectX-5]
    Subsystem: Mellanox Technologies Device 0000
    ...
        LnkSta:    Speed 8GT/s (ok), Width x8 (ok)
`

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript,
				func() ([]byte, []byte, error) {
					return []byte(lspciOutput), nil, nil
				},
			)

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("lspci"))
					Expect(args).To(Equal([]string{"-vv", "-s", pciAddr}))
					return fakeCmd
				},
			}

			speed, err := h.GetPCILinkSpeed(pciAddr)
			Expect(err).ToNot(HaveOccurred())
			Expect(speed).To(Equal(8))
		})

		It("should return an error when the speed value is not an integer", func() {
			// Mock lspci output with a decimal speed
			lspciOutput := `
03:00.0 Ethernet controller: Mellanox Technologies MT27800 Family [ConnectX-5]
    ...
        LnkSta:    Speed 2.5GT/s (ok), Width x8 (ok)
`

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript,
				func() ([]byte, []byte, error) {
					return []byte(lspciOutput), nil, nil
				},
			)

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("lspci"))
					Expect(args).To(Equal([]string{"-vv", "-s", pciAddr}))
					return fakeCmd
				},
			}

			speed, err := h.GetPCILinkSpeed(pciAddr)
			Expect(err).To(HaveOccurred())
			Expect(speed).To(Equal(-1))
		})

		It("should return -1 when the lspci output does not contain LnkSta line", func() {
			lspciOutput := `
03:00.0 Ethernet controller: Mellanox Technologies MT27800 Family [ConnectX-5]
    Subsystem: Mellanox Technologies Device 0000
    ...
`

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript,
				func() ([]byte, []byte, error) {
					return []byte(lspciOutput), nil, nil
				},
			)

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("lspci"))
					Expect(args).To(Equal([]string{"-vv", "-s", pciAddr}))
					return fakeCmd
				},
			}

			speed, err := h.GetPCILinkSpeed(pciAddr)
			Expect(err).ToNot(HaveOccurred())
			Expect(speed).To(Equal(-1))
		})

		It("should return an error when lspci command fails", func() {
			// Mock the command to fail
			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript,
				func() ([]byte, []byte, error) {
					return nil, nil, fmt.Errorf("lspci command failed")
				},
			)

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("lspci"))
					Expect(args).To(Equal([]string{"-vv", "-s", pciAddr}))
					return fakeCmd
				},
			}

			speed, err := h.GetPCILinkSpeed(pciAddr)
			Expect(err).To(HaveOccurred())
			Expect(speed).To(Equal(-1))
		})
	})
	Describe("GetMaxReadRequestSize", func() {
		var (
			h        *configurationUtils
			fakeExec *execTesting.FakeExec
			pciAddr  string
		)

		BeforeEach(func() {
			fakeExec = &execTesting.FakeExec{}

			h = &configurationUtils{
				execInterface: fakeExec,
			}
		})

		It("should return the correct PCI link speed when lspci output is valid", func() {
			lspciOutput := `
03:00.0 Ethernet controller: Mellanox Technologies MT27800 Family [ConnectX-5]
    Subsystem: Mellanox Technologies Device 0000
    ...
		DevCtl:	CorrErr- NonFatalErr- FatalErr- UnsupReq-
			RlxdOrd- ExtTag- PhantFunc- AuxPwr- NoSnoop- FLReset-
			MaxPayload 128 bytes, MaxReadReq 256 bytes
		DevSta:	CorrErr+ NonFatalErr- FatalErr+ UnsupReq- AuxPwr- TransPend-
`

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript,
				func() ([]byte, []byte, error) {
					return []byte(lspciOutput), nil, nil
				},
			)

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("lspci"))
					Expect(args).To(Equal([]string{"-vv", "-s", pciAddr}))
					return fakeCmd
				},
			}

			maxReadRequestSize, err := h.GetMaxReadRequestSize(pciAddr)
			Expect(err).ToNot(HaveOccurred())
			Expect(maxReadRequestSize).To(Equal(256))
		})

		It("should return -1 when the lspci output does not contain MaxReadReq line", func() {
			lspciOutput := `
03:00.0 Ethernet controller: Mellanox Technologies MT27800 Family [ConnectX-5]
    Subsystem: Mellanox Technologies Device 0000
    ...
`

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript,
				func() ([]byte, []byte, error) {
					return []byte(lspciOutput), nil, nil
				},
			)

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("lspci"))
					Expect(args).To(Equal([]string{"-vv", "-s", pciAddr}))
					return fakeCmd
				},
			}

			maxReadRequestSize, err := h.GetMaxReadRequestSize(pciAddr)
			Expect(err).ToNot(HaveOccurred())
			Expect(maxReadRequestSize).To(Equal(-1))
		})

		It("should return an error when lspci command fails", func() {
			// Mock the command to fail
			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript,
				func() ([]byte, []byte, error) {
					return nil, nil, fmt.Errorf("lspci command failed")
				},
			)

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("lspci"))
					Expect(args).To(Equal([]string{"-vv", "-s", pciAddr}))
					return fakeCmd
				},
			}

			maxReadRequestSize, err := h.GetMaxReadRequestSize(pciAddr)
			Expect(err).To(HaveOccurred())
			Expect(maxReadRequestSize).To(Equal(-1))
		})
	})
	Describe("GetTrustAndPFC", func() {
		It("should return parsed values", func() {
			device := &v1alpha1.NicDevice{
				Status: v1alpha1.NicDeviceStatus{
					SerialNumber: "serialnumber",
					Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:03:00.0", NetworkInterface: "enp3s0f0np0"}},
				},
			}
			trust := "dscp"
			pfc := "0,1,0,1,0,1,0,0"

			mockDMSManager := &mocks.DMSManager{}
			mockDMSClient := &mocks.DMSClient{}
			mockDMSManager.On("GetDMSClientBySerialNumber", "serialnumber").Return(mockDMSClient, nil)
			mockDMSClient.On("GetQoSSettings", "enp3s0f0np0").Return(&v1alpha1.QosSpec{Trust: trust, PFC: pfc}, nil)

			h := &configurationUtils{
				dmsManager:    mockDMSManager,
				execInterface: nil, // not used in this test
			}

			observedQoS, err := h.GetQoSSettings(device, "enp3s0f0np0")

			Expect(err).NotTo(HaveOccurred())
			Expect(observedQoS.Trust).To(Equal(trust))
			Expect(observedQoS.PFC).To(Equal(pfc))
		})
		It("should return empty string for both numbers if one is empty", func() {
			device := &v1alpha1.NicDevice{
				Status: v1alpha1.NicDeviceStatus{
					SerialNumber: "serialnumber",
					Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:03:00.0", NetworkInterface: "enp3s0f0np0"}},
				},
			}

			mockDMSManager := &mocks.DMSManager{}
			mockDMSClient := &mocks.DMSClient{}
			mockDMSManager.On("GetDMSClientBySerialNumber", "serialnumber").Return(mockDMSClient, nil)
			mockDMSClient.On("GetQoSSettings", "enp3s0f0np0").Return(&v1alpha1.QosSpec{}, nil)

			h := &configurationUtils{
				dmsManager:    mockDMSManager,
				execInterface: nil, // not used in this test
			}

			observedQoS, err := h.GetQoSSettings(device, "enp3s0f0np0")

			Expect(err).NotTo(HaveOccurred())
			Expect(observedQoS.Trust).To(Equal(""))
			Expect(observedQoS.PFC).To(Equal(""))
		})
	})
	Describe("SetMaxReadRequestSize", func() {
		var (
			h        *configurationUtils
			fakeExec *execTesting.FakeExec
			pciAddr  string
		)

		BeforeEach(func() {
			fakeExec = &execTesting.FakeExec{}

			h = &configurationUtils{
				execInterface: fakeExec,
			}
		})

		Context("with a valid maxReadRequestSize", func() {
			It("executes the setpci command successfully", func() {
				maxSize := 256
				valueToApply := 1

				fakeCmd := &execTesting.FakeCmd{}
				fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
					return nil, nil, nil
				})

				fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("setpci"))
					Expect(args).To(Equal([]string{"-s", pciAddr, fmt.Sprintf("CAP_EXP+08.w=%d000:F000", valueToApply)}))
					return fakeCmd
				})

				err := h.SetMaxReadRequestSize(pciAddr, maxSize)

				Expect(err).ToNot(HaveOccurred())
				Expect(fakeExec.CommandCalls).To(Equal(1))
			})
		})

		Context("with an invalid maxReadRequestSize", func() {
			It("returns an error without executing any command", func() {
				maxSize := 300

				err := h.SetMaxReadRequestSize(pciAddr, maxSize)

				Expect(err).To(MatchError(fmt.Sprintf(
					"unsupported maxReadRequestSize (%d) for pci device (%s). Acceptable values are powers of 2 from 128 to 4096",
					maxSize, pciAddr,
				)))
			})
		})

		Context("when the setpci command fails", func() {
			It("returns an error", func() {
				maxSize := 4096
				valueToApply := 5

				fakeCmd := &execTesting.FakeCmd{}
				fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
					return nil, nil, errors.New("some error")
				})

				fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("setpci"))
					Expect(args).To(Equal([]string{"-s", pciAddr, fmt.Sprintf("CAP_EXP+08.w=%d000:F000", valueToApply)}))
					return fakeCmd
				})

				err := h.SetMaxReadRequestSize(pciAddr, maxSize)

				Expect(err).To(HaveOccurred())
				Expect(fakeExec.CommandCalls).To(Equal(1))
			})
		})
	})
})
