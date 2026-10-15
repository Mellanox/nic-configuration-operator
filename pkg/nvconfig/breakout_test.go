/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES
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
	"encoding/json"
	"errors"
	"fmt"
	"maps"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	execUtils "k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

var _ = Describe("breakout lane DMS workaround", func() {
	const pci = "0000:b0:00.0"
	const lane1 = "/nvidia/link/breakout/module/[0]/port/[1]"
	const lane2 = "/nvidia/link/breakout/module/[0]/port/[2]"
	const lane255 = "/nvidia/link/breakout/module/[0]/port/[255]"
	var operations []dmscli.XPathOperation
	var config types.NvConfigQuery
	var commands [][]string
	var executor *execTesting.FakeExec

	BeforeEach(func() {
		operations = []dmscli.XPathOperation{
			{Path: lane1, Values: map[string]any{"lanes": []int{0, 1, 2, 3}}},
			{Path: lane2, Values: map[string]any{"lanes": []int{4, 5, 6, 7}}},
			{Path: lane255, Values: map[string]any{"lanes": []int{8, 9, 10, 11, 12, 13, 14, 15}}},
		}
		config = types.NewNvConfigQuery()
		config.AllParameters = true
		for lane := 0; lane < 16; lane++ {
			owner := "0xFF"
			if lane < 4 {
				owner = "1"
			} else if lane < 8 {
				owner = "0x2"
			}
			param := fmt.Sprintf("MODULE_SPLIT_M0[%d]", lane)
			config.CurrentConfig[param] = []string{owner}
			config.NextBootConfig[param] = []string{owner}
		}
		commands = nil
		executor = &execTesting.FakeExec{CommandScript: nil}
	})

	It("compiles all lanes without mutating the plan or losing sibling leaves", func() {
		operations[1].Values["planes"] = 0
		direct, raw, err := splitBreakoutLanes(operations)
		Expect(err).NotTo(HaveOccurred())
		Expect(raw).To(HaveLen(16))
		Expect(raw["MODULE_SPLIT_M0[4]"]).To(Equal("2"))
		Expect(raw["MODULE_SPLIT_M0[8]"]).To(Equal("0xff"))
		Expect(direct).To(Equal([]dmscli.XPathOperation{{Path: lane2, Values: map[string]any{"planes": 0}}}))
		Expect(operations[1].Values).To(HaveKey("lanes"))
	})

	It("supports JSON and CSV lane lists, deduplicates lanes, and separates modules", func() {
		operations[0].Values["lanes"] = []any{json.Number("0"), json.Number("0")}
		operations[1].Values["lanes"] = "4,5"
		operations = append(operations, dmscli.XPathOperation{
			Path: "/nvidia/link/breakout/module/[1]/port/[2]", Values: map[string]any{"lanes": []int{0}},
		})
		_, raw, err := splitBreakoutLanes(operations)
		Expect(err).NotTo(HaveOccurred())
		Expect(raw["MODULE_SPLIT_M0[0]"]).To(Equal("1"))
		Expect(raw["MODULE_SPLIT_M1[0]"]).To(Equal("2"))
	})

	DescribeTable("rejects malformed lane intent before executing commands", func(path string, lanes any) {
		h := &nvConfigUtils{execInterface: executor}
		invalid := []dmscli.XPathOperation{{Path: path, Values: map[string]any{"lanes": lanes}}}
		status, err := h.SetNvConfigParametersBatchWithXPaths(context.Background(), nvconfigPort(pci), 1, nil, invalid, false, false)
		Expect(err).To(HaveOccurred())
		Expect(status).To(Equal(types.ApplyStatusFailed))
		_, _, err = h.ValidateNvConfigXPaths(context.Background(), []v1alpha1.NicDevicePortSpec{nvconfigPort(pci)}, invalid)
		Expect(err).To(HaveOccurred())
		Expect(executor.CommandCalls).To(BeZero())
	},
		Entry("bad path", "/nvidia/link/breakout/module/[bad]/port/[2]", []int{1}),
		Entry("module outside uint8", "/nvidia/link/breakout/module/[256]/port/[2]", []int{1}),
		Entry("port outside uint8", "/nvidia/link/breakout/module/[0]/port/[256]", []int{1}),
		Entry("negative lane", lane2, []int{-1}),
		Entry("lane outside uint8", lane2, []int{256}),
		Entry("fraction", lane2, []any{1.5}),
		Entry("nil", lane2, nil),
	)

	It("rejects conflicting typed lane owners even with a native override", func() {
		operations[1].Values["lanes"] = []int{0}
		h := &nvConfigUtils{execInterface: executor}
		_, err := h.SetNvConfigParametersBatchWithXPaths(context.Background(), nvconfigPort(pci), 1,
			map[string]string{"MODULE_SPLIT_M0[0]": "3"}, operations, false, true)
		Expect(err).To(MatchError(ContainSubstring("conflicting breakout assignments")))
		Expect(executor.CommandCalls).To(BeZero())
	})

	It("keeps native overrides and remaining typed operations in exactly one apply command", func() {
		executor.CommandScript = []execTesting.FakeCommandAction{func(cmd string, args ...string) execUtils.Cmd {
			commands = append(commands, append([]string{cmd}, args...))
			return &execTesting.FakeCmd{RunScript: []execTesting.FakeAction{func() ([]byte, []byte, error) {
				return []byte(`{"status":"ok","requires-reset":true}`), nil, nil
			}}}
		}}
		native := map[string]string{"MODULE_SPLIT_M0[4]": "3", "NUM_OF_VFS": "8"}
		operations = append(operations, dmscli.XPathOperation{Path: "/nvidia/pci", Values: map[string]any{"num-pfs": 2}})
		h := &nvConfigUtils{execInterface: executor}
		status, err := h.SetNvConfigParametersBatchWithXPaths(context.Background(), nvconfigPort(pci), 1, native, operations, true, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(types.ApplyStatusSuccess))
		Expect(commands).To(HaveLen(1))
		Expect(commands[0][1:5]).To(Equal([]string{"--json", "-t", "pci/" + pci, "--input"}))
		Expect(commands[0][6]).To(Equal("/nvidia/nvconfig/apply"))
		var payload dmscli.ApplyNVConfigRequest
		Expect(json.Unmarshal([]byte(commands[0][5]), &payload)).To(Succeed())
		Expect(payload.Typed).To(Equal([]dmscli.XPathOperation{{Path: "/nvidia/pci", Values: map[string]any{"num-pfs": float64(2)}}}))
		Expect(payload.Raw).To(HaveLen(17))
		Expect(payload.Raw).To(ContainElement(dmscli.NVConfigParam{Param: "MODULE_SPLIT_M0[4]", Value: "3"}))
		Expect(payload.Raw).To(ContainElement(dmscli.NVConfigParam{Param: "MODULE_SPLIT_M0[8]", Value: "0xff"}))
		Expect(payload.Ports).To(Equal([]int{1}))
		Expect(payload.WithDefault).To(BeTrue())
		Expect(payload.Force).To(BeTrue())
		Expect(native).To(HaveLen(2))
		Expect(operations[0].Values).To(HaveKey("lanes"))
	})

	DescribeTable("retains current and next-boot drift decisions", func(current, pending string, update, reboot bool) {
		config.CurrentConfig["MODULE_SPLIT_M0[4]"] = []string{current}
		config.NextBootConfig["MODULE_SPLIT_M0[4]"] = []string{pending}
		h := &nvConfigUtils{execInterface: executor}
		actualUpdate, actualReboot, err := h.ValidateNvConfigXPathsWithOverrides(context.Background(),
			[]v1alpha1.NicDevicePortSpec{nvconfigPort(pci)}, operations, nil, map[string]types.NvConfigQuery{pci: config})
		Expect(err).NotTo(HaveOccurred())
		Expect(actualUpdate).To(Equal(update))
		Expect(actualReboot).To(Equal(reboot))
		Expect(executor.CommandCalls).To(BeZero())
	}, Entry("before apply", "1", "1", true, true), Entry("staged", "1", "2", false, true), Entry("converged", "2", "2", false, false))

	It("detects unexpected lane membership outside the desired list", func() {
		config.CurrentConfig["MODULE_SPLIT_M0[20]"] = []string{"2"}
		config.NextBootConfig["MODULE_SPLIT_M0[20]"] = []string{"2"}
		h := &nvConfigUtils{execInterface: executor}
		update, reboot, err := h.ValidateNvConfigXPathsWithOverrides(context.Background(),
			[]v1alpha1.NicDevicePortSpec{nvconfigPort(pci)}, operations, nil, map[string]types.NvConfigQuery{pci: config})
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeTrue())
		Expect(reboot).To(BeTrue())
	})

	It("uses native overrides for lanes moved away from or into typed membership", func() {
		config.CurrentConfig["MODULE_SPLIT_M0[4]"] = []string{"1"}
		config.NextBootConfig["MODULE_SPLIT_M0[4]"] = []string{"1"}
		h := &nvConfigUtils{execInterface: executor}
		update, reboot, err := h.ValidateNvConfigXPathsWithOverrides(context.Background(),
			[]v1alpha1.NicDevicePortSpec{nvconfigPort(pci)}, operations, map[string]string{"MODULE_SPLIT_M0[4]": "1"},
			map[string]types.NvConfigQuery{pci: config})
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeFalse())
		Expect(reboot).To(BeFalse())
	})

	It("validates every function using its own native state", func() {
		secondary := types.NewNvConfigQuery()
		secondary.AllParameters = true
		secondary.CurrentConfig = maps.Clone(config.CurrentConfig)
		secondary.NextBootConfig = maps.Clone(config.NextBootConfig)
		secondary.NextBootConfig["MODULE_SPLIT_M0[4]"] = []string{"1"}
		h := &nvConfigUtils{execInterface: executor}
		update, _, err := h.ValidateNvConfigXPathsWithOverrides(context.Background(),
			[]v1alpha1.NicDevicePortSpec{nvconfigPort(pci), nvconfigPort("0000:b0:00.1")}, operations, nil,
			map[string]types.NvConfigQuery{pci: config, "0000:b0:00.1": secondary})
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeTrue())
	})

	It("rejects missing native lane state rather than treating it as converged", func() {
		delete(config.NextBootConfig, "MODULE_SPLIT_M0[4]")
		h := &nvConfigUtils{execInterface: executor}
		_, _, err := h.ValidateNvConfigXPathsWithOverrides(context.Background(),
			[]v1alpha1.NicDevicePortSpec{nvconfigPort(pci)}, operations, nil, map[string]types.NvConfigQuery{pci: config})
		Expect(err).To(MatchError(ContainSubstring("missing current or next-boot value")))
	})

	It("requires native array state even for an empty desired lane list", func() {
		operations = []dmscli.XPathOperation{{Path: lane2, Values: map[string]any{"lanes": []int{}}}}
		config.CurrentConfig = map[string][]string{}
		config.NextBootConfig = map[string][]string{}
		h := &nvConfigUtils{execInterface: executor}
		_, _, err := h.ValidateNvConfigXPathsWithOverrides(context.Background(),
			[]v1alpha1.NicDevicePortSpec{nvconfigPort(pci)}, operations, nil,
			map[string]types.NvConfigQuery{pci: config})
		Expect(err).To(MatchError(ContainSubstring("does not contain array")))
	})

	DescribeTable("refreshes filtered snapshots without losing native override state",
		func(snapshot string, current, pending string, expectedUpdate, expectedReboot bool) {
			config.AllParameters = false
			config.CurrentConfig = map[string][]string{"NUM_OF_VFS": {"8"}}
			config.NextBootConfig = map[string][]string{"NUM_OF_VFS": {"8"}}
			if snapshot == "partial-array" {
				config.CurrentConfig["MODULE_SPLIT_M0[4]"] = []string{"2"}
				config.NextBootConfig["MODULE_SPLIT_M0[4]"] = []string{"2"}
			}
			if snapshot == "stale-extra-member" {
				config.CurrentConfig["MODULE_SPLIT_M0[20]"] = []string{"2"}
				config.NextBootConfig["MODULE_SPLIT_M0[20]"] = []string{"2"}
			}
			originalCurrent := maps.Clone(config.CurrentConfig)
			originalPending := maps.Clone(config.NextBootConfig)
			outputs := []string{
				"Configurations: Default Current Next Boot\nMODULE_SPLIT_M0  Array[4..5]  Array[4..5]  Array[4..5]\n",
				fmt.Sprintf("Configurations: Default Current Next Boot\nMODULE_SPLIT_M0[4]  2  2  2\nMODULE_SPLIT_M0[5]  255  %s  %s\n", current, pending),
			}
			for _, output := range outputs {
				executor.CommandScript = append(executor.CommandScript, func(cmd string, args ...string) execUtils.Cmd {
					commands = append(commands, append([]string{cmd}, args...))
					return &execTesting.FakeCmd{OutputScript: []execTesting.FakeAction{func() ([]byte, []byte, error) { return []byte(output), nil, nil }}}
				})
			}
			h := &nvConfigUtils{execInterface: executor}
			ops := []dmscli.XPathOperation{{Path: lane2, Values: map[string]any{"lanes": []int{4}}}}
			update, reboot, err := h.ValidateNvConfigXPathsWithOverrides(context.Background(),
				[]v1alpha1.NicDevicePortSpec{nvconfigPort(pci)}, ops, map[string]string{"NUM_OF_VFS": "8"},
				map[string]types.NvConfigQuery{pci: config})
			Expect(err).NotTo(HaveOccurred())
			Expect(update).To(Equal(expectedUpdate))
			Expect(reboot).To(Equal(expectedReboot))
			Expect(commands).To(Equal([][]string{
				{"mlxconfig", "-d", pci, "-e", "query", "MODULE_SPLIT_M0"},
				{"mlxconfig", "-d", pci, "-e", "query", "MODULE_SPLIT_M0[4..5]"},
			}))
			Expect(config.CurrentConfig).To(Equal(originalCurrent))
			Expect(config.NextBootConfig).To(Equal(originalPending))
		},
		Entry("partial snapshot cannot hide extra membership", "partial-array", "2", "2", true, true),
		Entry("unrelated native-only snapshot remains valid", "only-override", "255", "255", false, false),
		Entry("staged extra membership requires only a reboot", "partial-array", "2", "255", false, true),
		Entry("refreshed module replaces stale extra indices", "stale-extra-member", "255", "255", false, false),
	)

	It("queries native arrays and remaining typed values without a breakout GET", func() {
		outputs := []string{`{"num-pfs":2,"num-pfs-pending":2}`, "Configurations: Default Current Next Boot\nMODULE_SPLIT_M0  Array[0..1]  Array[0..1]  Array[0..1]\n", "Configurations: Default Current Next Boot\nMODULE_SPLIT_M0[0]  1  0x2  2\nMODULE_SPLIT_M0[1]  1  2  0x2\n"}
		for _, output := range outputs {
			executor.CommandScript = append(executor.CommandScript, func(cmd string, args ...string) execUtils.Cmd {
				commands = append(commands, append([]string{cmd}, args...))
				action := func() ([]byte, []byte, error) { return []byte(output), nil, nil }
				return &execTesting.FakeCmd{RunScript: []execTesting.FakeAction{action}, OutputScript: []execTesting.FakeAction{action}}
			})
		}
		h := &nvConfigUtils{execInterface: executor}
		ops := []dmscli.XPathOperation{
			{Path: "/nvidia/pci", Values: map[string]any{"num-pfs": 2}},
			{Path: lane2, Values: map[string]any{"lanes": []int{0, 1}}},
		}
		update, reboot, err := h.ValidateNvConfigXPaths(context.Background(),
			[]v1alpha1.NicDevicePortSpec{{PCI: pci, FwctlDevice: "/dev/fwctl/fwctl0"}}, ops)
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeFalse())
		Expect(reboot).To(BeFalse())
		Expect(commands).To(Equal([][]string{
			{"/opt/mellanox/doca/services/dms/dms-cli", "--json", "-t", "pci/" + pci + "?port=1", "/nvidia/pci", "num-pfs", "num-pfs-pending"},
			{"mlxconfig", "-d", "/dev/fwctl/fwctl0", "-e", "query", "MODULE_SPLIT_M0"},
			{"mlxconfig", "-d", "/dev/fwctl/fwctl0", "-e", "query", "MODULE_SPLIT_M0[0..1]"},
		}))
	})

	It("propagates native query errors without reporting successful validation", func() {
		executor.CommandScript = []execTesting.FakeCommandAction{func(_ string, _ ...string) execUtils.Cmd {
			return &execTesting.FakeCmd{OutputScript: []execTesting.FakeAction{func() ([]byte, []byte, error) { return nil, []byte("query failed"), errors.New("exit 3") }}}
		}}
		h := &nvConfigUtils{execInterface: executor}
		_, _, err := h.ValidateNvConfigXPaths(context.Background(), []v1alpha1.NicDevicePortSpec{nvconfigPort(pci)}, operations)
		Expect(err).To(MatchError(ContainSubstring("query breakout lane arrays")))
	})
})
