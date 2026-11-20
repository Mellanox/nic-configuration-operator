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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	execUtils "k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

type contextRecordingExec struct {
	execUtils.Interface
	observedContext context.Context
}

var _ func() NVConfigUtils = NewNVConfigUtils

func (e *contextRecordingExec) CommandContext(ctx context.Context, cmd string, args ...string) execUtils.Cmd {
	e.observedContext = ctx
	return e.Interface.CommandContext(ctx, cmd, args...)
}

var _ = Describe("mlxconfig device selection", func() {
	const pci = "0000:66:00.0"
	const fwctl = "/dev/fwctl/fwctl4"
	const header = "Configurations: Default Current Next Boot\n"
	var port v1alpha1.NicDevicePortSpec

	BeforeEach(func() {
		port = v1alpha1.NicDevicePortSpec{PCI: pci, FwctlDevice: fwctl}
	})

	operations := []struct {
		name        string
		args        []string
		output      string
		usesContext bool
		run         func(context.Context, *nvConfigUtils, v1alpha1.NicDevicePortSpec) error
	}{
		{
			name: "query", args: []string{"-e", "query"}, output: header + "NUM_OF_PF  1  4  4\n", usesContext: true,
			run: func(ctx context.Context, h *nvConfigUtils, port v1alpha1.NicDevicePortSpec) error {
				_, err := h.QueryNvConfig(ctx, port, nil)
				return err
			},
		},
		{
			name: "set", args: []string{"--yes", "set", "NUM_OF_VFS=1"}, output: "ok",
			run: func(_ context.Context, h *nvConfigUtils, port v1alpha1.NicDevicePortSpec) error {
				return h.SetNvConfigParameter(port, "NUM_OF_VFS", "1")
			},
		},
		{
			name: "reset", args: []string{"--yes", "reset"}, output: "ok",
			run: func(_ context.Context, h *nvConfigUtils, port v1alpha1.NicDevicePortSpec) error {
				return h.ResetNvConfig(port)
			},
		},
		{
			name: "set_system_conf", args: []string{"-y", "--force", "set_system_conf", "conf3[1]"}, output: "ok", usesContext: true,
			run: func(ctx context.Context, h *nvConfigUtils, port v1alpha1.NicDevicePortSpec) error {
				return h.SetSystemConf(ctx, port, "conf3", 1, true)
			},
		},
		{
			name: "validate_system_conf", args: []string{"-y", "validate_system_conf", "conf3[1]"},
			output: "Result: Device configuration MATCHES the system configuration.\n", usesContext: true,
			run: func(ctx context.Context, h *nvConfigUtils, port v1alpha1.NicDevicePortSpec) error {
				_, _, err := h.ValidateSystemConf(ctx, port, "conf3", 1)
				return err
			},
		},
		{
			name: "show_system_conf", args: []string{"show_system_conf"},
			output: "Configuration name: conf3 - profile\nASIC[1] Description:\nNUM_OF_PF=4\n", usesContext: true,
			run: func(ctx context.Context, h *nvConfigUtils, port v1alpha1.NicDevicePortSpec) error {
				_, err := h.GetSystemConfParams(ctx, port, "conf3", 1)
				return err
			},
		},
	}

	for _, operation := range operations {
		Describe(operation.name, func() {
			DescribeTable("selects the configured target without changing port metadata", func(usePCIAddress bool, target string) {
				h := NewNVConfigUtilsWithOptions(Options{UsePCIAddress: usePCIAddress}).(*nvConfigUtils)
				executor := &execTesting.FakeExec{CommandScript: []execTesting.FakeCommandAction{
					func(cmd string, args ...string) execUtils.Cmd {
						Expect(cmd).To(Equal("mlxconfig"))
						Expect(args).To(Equal(append([]string{"-d", target}, operation.args...)))
						action := func() ([]byte, []byte, error) { return []byte(operation.output), nil, nil }
						return &execTesting.FakeCmd{OutputScript: []execTesting.FakeAction{action}, CombinedOutputScript: []execTesting.FakeAction{action}}
					},
				}}
				h.execInterface = executor
				original := port
				Expect(operation.run(context.Background(), h, port)).To(Succeed())
				Expect(executor.CommandCalls).To(Equal(1))
				Expect(port).To(Equal(original))
			}, Entry("default fwctl", false, fwctl), Entry("explicit PCI", true, pci))

			DescribeTable("rejects a missing PCI address before running a command in PCI mode", func(pciAddress string) {
				h := NewNVConfigUtilsWithOptions(Options{UsePCIAddress: true}).(*nvConfigUtils)
				executor := &execTesting.FakeExec{CommandScript: nil}
				h.execInterface = executor
				port.PCI = pciAddress
				Expect(operation.run(context.Background(), h, port)).To(MatchError(ContainSubstring("PCI address is required")))
				Expect(executor.CommandCalls).To(BeZero())
				Expect(port.FwctlDevice).To(Equal(fwctl))
			}, Entry("empty PCI address", ""), Entry("whitespace PCI address", " \t\n"))

			DescribeTable("propagates command failure without switching targets or retrying", func(usePCIAddress bool) {
				h := NewNVConfigUtilsWithOptions(Options{UsePCIAddress: usePCIAddress}).(*nvConfigUtils)
				commandError := errors.New("mlxconfig command failed")
				action := execTesting.FakeAction(func() ([]byte, []byte, error) { return nil, nil, commandError })
				executor := &execTesting.FakeExec{CommandScript: []execTesting.FakeCommandAction{
					func(_ string, _ ...string) execUtils.Cmd {
						return &execTesting.FakeCmd{OutputScript: []execTesting.FakeAction{action}, CombinedOutputScript: []execTesting.FakeAction{action}}
					},
				}}
				h.execInterface = executor
				err := operation.run(context.Background(), h, port)
				Expect(errors.Is(err, commandError)).To(BeTrue())
				Expect(executor.CommandCalls).To(Equal(1))
			}, Entry("default", false), Entry("PCI mode", true))

			if operation.usesContext {
				DescribeTable("forwards context and propagates cancellation", func(usePCIAddress bool) {
					ctx, cancel := context.WithCancel(context.Background())
					cancel()
					h := NewNVConfigUtilsWithOptions(Options{UsePCIAddress: usePCIAddress}).(*nvConfigUtils)
					action := execTesting.FakeAction(func() ([]byte, []byte, error) { return nil, nil, ctx.Err() })
					fakeExec := &execTesting.FakeExec{CommandScript: []execTesting.FakeCommandAction{
						func(_ string, _ ...string) execUtils.Cmd {
							return &execTesting.FakeCmd{OutputScript: []execTesting.FakeAction{action}, CombinedOutputScript: []execTesting.FakeAction{action}}
						},
					}}
					executor := &contextRecordingExec{Interface: fakeExec, observedContext: nil}
					h.execInterface = executor
					err := operation.run(ctx, h, port)
					Expect(errors.Is(err, context.Canceled)).To(BeTrue())
					Expect(executor.observedContext).To(Equal(ctx))
					Expect(fakeExec.CommandCalls).To(Equal(1))
				}, Entry("default", false), Entry("PCI mode", true))
			}
		})
	}

	DescribeTable("uses the same target throughout full and filtered queries with array expansion", func(usePCIAddress, filtered bool) {
		h := NewNVConfigUtilsWithOptions(Options{UsePCIAddress: usePCIAddress}).(*nvConfigUtils)
		target := fwctl
		if usePCIAddress {
			target = pci
		}
		executor := &execTesting.FakeExec{CommandScript: nil}
		enqueue := func(parameter, output string) {
			expectedArgs := []string{"-d", target, "-e", "query"}
			if parameter != "" {
				expectedArgs = append(expectedArgs, parameter)
			}
			executor.CommandScript = append(executor.CommandScript, func(cmd string, args ...string) execUtils.Cmd {
				Expect(cmd).To(Equal("mlxconfig"))
				Expect(args).To(Equal(expectedArgs))
				return &execTesting.FakeCmd{OutputScript: []execTesting.FakeAction{func() ([]byte, []byte, error) {
					return []byte(output), nil, nil
				}}}
			})
		}
		var parameters []string
		if filtered {
			parameters = []string{"MODULE_SPLIT_M0", "NUM_OF_PF"}
			enqueue("MODULE_SPLIT_M0", header+"MODULE_SPLIT_M0  Array[0..1]  Array[0..1]  Array[0..1]\n")
		} else {
			enqueue("", header+"NUM_OF_PF  1  4  4\nMODULE_SPLIT_M0  Array[0..1]  Array[0..1]  Array[0..1]\n")
		}
		enqueue("MODULE_SPLIT_M0[0..1]", header+"MODULE_SPLIT_M0[0]  1  2  2\nMODULE_SPLIT_M0[1]  1  3  3\n")
		if filtered {
			enqueue("NUM_OF_PF", header+"NUM_OF_PF  1  4  4\n")
		}
		h.execInterface = executor
		original := port
		query, err := h.QueryNvConfig(context.Background(), port, parameters)
		Expect(err).NotTo(HaveOccurred())
		Expect(query.AllParameters).To(Equal(!filtered))
		Expect(query.NextBootConfig).To(Equal(map[string][]string{
			"NUM_OF_PF": {"4"}, "MODULE_SPLIT_M0[0]": {"2"}, "MODULE_SPLIT_M0[1]": {"3"},
		}))
		Expect(executor.CommandCalls).To(Equal(len(executor.CommandScript)))
		Expect(port).To(Equal(original))
		if filtered {
			Expect(parameters).To(Equal([]string{"MODULE_SPLIT_M0", "NUM_OF_PF"}))
		}
	},
		Entry("default full query", false, false), Entry("PCI full query", true, false),
		Entry("default filtered query", false, true), Entry("PCI filtered query", true, true),
	)

	It("keeps constructor settings independent from other instances and the environment", func() {
		GinkgoT().Setenv("MLXCONFIG_USE_PCI_ADDRESS", "true")
		defaultUtils := NewNVConfigUtils().(*nvConfigUtils)
		options := Options{UsePCIAddress: true}
		pciUtils := NewNVConfigUtilsWithOptions(options).(*nvConfigUtils)
		options.UsePCIAddress = false
		GinkgoT().Setenv("MLXCONFIG_USE_PCI_ADDRESS", "false")
		defaultTarget, err := defaultUtils.resolveDevice(port)
		Expect(err).NotTo(HaveOccurred())
		Expect(defaultTarget).To(Equal(fwctl))
		pciTarget, err := pciUtils.resolveDevice(port)
		Expect(err).NotTo(HaveOccurred())
		Expect(pciTarget).To(Equal(pci))
		port.FwctlDevice = ""
		defaultTarget, err = defaultUtils.resolveDevice(port)
		Expect(err).NotTo(HaveOccurred())
		Expect(defaultTarget).To(Equal(pci))
	})

	It("retains optional profile, contextual batch, and Spectrum-X capabilities", func() {
		configured := NewNVConfigUtilsWithOptions(Options{UsePCIAddress: true})
		_, hasProfiles := configured.(SystemConfParamsProvider)
		Expect(hasProfiles).To(BeTrue())
		_, hasContextualBatch := configured.(interface {
			SetNvConfigParametersBatchWithContext(context.Context, v1alpha1.NicDevicePortSpec, map[string]string, bool, bool) (types.ApplyStatus, error)
		})
		Expect(hasContextualBatch).To(BeTrue())
		_, hasSpectrumX := configured.(interface {
			ValidateNvConfigXPaths(context.Context, []v1alpha1.NicDevicePortSpec, []dmscli.XPathOperation) (bool, bool, error)
			SetNvConfigParametersBatchWithXPaths(context.Context, v1alpha1.NicDevicePortSpec, int,
				map[string]string, []dmscli.XPathOperation, bool, bool) (types.ApplyStatus, error)
		})
		Expect(hasSpectrumX).To(BeTrue())
		_, hasNativeOverrides := configured.(interface {
			ValidateNvConfigXPathsWithOverrides(context.Context, []v1alpha1.NicDevicePortSpec, []dmscli.XPathOperation,
				map[string]string, map[string]types.NvConfigQuery) (bool, bool, error)
		})
		Expect(hasNativeOverrides).To(BeTrue())
	})

	It("preserves DMS PCI targets and payloads in either mode", func() {
		var commands [][]string
		operations := []dmscli.XPathOperation{{Path: "/nvidia/pci", Values: map[string]any{"num-pfs": 4}}}
		for _, usePCIAddress := range []bool{false, true} {
			h := NewNVConfigUtilsWithOptions(Options{UsePCIAddress: usePCIAddress}).(*nvConfigUtils)
			h.execInterface = &execTesting.FakeExec{CommandScript: []execTesting.FakeCommandAction{
				func(cmd string, args ...string) execUtils.Cmd {
					Expect(cmd).To(Equal("/opt/mellanox/doca/services/dms/dms-cli"))
					Expect(args[:3]).To(Equal([]string{"--json", "-t", "pci/" + pci}))
					commands = append(commands, append([]string{cmd}, args...))
					return &execTesting.FakeCmd{RunScript: []execTesting.FakeAction{func() ([]byte, []byte, error) {
						return []byte(`{"status":"ok","requires-reset":true}`), nil, nil
					}}}
				},
			}}
			status, err := h.SetNvConfigParametersBatchWithXPaths(context.Background(), port, 1,
				map[string]string{"NUM_OF_VFS": "1"}, operations, false, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(types.ApplyStatusSuccess))
		}
		Expect(commands).To(HaveLen(2))
		Expect(commands[0]).To(Equal(commands[1]))
		var payload dmscli.ApplyNVConfigRequest
		Expect(json.Unmarshal([]byte(commands[0][5]), &payload)).To(Succeed())
		Expect(payload.Raw).To(Equal([]dmscli.NVConfigParam{{Param: "NUM_OF_VFS", Value: "1"}}))
		Expect(payload.Typed).To(HaveLen(1))
		Expect(payload.Typed[0].Path).To(Equal("/nvidia/pci"))
		Expect(payload.Ports).To(Equal([]int{1}))
		Expect(port.FwctlDevice).To(Equal(fwctl))
	})
})
