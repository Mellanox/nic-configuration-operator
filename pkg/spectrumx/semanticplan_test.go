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
	"fmt"
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

	decodeFixture := func(name string) *planDocument {
		document, err := decodePlanDocument(readFixture(name))
		Expect(err).NotTo(HaveOccurred())
		return document
	}

	compileFixture := func(name string, stage PlanStage) *Plan {
		document := decodeFixture(name)
		plan, err := buildDMSPlan(context.Background(), document, stage)
		Expect(err).NotTo(HaveOccurred())
		return plan
	}

	It("builds prepare batches and retains the post-breakout phase marker", func() {
		plan := compileFixture("prepare-plan.json", PlanStagePrepare)

		Expect(plan.Name).To(Equal("nco-parser-semantic-prepare"))
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
					if !strings.HasSuffix(leaf, "-pending") {
						Expect(query.Leaves).To(ContainElement(leaf + "-pending"))
					}
				}
			}
		}
		Expect(plan.Groups[1].Name).To(Equal("post-breakout"))
		Expect(plan.Groups[1].RequiresReboot).To(BeTrue())
		Expect(plan.Groups[1].PhaseMarker).To(BeTrue())
		Expect(plan.Groups[1].Targets).To(BeEmpty())
	})

	DescribeTable("compiles SPX_NetPlugin post-breakout operations against the expanded device view",
		func(planes int) {
			document := decodeFixture("prepare-plan.json")
			document.Plan.Profile = dospcxProfileNetPlugin
			document.Plan.Params.Planes = planes
			if planes == 4 {
				document.Plan.DetectedHW.PlatformType = "b300"
			}
			document.Plan.Devices = document.Plan.Devices[:1]
			document.Plan.PostBreakoutDevices = make([]planDevice, planes)
			postBreakout := findGroupRecord(document, "post-breakout")
			postBreakout.DeviceView = ""
			postBreakout.RequiresReboot = false
			postBreakout.OperationRefs = make([]string, 0, planes)
			for plane := 0; plane < planes; plane++ {
				bdf, err := bdfForPlane(document.Plan.Devices[0].BDF, plane)
				Expect(err).NotTo(HaveOccurred())
				document.Plan.PostBreakoutDevices[plane] = planDevice{
					BDF:           bdf,
					DMSTarget:     "pci/" + bdf,
					Rail:          0,
					Plane:         plane,
					PlaneExplicit: true,
					Network:       targetRoleEW,
				}
				operationID := fmt.Sprintf("post-breakout.port-%d", plane+1)
				operation := semanticOperationRecord{
					Path:        "/nvidia/roce/rtt",
					Values:      map[string]any{"dscp": json.Number("48")},
					TargetClass: targetClassPFNetdevAll,
				}
				if plane > 0 {
					port := plane + 1
					operation.Port = &port
				}
				document.Plan.Operations[operationID] = operation
				postBreakout.OperationRefs = append(postBreakout.OperationRefs, operationID)
			}

			plan, err := buildDMSPlan(context.Background(), document, PlanStagePrepare)

			Expect(err).NotTo(HaveOccurred())
			Expect(plan.Groups).To(HaveLen(2))
			compiled := plan.Groups[1]
			Expect(compiled.Name).To(Equal("post-breakout"))
			Expect(compiled.DeviceView).To(Equal(deviceViewPostBreakout))
			Expect(compiled.RequiresReboot).To(BeTrue())
			Expect(compiled.PhaseMarker).To(BeFalse())
			Expect(compiled.Targets).To(HaveLen(planes * planes))
			Expect(targetNames(compiled.Targets)).To(ContainElements(
				fmt.Sprintf("pci/%s?port=1", document.Plan.PostBreakoutDevices[planes-1].BDF),
				fmt.Sprintf("pci/%s?port=%d", document.Plan.PostBreakoutDevices[planes-1].BDF, planes),
			))
		},
		Entry("two planes", 2),
		Entry("four planes", 4),
	)

	It("skips eswitch and vf-lifecycle while compiling configure groups", func() {
		plan := compileFixture("configure-plan.json", PlanStageConfigure)

		Expect(groupOperationNames(plan.Groups)).To(Equal([]string{"link-runtime", "cc", "link-event"}))
		Expect(plan.SkippedGroups).To(Equal([]SkippedSemanticGroup{
			{Name: "eswitch", Order: 40, Reason: "eSwitch lifecycle is outside the current NCO plan execution scope"},
			{Name: "vf-lifecycle", Order: 60, Reason: "VF representor lifecycle is outside the current NCO plan execution scope"},
		}))
	})

	It("preserves ordered SET transitions but queries only final desired state", func() {
		document := decodeFixture("configure-plan.json")
		findGroupRecord(document, "link-runtime").FanoutOrder = "per_target"

		plan, err := buildDMSPlan(context.Background(), document, PlanStageConfigure)

		Expect(err).NotTo(HaveOccurred())
		linkRuntime := plan.Groups[0]
		Expect(linkRuntime.FanoutOrder).To(Equal("per_target"))
		Expect(linkRuntime.Targets).To(HaveLen(4))
		batch := linkRuntime.Targets[0]
		Expect(batch.Operations).To(HaveLen(4))
		Expect(batch.Operations[1].Values).To(HaveKeyWithValue("admin-status", "down"))
		Expect(batch.Operations[2].Values).To(HaveKeyWithValue("admin-status", "up"))
		Expect(batch.Desired).To(HaveLen(3))
		Expect(batch.Desired[1].Values).To(HaveKeyWithValue("admin-status", "up"))
		Expect(batch.Queries[1]).To(Equal(dmsQuery("/nvidia/link/physical", "admin-status")))
	})

	It("resolves bonded RDMA operations to each plane-zero target", func() {
		plan := compileFixture("configure-plan.json", PlanStageConfigure)

		cc := plan.Groups[1]
		Expect(cc.Name).To(Equal("cc"))
		Expect(cc.Targets).To(HaveLen(2))
		Expect([]string{cc.Targets[0].Target, cc.Targets[1].Target}).To(Equal([]string{
			"pci/0000:64:00.0", "pci/0001:15:00.0",
		}))
		Expect(cc.Targets[0].Operations).To(HaveLen(24))
	})

	It("combines per-device and RDMA-scoped operations without widening bonded targets", func() {
		plan := compileFixture("configure-plan.json", PlanStageConfigure)

		linkEvent := plan.Groups[2]
		Expect(linkEvent.Name).To(Equal("link-event"))
		Expect(linkEvent.Targets).To(HaveLen(4))
		Expect(linkEvent.Targets[0].Operations).To(HaveLen(21))
		Expect(linkEvent.Targets[1].Operations).To(HaveLen(2))
		Expect(linkEvent.Targets[2].Operations).To(HaveLen(21))
		Expect(linkEvent.Targets[3].Operations).To(HaveLen(2))
	})

	It("sorts semantic groups by declared order", func() {
		document := decodeFixture("prepare-plan.json")
		groups := document.Plan.Semantic.Groups
		document.Plan.Semantic.Groups = []semanticGroupRecord{groups[1], groups[0]}

		plan, err := buildDMSPlan(context.Background(), document, PlanStagePrepare)

		Expect(err).NotTo(HaveOccurred())
		Expect(groupOperationNames(plan.Groups)).To(Equal([]string{"breakout", "post-breakout"}))
	})

	It("uses documented defaults for omitted kind and target class", func() {
		document := decodeFixture("prepare-plan.json")
		operationID := findGroupRecord(document, "breakout").OperationRefs[0]
		operation := document.Plan.Operations[operationID]
		operation.Kind = ""
		operation.TargetClass = ""
		document.Plan.Operations[operationID] = operation

		plan, err := buildDMSPlan(context.Background(), document, PlanStagePrepare)

		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Groups[0].Targets).To(HaveLen(2))
	})

	It("validates references in intentionally skipped groups", func() {
		document := decodeFixture("configure-plan.json")
		findGroupRecord(document, "eswitch").OperationRefs = []string{"missing-operation"}

		plan, err := buildDMSPlan(context.Background(), document, PlanStageConfigure)

		Expect(plan).To(BeNil())
		Expect(err).To(MatchError(ContainSubstring("references missing operation")))
	})

	DescribeTable("rejects malformed plan contracts",
		func(mutate func(*planDocument), stage PlanStage, expected string) {
			document := decodeFixture("prepare-plan.json")
			mutate(document)
			plan, err := buildDMSPlan(context.Background(), document, stage)
			Expect(plan).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("missing semantic groups", func(document *planDocument) {
			document.Plan.Semantic = nil
		}, PlanStagePrepare, "semantic groups"),
		Entry("wrong path dialect", func(document *planDocument) {
			document.Plan.PathDialect = "legacy"
		}, PlanStagePrepare, "path dialect"),
		Entry("missing operation reference", func(document *planDocument) {
			document.Plan.Semantic.Groups[0].OperationRefs = []string{"missing-operation"}
		}, PlanStagePrepare, "references missing operation"),
		Entry("unsupported operation kind", func(document *planDocument) {
			operationID := document.Plan.Semantic.Groups[0].OperationRefs[0]
			operation := document.Plan.Operations[operationID]
			operation.Kind = "delete"
			document.Plan.Operations[operationID] = operation
		}, PlanStagePrepare, "unsupported kind"),
		Entry("unsupported target class", func(document *planDocument) {
			operationID := document.Plan.Semantic.Groups[0].OperationRefs[0]
			operation := document.Plan.Operations[operationID]
			operation.TargetClass = "host_global"
			document.Plan.Operations[operationID] = operation
		}, PlanStagePrepare, "unsupported target class"),
		Entry("empty leaf list", func(document *planDocument) {
			operationID := document.Plan.Semantic.Groups[0].OperationRefs[0]
			operation := document.Plan.Operations[operationID]
			operation.Values = map[string]any{"invalid": []string{}}
			document.Plan.Operations[operationID] = operation
		}, PlanStagePrepare, "unsupported value"),
		Entry("nested leaf list", func(document *planDocument) {
			operationID := document.Plan.Semantic.Groups[0].OperationRefs[0]
			operation := document.Plan.Operations[operationID]
			operation.Values = map[string]any{"invalid": [][]int{{1}}}
			document.Plan.Operations[operationID] = operation
		}, PlanStagePrepare, "unsupported value"),
		Entry("duplicated group", func(document *planDocument) {
			document.Plan.Semantic.Groups = append(document.Plan.Semantic.Groups, document.Plan.Semantic.Groups[0])
		}, PlanStagePrepare, `group "breakout" is duplicated`),
		Entry("invalid device target", func(document *planDocument) {
			document.Plan.Devices[0].DMSTarget = "pci/0000:ff:00.0"
		}, PlanStagePrepare, "invalid DMS target"),
		Entry("wrong prepare device view", func(document *planDocument) {
			document.Plan.Semantic.Groups[0].DeviceView = deviceViewPostBreakout
		}, PlanStagePrepare, "expected \"pre_breakout\""),
		Entry("invalid operation port", func(document *planDocument) {
			operationID := document.Plan.Semantic.Groups[0].OperationRefs[0]
			operation := document.Plan.Operations[operationID]
			port := 0
			operation.Port = &port
			document.Plan.Operations[operationID] = operation
		}, PlanStagePrepare, "invalid port"),
		Entry("missing post-breakout device view for executable operations", func(document *planDocument) {
			document.Plan.PostBreakoutDevices = nil
			document.Plan.Semantic.Groups[1].OperationRefs = []string{
				document.Plan.Semantic.Groups[0].OperationRefs[0],
			}
		}, PlanStagePrepare, "post-breakout device view is empty"),
		Entry("stage mismatch", func(_ *planDocument) {}, PlanStageConfigure, `stage is "prepare", expected "configure"`),
		Entry("unknown group", func(document *planDocument) {
			document.Plan.Semantic.Groups[0].Name = "new-runtime-phase"
		}, PlanStagePrepare, "unsupported doSPCX semantic group"),
	)

	DescribeTable("does not allow excluded operation classes in executable groups",
		func(mutate func(*semanticOperationRecord)) {
			document := decodeFixture("configure-plan.json")
			group := findGroupRecord(document, "link-runtime")
			operationID := group.OperationRefs[0]
			operation := document.Plan.Operations[operationID]
			mutate(&operation)
			document.Plan.Operations[operationID] = operation

			plan, err := buildDMSPlan(context.Background(), document, PlanStageConfigure)

			Expect(plan).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring("outside the current NCO execution scope")))
		},
		Entry("eSwitch path", func(operation *semanticOperationRecord) {
			operation.Path = "/nvidia/eswitch"
		}),
		Entry("eSwitch target class", func(operation *semanticOperationRecord) {
			operation.TargetClass = targetClassPerESwitch
		}),
		Entry("VF target class", func(operation *semanticOperationRecord) {
			operation.TargetClass = targetClassVFRepresentor
		}),
		Entry("per-VF scope", func(operation *semanticOperationRecord) {
			operation.Scope = "per_vf"
		}),
	)

	It("resolves per-PF RDMA operations only to devices with an RDMA endpoint", func() {
		document := decodeFixture("configure-plan.json")
		document.Plan.RuntimeContext.RDMATopology = rdmaTopologyPerPF
		document.Plan.Devices[1].RDMADevice = ""
		document.Plan.Devices[3].RDMADevice = ""

		plan, err := buildDMSPlan(context.Background(), document, PlanStageConfigure)

		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Groups[1].Name).To(Equal("cc"))
		Expect(plan.Groups[1].Targets).To(HaveLen(2))
		Expect([]string{plan.Groups[1].Targets[0].Target, plan.Groups[1].Targets[1].Target}).To(Equal([]string{
			"pci/0000:64:00.0", "pci/0001:15:00.0",
		}))
	})

	It("rejects empty and nil plans without panicking", func() {
		document, err := decodePlanDocument(nil)
		Expect(document).To(BeNil())
		Expect(err).To(MatchError("doSPCX plan must not be empty"))

		plan, err := buildDMSPlan(context.Background(), nil, PlanStagePrepare)
		Expect(plan).To(BeNil())
		Expect(err).To(MatchError("doSPCX plan must not be nil"))
	})
})

func findGroupRecord(document *planDocument, name string) *semanticGroupRecord {
	for index := range document.Plan.Semantic.Groups {
		if document.Plan.Semantic.Groups[index].Name == name {
			return &document.Plan.Semantic.Groups[index]
		}
	}
	return nil
}

func groupOperationNames(groups []DMSOperationGroup) []string {
	result := make([]string, 0, len(groups))
	for _, group := range groups {
		result = append(result, group.Name)
	}
	return result
}

func targetNames(targets []DMSTargetOperations) []string {
	result := make([]string, len(targets))
	for index, target := range targets {
		result[index] = target.Target
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
