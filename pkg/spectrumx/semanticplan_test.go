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

package spectrumx

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
)

var _ = Describe("doSPCX semantic plans", func() {
	readFixture := func(name string) []byte {
		content, err := os.ReadFile(filepath.Join("testdata", "semantic", name))
		Expect(err).NotTo(HaveOccurred())
		return content
	}

	parseFixture := func(name string, stage PlanStage) *SemanticPlan {
		plan, err := ParseSemanticPlan(readFixture(name), stage)
		Expect(err).NotTo(HaveOccurred())
		return plan
	}

	Describe("ParseSemanticPlan", func() {
		It("parses the generated host-k8s prepare semantic surface", func() {
			plan := parseFixture("prepare-plan.json", PlanStagePrepare)

			Expect(plan.Name).To(Equal("nco-parser-semantic-prepare"))
			Expect(plan.Profile).To(Equal("hwmp"))
			Expect(plan.PathDialect).To(Equal(semanticPathDialect))
			Expect(plan.Devices).To(HaveLen(2))
			Expect(plan.RuntimeContext.RDMATopology).To(Equal(rdmaTopologyPerPF))
			Expect(plan.Groups).To(HaveLen(2))
			Expect(plan.Groups[0].Name).To(Equal("breakout"))
			Expect(plan.Groups[0].Operations).To(HaveLen(15))
			Expect(plan.Groups[0].Operations[0].ID).To(Equal("spcx-base-nvconfig.roce.adaptive-routing-cc-steering-ext-tx-sched-locality-mode"))
			Expect(plan.Groups[0].Operations[0].Path).To(Equal("/nvidia/roce"))
			Expect(plan.Groups[1].Name).To(Equal("post-breakout"))
			Expect(plan.Groups[1].Operations).To(BeEmpty())
			Expect(plan.Groups[1].RequiresReboot).To(BeTrue())
		})

		It("parses all generated configure groups before applying execution policy", func() {
			plan := parseFixture("configure-plan.json", PlanStageConfigure)

			Expect(plan.Devices).To(HaveLen(4))
			Expect(plan.RuntimeContext.RDMATopology).To(Equal(rdmaTopologyPerRailBond))
			Expect(plan.Groups).To(HaveLen(5))
			Expect(groupNames(plan.Groups)).To(Equal([]string{
				"link-runtime", "eswitch", "vf-lifecycle", "cc", "link-event",
			}))
			Expect(plan.Groups[0].Operations).To(HaveLen(4))
			Expect(plan.Groups[0].Operations[1].Values).To(HaveKeyWithValue("admin-status", "down"))
			Expect(plan.Groups[0].Operations[2].Values).To(HaveKeyWithValue("admin-status", "up"))
			Expect(plan.Groups[1].FanoutOrder).To(Equal("per_step_barrier"))
			Expect(plan.Groups[2].Operations[0].TargetClass).To(Equal(targetClassVFRepresentor))
		})

		DescribeTable("rejects malformed semantic contracts",
			func(mutate func(map[string]any), expected string) {
				var document map[string]any
				Expect(json.Unmarshal(readFixture("prepare-plan.json"), &document)).To(Succeed())
				mutate(document)
				content, err := json.Marshal(document)
				Expect(err).NotTo(HaveOccurred())

				plan, err := ParseSemanticPlan(content, PlanStagePrepare)

				Expect(plan).To(BeNil())
				Expect(err).To(MatchError(ContainSubstring(expected)))
			},
			Entry("missing semantic groups", func(document map[string]any) {
				delete(document["plan"].(map[string]any), "semantic")
			}, "semantic groups"),
			Entry("wrong path dialect", func(document map[string]any) {
				document["plan"].(map[string]any)["path_dialect"] = "legacy"
			}, "path dialect"),
			Entry("missing operation reference", func(document map[string]any) {
				groups := document["plan"].(map[string]any)["semantic"].(map[string]any)["groups"].([]any)
				groups[0].(map[string]any)["operation_refs"] = []any{"missing-operation"}
			}, "references missing operation"),
			Entry("operation in another group", func(document map[string]any) {
				operations := document["plan"].(map[string]any)["operations"].(map[string]any)
				for _, value := range operations {
					value.(map[string]any)["execution_group"] = "other"
					break
				}
			}, "belongs to group"),
			Entry("unsupported operation kind", func(document map[string]any) {
				operations := document["plan"].(map[string]any)["operations"].(map[string]any)
				for _, value := range operations {
					value.(map[string]any)["kind"] = "delete"
					break
				}
			}, "unsupported kind"),
			Entry("unsupported target class", func(document map[string]any) {
				operations := document["plan"].(map[string]any)["operations"].(map[string]any)
				for _, value := range operations {
					value.(map[string]any)["target_class"] = "host_global"
					break
				}
			}, "unsupported target class"),
			Entry("duplicated group", func(document map[string]any) {
				semantic := document["plan"].(map[string]any)["semantic"].(map[string]any)
				groups := semantic["groups"].([]any)
				semantic["groups"] = append(groups, groups[0])
			}, "group \"breakout\" is duplicated"),
			Entry("invalid device target", func(document map[string]any) {
				devices := document["plan"].(map[string]any)["devices"].([]any)
				devices[0].(map[string]any)["dms_target"] = "pci/0000:ff:00.0"
			}, "invalid DMS target"),
		)

		It("rejects a stage mismatch", func() {
			plan, err := ParseSemanticPlan(readFixture("configure-plan.json"), PlanStagePrepare)

			Expect(plan).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring(`stage is "configure", expected "prepare"`)))
		})

		It("uses the documented pf_netdev_all default when target_class is omitted", func() {
			var document map[string]any
			Expect(json.Unmarshal(readFixture("prepare-plan.json"), &document)).To(Succeed())
			planObject := document["plan"].(map[string]any)
			firstRef := planObject["semantic"].(map[string]any)["groups"].([]any)[0].(map[string]any)["operation_refs"].([]any)[0].(string)
			delete(planObject["operations"].(map[string]any)[firstRef].(map[string]any), "target_class")
			content, err := json.Marshal(document)
			Expect(err).NotTo(HaveOccurred())

			plan, err := ParseSemanticPlan(content, PlanStagePrepare)

			Expect(err).NotTo(HaveOccurred())
			Expect(plan.Groups[0].Operations[0].TargetClass).To(Equal(targetClassPFNetdevAll))
		})

		It("sorts semantic groups by their declared order", func() {
			var document map[string]any
			Expect(json.Unmarshal(readFixture("prepare-plan.json"), &document)).To(Succeed())
			semantic := document["plan"].(map[string]any)["semantic"].(map[string]any)
			groups := semantic["groups"].([]any)
			semantic["groups"] = []any{groups[1], groups[0]}
			content, err := json.Marshal(document)
			Expect(err).NotTo(HaveOccurred())

			plan, err := ParseSemanticPlan(content, PlanStagePrepare)

			Expect(err).NotTo(HaveOccurred())
			Expect(groupNames(plan.Groups)).To(Equal([]string{"breakout", "post-breakout"}))
		})

		It("rejects nil input without panicking", func() {
			plan, err := ParseSemanticPlan(nil, PlanStagePrepare)

			Expect(plan).To(BeNil())
			Expect(err).To(MatchError("doSPCX plan must not be empty"))
		})
	})

	Describe("BuildDMSOperationPlan", func() {
		It("builds prepare batches and retains the post-breakout phase marker", func() {
			semantic := parseFixture("prepare-plan.json", PlanStagePrepare)

			plan, err := semantic.BuildDMSOperationPlan(context.Background())

			Expect(err).NotTo(HaveOccurred())
			Expect(plan.Stage).To(Equal(PlanStagePrepare))
			Expect(plan.SkippedGroups).To(BeEmpty())
			Expect(plan.Groups).To(HaveLen(2))
			Expect(plan.Groups[0].Name).To(Equal("breakout"))
			Expect(plan.Groups[0].Targets).To(HaveLen(2))
			for _, target := range plan.Groups[0].Targets {
				Expect(target.Operations).To(HaveLen(15))
				Expect(target.Desired).To(HaveLen(12))
				Expect(target.Queries).To(HaveLen(12))
				for _, query := range target.Queries {
					desired := findDesiredOperation(target.Desired, query.Path)
					Expect(desired.Path).To(Equal(query.Path))
					Expect(query.Leaves).To(HaveLen(2 * len(desired.Values)))
					for _, leaf := range query.Leaves {
						if strings.HasSuffix(leaf, "-pending") {
							continue
						}
						Expect(query.Leaves).To(ContainElement(leaf + "-pending"))
					}
				}
			}
			Expect(plan.Groups[1].Name).To(Equal("post-breakout"))
			Expect(plan.Groups[1].PhaseMarker).To(BeTrue())
			Expect(plan.Groups[1].Targets).To(BeEmpty())
		})

		It("skips eswitch and vf-lifecycle while compiling the remaining configure groups", func() {
			semantic := parseFixture("configure-plan.json", PlanStageConfigure)

			plan, err := semantic.BuildDMSOperationPlan(context.Background())

			Expect(err).NotTo(HaveOccurred())
			Expect(groupOperationNames(plan.Groups)).To(Equal([]string{"link-runtime", "cc", "link-event"}))
			Expect(plan.SkippedGroups).To(Equal([]SkippedSemanticGroup{
				{Name: "eswitch", Order: 40, Reason: "eSwitch lifecycle is outside the current NCO plan execution scope"},
				{Name: "vf-lifecycle", Order: 60, Reason: "VF representor lifecycle is outside the current NCO plan execution scope"},
			}))
		})

		It("preserves ordered SET transitions but queries only the final desired state", func() {
			semantic := parseFixture("configure-plan.json", PlanStageConfigure)
			plan, err := semantic.BuildDMSOperationPlan(context.Background())
			Expect(err).NotTo(HaveOccurred())

			linkRuntime := plan.Groups[0]
			Expect(linkRuntime.Name).To(Equal("link-runtime"))
			Expect(linkRuntime.Targets).To(HaveLen(4))
			batch := linkRuntime.Targets[0]
			Expect(batch.Operations).To(HaveLen(4))
			Expect(batch.Operations[1].Values).To(HaveKeyWithValue("admin-status", "down"))
			Expect(batch.Operations[2].Values).To(HaveKeyWithValue("admin-status", "up"))
			Expect(batch.Desired).To(HaveLen(3))
			Expect(batch.Desired[1].Path).To(Equal("/nvidia/link/physical"))
			Expect(batch.Desired[1].Values).To(HaveKeyWithValue("admin-status", "up"))
			Expect(batch.Queries[1]).To(Equal(dmsQuery("/nvidia/link/physical", "admin-status")))
		})

		It("resolves bonded RDMA operations only to the per-rail control targets", func() {
			semantic := parseFixture("configure-plan.json", PlanStageConfigure)
			plan, err := semantic.BuildDMSOperationPlan(context.Background())
			Expect(err).NotTo(HaveOccurred())

			cc := plan.Groups[1]
			Expect(cc.Name).To(Equal("cc"))
			Expect(cc.Targets).To(HaveLen(2))
			Expect([]string{cc.Targets[0].Target, cc.Targets[1].Target}).To(Equal([]string{
				"pci/0000:64:00.0", "pci/0001:15:00.0",
			}))
			Expect(cc.Targets[0].Operations).To(HaveLen(24))
		})

		It("combines per-device and RDMA-scoped operations without widening bonded targets", func() {
			semantic := parseFixture("configure-plan.json", PlanStageConfigure)
			plan, err := semantic.BuildDMSOperationPlan(context.Background())
			Expect(err).NotTo(HaveOccurred())

			linkEvent := plan.Groups[2]
			Expect(linkEvent.Name).To(Equal("link-event"))
			Expect(linkEvent.Targets).To(HaveLen(4))
			Expect(linkEvent.Targets[0].Operations).To(HaveLen(21))
			Expect(linkEvent.Targets[1].Operations).To(HaveLen(2))
			Expect(linkEvent.Targets[2].Operations).To(HaveLen(21))
			Expect(linkEvent.Targets[3].Operations).To(HaveLen(2))
		})

		It("fails closed for an unknown semantic group", func() {
			semantic := parseFixture("configure-plan.json", PlanStageConfigure)
			semantic.Groups[0].Name = "new-runtime-phase"

			plan, err := semantic.BuildDMSOperationPlan(context.Background())

			Expect(plan).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring(`unsupported doSPCX semantic group "new-runtime-phase"`)))
		})

		It("rejects a missing bonded RDMA control target", func() {
			semantic := parseFixture("configure-plan.json", PlanStageConfigure)
			semantic.RuntimeContext.ExpectedRDMA[0].ControlTarget = ""

			plan, err := semantic.BuildDMSOperationPlan(context.Background())

			Expect(plan).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring("rail 0 has no RDMA control target")))
		})

		It("rejects duplicated bonded RDMA rails", func() {
			semantic := parseFixture("configure-plan.json", PlanStageConfigure)
			semantic.RuntimeContext.ExpectedRDMA[1].Rail = 0

			plan, err := semantic.BuildDMSOperationPlan(context.Background())

			Expect(plan).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring("RDMA control rail 0 is duplicated")))
		})

		It("rejects a nil semantic plan", func() {
			var semantic *SemanticPlan

			plan, err := semantic.BuildDMSOperationPlan(context.Background())

			Expect(plan).To(BeNil())
			Expect(err).To(MatchError("semantic plan must not be nil"))
		})
	})
})

func groupNames(groups []SemanticGroup) []string {
	result := make([]string, 0, len(groups))
	for _, group := range groups {
		result = append(result, group.Name)
	}
	return result
}

func groupOperationNames(groups []DMSOperationGroup) []string {
	result := make([]string, 0, len(groups))
	for _, group := range groups {
		result = append(result, group.Name)
	}
	return result
}

func dmsQuery(path string, leaves ...string) dmscli.XPathQuery {
	return dmscli.XPathQuery{Path: path, Leaves: leaves}
}

func findDesiredOperation(operations []dmscli.XPathOperation, path string) dmscli.XPathOperation {
	for _, operation := range operations {
		if operation.Path == path {
			return operation
		}
	}
	return dmscli.XPathOperation{}
}
