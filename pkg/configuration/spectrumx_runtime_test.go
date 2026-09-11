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

package configuration

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	execUtils "k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	configurationmocks "github.com/Mellanox/nic-configuration-operator/pkg/configuration/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/spectrumx"
	spectrumxmocks "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

func runtimeFakeCommand(output string, calls *[][]string) execTesting.FakeCommandAction {
	return func(executable string, args ...string) execUtils.Cmd {
		*calls = append(*calls, append([]string{executable}, args...))
		command := &execTesting.FakeCmd{}
		command.RunScript = append(command.RunScript, func() ([]byte, []byte, error) {
			return []byte(output), nil, nil
		})
		return command
	}
}

func spectrumXRuntimeTestDevice() *v1alpha1.NicDevice {
	return &v1alpha1.NicDevice{
		Spec: v1alpha1.NicDeviceSpec{Configuration: &v1alpha1.NicDeviceConfigurationSpec{
			Template: &v1alpha1.ConfigurationTemplateSpec{SpectrumXOptimized: &v1alpha1.SpectrumXOptimizedSpec{
				Enabled: true, MultiplaneMode: consts.MultiplaneModeHwplb,
			}},
		}},
		Status: v1alpha1.NicDeviceStatus{Ports: []v1alpha1.NicDevicePortSpec{
			{PCI: "0000:64:00.0", RdmaInterface: "roce_r0"},
			{PCI: "0000:64:00.1", RdmaInterface: "roce_r0"},
		}},
	}
}

