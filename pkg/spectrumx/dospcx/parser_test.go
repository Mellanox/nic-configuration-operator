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
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
)

var _ = Describe("doSPCX semantic plan parsing", func() {
	readFixture := func(name string) []byte {
		content, err := os.ReadFile(filepath.Join("testdata", "semantic", name))
		Expect(err).NotTo(HaveOccurred())
		return content
	}

	decodeFixture := func(name string) *planDocument {
		document, err := decodePlanDocument(readFixture(name))
		Expect(err).NotTo(HaveOccurred())
		return document
	}

	compileFixture := func(name string, stage PlanStage) *Plan {
		plan, err := buildPlan(decodeFixture(name), stage)
		Expect(err).NotTo(HaveOccurred())
		return plan
	}

	It("extracts breakout and post-breakout operations from a prepare plan", func() {
		plan := compileFixture("prepare-plan.json", PlanStagePrepare)

		Expect(plan.Breakout).To(HaveLen(15))
		Expect(plan.Breakout[0].Path).To(Equal("/nvidia/roce"))
		Expect(plan.Breakout[0].Values).To(HaveKeyWithValue("adaptive-routing", true))
		Expect(plan.Breakout[1].Path).To(Equal("/nvidia/cc/config"))
		Expect(plan.PostBreakout).To(BeEmpty())
		Expect(plan.RuntimeConfig).To(BeEmpty())
	})

	It("extracts non-empty post-breakout operations", func() {
		document := decodeFixture("prepare-plan.json")
		group := findGroup(document, semanticGroupPostBreakout)
		group.OperationRefs = []string{"post-breakout.roce-rtt"}
		document.Plan.Operations["post-breakout.roce-rtt"] = semanticOperationRecord{
			Path:   "/nvidia/roce/rtt",
			Values: map[string]any{"dscp": json.Number("48")},
		}

		plan, err := buildPlan(document, PlanStagePrepare)

		Expect(err).NotTo(HaveOccurred())
		Expect(plan.PostBreakout).To(Equal([]dmscli.XPathOperation{
			{Path: "/nvidia/roce/rtt", Values: map[string]any{"dscp": json.Number("48")}},
		}))
	})

	It("extracts supported configure groups and skips eSwitch and VF lifecycle", func() {
		plan := compileFixture("configure-plan.json", PlanStageConfigure)

		Expect(runtimeGroupNames(plan)).To(Equal([]string{"link-runtime", "cc", "link-event"}))
		Expect(plan.RuntimeConfig[0].Operations).To(HaveLen(4))
		Expect(plan.RuntimeConfig[1].Operations).To(HaveLen(24))
		Expect(plan.RuntimeConfig[2].Operations).To(HaveLen(21))
		Expect(plan.Breakout).To(BeEmpty())
		Expect(plan.PostBreakout).To(BeEmpty())
	})

	It("preserves operation order, including repeated paths", func() {
		plan := compileFixture("configure-plan.json", PlanStageConfigure)
		operations := plan.RuntimeConfig[0].Operations

		Expect(operations[0].Path).To(Equal("/nvidia/link/ipg"))
		Expect(operations[1].Path).To(Equal("/nvidia/link/physical"))
		Expect(operations[1].Values).To(HaveKeyWithValue("admin-status", "down"))
		Expect(operations[2].Path).To(Equal("/nvidia/link/physical"))
		Expect(operations[2].Values).To(HaveKeyWithValue("admin-status", "up"))
		Expect(operations[3].Path).To(Equal("/nvidia/link/netdev"))
	})

	It("sorts runtime groups by semantic order", func() {
		document := decodeFixture("configure-plan.json")
		groups := document.Plan.Semantic.Groups
		for left, right := 0, len(groups)-1; left < right; left, right = left+1, right-1 {
			groups[left], groups[right] = groups[right], groups[left]
		}

		plan, err := buildPlan(document, PlanStageConfigure)

		Expect(err).NotTo(HaveOccurred())
		Expect(runtimeGroupNames(plan)).To(Equal([]string{"link-runtime", "cc", "link-event"}))
	})

	It("ignores operation contents belonging to skipped groups", func() {
		document := decodeFixture("configure-plan.json")
		findGroup(document, semanticGroupESwitch).OperationRefs = []string{"missing-operation"}

		plan, err := buildPlan(document, PlanStageConfigure)

		Expect(err).NotTo(HaveOccurred())
		Expect(runtimeGroupNames(plan)).To(Equal([]string{"link-runtime", "cc", "link-event"}))
	})

	It("deep-clones parsed operations", func() {
		plan := compileFixture("prepare-plan.json", PlanStagePrepare)
		clone := clonePlan(plan)
		clone.Breakout[0].Values["adaptive-routing"] = false

		Expect(plan.Breakout[0].Values).To(HaveKeyWithValue("adaptive-routing", true))
	})

	DescribeTable("rejects invalid semantic plans",
		func(stage PlanStage, mutate func(*planDocument), expected string) {
			document := decodeFixture("prepare-plan.json")
			if stage == PlanStageConfigure {
				document = decodeFixture("configure-plan.json")
			}
			mutate(document)

			_, err := buildPlan(document, stage)

			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("empty name", PlanStagePrepare, func(document *planDocument) {
			document.Plan.Name = ""
		}, "name must not be empty"),
		Entry("wrong family", PlanStagePrepare, func(document *planDocument) {
			document.Plan.Family = "other"
		}, "family"),
		Entry("empty profile", PlanStagePrepare, func(document *planDocument) {
			document.Plan.Profile = ""
		}, "profile must not be empty"),
		Entry("wrong stage", PlanStagePrepare, func(document *planDocument) {
			document.Plan.Stage = string(PlanStageConfigure)
		}, "stage"),
		Entry("wrong deployment mode", PlanStagePrepare, func(document *planDocument) {
			document.Plan.Params.DeploymentMode = "bare-metal"
		}, "deployment mode"),
		Entry("wrong path dialect", PlanStagePrepare, func(document *planDocument) {
			document.Plan.PathDialect = "other"
		}, "path dialect"),
		Entry("missing semantic groups", PlanStagePrepare, func(document *planDocument) {
			document.Plan.Semantic = nil
		}, "does not contain semantic groups"),
		Entry("duplicate group", PlanStagePrepare, func(document *planDocument) {
			document.Plan.Semantic.Groups = append(
				document.Plan.Semantic.Groups, document.Plan.Semantic.Groups[0])
		}, "is duplicated"),
		Entry("wrong group stage", PlanStagePrepare, func(document *planDocument) {
			document.Plan.Semantic.Groups[0].Stage = "configure"
		}, "stage"),
		Entry("unknown group", PlanStageConfigure, func(document *planDocument) {
			document.Plan.Semantic.Groups[0].Name = "unknown-runtime"
		}, "unsupported doSPCX semantic group"),
		Entry("empty operation reference", PlanStagePrepare, func(document *planDocument) {
			document.Plan.Semantic.Groups[0].OperationRefs[0] = ""
		}, "reference at index 0 is empty"),
		Entry("duplicate operation reference", PlanStagePrepare, func(document *planDocument) {
			refs := document.Plan.Semantic.Groups[0].OperationRefs
			document.Plan.Semantic.Groups[0].OperationRefs = append(refs, refs[0])
		}, "is duplicated"),
		Entry("missing operation", PlanStagePrepare, func(document *planDocument) {
			document.Plan.Semantic.Groups[0].OperationRefs[0] = "missing-operation"
		}, "references missing operation"),
		Entry("unsupported operation kind", PlanStagePrepare, func(document *planDocument) {
			mutateFirstOperation(document, func(operation *semanticOperationRecord) {
				operation.Kind = "delete"
			})
		}, "unsupported kind"),
		Entry("invalid operation path", PlanStagePrepare, func(document *planDocument) {
			mutateFirstOperation(document, func(operation *semanticOperationRecord) {
				operation.Path = "/other/path"
			})
		}, "invalid path"),
		Entry("empty operation values", PlanStagePrepare, func(document *planDocument) {
			mutateFirstOperation(document, func(operation *semanticOperationRecord) {
				operation.Values = nil
			})
		}, "has no values"),
		Entry("invalid leaf", PlanStagePrepare, func(document *planDocument) {
			mutateFirstOperation(document, func(operation *semanticOperationRecord) {
				operation.Values = map[string]any{"bad leaf": true}
			})
		}, "invalid leaf"),
		Entry("invalid value", PlanStagePrepare, func(document *planDocument) {
			mutateFirstOperation(document, func(operation *semanticOperationRecord) {
				operation.Values = map[string]any{"value": map[string]any{"nested": true}}
			})
		}, "unsupported value"),
	)

	It("rejects a nil plan", func() {
		_, err := buildPlan(nil, PlanStagePrepare)
		Expect(err).To(MatchError(ContainSubstring("must not be nil")))
	})

	DescribeTable("rejects invalid JSON documents",
		func(content string, expected string) {
			_, err := decodePlanDocument([]byte(content))
			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("empty", "", "must not be empty"),
		Entry("malformed", "{", "decode doSPCX semantic plan"),
		Entry("trailing", `{"plan": {}} {}`, "trailing JSON data"),
	)
})

func findGroup(document *planDocument, name string) *semanticGroupRecord {
	for index := range document.Plan.Semantic.Groups {
		if document.Plan.Semantic.Groups[index].Name == name {
			return &document.Plan.Semantic.Groups[index]
		}
	}
	Fail("semantic group not found: " + name)
	return nil
}

func runtimeGroupNames(plan *Plan) []string {
	result := make([]string, len(plan.RuntimeConfig))
	for index, group := range plan.RuntimeConfig {
		result[index] = group.Name
	}
	return result
}

func mutateFirstOperation(document *planDocument, mutate func(*semanticOperationRecord)) {
	ref := document.Plan.Semantic.Groups[0].OperationRefs[0]
	operation := document.Plan.Operations[ref]
	mutate(&operation)
	document.Plan.Operations[ref] = operation
}
