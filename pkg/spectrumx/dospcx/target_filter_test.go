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

package dospcx

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
)

var _ = Describe("doSPCX operation target filters", func() {
	target := func(index *int) preBreakoutTarget {
		return preBreakoutTarget{ID: "nic", BDF: "0000:03:00.0", DeviceID: "0x1025", Role: "ew", Rail: 0, NicIndexInRail: index}
	}
	intPtr := func(value int) *int { return &value }

	DescribeTable("matches the planner's NIC and HCA semantics",
		func(raw string, index *int, platform string, expected bool) {
			filter, err := parseTargetFilter(json.RawMessage(raw))
			Expect(err).NotTo(HaveOccurred())
			matches, err := filter.matches(target(index), platform)
			Expect(err).NotTo(HaveOccurred())
			Expect(matches).To(Equal(expected))
		},
		Entry("absent filter", "", nil, "vr", true),
		Entry("empty object", `{}`, nil, "vr", true),
		Entry("empty HCA list is unrestricted", `{"hca_type":[]}`, nil, "vr", true),
		Entry("empty NIC list matches none", `{"nic_index_in_rail":[]}`, nil, "vr", false),
		Entry("NIC zero", `{"nic_index_in_rail":[0]}`, intPtr(0), "vr", true),
		Entry("NIC mismatch", `{"nic_index_in_rail":[0]}`, intPtr(1), "vr", false),
		Entry("multiple NICs", `{"nic_index_in_rail":[0,1]}`, intPtr(1), "vr", true),
		Entry("default singleton index", `{"nic_index_in_rail":[0]}`, nil, "gb300", true),
		Entry("both filters match", `{"hca_type":["ConnectX-8","ConnectX-9"],"nic_index_in_rail":[1]}`, intPtr(1), "vr", true),
		Entry("HCA mismatch", `{"hca_type":["ConnectX-8"],"nic_index_in_rail":[1]}`, intPtr(1), "vr", false),
		Entry("NIC mismatch with matching HCA", `{"hca_type":["ConnectX-9"],"nic_index_in_rail":[1]}`, intPtr(0), "vr", false),
	)

	DescribeTable("rejects malformed or unsupported filters",
		func(raw string) {
			_, err := parseTargetFilter(json.RawMessage(raw))
			Expect(err).To(HaveOccurred())
		},
		Entry("null object", `null`),
		Entry("array object", `[]`),
		Entry("unknown selector", `{"rail":[0]}`),
		Entry("planes", `{"planes":[0]}`),
		Entry("empty planes", `{"planes":[]}`),
		Entry("null NIC list", `{"nic_index_in_rail":null}`),
		Entry("null NIC element", `{"nic_index_in_rail":[null]}`),
		Entry("mixed null NIC element", `{"nic_index_in_rail":[0,null]}`),
		Entry("fractional NIC", `{"nic_index_in_rail":[0.5]}`),
		Entry("negative NIC", `{"nic_index_in_rail":[-1]}`),
		Entry("string NIC", `{"nic_index_in_rail":["0"]}`),
		Entry("scalar NIC", `{"nic_index_in_rail":0}`),
		Entry("null HCA list", `{"hca_type":null}`),
		Entry("null HCA element", `{"hca_type":[null]}`),
		Entry("empty HCA name", `{"hca_type":[""]}`),
		Entry("numeric HCA", `{"hca_type":[9]}`),
	)

	It("requires a usable index for a constrained VR target", func() {
		filter, err := parseTargetFilter(json.RawMessage(`{"nic_index_in_rail":[0]}`))
		Expect(err).NotTo(HaveOccurred())
		_, err = filter.matches(target(nil), "vr")
		Expect(err).To(MatchError(ContainSubstring("missing nic_index_in_rail")))
		_, err = filter.matches(target(intPtr(-1)), "vr")
		Expect(err).To(MatchError(ContainSubstring("negative nic_index_in_rail")))
	})

	DescribeTable("maps every supported HCA identity", func(id, name string) {
		got, err := targetHCAType(id)
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(Equal(name))
	},
		Entry("CX7", "0x1021", "ConnectX-7"),
		Entry("CX8", "0x1023", "ConnectX-8"),
		Entry("CX9", "0x1025", "ConnectX-9"),
		Entry("BF3", "0xa2dc", "BlueField-3"),
	)

	It("rejects unknown HCA identities when a filter needs them", func() {
		filter, err := parseTargetFilter(json.RawMessage(`{"hca_type":["ConnectX-9"]}`))
		Expect(err).NotTo(HaveOccurred())
		_, err = filter.matches(preBreakoutTarget{DeviceID: "unknown"}, "gb300")
		Expect(err).To(HaveOccurred())
	})

	It("filters all phases in order and returns independent values without altering the cache", func() {
		op := func(value string, indexes []int) compiledOperation {
			return compiledOperation{
				XPathOperation: dmscli.XPathOperation{
					Path: "/nvidia/test", Values: map[string]any{"value": []any{value}},
					Scope: "per_device", TargetClass: "pf_netdev_all",
				},
				filter: targetFilter{hcaTypes: []string{"ConnectX-9"}, nicIndexes: indexes},
			}
		}
		operations := []compiledOperation{op("common", nil), op("zero", []int{0}), op("one", []int{1})}
		deferred := errors.New("configure deferred")
		source := &compiledPlan{
			Breakout: operations, PostBreakout: operations,
			RuntimeConfig: []compiledOperationGroup{
				{Name: "link-runtime", Scope: "per_device", Operations: operations},
				{Name: "cc", Scope: "per_device", Operations: []compiledOperation{op("none", []int{})}},
			},
			ConfigureError: deferred,
		}
		for _, index := range []int{0, 1, 0} {
			plan, err := projectPlan(source, target(&index), "vr")
			Expect(err).NotTo(HaveOccurred())
			Expect(plan.ConfigureError).To(BeIdenticalTo(deferred))
			Expect(plan.RuntimeConfig).To(HaveLen(1))
			selected := "zero"
			if index == 1 {
				selected = "one"
			}
			for _, ops := range [][]dmscli.XPathOperation{plan.Breakout, plan.PostBreakout, plan.RuntimeConfig[0].Operations} {
				Expect(ops).To(HaveLen(2))
				Expect(ops[0].Values["value"]).To(Equal([]any{"common"}))
				Expect(ops[1].Values["value"]).To(Equal([]any{selected}))
				ops[0].Values["value"].([]any)[0] = "changed"
				ops[1].Values["value"] = "changed"
			}
			Expect(plan.Breakout[0].Scope).To(BeEmpty())
			Expect(plan.PostBreakout[0].TargetClass).To(BeEmpty())
			Expect(plan.RuntimeConfig[0].Operations[0].Scope).To(Equal("per_device"))
			Expect(plan.RuntimeConfig[0].Operations[0].TargetClass).To(Equal("pf_netdev_all"))
		}
	})

	It("reports a bad filter on a referenced operation during compilation", func() {
		content, err := os.ReadFile(filepath.Join("testdata", "semantic", "prepare-plan.json"))
		Expect(err).NotTo(HaveOccurred())
		document, err := decodePlanDocument(content)
		Expect(err).NotTo(HaveOccurred())
		ref := document.Plan.Semantic.Groups[0].OperationRefs[0]
		operation := document.Plan.Operations[ref]
		operation.TargetFilter = json.RawMessage(`{"planes":[0]}`)
		document.Plan.Operations[ref] = operation
		_, err = buildPlan(document, planStagePrepare)
		Expect(err).To(MatchError(And(ContainSubstring(ref), ContainSubstring("unsupported target_filter field"))))
	})
})
