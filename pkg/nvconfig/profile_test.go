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
	"maps"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	execUtils "k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

var _ = Describe("Network Bay profile filtering", func() {
	const path = "/nvidia/pci"
	const lanesPath = "/nvidia/link/breakout/module/[0]/port/[1]"
	var profile map[string]string
	var ports []v1alpha1.NicDevicePortSpec
	var operations []dmscli.XPathOperation
	var executor *execTesting.FakeExec
	var metadata map[string]string

	BeforeEach(func() {
		profile = map[string]string{"NUM_OF_PF": "4", "MODULE_SPLIT_M0[0]": "255", "MODULE_SPLIT_M0[7]": "FF", "BOARD_CONFIGURATION_MODE": "1"}
		ports = []v1alpha1.NicDevicePortSpec{{PCI: "0000:03:00.0"}, {PCI: "0000:03:00.1"}}
		operations = []dmscli.XPathOperation{
			{Path: path, Values: map[string]any{"num-pfs": 2}},
			{Path: lanesPath, Values: map[string]any{"lanes": []int{0, 1}}},
		}
		metadata = map[string]string{path + "/num-pfs": "NUM_OF_PF", path + "/num-pfs-pending": "NUM_OF_PF"}
		executor = &execTesting.FakeExec{}
	})

	addResponse := func(mapping map[string]string, commandErr error) {
		output, err := json.Marshal(map[string]any{"num-pfs": 2, "num-pfs-pending": 2, "_nvconfig": mapping})
		Expect(err).NotTo(HaveOccurred())
		executor.CommandScript = append(executor.CommandScript, func(_ string, args ...string) execUtils.Cmd {
			Expect(args).To(ContainElement("pci/" + ports[executor.CommandCalls-1].PCI + "?port=1"))
			return &execTesting.FakeCmd{RunScript: []execTesting.FakeAction{func() ([]byte, []byte, error) {
				return output, nil, commandErr
			}}}
		})
	}

	It("filters direct and composite ownership on both functions without changing inputs", func() {
		original := maps.Clone(profile)
		addResponse(metadata, nil)
		addResponse(metadata, nil)
		filtered, err := FilterProfileParams(context.Background(), executor, ports, operations, profile)
		Expect(err).NotTo(HaveOccurred())
		Expect(filtered).To(Equal(map[string]string{"MODULE_SPLIT_M0[7]": "FF", "BOARD_CONFIGURATION_MODE": "1"}))
		Expect(profile).To(Equal(original))
		Expect(operations[1].Values["lanes"]).To(Equal([]int{0, 1}))
		Expect(executor.CommandCalls).To(Equal(2))
	})

	It("rejects differing per-function mappings even if their values match", func() {
		addResponse(metadata, nil)
		metadata[path+"/num-pfs"] = "OTHER_PARAM"
		metadata[path+"/num-pfs-pending"] = "OTHER_PARAM"
		addResponse(metadata, nil)
		_, err := FilterProfileParams(context.Background(), executor, ports, operations, profile)
		Expect(err).To(MatchError(ContainSubstring("inconsistent system profile ownership")))
	})

	It("requires explicit pending ownership", func() {
		delete(metadata, path+"/num-pfs-pending")
		addResponse(metadata, nil)
		_, err := FilterProfileParams(context.Background(), executor, ports, operations, profile)
		Expect(err).To(MatchError(ContainSubstring("mapping metadata is required")))
	})

	It("propagates query errors", func() {
		addResponse(metadata, errors.New("query failed"))
		_, err := FilterProfileParams(context.Background(), executor, ports, operations, profile)
		Expect(err).To(MatchError(ContainSubstring("query failed")))
	})

	It("filters only listed lanes without querying synthetic mapping metadata", func() {
		operations = operations[1:]
		profile["MODULE_SPLIT_M1[0]"] = "1"
		filtered, err := FilterProfileParams(context.Background(), nil, ports, operations, profile)
		Expect(err).NotTo(HaveOccurred())
		Expect(filtered).NotTo(HaveKey("MODULE_SPLIT_M0[0]"))
		Expect(filtered).To(HaveKeyWithValue("MODULE_SPLIT_M0[7]", "FF"))
		Expect(filtered).To(HaveKeyWithValue("MODULE_SPLIT_M1[0]", "1"))
	})

	It("handles an empty profile without commands", func() {
		filtered, err := FilterProfileParams(context.Background(), nil, nil, operations, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(filtered).To(BeEmpty())
	})

	It("rejects missing targets and executors", func() {
		_, err := FilterProfileParams(context.Background(), executor, nil, operations, profile)
		Expect(err).To(MatchError(ContainSubstring("without PCI functions")))
		_, err = FilterProfileParams(context.Background(), nil, ports, operations, profile)
		Expect(err).To(MatchError(ContainSubstring("executor must not be nil")))
	})

	It("rejects malformed lane paths before querying", func() {
		operations[1].Path = "/nvidia/link/breakout/unknown"
		_, err := FilterProfileParams(context.Background(), executor, ports, operations, profile)
		Expect(err).To(MatchError(ContainSubstring("unsupported breakout lanes XPath")))
		Expect(executor.CommandCalls).To(BeZero())
	})
})

var _ = Describe("native lane overrides", func() {
	const path = "/nvidia/link/breakout/module/[0]/port/[1]"
	const param = "MODULE_SPLIT_M0[1]"

	DescribeTable("partitions native and typed validation",
		func(current, pending any, nativeCurrent, nativePending string, supported bool, updateExpected, rebootExpected bool) {
			desired := []int{0, 1}
			operations := []dmscli.XPathOperation{{Path: path, Values: map[string]any{"lanes": desired}}}
			state := &dmscli.QueryXPathsResult{
				Status: "ok", Values: map[string]map[string]any{path: {"lanes": current, "lanes-pending": pending}},
				NVConfig: nil, Failures: nil, ErrorMessage: "", ErrorCode: nil,
			}
			config := types.NewNvConfigQuery()
			if supported {
				config.CurrentConfig[param] = []string{nativeCurrent}
				config.NextBootConfig[param] = []string{nativePending}
			}
			update, reboot, err := matchXPathValuesWithOverrides(state, operations, map[string]string{param: "2"}, config)
			Expect(err).NotTo(HaveOccurred())
			Expect(update).To(Equal(updateExpected))
			Expect(reboot).To(Equal(rebootExpected))
			Expect(desired).To(Equal([]int{0, 1}))
		},
		Entry("moved lane converged", "0", "0", "2", "2", true, false, false),
		Entry("native drift cannot be masked", "0", "0", "1", "1", true, true, true),
		Entry("native pending-only match", "0,1", "0", "1", "2", true, false, true),
		Entry("remaining typed pending drift", "", "", "2", "2", true, true, true),
		Entry("remaining typed pending-only match", "", "0", "2", "2", true, false, true),
		Entry("unsupported override cannot hide drift", "0", "0", "", "", false, true, true),
		Entry("unsupported override leaves matching typed state alone", "1,0", "0,1", "", "", false, false, false),
		Entry("JSON arrays", []any{json.Number("0")}, []int{0}, "0x2", "2", true, false, false),
	)

	It("allows a retained profile lane to add membership and validates its native value", func() {
		config := types.NewNvConfigQuery()
		config.CurrentConfig["MODULE_SPLIT_M0[7]"] = []string{"1"}
		config.NextBootConfig["MODULE_SPLIT_M0[7]"] = []string{"1"}
		update, reboot, err := matchLaneValues(path, []int{0, 1}, "0,1,7", "7,1,0",
			map[string]string{"MODULE_SPLIT_M0[7]": "1"}, config)
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeFalse())
		Expect(reboot).To(BeFalse())
	})

	It("does not mask the same lane number in another module", func() {
		config := types.NewNvConfigQuery()
		config.CurrentConfig["MODULE_SPLIT_M1[1]"] = []string{"2"}
		config.NextBootConfig["MODULE_SPLIT_M1[1]"] = []string{"2"}
		update, reboot, err := matchLaneValues(path, []int{0, 1}, "0", "0",
			map[string]string{"MODULE_SPLIT_M1[1]": "2"}, config)
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeTrue())
		Expect(reboot).To(BeTrue())
	})

	It("handles the reserved port and hexadecimal native values", func() {
		config := types.NewNvConfigQuery()
		config.CurrentConfig[param] = []string{"0xFF"}
		config.NextBootConfig[param] = []string{"255"}
		update, reboot, err := matchLaneValues("/nvidia/link/breakout/module/[0]/port/[255]", []int{7}, "1,7", "7,1",
			map[string]string{param: "FF"}, config)
		Expect(err).NotTo(HaveOccurred())
		Expect(update).To(BeFalse())
		Expect(reboot).To(BeFalse())
	})

	DescribeTable("rejects malformed lane lists", func(value any) {
		_, err := laneIndices(value)
		Expect(err).To(HaveOccurred())
	}, Entry("negative", []int{-1}), Entry("fraction", []any{1.5}), Entry("scalar", 1),
		Entry("nil", nil), Entry("invalid CSV", "0,nope"))

	It("rejects malformed native lane keys", func() {
		_, _, err := matchLaneValues(path, []int{0}, "0", "0", map[string]string{"MODULE_SPLIT_M0[0..3]": "2"}, types.NewNvConfigQuery())
		Expect(err).To(MatchError(ContainSubstring("invalid native lane parameter")))
	})
})
