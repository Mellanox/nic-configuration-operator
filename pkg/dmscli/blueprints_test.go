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

package dmscli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Blueprint plan", func() {
	const (
		planName           = "nco-node-1-spcx-prepare"
		blueprintsRoot     = "/opt/nvidia/blueprints"
		blueprintsStateDir = "/var/lib/blueprints"
	)

	var commands []recordedCommand

	newRequest := func() BlueprintPlanRequest {
		return BlueprintPlanRequest{
			BlueprintsRoot:     blueprintsRoot,
			BlueprintsStateDir: blueprintsStateDir,
			Profile:            "SPX_Multiplane",
			Name:               planName,
			Stage:              "prepare",
			TargetMapFile:      "/var/lib/blueprints/target-maps/nco-node-1.json",
			Params:             []string{"deployment_mode=host-k8s", "planes=2"},
		}
	}

	It("generates a prepare plan through the agentless action", func() {
		executor := fakeExecutor([]byte(`{
			"status":"ok",
			"plan-json":{"plan":{"name":"nco-node-1-spcx-prepare","stage":"prepare"},"artifacts":{"manifest":[]}}
		}`), nil, &commands)

		result, err := GenerateBlueprintPlan(context.Background(), executor, newRequest())

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status).To(Equal("ok"))
		Expect(json.Valid(result.PlanJSON)).To(BeTrue())

		Expect(commands).To(HaveLen(1))
		Expect(commands[0].executable).To(Equal(dmsCLIExecutable))
		Expect(commands[0].args).To(Equal([]string{
			"--json",
			blueprintsPlanPath,
			"profile=SPX_Multiplane",
			"name=" + planName,
			"stage=prepare",
			"target-map-file=file:/var/lib/blueprints/target-maps/nco-node-1.json",
			"params=deployment_mode=host-k8s,planes=2",
		}))
		Expect(commands[0].command.RunCalls).To(Equal(1))
		Expect(commands[0].command.CombinedOutputCalls).To(BeZero())
		Expect(commands[0].command.Env).To(ContainElement("BLUEPRINTS_ROOT=" + blueprintsRoot))
		Expect(commands[0].command.Env).To(ContainElement("BP_STATE_DIR=" + blueprintsStateDir))
	})

	It("accepts the YANG string encoding of plan-json", func() {
		encodedPlan, err := json.Marshal(`{"plan":{"name":"nco-node-1-spcx-prepare","stage":"prepare"}}`)
		Expect(err).NotTo(HaveOccurred())
		output := append([]byte(`{"status":"ok","plan":"nco-node-1-spcx-prepare","plan-json":`), encodedPlan...)
		output = append(output, '}')
		executor := fakeExecutor(output, nil, &commands)

		result, err := GenerateBlueprintPlan(context.Background(), executor, newRequest())

		Expect(err).NotTo(HaveOccurred())
		Expect(string(result.PlanJSON)).To(Equal(`{"plan":{"name":"nco-node-1-spcx-prepare","stage":"prepare"}}`))
	})

	It("decodes JSON stdout independently from DMS diagnostics on stderr", func() {
		stdout := []byte(`{"status":"ok","plan":"nco-node-1-spcx-prepare","plan-json":{"plan":{"name":"nco-node-1-spcx-prepare"}}}`)
		stderr := []byte("[2026-09-07 11:10:40][DOCA][WRN] Ignoring unknown batch_op 'GET-CAPS'\n")
		executor := fakeExecutorWithStderr(stdout, stderr, nil, &commands)

		result, err := GenerateBlueprintPlan(context.Background(), executor, newRequest())

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status).To(Equal("ok"))
		Expect(json.Valid(result.PlanJSON)).To(BeTrue())
	})

	It("logs the exact command, structured result, and stderr without the full plan JSON", func() {
		stdout := []byte(`{"status":"ok","plan-json":{"plan":{"name":"nco-node-1-spcx-prepare"}},"error":""}`)
		stderr := []byte("DMS diagnostic\n")
		executor := fakeExecutorWithStderr(stdout, stderr, nil, &commands)
		entries := []capturedLogEntry{}
		ctx := logr.NewContext(context.Background(), logr.New(&capturingLogSink{entries: &entries}))

		result, err := GenerateBlueprintPlan(ctx, executor, newRequest())

		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].message).To(Equal("command output"))
		Expect(entries[0].fields).To(HaveKeyWithValue("command", append([]string{dmsCLIExecutable}, commands[0].args...)))
		Expect(entries[0].fields).To(HaveKeyWithValue("plan", planName))
		Expect(entries[0].fields).To(HaveKeyWithValue("blueprintsRoot", blueprintsRoot))
		Expect(entries[0].fields).To(HaveKeyWithValue("blueprintsStateDir", blueprintsStateDir))
		Expect(entries[0].fields).NotTo(HaveKey("stdout"))
		Expect(entries[0].fields).To(HaveKeyWithValue("status", "ok"))
		Expect(entries[0].fields).To(HaveKeyWithValue("planJSONBytes", len(result.PlanJSON)))
		Expect(entries[0].fields).To(HaveKeyWithValue("stderr", string(stderr)))
	})

	It("bounds malformed stdout in diagnostics", func() {
		stdout := []byte("not-json-" + strings.Repeat("x", maxBlueprintLogOutputLen))
		executor := fakeExecutor(stdout, nil, &commands)
		entries := []capturedLogEntry{}
		ctx := logr.NewContext(context.Background(), logr.New(&capturingLogSink{entries: &entries}))

		_, err := GenerateBlueprintPlan(ctx, executor, newRequest())

		Expect(err).To(HaveOccurred())
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].fields["stdout"]).To(HaveLen(maxBlueprintLogOutputLen + len("... [truncated]")))
		Expect(entries[0].fields["stdout"]).To(HaveSuffix("... [truncated]"))
	})

	It("returns the structured planner error", func() {
		commandErr := errors.New("exit status 1")
		executor := fakeExecutor([]byte(`{"status":"error","error_msg":"target map does not match live hardware"}`), commandErr, &commands)

		result, err := GenerateBlueprintPlan(context.Background(), executor, newRequest())

		Expect(result).To(Equal(&BlueprintPlanResult{
			Status:       "error",
			ErrorMessage: "target map does not match live hardware",
		}))
		Expect(err).To(MatchError(ContainSubstring("target map does not match live hardware")))
		Expect(errors.Is(err, commandErr)).To(BeTrue())
	})

	It("returns a planner error even when dms-cli exits successfully", func() {
		executor := fakeExecutor([]byte(`{"status":"failed","error":"invalid profile parameters"}`), nil, &commands)

		result, err := GenerateBlueprintPlan(context.Background(), executor, newRequest())

		Expect(result).To(Equal(&BlueprintPlanResult{
			Status:       "failed",
			ErrorMessage: "invalid profile parameters",
		}))
		Expect(err).To(MatchError(ContainSubstring("invalid profile parameters")))
	})

	DescribeTable("validates requests",
		func(mutate func(*BlueprintPlanRequest), expected string) {
			request := newRequest()
			mutate(&request)

			result, err := GenerateBlueprintPlan(context.Background(), fakeExecutor(nil, nil, &commands), request)

			Expect(result).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("empty Blueprints root", func(request *BlueprintPlanRequest) { request.BlueprintsRoot = "" }, "blueprints root"),
		Entry("relative Blueprints root", func(request *BlueprintPlanRequest) { request.BlueprintsRoot = "blueprints" }, "absolute path"),
		Entry("empty Blueprints state directory", func(request *BlueprintPlanRequest) { request.BlueprintsStateDir = "" }, "state directory"),
		Entry("relative Blueprints state directory", func(request *BlueprintPlanRequest) { request.BlueprintsStateDir = "blueprints-state" }, "absolute path"),
		Entry("empty profile", func(request *BlueprintPlanRequest) { request.Profile = "" }, "profile"),
		Entry("empty name", func(request *BlueprintPlanRequest) { request.Name = "" }, "name"),
		Entry("invalid stage", func(request *BlueprintPlanRequest) { request.Stage = "install" }, "stage"),
		Entry("empty target map", func(request *BlueprintPlanRequest) { request.TargetMapFile = "" }, "target map"),
		Entry("malformed param", func(request *BlueprintPlanRequest) { request.Params = []string{"planes"} }, "key=value"),
		Entry("param containing a comma", func(request *BlueprintPlanRequest) { request.Params = []string{"modes=a,b"} }, "must not contain a comma"),
	)

	It("rejects a response without plan-json", func() {
		executor := fakeExecutor([]byte(`{"status":"ok","plan":"nco-node-1-spcx-prepare"}`), nil, &commands)

		result, err := GenerateBlueprintPlan(context.Background(), executor, newRequest())

		Expect(result).To(BeNil())
		Expect(err).To(MatchError(ContainSubstring("does not contain plan-json")))
	})

	It("replaces an inherited Blueprints root without duplicating it", func() {
		environment := environmentWithOverride([]string{
			"PATH=/usr/bin",
			"BLUEPRINTS_ROOT=/old/blueprints",
			"HOME=/tmp/test-home",
		}, "BLUEPRINTS_ROOT", blueprintsRoot)

		Expect(environment).To(Equal([]string{
			"PATH=/usr/bin",
			"HOME=/tmp/test-home",
			"BLUEPRINTS_ROOT=" + blueprintsRoot,
		}))
	})

	It("replaces an inherited DMS state directory without duplicating it", func() {
		environment := environmentWithOverride([]string{
			"PATH=/usr/bin",
			"BP_STATE_DIR=/old/blueprints-state",
			"HOME=/tmp/test-home",
		}, "BP_STATE_DIR", blueprintsStateDir)

		Expect(environment).To(Equal([]string{
			"PATH=/usr/bin",
			"HOME=/tmp/test-home",
			"BP_STATE_DIR=" + blueprintsStateDir,
		}))
	})
})
