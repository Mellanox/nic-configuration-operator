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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	execUtils "k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

var _ = Describe("typed NVConfig native overrides", func() {
	const pci = "0000:3b:00.0"
	const path = "/nvidia/roce"
	const param = "ROCE_ADAPTIVE_ROUTING_EN"
	var operations []dmscli.XPathOperation
	var state *dmscli.QueryXPathsResult

	BeforeEach(func() {
		operations = []dmscli.XPathOperation{{Path: path, Values: map[string]any{"adaptive-routing": true}}}
		state = &dmscli.QueryXPathsResult{
			Status: "ok",
			Values: map[string]map[string]any{path: {"adaptive-routing": false, "adaptive-routing-pending": false}},
			NVConfig: map[string]string{
				path + "/adaptive-routing":         param,
				path + "/adaptive-routing-pending": param,
			},
			Failures: nil, ErrorMessage: "", ErrorCode: nil,
		}
	})

	DescribeTable("compares native state for an overridden typed leaf",
		func(current, pending []string, expectedUpdate, expectedReboot bool) {
			config := types.NvConfigQuery{
				DefaultConfig:  map[string][]string{},
				CurrentConfig:  map[string][]string{param: current},
				NextBootConfig: map[string][]string{param: pending},
			}
			update, reboot, err := matchXPathValuesWithOverrides(state, operations, map[string]string{param: "0"}, config)
			Expect(err).NotTo(HaveOccurred())
			Expect(update).To(Equal(expectedUpdate))
			Expect(reboot).To(Equal(expectedReboot))
		},
		Entry("before apply", []string{"1"}, []string{"1"}, true, true),
		Entry("staged until reboot", []string{"1"}, []string{"0"}, false, true),
		Entry("converged after reboot", []string{"0"}, []string{"0"}, false, false),
		Entry("native enum and hex representations", []string{"disabled", "0"}, []string{"0x0"}, false, false),
		Entry("unknown current state", []string{}, []string{"0"}, false, true),
	)

	It("continues validating typed leaves not owned by a native override", func() {
		update, reboot, err := matchXPathValuesWithOverrides(state, operations,
			map[string]string{"OTHER_PARAM": "0"}, types.NewNvConfigQuery())
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeTrue())
		Expect(reboot).To(BeTrue())
	})

	It("does not suppress typed drift for an unsupported native parameter omitted from apply", func() {
		update, reboot, err := matchXPathValuesWithOverrides(state, operations,
			map[string]string{param: "0"}, types.NewNvConfigQuery())
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeTrue())
		Expect(reboot).To(BeTrue())
	})

	It("rejects missing metadata even when the typed values already match", func() {
		state.Values[path]["adaptive-routing"] = true
		state.Values[path]["adaptive-routing-pending"] = true
		state.NVConfig = nil
		_, _, err := matchXPathValuesWithOverrides(state, operations,
			map[string]string{param: "0"}, types.NewNvConfigQuery())
		Expect(err).To(MatchError(ContainSubstring("mapping metadata is required")))
	})

	It("requires consistent current and pending metadata", func() {
		state.NVConfig[path+"/adaptive-routing-pending"] = "OTHER_PARAM"
		_, _, err := matchXPathValuesWithOverrides(state, operations,
			map[string]string{param: "0"}, types.NewNvConfigQuery())
		Expect(err).To(MatchError(ContainSubstring("inconsistent DMS NVConfig mappings")))
	})

	It("uses exact indexed XPath identity", func() {
		const indexed = "/nvidia/example/[1]"
		operations = []dmscli.XPathOperation{{Path: indexed, Values: map[string]any{"value": 1}}}
		state.Values = map[string]map[string]any{indexed: {"value": 0, "value-pending": 0}}
		state.NVConfig = map[string]string{
			indexed + "/value": "PARAM[1]", indexed + "/value-pending": "PARAM[1]",
			"/nvidia/example/[0]/value": "PARAM[0]", "/nvidia/example/[0]/value-pending": "PARAM[0]",
		}
		config := types.NewNvConfigQuery()
		config.CurrentConfig["PARAM[0]"] = []string{"0"}
		config.NextBootConfig["PARAM[0]"] = []string{"0"}
		update, _, err := matchXPathValuesWithOverrides(state, operations, map[string]string{"PARAM[0]": "0"}, config)
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeTrue())
	})

	It("rejects composite lane overrides but permits unrelated native values", func() {
		const lanes = "/nvidia/link/breakout/module/[0]/port/[1]"
		operations = []dmscli.XPathOperation{{Path: lanes, Values: map[string]any{"lanes": []int{0, 1}}}}
		state.Values = map[string]map[string]any{lanes: {"lanes": "0,1", "lanes-pending": "0,1"}}
		state.NVConfig = nil
		_, _, err := matchXPathValuesWithOverrides(state, operations,
			map[string]string{"MODULE_SPLIT_M0[0]": "0xff"}, types.NewNvConfigQuery())
		Expect(err).To(MatchError(ContainSubstring("cannot override doSPCX breakout lanes")))
		update, reboot, err := matchXPathValuesWithOverrides(state, operations,
			map[string]string{param: "0"}, types.NewNvConfigQuery())
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeFalse())
		Expect(reboot).To(BeFalse())
	})

	It("keeps native state and metadata local to each PCI function", func() {
		const secondary = "0000:3b:00.1"
		configs := map[string]types.NvConfigQuery{pci: types.NewNvConfigQuery(), secondary: types.NewNvConfigQuery()}
		for _, config := range configs {
			config.CurrentConfig[param] = []string{"0"}
			config.NextBootConfig[param] = []string{"0"}
		}
		commands := make([]execTesting.FakeCommandAction, 0, 2)
		for _, mapping := range []string{param, "OTHER_PARAM"} {
			state.NVConfig[path+"/adaptive-routing"] = mapping
			state.NVConfig[path+"/adaptive-routing-pending"] = mapping
			output := map[string]any{
				"adaptive-routing": false, "adaptive-routing-pending": false, "_nvconfig": state.NVConfig,
			}
			data, err := json.Marshal(output)
			Expect(err).NotTo(HaveOccurred())
			commands = append(commands, func(_ string, _ ...string) execUtils.Cmd {
				return &execTesting.FakeCmd{RunScript: []execTesting.FakeAction{func() ([]byte, []byte, error) {
					return data, nil, nil
				}}}
			})
		}
		h := &nvConfigUtils{execInterface: &execTesting.FakeExec{CommandScript: commands}}
		update, reboot, err := h.ValidateNvConfigXPathsWithOverrides(context.Background(),
			[]v1alpha1.NicDevicePortSpec{{PCI: pci}, {PCI: secondary}}, operations, map[string]string{param: "0"}, configs)
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeTrue())
		Expect(reboot).To(BeTrue())
	})
})
