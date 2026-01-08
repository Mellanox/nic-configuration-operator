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

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"
)

var _ = Describe("NVConfigUtils", func() {
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
			query, err := h.QueryNvConfig(context.TODO(), pciAddress, "")
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

			query, err := h.QueryNvConfig(context.TODO(), pciAddress, "")
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

			query, err := h.QueryNvConfig(context.TODO(), pciAddress, consts.BF3OperationModeParam)
			Expect(err).ToNot(HaveOccurred())

			// Verify regular parameters
			Expect(query.DefaultConfig).To(HaveKeyWithValue("INTERNAL_CPU_OFFLOAD_ENGINE", []string{"enabled", "0"}))
			Expect(query.CurrentConfig).To(HaveKeyWithValue("INTERNAL_CPU_OFFLOAD_ENGINE", []string{"enabled", "0"}))
			Expect(query.NextBootConfig).To(HaveKeyWithValue("INTERNAL_CPU_OFFLOAD_ENGINE", []string{"enabled", "0"}))
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

			_, err := h.QueryNvConfig(context.TODO(), pciAddress, "")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(Equal("mlxconfig error"))
		})
	})
})
