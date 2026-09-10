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

package nvconfig

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	execUtils "k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

var _ = Describe("system configuration profiles", func() {
	const showSystemConfOutput = `System configurations available for ConnectX9 devices:

Configuration name: conf2 - Single port
Description: another profile
----------------------------------------------------------------
    ASIC[0] Description:
        BOARD_CONFIGURATION_MODE=1 NUM_OF_PF=1

Configuration name: conf3 - Dual ASIC Network Bay
Description: selected profile
----------------------------------------------------------------
    ASIC[0] Description:
        BOARD_CONFIGURATION_MODE=0 MODULE_SPLIT_M0[0]=1 MODULE_SPLIT_M0[4..6]=FF LINK_TYPE_P1=2
    ASIC[1] Description:
        BOARD_CONFIGURATION_MODE=1 MODULE_SPLIT_M1[0..1]=7
`

	Describe("parseShowSystemConf", func() {
		It("selects the requested profile and ASIC and expands ranged parameters", func() {
			params, err := parseShowSystemConf([]byte(showSystemConfOutput), "conf3", 0)

			Expect(err).NotTo(HaveOccurred())
			Expect(params).To(Equal(map[string]string{
				"BOARD_CONFIGURATION_MODE": "0",
				"MODULE_SPLIT_M0[0]":       "1",
				"MODULE_SPLIT_M0[4]":       "FF",
				"MODULE_SPLIT_M0[5]":       "FF",
				"MODULE_SPLIT_M0[6]":       "FF",
				"LINK_TYPE_P1":             "2",
			}))
		})

		It("selects a different ASIC independently", func() {
			params, err := parseShowSystemConf([]byte(showSystemConfOutput), "conf3", 1)

			Expect(err).NotTo(HaveOccurred())
			Expect(params).To(Equal(map[string]string{
				"BOARD_CONFIGURATION_MODE": "1",
				"MODULE_SPLIT_M1[0]":       "7",
				"MODULE_SPLIT_M1[1]":       "7",
			}))
		})

		It("returns an error when the profile is absent", func() {
			_, err := parseShowSystemConf([]byte(showSystemConfOutput), "conf99", 0)
			Expect(err).To(MatchError(ContainSubstring(`configuration "conf99" was not found`)))
		})

		It("returns an error when the ASIC is absent", func() {
			_, err := parseShowSystemConf([]byte(showSystemConfOutput), "conf3", 2)
			Expect(err).To(MatchError(ContainSubstring(`ASIC 2 was not found`)))
		})

		It("rejects descending ranges", func() {
			output := "Configuration name: conf3 - profile\nASIC[0] Description:\nPARAM[3..1]=FF\n"
			_, err := parseShowSystemConf([]byte(output), "conf3", 0)
			Expect(err).To(MatchError(ContainSubstring("invalid descending range")))
		})

		It("rejects duplicate concrete parameters", func() {
			output := "Configuration name: conf3 - profile\nASIC[0] Description:\nPARAM[0..1]=1 PARAM[1]=2\n"
			_, err := parseShowSystemConf([]byte(output), "conf3", 0)
			Expect(err).To(MatchError(ContainSubstring(`duplicate parameter "PARAM[1]"`)))
		})
	})

	Describe("GetSystemConfParams", func() {
		var (
			h        *nvConfigUtils
			fakeExec *execTesting.FakeExec
		)

		BeforeEach(func() {
			fakeExec = &execTesting.FakeExec{}
			h = &nvConfigUtils{execInterface: fakeExec}
		})

		It("runs show_system_conf against the fwctl target", func() {
			cmd := &execTesting.FakeCmd{}
			cmd.CombinedOutputScript = append(cmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte(showSystemConfOutput), nil, nil
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(name string, args ...string) execUtils.Cmd {
					Expect(name).To(Equal("mlxconfig"))
					Expect(args).To(Equal([]string{"-d", "/dev/fwctl/fwctl2", "show_system_conf"}))
					return cmd
				},
			}
			port := v1alpha1.NicDevicePortSpec{PCI: "0000:0b:00.0", FwctlDevice: "/dev/fwctl/fwctl2"}

			params, err := h.GetSystemConfParams(context.TODO(), port, "conf3", 1)

			Expect(err).NotTo(HaveOccurred())
			Expect(params).To(HaveKeyWithValue("MODULE_SPLIT_M1[1]", "7"))
		})

		It("includes command output when mlxconfig fails", func() {
			cmd := &execTesting.FakeCmd{}
			cmd.CombinedOutputScript = append(cmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("device unsupported"), nil, fmt.Errorf("exit status 1")
			})
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(_ string, _ ...string) execUtils.Cmd { return cmd },
			}

			_, err := h.GetSystemConfParams(context.TODO(), nvconfigPort("0000:0b:00.0"), "conf3", 0)

			Expect(err).To(MatchError(ContainSubstring("device unsupported")))
		})
	})
})
