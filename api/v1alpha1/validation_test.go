/*
2025 NVIDIA CORPORATION & AFFILIATES
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

package v1alpha1

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	nicTypeConnectX7   = "1021"
	nicTypeConnectX8   = "1023"
	nicTypeConnectX9   = "1025"
	nicTypeBlueField3  = "a2dc"
	unsupportedNicType = "a2d0"
)

var _ = Describe("NicConfigurationTemplate CEL validation", func() {
	var (
		k8sClient client.Client
		ctx       context.Context
		cancel    context.CancelFunc
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		scheme := runtime.NewScheme()
		Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
		Expect(AddToScheme(scheme)).To(Succeed())

		var err error
		k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
		Expect(err).NotTo(HaveOccurred())
		Expect(k8sClient).NotTo(BeNil())
	})

	AfterEach(func() {
		cancel()
	})

	newNicConfigurationTemplate := func(name string, numVfs int) *NicConfigurationTemplate {
		return &NicConfigurationTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: metav1.NamespaceDefault,
			},
			Spec: NicConfigurationTemplateSpec{
				NicSelector: &NicSelectorSpec{NicType: nicTypeBlueField3},
				Template: &ConfigurationTemplateSpec{
					NumVfs:   numVfs,
					LinkType: "Ethernet",
				},
			},
		}
	}

	It("accepts plan-only templates and devices and ordinary overrides", func() {
		obj := newNicConfigurationTemplate("plan-only", 0)
		obj.Spec.Template = nil
		obj.Spec.PlanRef = &DMSPlanReference{Namespace: "default", Name: "host-plan"}
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		nic := &NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "plan-device", Namespace: "default"}, Spec: NicDeviceSpec{Configuration: &NicDeviceConfigurationSpec{PlanRef: obj.Spec.PlanRef}}}
		Expect(k8sClient.Create(ctx, nic)).To(Succeed())
		obj = newNicConfigurationTemplate("plan-overrides", 1)
		obj.Spec.PlanRef = &DMSPlanReference{Namespace: "default", Name: "host-plan"}
		obj.Spec.Template.RawNvConfig = []NvConfigParam{{Name: "SRIOV_EN", Value: "1"}}
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
	})

	It("rejects reset combined with a plan and incomplete references", func() {
		obj := newNicConfigurationTemplate("plan-reset", 0)
		obj.Spec.PlanRef = &DMSPlanReference{Namespace: "default", Name: "host-plan"}
		obj.Spec.ResetToDefault = true
		Expect(k8sClient.Create(ctx, obj)).NotTo(Succeed())
		obj = newNicConfigurationTemplate("plan-empty-ref", 0)
		obj.Spec.PlanRef = &DMSPlanReference{Name: "host-plan"}
		Expect(k8sClient.Create(ctx, obj)).NotTo(Succeed())
	})

	It("accepts ordinary RoCE and raw intent without a plan", func() {
		obj := newNicConfigurationTemplate("ordinary", 8)
		obj.Spec.Template.RoceOptimized = &RoceOptimizedSpec{Enabled: true, RoceMode: 2}
		obj.Spec.Template.RawNvConfig = []NvConfigParam{{Name: "SRIOV_EN", Value: "1"}}
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
	})

	It("rejects the removed Spectrum-X field with strict field validation", func() {
		for _, kind := range []string{"NicConfigurationTemplate", "NicDevice"} {
			old := map[string]any{"numVfs": int64(1), "linkType": "Ethernet", "spectrumXOptimized": map[string]any{"enabled": true}}
			spec := map[string]any{"nicSelector": map[string]any{"nicType": "1023"}, "template": old}
			if kind == "NicDevice" {
				spec = map[string]any{"configuration": map[string]any{"template": old}}
			}
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "configuration.net.nvidia.com/v1alpha1", "kind": kind, "metadata": map[string]any{"name": "obsolete-field", "namespace": "default"}, "spec": spec}}
			err := k8sClient.Create(ctx, obj, client.FieldValidation(metav1.FieldValidationStrict))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spectrumXOptimized"))
		}
	})

	It("allows a rawNvConfig param with an explicit index key", func() {
		obj := newNicConfigurationTemplate("rawnv-explicit-index", 1)
		obj.Spec.Template.RawNvConfig = []NvConfigParam{
			{Name: "MODULE_SPLIT_M0[0]", Value: "1"},
			{Name: "MODULE_SPLIT_M0[2]", Value: "5"},
		}
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
	})

	It("rejects a rawNvConfig param name using index-range syntax", func() {
		// A range like MODULE_SPLIT_M0[0..3] plus an explicit MODULE_SPLIT_M0[2] would both normalize
		// to the same concrete key; disallow ranges in rawNvConfig so overrides are unambiguous.
		obj := newNicConfigurationTemplate("rawnv-range", 1)
		obj.Spec.Template.RawNvConfig = []NvConfigParam{
			{Name: "MODULE_SPLIT_M0[0..3]", Value: "1"},
			{Name: "MODULE_SPLIT_M0[2]", Value: "5"},
		}
		err := k8sClient.Create(ctx, obj)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("index-range syntax"))
	})

	Context("NetworkBay validation", func() {
		It("allows networkBay for ConnectX-9 (NicType 1025) with linkType unset", func() {
			obj := newNicConfigurationTemplate("bay-allow-1025", 0)
			obj.Spec.Template.LinkType = ""
			obj.Spec.NicSelector.NicType = nicTypeConnectX9
			obj.Spec.Template.NetworkBay = &NetworkBaySpec{Conf: "3"}
			Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		})

		It("rejects networkBay for non-ConnectX-9 NicType", func() {
			obj := newNicConfigurationTemplate("bay-reject-cx8", 0)
			obj.Spec.Template.LinkType = ""
			obj.Spec.NicSelector.NicType = nicTypeConnectX8
			obj.Spec.Template.NetworkBay = &NetworkBaySpec{Conf: "3"}
			err := k8sClient.Create(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("networkBay can only be configured for ConnectX-9 (NicType 1025)"))
		})

		It("rejects networkBay when linkType is also set", func() {
			obj := newNicConfigurationTemplate("bay-reject-linktype", 0)
			obj.Spec.NicSelector.NicType = nicTypeConnectX9
			obj.Spec.Template.NetworkBay = &NetworkBaySpec{Conf: "3"}
			err := k8sClient.Create(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("linkType must not be set when networkBay is configured"))
		})

		It("rejects a template without networkBay and without linkType", func() {
			obj := newNicConfigurationTemplate("no-linktype-no-bay", 8)
			obj.Spec.Template.LinkType = ""
			obj.Spec.NicSelector.NicType = nicTypeConnectX9
			err := k8sClient.Create(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("linkType is required unless networkBay is configured"))
		})

	})
})
