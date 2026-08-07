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
	execUtils "k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"
)

type recordedCommand struct {
	executable string
	args       []string
}

type capturedLogEntry struct {
	message string
	fields  map[string]any
}

type capturingLogSink struct {
	entries *[]capturedLogEntry
	values  []any
	name    string
}

func (s *capturingLogSink) Init(logr.RuntimeInfo) {}

func (s *capturingLogSink) Enabled(int) bool {
	return true
}

func (s *capturingLogSink) Info(_ int, message string, keysAndValues ...any) {
	allValues := append(append([]any(nil), s.values...), keysAndValues...)
	fields := make(map[string]any, len(allValues)/2)
	for index := 0; index+1 < len(allValues); index += 2 {
		key, ok := allValues[index].(string)
		if ok {
			fields[key] = allValues[index+1]
		}
	}
	*s.entries = append(*s.entries, capturedLogEntry{message: s.name + message, fields: fields})
}

func (s *capturingLogSink) Error(err error, message string, keysAndValues ...any) {
	s.Info(0, message, append(keysAndValues, "error", err)...)
}

func (s *capturingLogSink) WithValues(keysAndValues ...any) logr.LogSink {
	clone := *s
	clone.values = append(append([]any(nil), s.values...), keysAndValues...)
	return &clone
}

func (s *capturingLogSink) WithName(name string) logr.LogSink {
	clone := *s
	clone.name = s.name + name + "/"
	return &clone
}

func fakeExecutor(output []byte, commandErr error, commands *[]recordedCommand) *execTesting.FakeExec {
	command := &execTesting.FakeCmd{}
	command.CombinedOutputScript = append(command.CombinedOutputScript, func() ([]byte, []byte, error) {
		return output, nil, commandErr
	})

	executor := &execTesting.FakeExec{}
	executor.CommandScript = []execTesting.FakeCommandAction{
		func(executable string, args ...string) execUtils.Cmd {
			*commands = append(*commands, recordedCommand{
				executable: executable,
				args:       append([]string(nil), args...),
			})
			return command
		},
	}
	return executor
}

