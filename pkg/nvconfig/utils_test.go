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

package nvconfig

import (
	"context"
	"fmt"
	"regexp"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"
)

func nvconfigPort(pciAddr string) v1alpha1.NicDevicePortSpec {
	return v1alpha1.NicDevicePortSpec{PCI: pciAddr}
}

var _ = Describe("NVConfigUtils", func() {
	Describe("parseMLXConfigValue", func() {
		var valueInBracketsRegex *regexp.Regexp

		BeforeEach(func() {
			valueInBracketsRegex = regexp.MustCompile(`^(.*?)\(([^)]*)\)$`)
		})

		DescribeTable("parses mlxconfig value encodings",
			func(value string, expected []string) {
				Expect(parseMLXConfigValue(value, valueInBracketsRegex)).To(Equal(expected))
			},
			Entry("enum with numeric value", "DEVICE_DEFAULT(0)", []string{"device_default", "0"}),
			Entry("enum containing underscore and digit", "HOST_0(1)", []string{"host_0", "1"}),
			Entry("uppercase enum alias", "DISABLED(0)", []string{"disabled", "0"}),
			Entry("plain numeric value", "148", []string{"148"}),
			Entry("plain symbolic value", "HOST_0", []string{"HOST_0"}),
			Entry("empty value", "", []string{""}),
		)
	})

	Describe("resolveDevice", func() {
		It("prefers fwctl when discovered", func() {
			port := v1alpha1.NicDevicePortSpec{PCI: "0000:3b:00.0", FwctlDevice: "/dev/fwctl/fwctl0"}
			Expect(resolveDevice(port)).To(Equal("/dev/fwctl/fwctl0"))
		})

		It("falls back to PCI when fwctl is empty", func() {
			Expect(resolveDevice(nvconfigPort("0000:3b:00.0"))).To(Equal("0000:3b:00.0"))
		})
	})

	Describe("queryMLXConfig", func() {
		var (
			h          *nvConfigUtils
			fakeExec   *execTesting.FakeExec
			pciAddress = "0000:3b:00.0"
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

			h = &nvConfigUtils{
				execInterface: fakeExec,
			}
		})

		It("should parse mlxconfig output correctly", func() {
			query, err := h.QueryNvConfig(context.TODO(), nvconfigPort(pciAddress), nil)
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

			query, err := h.QueryNvConfig(context.TODO(), nvconfigPort(pciAddress), nil)
			Expect(err).ToNot(HaveOccurred())

			Expect(query.DefaultConfig).To(BeEmpty())
			Expect(query.CurrentConfig).To(BeEmpty())
			Expect(query.NextBootConfig).To(BeEmpty())
		})

		It("should parse mlxconfig output correctly for identify if BlueField device is DPU mode", func() {
			cmd1 := &execTesting.FakeCmd{}
			cmd1.OutputScript = append(cmd1.OutputScript,
				func() ([]byte, []byte, error) {
					output := `
Device #1:
----------

Device type:        BlueField3
Configurations:                              Default         Current         Next Boot
         INTERNAL_CPU_OFFLOAD_ENGINE         ENABLED(0)      ENABLED(0)      ENABLED(0)
`
					return []byte(output), nil, nil
				},
			)

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("mlxconfig"))
					Expect(args).To(Equal([]string{"-d", pciAddress, "-e", "query", consts.BF3OperationModeParam}))
					return cmd1
				},
			}

			h.execInterface = fakeExec

			query, err := h.QueryNvConfig(context.TODO(), nvconfigPort(pciAddress), []string{consts.BF3OperationModeParam})
			Expect(err).ToNot(HaveOccurred())

			// Verify regular parameters
			Expect(query.DefaultConfig).To(HaveKeyWithValue("INTERNAL_CPU_OFFLOAD_ENGINE", []string{"enabled", "0"}))
			Expect(query.CurrentConfig).To(HaveKeyWithValue("INTERNAL_CPU_OFFLOAD_ENGINE", []string{"enabled", "0"}))
			Expect(query.NextBootConfig).To(HaveKeyWithValue("INTERNAL_CPU_OFFLOAD_ENGINE", []string{"enabled", "0"}))
		})

		It("should parse array values when mlxconfig returns mixed enum and raw current values", func() {
			cmd1 := &execTesting.FakeCmd{}
			cmd1.OutputScript = append(cmd1.OutputScript,
				func() ([]byte, []byte, error) {
					output := `
Device #1:
----------

Device type:        ConnectX9
Configurations:                                         Default                           Current                           Next Boot
        PCI_DOWNSTREAM_PORT_OWNER                       Array[0..15]                      Array[0..15]                      Array[0..15]
`
					return []byte(output), nil, nil
				},
			)

			cmd2 := &execTesting.FakeCmd{}
			cmd2.OutputScript = append(cmd2.OutputScript,
				func() ([]byte, []byte, error) {
					output := `
Device #1:
----------

Device type:        ConnectX9
Configurations:                                         Default                           Current                           Next Boot
*       PCI_DOWNSTREAM_PORT_OWNER[0]                    DEVICE_DEFAULT(0)                 148                               DEVICE_DEFAULT(0)
*       PCI_DOWNSTREAM_PORT_OWNER[1]                    DEVICE_DEFAULT(0)                 HOST_0(1)                         DEVICE_DEFAULT(0)
        PCI_DOWNSTREAM_PORT_OWNER[2]                    DEVICE_DEFAULT(0)                 DEVICE_DEFAULT(0)                 DEVICE_DEFAULT(0)
*       PCI_DOWNSTREAM_PORT_OWNER[4]                    DEVICE_DEFAULT(0)                 164                               DEVICE_DEFAULT(0)
The '*' shows parameters with next value different from default/current value.
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
					Expect(args).To(Equal([]string{"-d", pciAddress, "-e", "query", "PCI_DOWNSTREAM_PORT_OWNER[0..15]"}))
					return cmd2
				},
			}

			h.execInterface = fakeExec

			query, err := h.QueryNvConfig(context.TODO(), nvconfigPort(pciAddress), nil)
			Expect(err).ToNot(HaveOccurred())

			Expect(query.DefaultConfig).To(HaveKeyWithValue("PCI_DOWNSTREAM_PORT_OWNER[0]", []string{"device_default", "0"}))
			Expect(query.CurrentConfig).To(HaveKeyWithValue("PCI_DOWNSTREAM_PORT_OWNER[0]", []string{"148"}))
			Expect(query.NextBootConfig).To(HaveKeyWithValue("PCI_DOWNSTREAM_PORT_OWNER[0]", []string{"device_default", "0"}))

			Expect(query.DefaultConfig).To(HaveKeyWithValue("PCI_DOWNSTREAM_PORT_OWNER[1]", []string{"device_default", "0"}))
			Expect(query.CurrentConfig).To(HaveKeyWithValue("PCI_DOWNSTREAM_PORT_OWNER[1]", []string{"host_0", "1"}))
			Expect(query.NextBootConfig).To(HaveKeyWithValue("PCI_DOWNSTREAM_PORT_OWNER[1]", []string{"device_default", "0"}))

			Expect(query.DefaultConfig).To(HaveKeyWithValue("PCI_DOWNSTREAM_PORT_OWNER[4]", []string{"device_default", "0"}))
			Expect(query.CurrentConfig).To(HaveKeyWithValue("PCI_DOWNSTREAM_PORT_OWNER[4]", []string{"164"}))
			Expect(query.NextBootConfig).To(HaveKeyWithValue("PCI_DOWNSTREAM_PORT_OWNER[4]", []string{"device_default", "0"}))
		})

		It("should parse default, current, and next boot column formats independently", func() {
			cmd1 := &execTesting.FakeCmd{}
			cmd1.OutputScript = append(cmd1.OutputScript,
				func() ([]byte, []byte, error) {
					output := `
Device #1:
----------

Device type:        ConnectX9
Configurations:                                         Default                           Current                           Next Boot
        MIXED_FORMAT_PARAM                              7                                 HOST_0(1)                         DEVICE_DEFAULT(0)
        PLAIN_FORMAT_PARAM                              32                                64                                128
        ENUM_FORMAT_PARAM                               DISABLED(0)                       ENABLED(1)                        ENABLED(1)
`
					return []byte(output), nil, nil
				},
			)

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					Expect(cmd).To(Equal("mlxconfig"))
					Expect(args).To(Equal([]string{"-d", pciAddress, "-e", "query", "MIXED_FORMAT_PARAM"}))
					return cmd1
				},
			}

			h.execInterface = fakeExec

			query, err := h.QueryNvConfig(context.TODO(), nvconfigPort(pciAddress), []string{"MIXED_FORMAT_PARAM"})
			Expect(err).ToNot(HaveOccurred())

			Expect(query.DefaultConfig).To(HaveKeyWithValue("MIXED_FORMAT_PARAM", []string{"7"}))
			Expect(query.CurrentConfig).To(HaveKeyWithValue("MIXED_FORMAT_PARAM", []string{"host_0", "1"}))
			Expect(query.NextBootConfig).To(HaveKeyWithValue("MIXED_FORMAT_PARAM", []string{"device_default", "0"}))

			Expect(query.DefaultConfig).To(HaveKeyWithValue("PLAIN_FORMAT_PARAM", []string{"32"}))
			Expect(query.CurrentConfig).To(HaveKeyWithValue("PLAIN_FORMAT_PARAM", []string{"64"}))
			Expect(query.NextBootConfig).To(HaveKeyWithValue("PLAIN_FORMAT_PARAM", []string{"128"}))

			Expect(query.DefaultConfig).To(HaveKeyWithValue("ENUM_FORMAT_PARAM", []string{"disabled", "0"}))
			Expect(query.CurrentConfig).To(HaveKeyWithValue("ENUM_FORMAT_PARAM", []string{"enabled", "1"}))
			Expect(query.NextBootConfig).To(HaveKeyWithValue("ENUM_FORMAT_PARAM", []string{"enabled", "1"}))
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

			_, err := h.QueryNvConfig(context.TODO(), nvconfigPort(pciAddress), nil)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("mlxconfig error"))
		})
	})

	Describe("systemConfToken", func() {
		It("formats conf and asic index", func() {
			Expect(systemConfToken("conf3", 0)).To(Equal("conf3[0]"))
			Expect(systemConfToken("conf16", 1)).To(Equal("conf16[1]"))
		})
	})

	Describe("parseValidateSystemConf", func() {
		It("parses an all-OK MATCHES output with skipped params", func() {
			output := `Validating system configuration 'conf6[1]' on device 0001:03:00.0
------------------------------------------------------------------------
  OK:       BOARD_CONFIGURATION_MODE = 1
  OK:       MODULE_SPLIT_M0[0] = 0x1
  OK:       LINK_TYPE_P1 = IB
  OK:       NUM_OF_PLANES_P1 = 4
  OK:       NUM_OF_PF = 1
  SKIPPED (failed to query):
    - MODULE_SPLIT_M1[0]
    - MODULE_SPLIT_M1[1]
------------------------------------------------------------------------
Result: Device configuration MATCHES the system configuration.
`
			matches, mismatched, foundResult, err := parseValidateSystemConf([]byte(output))
			Expect(err).ToNot(HaveOccurred())
			Expect(foundResult).To(BeTrue())
			Expect(matches).To(BeTrue())
			Expect(mismatched).To(BeEmpty())
		})

		It("parses a mixed MISMATCH/OK output and reports does NOT match", func() {
			output := `Validating system configuration 'conf3[1]' on device 0001:03:00.0
------------------------------------------------------------------------
  MISMATCH: BOARD_CONFIGURATION_MODE	Expected: 0	Actual: 1
  MISMATCH: MODULE_SPLIT_M0[4]	Expected: 0x1	Actual: 0xFF
  OK:       MODULE_SPLIT_M0[8] = 0xFF
  MISMATCH: LINK_TYPE_P1	Expected: ETH	Actual: IB
  MISMATCH: NUM_OF_PF	Expected: 4	Actual: 1
  SKIPPED (failed to query):
    - MODULE_SPLIT_M1[0]
    - LINK_TYPE_P2
------------------------------------------------------------------------
Result: Device configuration does NOT match the system configuration.
`
			matches, mismatched, foundResult, err := parseValidateSystemConf([]byte(output))
			Expect(err).ToNot(HaveOccurred())
			Expect(foundResult).To(BeTrue())
			Expect(matches).To(BeFalse())
			Expect(mismatched).To(ConsistOf("BOARD_CONFIGURATION_MODE", "MODULE_SPLIT_M0[4]", "LINK_TYPE_P1", "NUM_OF_PF"))
		})

		It("derives matches from rows when no Result line is present", func() {
			output := `  OK:       NUM_OF_PF = 1
  MISMATCH: LINK_TYPE_P1	Expected: ETH	Actual: IB
`
			matches, mismatched, foundResult, err := parseValidateSystemConf([]byte(output))
			Expect(err).ToNot(HaveOccurred())
			Expect(foundResult).To(BeFalse())
			Expect(matches).To(BeFalse())
			Expect(mismatched).To(ConsistOf("LINK_TYPE_P1"))
		})

		It("reports foundResult=true when a Result line is present", func() {
			output := `  OK:       NUM_OF_PF = 1
Result: Device configuration MATCHES the system configuration.
`
			_, _, foundResult, err := parseValidateSystemConf([]byte(output))
			Expect(err).ToNot(HaveOccurred())
			Expect(foundResult).To(BeTrue())
		})

		It("counts an OK row even when the '=' is absent (no spurious parse error)", func() {
			matches, mismatched, _, err := parseValidateSystemConf([]byte("  OK:       NUM_OF_PF\nResult: Device configuration MATCHES the system configuration.\n"))
			Expect(err).ToNot(HaveOccurred())
			Expect(matches).To(BeTrue())
			Expect(mismatched).To(BeEmpty())
		})

		It("does not mis-classify a stray '-' line after a SKIPPED block that a header interrupted", func() {
			output := `  SKIPPED (failed to query):
    - MODULE_SPLIT_M1[0]
Validating system configuration 'conf3[1]' on device 0001:03:00.1
    - SHOULD_NOT_BE_SKIPPED
Result: Device configuration does NOT match the system configuration.
`
			matches, mismatched, foundResult, err := parseValidateSystemConf([]byte(output))
			Expect(err).ToNot(HaveOccurred())
			Expect(foundResult).To(BeTrue())
			Expect(matches).To(BeFalse())
			Expect(mismatched).To(BeEmpty())
		})

		It("does not error on a SKIPPED-only output (rows seen, no Result line)", func() {
			output := `  SKIPPED (failed to query):
    - MODULE_SPLIT_M1[0]
`
			matches, mismatched, foundResult, err := parseValidateSystemConf([]byte(output))
			Expect(err).ToNot(HaveOccurred())
			Expect(foundResult).To(BeFalse())
			Expect(matches).To(BeTrue())
			Expect(mismatched).To(BeEmpty())
		})

		It("returns an error when the output cannot be parsed", func() {
			_, _, _, err := parseValidateSystemConf([]byte("some unexpected output"))
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("SetSystemConf", func() {
		var (
			h          *nvConfigUtils
			fakeExec   *execTesting.FakeExec
			pciAddress = "0000:0b:00.0"
		)

		BeforeEach(func() {
			fakeExec = &execTesting.FakeExec{}
			h = &nvConfigUtils{execInterface: fakeExec}
		})

		It("builds args without --force", func() {
			cmd := &execTesting.FakeCmd{}
			cmd.CombinedOutputScript = append(cmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("ok"), nil, nil
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(name string, args ...string) exec.Cmd {
					Expect(name).To(Equal("mlxconfig"))
					Expect(args).To(Equal([]string{"-d", pciAddress, "-y", "set_system_conf", "conf3[0]"}))
					return cmd
				},
			}
			Expect(h.SetSystemConf(context.TODO(), nvconfigPort(pciAddress), "conf3", 0, false)).To(Succeed())
		})

		It("uses fwctl device when present", func() {
			cmd := &execTesting.FakeCmd{}
			cmd.CombinedOutputScript = append(cmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("ok"), nil, nil
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(name string, args ...string) exec.Cmd {
					Expect(args).To(Equal([]string{"-d", "/dev/fwctl/fwctl2", "-y", "set_system_conf", "conf3[0]"}))
					return cmd
				},
			}
			port := v1alpha1.NicDevicePortSpec{PCI: pciAddress, FwctlDevice: "/dev/fwctl/fwctl2"}
			Expect(h.SetSystemConf(context.TODO(), port, "conf3", 0, false)).To(Succeed())
		})

		It("builds args with --force", func() {
			cmd := &execTesting.FakeCmd{}
			cmd.CombinedOutputScript = append(cmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("ok"), nil, nil
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(name string, args ...string) exec.Cmd {
					Expect(args).To(Equal([]string{"-d", pciAddress, "-y", "--force", "set_system_conf", "conf3[1]"}))
					return cmd
				},
			}
			Expect(h.SetSystemConf(context.TODO(), nvconfigPort(pciAddress), "conf3", 1, true)).To(Succeed())
		})

		It("returns an error when mlxconfig fails", func() {
			cmd := &execTesting.FakeCmd{}
			cmd.CombinedOutputScript = append(cmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("boom"), nil, fmt.Errorf("set failed")
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(name string, args ...string) exec.Cmd { return cmd },
			}
			Expect(h.SetSystemConf(context.TODO(), nvconfigPort(pciAddress), "conf3", 0, false)).ToNot(Succeed())
		})
	})

	Describe("ValidateSystemConf", func() {
		var (
			h          *nvConfigUtils
			fakeExec   *execTesting.FakeExec
			pciAddress = "0000:0e:00.0"
		)

		BeforeEach(func() {
			fakeExec = &execTesting.FakeExec{}
			h = &nvConfigUtils{execInterface: fakeExec}
		})

		It("runs validate_system_conf and reports a match", func() {
			cmd := &execTesting.FakeCmd{}
			cmd.CombinedOutputScript = append(cmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("Result: Device configuration MATCHES the system configuration.\n"), nil, nil
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(name string, args ...string) exec.Cmd {
					Expect(name).To(Equal("mlxconfig"))
					Expect(args).To(Equal([]string{"-d", pciAddress, "-y", "validate_system_conf", "conf16[0]"}))
					return cmd
				},
			}
			matches, mismatched, err := h.ValidateSystemConf(context.TODO(), nvconfigPort(pciAddress), "conf16", 0)
			Expect(err).ToNot(HaveOccurred())
			Expect(matches).To(BeTrue())
			Expect(mismatched).To(BeEmpty())
		})

		It("reports a mismatch even when mlxconfig exits non-zero", func() {
			// validate_system_conf exits 3 on a mismatch; the result line is authoritative.
			output := `Validating system configuration 'conf9[0]' on device 0001:03:00.0
  OK:       BOARD_CONFIGURATION_MODE = 1
  MISMATCH: LINK_TYPE_P1    Expected: ETH    Actual: IB
  SKIPPED (failed to query):
    - LINK_TYPE_P2
Result: Device configuration does NOT match the system configuration.
`
			cmd := &execTesting.FakeCmd{}
			cmd.CombinedOutputScript = append(cmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte(output), nil, fmt.Errorf("exit status 3")
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(name string, args ...string) exec.Cmd { return cmd },
			}
			matches, mismatched, err := h.ValidateSystemConf(context.TODO(), nvconfigPort(pciAddress), "conf3", 1)
			Expect(err).ToNot(HaveOccurred())
			Expect(matches).To(BeFalse())
			Expect(mismatched).To(ConsistOf("LINK_TYPE_P1"))
		})

		It("returns an error when mlxconfig fails", func() {
			cmd := &execTesting.FakeCmd{}
			cmd.CombinedOutputScript = append(cmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("boom"), nil, fmt.Errorf("validate failed")
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(name string, args ...string) exec.Cmd { return cmd },
			}
			_, _, err := h.ValidateSystemConf(context.TODO(), nvconfigPort(pciAddress), "conf3", 0)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("SetNvConfigParametersBatch", func() {
		var (
			h          *nvConfigUtils
			fakeExec   *execTesting.FakeExec
			pciAddress = "0000:3b:00.0"
		)

		BeforeEach(func() {
			fakeExec = &execTesting.FakeExec{}
			h = &nvConfigUtils{execInterface: fakeExec}
		})

		It("appends --force when force is true", func() {
			cmd := &execTesting.FakeCmd{}
			cmd.OutputScript = append(cmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("ok"), nil, nil
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(name string, args ...string) exec.Cmd {
					Expect(name).To(Equal("mlxconfig"))
					Expect(args).To(Equal([]string{"-d", pciAddress, "--yes", "--force", "set", "PARAM=1"}))
					return cmd
				},
			}
			Expect(h.SetNvConfigParametersBatch(nvconfigPort(pciAddress), map[string]string{"PARAM": "1"}, false, true)).To(Succeed())
		})

		It("omits --force when force is false", func() {
			cmd := &execTesting.FakeCmd{}
			cmd.OutputScript = append(cmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("ok"), nil, nil
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(name string, args ...string) exec.Cmd {
					Expect(args).To(Equal([]string{"-d", pciAddress, "--yes", "set", "PARAM=1"}))
					return cmd
				},
			}
			Expect(h.SetNvConfigParametersBatch(nvconfigPort(pciAddress), map[string]string{"PARAM": "1"}, false, false)).To(Succeed())
		})

		It("uses fwctl device when present", func() {
			cmd := &execTesting.FakeCmd{}
			cmd.OutputScript = append(cmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("ok"), nil, nil
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(name string, args ...string) exec.Cmd {
					Expect(args).To(Equal([]string{"-d", "/dev/fwctl/fwctl3", "--yes", "set", "PARAM=1"}))
					return cmd
				},
			}
			port := v1alpha1.NicDevicePortSpec{PCI: pciAddress, FwctlDevice: "/dev/fwctl/fwctl3"}
			Expect(h.SetNvConfigParametersBatch(port, map[string]string{"PARAM": "1"}, false, false)).To(Succeed())
		})
	})
})