var _ = Describe("doSPCX runtime configuration", func() {
	It("validates the final value of an ordered operation sequence", func() {
		operations := []dmscli.XPathOperation{
			{Path: "/nvidia/link/physical", Values: map[string]any{"admin-status": "down"}},
			{Path: "/nvidia/link/physical", Values: map[string]any{"admin-status": "up"}},
		}

		queries, desired := spectrumXRuntimeQueries(operations)

		Expect(queries).To(Equal([]dmscli.XPathQuery{{
			Path: "/nvidia/link/physical", Leaves: []string{"admin-status"},
		}}))
		Expect(desired["/nvidia/link/physical"]).To(HaveKeyWithValue("admin-status", "up"))
	})

	It("uses one HWPLB target for CC and every target for other groups", func() {
		device := spectrumXRuntimeTestDevice()

		ccPorts, err := spectrumXRuntimeGroupPorts(device, spectrumXRuntimeGroupCC)
		Expect(err).NotTo(HaveOccurred())
		Expect(ccPorts).To(Equal(device.Status.Ports[:1]))

		linkPorts, err := spectrumXRuntimeGroupPorts(device, "link-runtime")
		Expect(err).NotTo(HaveOccurred())
		Expect(linkPorts).To(Equal(device.Status.Ports))
	})

	It("starts CC before applying the CC operation group", func() {
		device := spectrumXRuntimeTestDevice()
		managerMock := spectrumxmocks.NewSpectrumXManager(GinkgoT())
		managerMock.On("RunDocaSpcXCC", device.Status.Ports[0]).Return(nil).Once()
		calls := [][]string{}
		manager := configurationManager{
			spectrumXConfigManager: managerMock,
			execInterface: &execTesting.FakeExec{CommandScript: []execTesting.FakeCommandAction{
				runtimeFakeCommand(`{"status":"ok"}`, &calls),
			}},
		}
		plan := &spectrumx.Plan{RuntimeConfig: []spectrumx.OperationGroup{{
			Name: spectrumXRuntimeGroupCC,
			Operations: []dmscli.XPathOperation{{
				Path: "/nvidia/cc/algo/slot/[0]", Values: map[string]any{"enabled": true},
			}},
		}}}

		Expect(manager.applySpectrumXRuntimeConfig(context.Background(), device, plan)).To(Succeed())
		Expect(calls).To(HaveLen(1))
		Expect(calls[0]).To(ContainElements(
			"pci/0000:64:00.0", "/nvidia/cc/algo/slot/[0]", "enabled=true"))
	})

	It("starts CC before validating the CC operation group", func() {
		device := spectrumXRuntimeTestDevice()
		managerMock := spectrumxmocks.NewSpectrumXManager(GinkgoT())
		managerMock.On("RunDocaSpcXCC", device.Status.Ports[0]).Return(nil).Once()
		calls := [][]string{}
		manager := configurationManager{
			spectrumXConfigManager: managerMock,
			execInterface: &execTesting.FakeExec{CommandScript: []execTesting.FakeCommandAction{
				runtimeFakeCommand(`{"enabled":true}`, &calls),
			}},
		}
		plan := &spectrumx.Plan{RuntimeConfig: []spectrumx.OperationGroup{{
			Name: spectrumXRuntimeGroupCC,
			Operations: []dmscli.XPathOperation{{
				Path: "/nvidia/cc/algo/slot/[0]", Values: map[string]any{"enabled": true},
			}},
		}}}

		applied, err := manager.validateSpectrumXRuntimeConfig(context.Background(), device, plan)

		Expect(err).NotTo(HaveOccurred())
		Expect(applied).To(BeTrue())
		Expect(calls).To(HaveLen(1))
		Expect(calls[0]).To(ContainElements(
			"pci/0000:64:00.0", "/nvidia/cc/algo/slot/[0]", "enabled"))
	})

	It("queries indexed paths separately and detects a mismatch on a later index", func() {
		const (
			target     = "pci/0000:64:00.0"
			firstIndex = "/nvidia/cc/algo/slot/[0]/param/[0]"
			lastIndex  = "/nvidia/cc/algo/slot/[0]/param/[1]"
		)
		queries := []dmscli.XPathQuery{
			{Path: "/nvidia/qos", Leaves: []string{"trust-mode"}},
			{Path: firstIndex, Leaves: []string{"value"}},
			{Path: "/nvidia/roce", Leaves: []string{"adaptive-routing"}},
			{Path: lastIndex, Leaves: []string{"value"}},
		}
		desired := map[string]map[string]any{
			"/nvidia/qos":  {"trust-mode": "dscp"},
			firstIndex:     {"value": 400},
			"/nvidia/roce": {"adaptive-routing": true},
			lastIndex:      {"value": 6553},
		}
		calls := [][]string{}
		manager := configurationManager{execInterface: &execTesting.FakeExec{
			CommandScript: []execTesting.FakeCommandAction{
				runtimeFakeCommand(`{"/nvidia/qos":{"trust-mode":"dscp"},"/nvidia/roce":{"adaptive-routing":true}}`, &calls),
				runtimeFakeCommand(`{"value":400}`, &calls),
				runtimeFakeCommand(`{"value":999}`, &calls),
			},
		}}

		matches, err := manager.validateSpectrumXRuntimeTarget(context.Background(), target, queries, desired)

		Expect(err).NotTo(HaveOccurred())
		Expect(matches).To(BeFalse())
		Expect(calls).To(HaveLen(3))
		Expect(calls[0]).To(ContainElements("/nvidia/qos", "/nvidia/roce", ";"))
		Expect(calls[0]).NotTo(ContainElement(firstIndex))
		Expect(calls[0]).NotTo(ContainElement(lastIndex))
		Expect(calls[1]).To(ContainElement(firstIndex))
		Expect(calls[1]).NotTo(ContainElement(";"))
		Expect(calls[2]).To(ContainElement(lastIndex))
		Expect(calls[2]).NotTo(ContainElement(";"))
	})

	It("validates, applies, and post-validates the prepared runtime plan", func() {
		device := spectrumXRuntimeTestDevice()
		plan := &spectrumx.Plan{RuntimeConfig: []spectrumx.OperationGroup{{
			Name: "link-runtime",
			Operations: []dmscli.XPathOperation{
				{Path: "/nvidia/link/physical", Values: map[string]any{"admin-status": "down"}},
				{Path: "/nvidia/link/physical", Values: map[string]any{"admin-status": "up"}},
			},
		}}}
		managerMock := spectrumxmocks.NewSpectrumXManager(GinkgoT())
		managerMock.On("GetPreparedPlan", device, spectrumx.PlanStageConfigure).Return(plan, nil).Once()
		validationMock := &configurationmocks.ConfigValidation{}
		validationMock.On("RuntimeConfigApplied", device).Return(true, nil).Once()

		calls := [][]string{}
		executor := &execTesting.FakeExec{CommandScript: []execTesting.FakeCommandAction{
			// Initial validation stops on the first mismatching target.
			runtimeFakeCommand(`{"admin-status":"down"}`, &calls),
			// Apply the full ordered group to both target functions.
			runtimeFakeCommand(`{"status":"ok"}`, &calls),
			runtimeFakeCommand(`{"status":"ok"}`, &calls),
			// Post-validation checks both target functions.
			runtimeFakeCommand(`{"admin-status":"up"}`, &calls),
			runtimeFakeCommand(`{"admin-status":"up"}`, &calls),
		}}
		manager := configurationManager{
			configValidation: validationMock, spectrumXConfigManager: managerMock, execInterface: executor,
		}

		result, err := manager.ApplyRuntimeConfiguration(context.Background(), device)

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
		Expect(calls).To(HaveLen(5))
		Expect(calls[1]).To(ContainElements("admin-status=down", "admin-status=up"))
	})

	It("reapplies doSPCX after changing generic runtime configuration", func() {
		device := spectrumXRuntimeTestDevice()
		plan := &spectrumx.Plan{RuntimeConfig: []spectrumx.OperationGroup{{
			Name: "link-runtime",
			Operations: []dmscli.XPathOperation{{
				Path: "/nvidia/link/physical", Values: map[string]any{"admin-status": "up"},
			}},
		}}}
		managerMock := spectrumxmocks.NewSpectrumXManager(GinkgoT())
		managerMock.On("GetPreparedPlan", device, spectrumx.PlanStageConfigure).Return(plan, nil).Once()
		validationMock := &configurationmocks.ConfigValidation{}
		validationMock.On("RuntimeConfigApplied", device).Return(false, nil).Once()
		validationMock.On("CalculateDesiredRuntimeConfig", device).Return(types.DesiredRuntimeConfig{}).Once()

		calls := [][]string{}
		executor := &execTesting.FakeExec{CommandScript: []execTesting.FakeCommandAction{
			// The plan initially matches both functions.
			runtimeFakeCommand(`{"admin-status":"up"}`, &calls),
			runtimeFakeCommand(`{"admin-status":"up"}`, &calls),
			// It is still applied after generic runtime configuration to preserve final precedence.
			runtimeFakeCommand(`{"status":"ok"}`, &calls),
			runtimeFakeCommand(`{"status":"ok"}`, &calls),
			runtimeFakeCommand(`{"admin-status":"up"}`, &calls),
			runtimeFakeCommand(`{"admin-status":"up"}`, &calls),
		}}
		manager := configurationManager{
			configurationUtils: &configurationmocks.ConfigurationUtils{},
			configValidation:   validationMock, spectrumXConfigManager: managerMock, execInterface: executor,
		}

		result, err := manager.ApplyRuntimeConfiguration(context.Background(), device)

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
		Expect(calls).To(HaveLen(6))
		Expect(calls[2]).To(ContainElement("admin-status=up"))
	})
})
