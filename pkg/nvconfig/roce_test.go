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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	execUtils "k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

var _ = Describe("native RoCE validation", func() {
	const pci = "0012:03:00.0"
	var state types.NvConfigQuery
	var operations []dmscli.XPathOperation
	var executor *execTesting.FakeExec
	var h *nvConfigUtils
	var ports []v1alpha1.NicDevicePortSpec

	BeforeEach(func() {
		state = types.NewNvConfigQuery()
		state.AllParameters = true
		for param, value := range map[string]string{"ROCE_ADAPTIVE_ROUTING_EN": "1", "ROCE_CC_STEERING_EXT": "2", "TX_SCHEDULER_LOCALITY_MODE": "2"} {
			state.CurrentConfig[param] = []string{value}
			state.NextBootConfig[param] = []string{value}
		}
		operations = []dmscli.XPathOperation{{Path: "/nvidia/roce", Values: map[string]any{
			"adaptive-routing": true, "cc-steering-ext": "enabled", "tx-sched-locality-mode": "accumulative",
		}}}
		executor = &execTesting.FakeExec{CommandScript: nil}
		h = &nvConfigUtils{execInterface: executor}
		ports = []v1alpha1.NicDevicePortSpec{nvconfigPort(pci)}
	})

	validate := func(overrides map[string]string) (bool, bool, error) {
		return h.ValidateNvConfigXPathsWithOverrides(context.Background(), ports, operations, overrides,
			map[string]types.NvConfigQuery{pci: state})
	}

	It("validates the GB300 plan without DMS GET or unrequested MULTIPATH_DSCP", func() {
		update, reboot, err := validate(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeFalse())
		Expect(reboot).To(BeFalse())
		Expect(executor.CommandCalls).To(BeZero())
		Expect(operations[0].Values).To(HaveLen(3))
	})

	DescribeTable("preserves current and pending drift semantics", func(current, pending string, updateExpected, rebootExpected bool) {
		state.CurrentConfig["ROCE_CC_STEERING_EXT"] = []string{current}
		state.NextBootConfig["ROCE_CC_STEERING_EXT"] = []string{pending}
		update, reboot, err := validate(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(Equal(updateExpected))
		Expect(reboot).To(Equal(rebootExpected))
	},
		Entry("converged", "0x2", "2", false, false),
		Entry("pending reboot", "1", "2", false, true),
		Entry("pending drift", "2", "1", true, true),
		Entry("both differ", "1", "1", true, true),
	)

	It("preserves native override precedence without interpreting overridden typed values", func() {
		operations[0].Values["cc-steering-ext"] = "future-enum"
		update, reboot, err := validate(map[string]string{"ROCE_CC_STEERING_EXT": "0x2"})
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeFalse())
		Expect(reboot).To(BeFalse())
		_, _, err = validate(nil)
		Expect(err).To(MatchError(ContainSubstring("unsupported value future-enum")))
	})

	It("rejects explicitly requested unsupported multipath DSCP", func() {
		operations[0].Values["multipath-dscp"] = "default"
		_, _, err := validate(nil)
		Expect(err).To(MatchError(And(ContainSubstring("MULTIPATH_DSCP"), ContainSubstring(pci), ContainSubstring("missing"))))
	})

	It("requires both current and pending observations", func() {
		delete(state.CurrentConfig, "ROCE_CC_STEERING_EXT")
		_, _, err := validate(nil)
		Expect(err).To(MatchError(ContainSubstring("missing current or next-boot")))
	})

	It("rejects missing pending observations even when current state matches", func() {
		delete(state.NextBootConfig, "ROCE_CC_STEERING_EXT")
		_, _, err := validate(nil)
		Expect(err).To(MatchError(And(ContainSubstring("missing current or next-boot"), ContainSubstring("ROCE_CC_STEERING_EXT"))))
	})

	It("detects drift on the second PCI function without reusing the first function's state", func() {
		const secondaryPCI = "0012:03:00.1"
		operations[0].Values = map[string]any{"cc-steering-ext": "enabled"}
		secondary := types.NewNvConfigQuery()
		secondary.AllParameters = true
		secondary.CurrentConfig["ROCE_CC_STEERING_EXT"] = []string{"1"}
		secondary.NextBootConfig["ROCE_CC_STEERING_EXT"] = []string{"1"}
		ports = append(ports, nvconfigPort(secondaryPCI))
		update, reboot, err := h.ValidateNvConfigXPathsWithOverrides(context.Background(), ports, operations, nil,
			map[string]types.NvConfigQuery{pci: state, secondaryPCI: secondary})
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeTrue())
		Expect(reboot).To(BeTrue())
		Expect(state.CurrentConfig["ROCE_CC_STEERING_EXT"]).To(Equal([]string{"2"}))
		Expect(executor.CommandCalls).To(BeZero())
	})

	It("queries only requested native parameters through fwctl for partial snapshots", func() {
		state.AllParameters = false
		ports[0].FwctlDevice = "/dev/fwctl/fwctl6"
		operations[0].Values = map[string]any{"cc-steering-ext": "enabled"}
		state.CurrentConfig["ROCE_CC_STEERING_EXT"] = []string{"1"}
		executor.CommandScript = []execTesting.FakeCommandAction{func(cmd string, args ...string) execUtils.Cmd {
			Expect(cmd).To(Equal("mlxconfig"))
			Expect(args).To(Equal([]string{"-d", "/dev/fwctl/fwctl6", "-e", "query", "ROCE_CC_STEERING_EXT"}))
			return &execTesting.FakeCmd{OutputScript: []execTesting.FakeAction{func() ([]byte, []byte, error) {
				return []byte("Configurations: Default Current Next Boot\nROCE_CC_STEERING_EXT  DISABLED(1)  ENABLED(2)  ENABLED(2)\n"), nil, nil
			}}}
		}}
		update, reboot, err := validate(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeFalse())
		Expect(reboot).To(BeFalse())
		Expect(state.CurrentConfig["ROCE_CC_STEERING_EXT"]).To(Equal([]string{"1"}))
		Expect(state.AllParameters).To(BeFalse())
	})

	It("propagates native query errors for callers without snapshots", func() {
		operations[0].Values = map[string]any{"multipath-dscp": "default"}
		executor.CommandScript = []execTesting.FakeCommandAction{func(_ string, args ...string) execUtils.Cmd {
			Expect(args).To(ContainElement("MULTIPATH_DSCP"))
			return &execTesting.FakeCmd{OutputScript: []execTesting.FakeAction{func() ([]byte, []byte, error) {
				return []byte("unsupported MULTIPATH_DSCP"), nil, errors.New("exit status 3")
			}}}
		}}
		_, _, err := h.ValidateNvConfigXPaths(context.Background(), ports, operations)
		Expect(err).To(MatchError(ContainSubstring("exit status 3")))
	})

	It("filters only requested profile ownership without querying DMS", func() {
		profile := map[string]string{"ROCE_CC_STEERING_EXT": "1", "MULTIPATH_DSCP": "0", "OTHER_PARAM": "1"}
		filtered, err := FilterProfileParams(context.Background(), executor, ports, operations, profile)
		Expect(err).NotTo(HaveOccurred())
		Expect(filtered).To(Equal(map[string]string{"MULTIPATH_DSCP": "0", "OTHER_PARAM": "1"}))
		Expect(profile).To(HaveKey("ROCE_CC_STEERING_EXT"))
		Expect(operations[0].Values).To(HaveLen(3))
		Expect(executor.CommandCalls).To(BeZero())
	})

	It("retains unknown leaves and other containers for ordinary DMS validation", func() {
		operations[0].Values["future-leaf"] = 1
		operations = append(operations, dmscli.XPathOperation{Path: "/nvidia/roce/rtt", Values: map[string]any{"dscp": 48}})
		direct, native := splitRoceValidation(operations)
		Expect(direct).To(Equal([]dmscli.XPathOperation{
			{Path: "/nvidia/roce", Values: map[string]any{"future-leaf": 1}},
			{Path: "/nvidia/roce/rtt", Values: map[string]any{"dscp": 48}},
		}))
		Expect(native[0].Values).To(HaveLen(3))
		Expect(operations[0].Values).To(HaveLen(4))
	})

	It("keeps RoCE typed and breakout native settings in one apply", func() {
		operations = append(operations, dmscli.XPathOperation{Path: "/nvidia/link/breakout/module/[0]/port/[1]", Values: map[string]any{"lanes": []int{0}}})
		executor.CommandScript = []execTesting.FakeCommandAction{func(_ string, args ...string) execUtils.Cmd {
			Expect(args[5]).To(Equal("/nvidia/nvconfig/apply"))
			var payload dmscli.ApplyNVConfigRequest
			Expect(json.Unmarshal([]byte(args[4]), &payload)).To(Succeed())
			Expect(payload.Typed).To(Equal(operations[:1]))
			Expect(payload.Raw).To(ContainElements(dmscli.NVConfigParam{Param: "MODULE_SPLIT_M0[0]", Value: "1"},
				dmscli.NVConfigParam{Param: "NUM_OF_VFS", Value: "8"}))
			return &execTesting.FakeCmd{RunScript: []execTesting.FakeAction{func() ([]byte, []byte, error) {
				return []byte(`{"status":"ok","requires-reset":true}`), nil, nil
			}}}
		}}
		_, err := h.SetNvConfigParametersBatchWithXPaths(context.Background(), ports[0], 1,
			map[string]string{"NUM_OF_VFS": "8"}, operations, false, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(executor.CommandCalls).To(Equal(1))
	})

	DescribeTable("matches native encodings", func(leaf string, desired any, native string) {
		operations[0].Values = map[string]any{leaf: desired}
		param := roceParameter("/nvidia/roce", leaf)
		state.CurrentConfig[param] = []string{native}
		state.NextBootConfig[param] = []string{native}
		update, reboot, err := validate(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(update || reboot).To(BeFalse(), fmt.Sprintf("%s=%v", leaf, desired))
	},
		Entry("boolean false", "adaptive-routing", false, "0"),
		Entry("scheduler default", "tx-sched-locality-mode", "default", "0"),
		Entry("scheduler static", "tx-sched-locality-mode", "static", "1"),
		Entry("scheduler disabled", "tx-sched-locality-mode", "disabled", "3"),
		Entry("multipath default", "multipath-dscp", "default", "0"),
		Entry("multipath DSCP", "multipath-dscp", "dscp-2", "4"),
		Entry("steering disabled", "cc-steering-ext", "disabled", "1"),
	)
})
