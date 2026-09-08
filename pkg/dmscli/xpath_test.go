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

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("typed XPath operations", func() {
	const target = "pci/0000:64:00.0"

	var commands []recordedCommand

	BeforeEach(func() {
		commands = nil
	})

	Describe("QueryXPaths", func() {
		It("batches query paths and decodes the public keyed response", func() {
			executor := fakeExecutor([]byte(`{
				"/nvidia/link/ipg":{"admin":25},
				"/nvidia/link/physical":{"admin-status":"up"}
			}`), nil, &commands)

			result, err := QueryXPaths(context.Background(), executor, target, []XPathQuery{
				{Path: "/nvidia/link/ipg", Leaves: []string{"admin"}},
				{Path: "/nvidia/link/physical", Leaves: []string{"admin-status"}},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status).To(Equal("ok"))
			Expect(result.Values).To(HaveKeyWithValue("/nvidia/link/ipg", map[string]any{"admin": json.Number("25")}))
			Expect(result.Values).To(HaveKeyWithValue("/nvidia/link/physical", map[string]any{"admin-status": "up"}))
			Expect(commands).To(HaveLen(1))
			Expect(commands[0].args).To(Equal([]string{
				"--json", "-t", target,
				"/nvidia/link/ipg", "admin",
				";", "/nvidia/link/physical", "admin-status",
			}))
		})

		It("decodes the public flat single-container response", func() {
			executor := fakeExecutor([]byte(`{"operating-mode":"dpu","status":"ready"}`), nil, &commands)

			result, err := QueryXPaths(context.Background(), executor, target, []XPathQuery{
				{Path: "/nvidia/mode", Leaves: []string{"operating-mode", "status"}},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.Values).To(Equal(map[string]map[string]any{
				"/nvidia/mode": {"operating-mode": "dpu", "status": "ready"},
			}))
		})

		It("decodes JSON stdout independently from diagnostics on stderr", func() {
			executor := fakeExecutorWithStderr(
				[]byte(`{"admin":25}`), []byte("DMS warning\n"), nil, &commands)

			result, err := QueryXPaths(context.Background(), executor, target, []XPathQuery{
				{Path: "/nvidia/link/ipg", Leaves: []string{"admin"}},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.Values["/nvidia/link/ipg"]).To(HaveKeyWithValue("admin", json.Number("25")))
		})

		It("preserves successes and failures from a partial response", func() {
			commandErr := errors.New("exit status 10")
			executor := fakeExecutor([]byte(`{
				"status":"partial",
				"successes":{"/nvidia/pci/sriov-enabled":true},
				"failures":{"/nvidia/lag/resource-allocation":"not-supported"},
				"error_code":"E10"
			}`), commandErr, &commands)

			result, err := QueryXPaths(context.Background(), executor, target, []XPathQuery{
				{Path: "/nvidia/pci", Leaves: []string{"sriov-enabled"}},
				{Path: "/nvidia/lag", Leaves: []string{"resource-allocation"}},
			})

			Expect(errors.Is(err, commandErr)).To(BeTrue())
			Expect(result.Status).To(Equal("partial"))
			Expect(result.Values["/nvidia/pci"]).To(HaveKeyWithValue("sriov-enabled", true))
			Expect(result.Failures).To(Equal(map[string]any{
				"/nvidia/lag/resource-allocation": "not-supported",
			}))
			Expect(result.ErrorCode).To(Equal("E10"))
		})

		It("returns the structured total failure and preserves the command error", func() {
			commandErr := errors.New("exit status 7")
			executor := fakeExecutor(
				[]byte(`{"status":"error","error_msg":"device unavailable","error_code":5}`),
				commandErr, &commands)

			result, err := QueryXPaths(context.Background(), executor, target, []XPathQuery{
				{Path: "/nvidia/pci", Leaves: []string{"sriov-enabled"}},
			})

			Expect(errors.Is(err, commandErr)).To(BeTrue())
			Expect(err).To(MatchError(ContainSubstring("device unavailable")))
			Expect(result.Status).To(Equal("error"))
			Expect(result.ErrorMessage).To(Equal("device unavailable"))
		})

		It("rejects malformed JSON returned by a successful command", func() {
			executor := fakeExecutor([]byte("not-json"), nil, &commands)

			result, err := QueryXPaths(context.Background(), executor, target, []XPathQuery{
				{Path: "/nvidia/pci", Leaves: []string{"sriov-enabled"}},
			})

			Expect(result).To(BeNil())
			Expect(err).To(MatchError(ContainSubstring("invalid dms-cli JSON response")))
		})

		DescribeTable("validates query input before execution",
			func(targetValue string, queries []XPathQuery, expected string) {
				executor := fakeExecutor([]byte(`{}`), nil, &commands)
				result, err := QueryXPaths(context.Background(), executor, targetValue, queries)
				Expect(result).To(BeNil())
				Expect(err).To(MatchError(ContainSubstring(expected)))
				Expect(commands).To(BeEmpty())
			},
			Entry("empty target", "", []XPathQuery{{Path: "/nvidia/pci", Leaves: []string{"num-pfs"}}}, "target"),
			Entry("no paths", target, nil, "at least one path"),
			Entry("invalid path", target, []XPathQuery{{Path: "nvidia/pci", Leaves: []string{"num-pfs"}}}, "absolute /nvidia"),
			Entry("no leaves", target, []XPathQuery{{Path: "/nvidia/pci", Leaves: nil}}, "at least one leaf"),
			Entry("invalid leaf", target, []XPathQuery{{Path: "/nvidia/pci", Leaves: []string{"num/pfs"}}}, "unsupported characters"),
			Entry("duplicate path", target, []XPathQuery{
				{Path: "/nvidia/pci", Leaves: []string{"num-pfs"}},
				{Path: "/nvidia/pci", Leaves: []string{"num-vfs"}},
			}, "duplicated"),
			Entry("duplicate leaf", target, []XPathQuery{{Path: "/nvidia/pci", Leaves: []string{"num-pfs", "num-pfs"}}}, "duplicated"),
		)

		It("rejects a nil command executor", func() {
			result, err := QueryXPaths(context.Background(), nil, target, []XPathQuery{
				{Path: "/nvidia/pci", Leaves: []string{"num-pfs"}},
			})
			Expect(result).To(BeNil())
			Expect(err).To(MatchError("command executor must not be nil"))
		})
	})

	Describe("SetXPaths", func() {
		It("preserves operation order and expands leaf-lists as repeated assignments", func() {
			executor := fakeExecutor([]byte(`{"status":"ok"}`), nil, &commands)

			result, err := SetXPaths(context.Background(), executor, target, []XPathOperation{
				{Path: "/nvidia/roce", Values: map[string]any{
					"tx-sched-locality-mode": "accumulative", "adaptive-routing": true,
				}},
				{Path: "/nvidia/link/physical", Values: map[string]any{"admin-status": "down"}},
				{Path: "/nvidia/link/physical", Values: map[string]any{"admin-status": "up"}},
				{Path: "/nvidia/link/breakout/module/[0]/port/[1]", Values: map[string]any{"lanes": []int{0, 1, 2, 3}}},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(result.Status).To(Equal("ok"))
			Expect(commands[0].args).To(Equal([]string{
				"--json", "-t", target,
				"/nvidia/roce", "adaptive-routing=true", "tx-sched-locality-mode=accumulative",
				";", "/nvidia/link/physical", "admin-status=down",
				";", "/nvidia/link/physical", "admin-status=up",
				";", "/nvidia/link/breakout/module/[0]/port/[1]", "lanes=0", "lanes=1", "lanes=2", "lanes=3",
			}))
		})

		It("returns a normalized partial failure", func() {
			commandErr := errors.New("exit status 10")
			executor := fakeExecutor([]byte(`{
				"status":"partial",
				"successes":{"/nvidia/roce/adaptive-routing":true},
				"failures":{"/nvidia/roce/cc-steering-ext":"not-supported"}
			}`), commandErr, &commands)

			result, err := SetXPaths(context.Background(), executor, target, []XPathOperation{
				{Path: "/nvidia/roce", Values: map[string]any{
					"adaptive-routing": true, "cc-steering-ext": "enabled",
				}},
			})

			Expect(errors.Is(err, commandErr)).To(BeTrue())
			Expect(result.Status).To(Equal("partial"))
			Expect(result.Successes).To(Equal(map[string]any{
				"/nvidia/roce/adaptive-routing": true,
			}))
			Expect(result.Failures).To(Equal(map[string]any{
				"/nvidia/roce/cc-steering-ext": "not-supported",
			}))
		})

		It("logs the exact command and separate output streams", func() {
			executor := fakeExecutorWithStderr([]byte(`{"status":"ok"}`), []byte("warning\n"), nil, &commands)
			entries := []capturedLogEntry{}
			ctx := logr.NewContext(context.Background(), logr.New(&capturingLogSink{entries: &entries}))

			_, err := SetXPaths(ctx, executor, target, []XPathOperation{
				{Path: "/nvidia/pci", Values: map[string]any{"num-pfs": 2}},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(HaveLen(1))
			Expect(entries[0].fields).To(HaveKeyWithValue("command", append([]string{dmsCLIExecutable}, commands[0].args...)))
			Expect(entries[0].fields).To(HaveKeyWithValue("target", target))
			Expect(entries[0].fields).To(HaveKeyWithValue("stdout", `{"status":"ok"}`))
			Expect(entries[0].fields).To(HaveKeyWithValue("stderr", "warning"))
		})

		DescribeTable("rejects invalid set input before execution",
			func(targetValue string, operations []XPathOperation, expected string) {
				executor := fakeExecutor([]byte(`{"status":"ok"}`), nil, &commands)
				result, err := SetXPaths(context.Background(), executor, targetValue, operations)
				Expect(result).To(BeNil())
				Expect(err).To(MatchError(ContainSubstring(expected)))
				Expect(commands).To(BeEmpty())
			},
			Entry("empty target", "", []XPathOperation{{Path: "/nvidia/pci", Values: map[string]any{"num-pfs": 2}}}, "target"),
			Entry("no operations", target, nil, "at least one operation"),
			Entry("invalid path", target, []XPathOperation{{Path: "nvidia/pci", Values: map[string]any{"num-pfs": 2}}}, "absolute /nvidia"),
			Entry("no values", target, []XPathOperation{{Path: "/nvidia/pci", Values: nil}}, "at least one value"),
			Entry("invalid leaf", target, []XPathOperation{{Path: "/nvidia/pci", Values: map[string]any{"num/pfs": 2}}}, "unsupported characters"),
			Entry("null value", target, []XPathOperation{{Path: "/nvidia/pci", Values: map[string]any{"num-pfs": nil}}}, "must not be null"),
			Entry("object value", target, []XPathOperation{{Path: "/nvidia/pci", Values: map[string]any{"num-pfs": map[string]any{"value": 2}}}}, "string, boolean, or number"),
			Entry("empty array", target, []XPathOperation{{Path: "/nvidia/pci", Values: map[string]any{"num-pfs": []int{}}}}, "at least one value"),
			Entry("nested array", target, []XPathOperation{{Path: "/nvidia/pci", Values: map[string]any{"num-pfs": [][]int{{2}}}}}, "string, boolean, or number"),
		)

		It("rejects a nil command executor", func() {
			result, err := SetXPaths(context.Background(), nil, target, []XPathOperation{
				{Path: "/nvidia/pci", Values: map[string]any{"num-pfs": 2}},
			})
			Expect(result).To(BeNil())
			Expect(err).To(MatchError("command executor must not be nil"))
		})
	})
})