var _ = Describe("NVConfig apply", func() {
	const target = "pci/0000:3b:00.0"

	var (
		commands []recordedCommand
		executor *execTesting.FakeExec
	)

	BeforeEach(func() {
		commands = nil
		executor = fakeExecutor([]byte(`{
			"status":"ok",
			"primary-target":"pci/0000:3b:00.0",
			"compiled-count":2,
			"requires-reset":true,
			"with-default":true,
			"force":false,
			"params":["A=1","B=2"]
		}`), nil, &commands)
	})

	It("sends raw parameters and flags through the agentless action", func() {
		result, err := ApplyNVConfig(context.Background(), executor, ApplyNVConfigRequest{
			Target:      target,
			Ports:       nil,
			Typed:       nil,
			Raw:         []NVConfigParam{{Param: "A", Value: "1"}, {Param: "B", Value: "2"}},
			WithDefault: true,
			Force:       false,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(&ApplyNVConfigResult{
			Status:        "ok",
			PrimaryTarget: target,
			CompiledCount: 2,
			RequiresReset: true,
			WithDefault:   true,
			Force:         false,
			Params:        []string{"A=1", "B=2"},
			ErrorMessage:  "",
		}))
		Expect(commands).To(HaveLen(1))
		Expect(commands[0].executable).To(Equal(dmsCLIExecutable))
		Expect(commands[0].args).To(HaveLen(6))
		Expect(commands[0].args[0:4]).To(Equal([]string{"--json", "-t", target, "--input"}))
		Expect(commands[0].args[5]).To(Equal(nvConfigApplyPath))

		var payload map[string]any
		Expect(json.Unmarshal([]byte(commands[0].args[4]), &payload)).To(Succeed())
		Expect(payload).To(Equal(map[string]any{
			"raw": []any{
				map[string]any{"param": "A", "value": "1"},
				map[string]any{"param": "B", "value": "2"},
			},
			"with-default": true,
			"force":        false,
		}))
	})

	It("logs the exact command argv, target, and combined output", func() {
		entries := []capturedLogEntry{}
		ctx := logr.NewContext(context.Background(), logr.New(&capturingLogSink{entries: &entries}))

		_, err := ApplyNVConfig(ctx, executor, ApplyNVConfigRequest{
			Target:      target,
			Ports:       nil,
			Typed:       nil,
			Raw:         []NVConfigParam{{Param: "A", Value: "1"}},
			WithDefault: false,
			Force:       false,
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(entries).To(HaveLen(1))
		Expect(entries[0].message).To(Equal("command output"))
		Expect(entries[0].fields).To(HaveKeyWithValue("command", append([]string{dmsCLIExecutable}, commands[0].args...)))
		Expect(entries[0].fields).To(HaveKeyWithValue("target", target))
		Expect(entries[0].fields["output"]).To(ContainSubstring(`"compiled-count":2`))
	})

	It("serializes typed XPath operations, port fanout, arrays, and raw overrides", func() {
		_, err := ApplyNVConfig(context.Background(), executor, ApplyNVConfigRequest{
			Target: target,
			Ports:  []int{1, 2},
			Typed: []NVConfigXPathOperation{
				{
					Path: "/nvidia/cc/config",
					Values: map[string]any{
						"user-programmable": true,
						"profile":           "spcx",
					},
				},
				{
					Path: "/nvidia/link/breakout/module/[0]/port/[1]",
					Values: map[string]any{
						"lanes": []int{0, 1, 2, 3},
					},
				},
			},
			Raw: []NVConfigParam{
				{Param: "USER_PROGRAMMABLE_CC", Value: true},
				{Param: "NUM_OF_PF", Value: 2},
			},
			WithDefault: false,
			Force:       true,
		})

		Expect(err).NotTo(HaveOccurred())
		var payload struct {
			Ports       []int                    `json:"ports"`
			Typed       []NVConfigXPathOperation `json:"typed"`
			Raw         []NVConfigParam          `json:"raw"`
			WithDefault bool                     `json:"with-default"`
			Force       bool                     `json:"force"`
		}
		Expect(json.Unmarshal([]byte(commands[0].args[4]), &payload)).To(Succeed())
		Expect(payload.Ports).To(Equal([]int{1, 2}))
		Expect(payload.Typed).To(HaveLen(2))
		Expect(payload.Typed[0].Path).To(Equal("/nvidia/cc/config"))
		Expect(payload.Typed[0].Values).To(HaveKeyWithValue("user-programmable", true))
		Expect(payload.Typed[1].Values).To(HaveKey("lanes"))
		Expect(payload.Raw).To(Equal([]NVConfigParam{
			{Param: "USER_PROGRAMMABLE_CC", Value: true},
			{Param: "NUM_OF_PF", Value: float64(2)},
		}))
		Expect(payload.WithDefault).To(BeFalse())
		Expect(payload.Force).To(BeTrue())
	})

	It("returns the structured DMS error and preserves the command error", func() {
		commandErr := errors.New("exit status 7")
		executor = fakeExecutor([]byte(`{"status":"error","error_msg":"mlxconfig rejected the batch"}`), commandErr, &commands)

		result, err := ApplyNVConfig(context.Background(), executor, ApplyNVConfigRequest{
			Target:      target,
			Ports:       nil,
			Typed:       nil,
			Raw:         []NVConfigParam{{Param: "A", Value: "1"}},
			WithDefault: false,
			Force:       false,
		})

		Expect(result).To(Equal(&ApplyNVConfigResult{Status: "error", ErrorMessage: "mlxconfig rejected the batch"}))
		Expect(err).To(MatchError(ContainSubstring("mlxconfig rejected the batch")))
		Expect(errors.Is(err, commandErr)).To(BeTrue())
	})

	It("rejects a successful command with an error status", func() {
		executor = fakeExecutor([]byte(`{"status":"error","error_msg":"invalid payload"}`), nil, &commands)

		result, err := ApplyNVConfig(context.Background(), executor, ApplyNVConfigRequest{
			Target:      target,
			Ports:       nil,
			Typed:       nil,
			Raw:         []NVConfigParam{{Param: "A", Value: "1"}},
			WithDefault: false,
			Force:       false,
		})

		Expect(result).To(Equal(&ApplyNVConfigResult{Status: "error", ErrorMessage: "invalid payload"}))
		Expect(err).To(MatchError(ContainSubstring("invalid payload")))
	})

	It("rejects invalid JSON output", func() {
		executor = fakeExecutor([]byte("not-json"), nil, &commands)

		_, err := ApplyNVConfig(context.Background(), executor, ApplyNVConfigRequest{
			Target:      target,
			Ports:       nil,
			Typed:       nil,
			Raw:         []NVConfigParam{{Param: "A", Value: "1"}},
			WithDefault: false,
			Force:       false,
		})

		Expect(err).To(MatchError(ContainSubstring("invalid dms-cli JSON response")))
	})

	It("validates the target and operation set before execution", func() {
		_, err := ApplyNVConfig(context.Background(), executor, ApplyNVConfigRequest{
			Target:      "",
			Ports:       nil,
			Typed:       nil,
			Raw:         []NVConfigParam{{Param: "A", Value: "1"}},
			WithDefault: false,
			Force:       false,
		})
		Expect(err).To(MatchError("NVConfig target must not be empty"))

		_, err = ApplyNVConfig(context.Background(), executor, ApplyNVConfigRequest{
			Target:      target,
			Ports:       nil,
			Typed:       nil,
			Raw:         nil,
			WithDefault: false,
			Force:       false,
		})
		Expect(err).To(MatchError("NVConfig request must contain at least one typed or raw operation"))
		Expect(commands).To(BeEmpty())
	})

	It("validates typed, raw, and port payload fields before execution", func() {
		invalidRequests := []struct {
			request ApplyNVConfigRequest
			message string
		}{
			{
				request: ApplyNVConfigRequest{Target: target, Ports: []int{0}, Typed: nil, Raw: []NVConfigParam{{Param: "A", Value: "1"}}},
				message: "NVConfig port at index 0 must be between 1 and 255",
			},
			{
				request: ApplyNVConfigRequest{Target: target, Ports: nil, Typed: []NVConfigXPathOperation{{Path: "", Values: map[string]any{}}}, Raw: nil},
				message: "typed NVConfig operation at index 0 must have a path",
			},
			{
				request: ApplyNVConfigRequest{Target: target, Ports: nil, Typed: []NVConfigXPathOperation{{Path: "/nvidia/pci", Values: nil}}, Raw: nil},
				message: "typed NVConfig operation at index 0 must have a values object",
			},
			{
				request: ApplyNVConfigRequest{Target: target, Ports: nil, Typed: []NVConfigXPathOperation{{Path: "/nvidia/pci", Values: map[string]any{}}}, Raw: nil},
				message: "typed NVConfig operation at index 0 must have at least one value",
			},
			{
				request: ApplyNVConfigRequest{Target: target, Ports: nil, Typed: nil, Raw: []NVConfigParam{{Param: "", Value: "1"}}},
				message: "raw NVConfig parameter at index 0 must have a name",
			},
			{
				request: ApplyNVConfigRequest{Target: target, Ports: nil, Typed: nil, Raw: []NVConfigParam{{Param: "A", Value: []int{1}}}},
				message: "raw NVConfig parameter \"A\" must have a string, boolean, or numeric value",
			},
		}

		for _, test := range invalidRequests {
			_, err := ApplyNVConfig(context.Background(), executor, test.request)
			Expect(err).To(MatchError(test.message))
		}
		Expect(commands).To(BeEmpty())
	})

	It("rejects a nil command executor without panicking", func() {
		request := ApplyNVConfigRequest{
			Target:      target,
			Ports:       nil,
			Typed:       nil,
			Raw:         []NVConfigParam{{Param: "A", Value: "1"}},
			WithDefault: false,
			Force:       false,
		}

		_, err := ApplyNVConfig(context.Background(), nil, request)
		Expect(err).To(MatchError("command executor must not be nil"))
	})
})
