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

package host

import (
	"context"
	"errors"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"
)

const pciAddress = "0000:03:00.0"

var _ = Describe("HostUtils", func() {
	Describe("GetPCILinkSpeed", func() {
		var (
			h        *hostUtils
			fakeExec *execTesting.FakeExec
			pciAddr  string
		)

		BeforeEach(func() {
			fakeExec = &execTesting.FakeExec{}

			h = &hostUtils{
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
	Describe("queryMLXConfig", func() {
		var (
			h        *hostUtils
			fakeExec *execTesting.FakeExec
		)

		BeforeEach(func() {
			fakeExec = &execTesting.FakeExec{}

			// Prepare the fake commands and their outputs
			// First command: mlxconfig -d 0000:03:00.0 q
			cmd1 := &execTesting.FakeCmd{}
			cmd1.OutputScript = append(cmd1.OutputScript,
				func() ([]byte, []byte, error) {
					output := `
Device #1:
----------

Device type:    ConnectX4
Configurations:                              Default         Current         Next Boot
*        KEEP_IB_LINK_UP_P2                  False(0)        True(1)         True(1)
         KEEP_LINK_UP_ON_BOOT_P2             False(0)        False(0)        False(0)
         ESWITCH_HAIRPIN_DESCRIPTORS         Array[0..7]     Array[0..7]     Array[0..7]
*        MEMIC_SIZE_LIMIT                    _256KB(1)       _256KB(1)       DISABLED(0)
The '*' shows parameters with next value different from default/current value.
`
					return []byte(output), nil, nil
				},
			)

			// Second command: mlxconfig -d 0000:03:00.0 q ESWITCH_HAIRPIN_DESCRIPTORS[0..7]
			cmd2 := &execTesting.FakeCmd{}
			cmd2.OutputScript = append(cmd2.OutputScript,
				func() ([]byte, []byte, error) {
					output := `
Configurations:                              Default         Current         Next Boot
         ESWITCH_HAIRPIN_DESCRIPTORS[0]      128             128             128
         ESWITCH_HAIRPIN_DESCRIPTORS[1]      128             128             128
         ESWITCH_HAIRPIN_DESCRIPTORS[2]      128             128             128
         ESWITCH_HAIRPIN_DESCRIPTORS[3]      128             128             128
         ESWITCH_HAIRPIN_DESCRIPTORS[4]      128             128             128
         ESWITCH_HAIRPIN_DESCRIPTORS[5]      128             128             128
         ESWITCH_HAIRPIN_DESCRIPTORS[6]      128             128             128
         ESWITCH_HAIRPIN_DESCRIPTORS[7]      128             128             128
`
					return []byte(output), nil, nil
				},
			)

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("mlxconfig"))
					Expect(args).To(Equal([]string{"-d", pciAddress, "-e", "query"}))
					return cmd1
				},
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("mlxconfig"))
					Expect(args).To(Equal([]string{"-d", pciAddress, "-e", "query", "ESWITCH_HAIRPIN_DESCRIPTORS[0..7]"}))
					return cmd2
				},
			}

			h = &hostUtils{
				execInterface: fakeExec,
			}
		})

		It("should parse mlxconfig output correctly", func() {
			query, err := h.QueryNvConfig(context.TODO(), pciAddress)
			Expect(err).ToNot(HaveOccurred())

			// Verify regular parameters
			Expect(query.DefaultConfig).To(HaveKeyWithValue("KEEP_IB_LINK_UP_P2", []string{"false", "0"}))
			Expect(query.CurrentConfig).To(HaveKeyWithValue("KEEP_IB_LINK_UP_P2", []string{"true", "1"}))
			Expect(query.NextBootConfig).To(HaveKeyWithValue("KEEP_IB_LINK_UP_P2", []string{"true", "1"}))

			Expect(query.DefaultConfig).To(HaveKeyWithValue("MEMIC_SIZE_LIMIT", []string{"_256kb", "1"}))
			Expect(query.CurrentConfig).To(HaveKeyWithValue("MEMIC_SIZE_LIMIT", []string{"_256kb", "1"}))
			Expect(query.NextBootConfig).To(HaveKeyWithValue("MEMIC_SIZE_LIMIT", []string{"disabled", "0"}))

			Expect(query.DefaultConfig).To(HaveKeyWithValue("KEEP_LINK_UP_ON_BOOT_P2", []string{"false", "0"}))
			Expect(query.CurrentConfig).To(HaveKeyWithValue("KEEP_LINK_UP_ON_BOOT_P2", []string{"false", "0"}))
			Expect(query.NextBootConfig).To(HaveKeyWithValue("KEEP_LINK_UP_ON_BOOT_P2", []string{"false", "0"}))

			// Verify array parameters
			for i := 0; i <= 7; i++ {
				key := fmt.Sprintf("ESWITCH_HAIRPIN_DESCRIPTORS[%d]", i)
				Expect(query.DefaultConfig).To(HaveKeyWithValue(key, []string{"128"}))
				Expect(query.CurrentConfig).To(HaveKeyWithValue(key, []string{"128"}))
				Expect(query.NextBootConfig).To(HaveKeyWithValue(key, []string{"128"}))
			}
		})

		It("should handle missing configurations section gracefully", func() {
			cmd1 := &execTesting.FakeCmd{}
			cmd1.OutputScript = append(cmd1.OutputScript,
				func() ([]byte, []byte, error) {
					output := `
Device #1:
----------

Device type:    ConnectX4
`
					return []byte(output), nil, nil
				},
			)

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("mlxconfig"))
					Expect(args).To(Equal([]string{"-d", pciAddress, "-e", "query"}))
					return cmd1
				},
			}

			h.execInterface = fakeExec

			query, err := h.QueryNvConfig(context.TODO(), pciAddress)
			Expect(err).ToNot(HaveOccurred())

			Expect(query.DefaultConfig).To(BeEmpty())
			Expect(query.CurrentConfig).To(BeEmpty())
			Expect(query.NextBootConfig).To(BeEmpty())
		})

		It("should return error if mlxconfig command fails", func() {
			// Set the command to return an error
			cmd1 := &execTesting.FakeCmd{}
			cmd1.OutputScript = append(cmd1.OutputScript,
				func() ([]byte, []byte, error) {
					return nil, nil, fmt.Errorf("mlxconfig error")
				},
			)

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("mlxconfig"))
					Expect(args).To(Equal([]string{"-d", pciAddress, "-e", "query"}))
					return cmd1
				},
			}

			h.execInterface = fakeExec

			_, err := h.QueryNvConfig(context.TODO(), pciAddress)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("mlxconfig error"))
		})
	})
	Describe("GetMaxReadRequestSize", func() {
		var (
			h        *hostUtils
			fakeExec *execTesting.FakeExec
			pciAddr  string
		)

		BeforeEach(func() {
			fakeExec = &execTesting.FakeExec{}

			h = &hostUtils{
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
			interfaceName := "enp3s0f0np0"
			trust := "dscp"
			pfc := "0,1,0,1,0,1,0,0"

			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.CombinedOutputScript = append(fakeCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("irrelevant line\n" +
						"Priority trust state: dscp\n" +
						"           enabled  0   1   0   1   0   1   0   0  \n" +
						"another irrelevant line"),
					nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mlnx_qos"))
				Expect(args).To(Equal([]string{"-i", interfaceName}))
				return fakeCmd
			})

			h := &hostUtils{
				execInterface: fakeExec,
			}

			observedTrust, observedPFC, err := h.GetTrustAndPFC(interfaceName)

			Expect(err).NotTo(HaveOccurred())
			Expect(observedTrust).To(Equal(strings.ToLower(trust)))
			Expect(observedPFC).To(Equal(strings.ToLower(pfc)))
		})
		It("should return empty string for both numbers if one is empty", func() {
			interfaceName := "enp3s0f0np0"

			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.CombinedOutputScript = append(fakeCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("irrelevant line\n" +
						"Priority trust state: dscp\n" +
						"another irrelevant line"),
					nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mlnx_qos"))
				Expect(args).To(Equal([]string{"-i", interfaceName}))
				return fakeCmd
			})

			h := &hostUtils{
				execInterface: fakeExec,
			}

			observedTrust, observedPFC, err := h.GetTrustAndPFC(interfaceName)

			Expect(err).To(HaveOccurred())
			Expect(observedTrust).To(Equal(""))
			Expect(observedPFC).To(Equal(""))
		})
	})
	Describe("SetMaxReadRequestSize", func() {
		var (
			h        *hostUtils
			fakeExec *execTesting.FakeExec
			pciAddr  string
		)

		BeforeEach(func() {
			fakeExec = &execTesting.FakeExec{}

			h = &hostUtils{
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
