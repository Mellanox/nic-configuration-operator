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
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

	newNicConfigurationTemplate := func(name string, linkType LinkTypeEnum, numVfs int, spectrumXSpec *SpectrumXOptimizedSpec) *NicConfigurationTemplate {
		return &NicConfigurationTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: metav1.NamespaceDefault,
			},
			Spec: NicConfigurationTemplateSpec{
				NicSelector: &NicSelectorSpec{NicType: "a2dc"},
				Template: &ConfigurationTemplateSpec{
					NumVfs:             numVfs,
					LinkType:           linkType,
					SpectrumXOptimized: spectrumXSpec,
				},
			},
		}
	}

	It("allows nil SpectrumXOptimized", func() {
		obj := newNicConfigurationTemplate("spcx-nil", "Ethernet", 8, nil)
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
	})

	It("allows disabled SpectrumXOptimized regardless of other fields", func() {
		obj := newNicConfigurationTemplate("spcx-disabled", "Infiniband", 8, &SpectrumXOptimizedSpec{Enabled: false, Version: "RA2.0"})
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
	})

	It("allows enabled SpectrumXOptimized only with Ethernet and numVfs==1", func() {
		obj := newNicConfigurationTemplate("spcx-valid", "Ethernet", 1, &SpectrumXOptimizedSpec{Enabled: true, Version: "RA2.0"})
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())
	})

	It("rejects enabled SpectrumXOptimized for Infiniband", func() {
		obj := newNicConfigurationTemplate("spcx-invalid-link-type-ib", "Infiniband", 1, &SpectrumXOptimizedSpec{Enabled: true, Version: "RA2.0"})
		err := k8sClient.Create(ctx, obj)
		Expect(err).To(HaveOccurred())
	})

	It("rejects enabled SpectrumXOptimized when numVfs!=1", func() {
		obj := newNicConfigurationTemplate("spcx-invalid-num-vfs", "Ethernet", 2, &SpectrumXOptimizedSpec{Enabled: true, Version: "RA2.0"})
		err := k8sClient.Create(ctx, obj)
		Expect(err).To(HaveOccurred())
	})

	It("allows disabled SpectrumXOptimized together with RoCE optimizations", func() {
		obj := newNicConfigurationTemplate("no-spcx-with-optimizations", "Ethernet", 1, &SpectrumXOptimizedSpec{Enabled: false, Version: "RA2.0"})
		obj.Spec.Template.PciPerformanceOptimized = &PciPerformanceOptimizedSpec{Enabled: true}
		obj.Spec.Template.RoceOptimized = &RoceOptimizedSpec{Enabled: true}
		obj.Spec.Template.GpuDirectOptimized = &GpuDirectOptimizedSpec{Enabled: true, Env: "Baremetal"}
		err := k8sClient.Create(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("allows SpectrumXOptimized enabled together with PciPerformanceOptimized", func() {
		obj := newNicConfigurationTemplate("spcx-with-pci-perf", "Ethernet", 1, &SpectrumXOptimizedSpec{Enabled: true, Version: "RA2.0"})
		obj.Spec.Template.PciPerformanceOptimized = &PciPerformanceOptimizedSpec{Enabled: true}
		err := k8sClient.Create(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects when SpectrumXOptimized enabled together with RoceOptimized", func() {
		obj := newNicConfigurationTemplate("spcx-with-roce", "Ethernet", 1, &SpectrumXOptimizedSpec{Enabled: true, Version: "RA2.0"})
		obj.Spec.Template.RoceOptimized = &RoceOptimizedSpec{Enabled: true}
		err := k8sClient.Create(ctx, obj)
		Expect(err).To(HaveOccurred())
	})

	It("allows SpectrumXOptimized enabled together with GpuDirectOptimized", func() {
		obj := newNicConfigurationTemplate("spcx-with-gd", "Ethernet", 1, &SpectrumXOptimizedSpec{Enabled: true, Version: "RA2.0"})
		obj.Spec.Template.GpuDirectOptimized = &GpuDirectOptimizedSpec{Enabled: true, Env: "Baremetal"}
		err := k8sClient.Create(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("allows nvRawConfig when SpectrumXOptimized is disabled", func() {
		obj := newNicConfigurationTemplate("no-spcx-with-rawnv", "Ethernet", 1, &SpectrumXOptimizedSpec{Enabled: false, Version: "RA2.0"})
		obj.Spec.Template.RawNvConfig = []NvConfigParam{{Name: "FOO", Value: "BAR"}}
		err := k8sClient.Create(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects when SpectrumXOptimized enabled together with non-empty RawNvConfig", func() {
		obj := newNicConfigurationTemplate("spcx-with-rawnv", "Ethernet", 1, &SpectrumXOptimizedSpec{Enabled: true, Version: "RA2.0"})
		obj.Spec.Template.RawNvConfig = []NvConfigParam{{Name: "FOO", Value: "BAR"}}
		err := k8sClient.Create(ctx, obj)
		Expect(err).To(HaveOccurred())
	})

	It("allows SpectrumXOptimized enabled for ConnectX-8 (NicType 1023)", func() {
		obj := newNicConfigurationTemplate("spcx-allow-1023", "Ethernet", 1, &SpectrumXOptimizedSpec{Enabled: true, Version: "RA2.0"})
		obj.Spec.NicSelector.NicType = "1023"
		err := k8sClient.Create(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("allows SpectrumXOptimized enabled for BlueField-3 ConnectX-7 (NicType a2dc)", func() {
		obj := newNicConfigurationTemplate("spcx-allow-a2dc", "Ethernet", 1, &SpectrumXOptimizedSpec{Enabled: true, Version: "RA2.0"})
		obj.Spec.NicSelector.NicType = "a2dc"
		err := k8sClient.Create(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})

	It("rejects SpectrumXOptimized enabled for unsupported NicType", func() {
		obj := newNicConfigurationTemplate("spcx-reject-unsupported-nictype", "Ethernet", 1, &SpectrumXOptimizedSpec{Enabled: true, Version: "RA2.0"})
		obj.Spec.NicSelector.NicType = "a2d0"
		err := k8sClient.Create(ctx, obj)
		Expect(err).To(HaveOccurred())
	})

	It("allow configuration template without spectrumXOptimized and arbitrary nicType", func() {
		obj := newNicConfigurationTemplate("spcx-disabled-arbitrary-nictype", "Ethernet", 1, &SpectrumXOptimizedSpec{Enabled: false, Version: "RA2.0"})
		obj.Spec.NicSelector.NicType = "a2d0"
		err := k8sClient.Create(ctx, obj)
		Expect(err).NotTo(HaveOccurred())
	})
})
