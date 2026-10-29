// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	dmsconfiguration "github.com/Mellanox/dms-lib/pkg/configuration"
	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/ptr"
)

var _ = Describe("Plan intent translation", func() {
	var observed device.Device
	BeforeEach(func() { observed = device.Device{ID: "asic", Ports: []device.Port{{PCI: "0000:03:00.0"}}} })
	It("distinguishes no template from an explicit zero VF template", func() {
		values, err := TranslateNVOverrides(nil, observed, nil, nil, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(values).To(BeEmpty())
		values, err = TranslateNVOverrides(&v1alpha1.ConfigurationTemplateSpec{NumVfs: 0}, observed, nil, nil, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(values).To(ContainElement(dmsconfiguration.NVConfigParam{Name: consts.SriovNumOfVfsParam, Value: "0"}))
	})
	It("preserves ordinary omitted-default and future-port policy", func() {
		template := &v1alpha1.ConfigurationTemplateSpec{RawNvConfig: []v1alpha1.NvConfigParam{{Name: "CUSTOM_P8", Value: "4"}}}
		values, err := TranslateNVOverrides(template, observed, map[string]string{consts.AtsEnabledParam: "1"}, nil, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(values).To(ContainElement(dmsconfiguration.NVConfigParam{Name: consts.AtsEnabledParam, Value: "1"}))
		Expect(values).NotTo(ContainElement(dmsconfiguration.NVConfigParam{Name: "CUSTOM_P8", Value: "4"}))
	})
	It("retains future indexed raw overrides and gives them precedence", func() {
		template := &v1alpha1.ConfigurationTemplateSpec{NumVfs: 1, RawNvConfig: []v1alpha1.NvConfigParam{{Name: consts.SriovNumOfVfsParam, Value: "4"}, {Name: "CUSTOM_P8[3]", Value: "0"}}}
		values, err := TranslateNVOverrides(template, observed, nil, nil, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(values).To(ContainElements(dmsconfiguration.NVConfigParam{Name: consts.SriovNumOfVfsParam, Value: "4"}, dmsconfiguration.NVConfigParam{Name: "CUSTOM_P8[3]", Value: "0"}))
	})
	It("does not reset omitted plan optimization but honors explicitly disabled intent", func() {
		defaults := map[string]string{consts.AtsEnabledParam: "1"}
		template := &v1alpha1.ConfigurationTemplateSpec{}
		values, err := TranslateNVOverrides(template, observed, defaults, nil, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(values).NotTo(ContainElement(dmsconfiguration.NVConfigParam{Name: consts.AtsEnabledParam, Value: "1"}))
		template.GpuDirectOptimized = &v1alpha1.GpuDirectOptimizedSpec{Enabled: false}
		values, err = TranslateNVOverrides(template, observed, defaults, nil, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(values).To(ContainElement(dmsconfiguration.NVConfigParam{Name: consts.AtsEnabledParam, Value: "1"}))
	})
	It("rejects contradictory raw values and missing fixed-link evidence", func() {
		template := &v1alpha1.ConfigurationTemplateSpec{RawNvConfig: []v1alpha1.NvConfigParam{{Name: "A", Value: "1"}, {Name: "A", Value: "0"}}}
		_, err := TranslateNVOverrides(template, observed, nil, nil, false)
		Expect(err).To(MatchError(ContainSubstring("conflicting raw override")))
		template = &v1alpha1.ConfigurationTemplateSpec{LinkType: consts.Ethernet}
		_, err = TranslateNVOverrides(template, observed, nil, nil, false)
		Expect(err).To(MatchError(ContainSubstring("no netdev")))
	})
	It("preserves ordinary NV intent on fixed-link PFs without netdevs", func() {
		template := &v1alpha1.ConfigurationTemplateSpec{LinkType: consts.Ethernet, NumVfs: 4, RawNvConfig: []v1alpha1.NvConfigParam{{Name: "CUSTOM", Value: "7"}}}
		values, err := TranslateNVOverrides(template, observed, nil, nil, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(values).To(ContainElements(
			dmsconfiguration.NVConfigParam{Name: consts.SriovNumOfVfsParam, Value: "4"},
			dmsconfiguration.NVConfigParam{Name: "CUSTOM", Value: "7"},
		))
		_, err = TranslateNVOverrides(template, observed, nil, nil, false)
		Expect(err).To(MatchError(ContainSubstring("no netdev")))
	})
	It("still validates every named PF beside an ordinary unbound PF", func() {
		observed.Ports = append(observed.Ports, device.Port{PCI: "0000:03:00.1", Netdev: "observed-only"})
		template := &v1alpha1.ConfigurationTemplateSpec{LinkType: consts.Ethernet}
		_, err := TranslateNVOverrides(template, observed, nil, map[string]string{"observed-only": consts.Ethernet}, true)
		Expect(err).NotTo(HaveOccurred())
		_, err = TranslateNVOverrides(template, observed, nil, nil, true)
		Expect(err).To(MatchError(ContainSubstring("cannot determine link type")))
		_, err = TranslateNVOverrides(template, observed, nil, map[string]string{"observed-only": consts.Infiniband}, true)
		Expect(err).To(MatchError(ContainSubstring("wrong link type")))
	})
	It("validates fixed link type from explicit observation without host reads", func() {
		observed.Ports[0].Netdev = "not-a-host-interface"
		template := &v1alpha1.ConfigurationTemplateSpec{LinkType: consts.Ethernet}
		_, err := TranslateNVOverrides(template, observed, nil, map[string]string{"not-a-host-interface": consts.Ethernet}, false)
		Expect(err).NotTo(HaveOccurred())
		_, err = TranslateNVOverrides(template, observed, nil, map[string]string{"not-a-host-interface": consts.Infiniband}, false)
		Expect(err).To(HaveOccurred())
	})
	It("retains requested false values and both legacy ToS effects without inventing endpoint names", func() {
		template := &v1alpha1.ConfigurationTemplateSpec{
			PciPerformanceOptimized:     &v1alpha1.PciPerformanceOptimizedSpec{Enabled: true},
			RoceOptimized:               &v1alpha1.RoceOptimizedSpec{Enabled: true, RoceMode: 2, Qos: &v1alpha1.QosSpec{Trust: "dscp", PFC: "0,0,0,1,0,0,0,0", ToS: 106, ECN: &v1alpha1.ECNSpec{Enabled: false, Priority: 3}, PauseFrames: &v1alpha1.PauseFramesSpec{Enabled: false}}},
			RuntimePerformanceOptimized: &v1alpha1.RuntimePerformanceOptimizedSpec{Enabled: true, RxRingSize: 1024, LRO: ptr.To(false)},
		}
		values, err := TranslateRuntime(template, observed)
		Expect(err).NotTo(HaveOccurred())
		Expect(values).To(HaveLen(1))
		value := values[0]
		Expect(value.PCI).To(Equal("0000:03:00.0"))
		Expect(value.MaxReadRequestBytes).To(HaveValue(Equal(uint32(4096))))
		Expect(value.ToS).To(HaveValue(Equal(uint8(106))))
		Expect(value.TrafficClass).To(HaveValue(Equal(int32(106))))
		Expect(value.ECN[0].RP).To(HaveValue(BeFalse()))
		Expect(value.ECN[0].NP).To(HaveValue(BeFalse()))
		Expect(value.Pause.RX).To(HaveValue(BeFalse()))
		Expect(value.LRO).To(HaveValue(BeFalse()))
	})
	It("preserves IB runtime policy and absent template semantics", func() {
		values, err := TranslateRuntime(nil, observed)
		Expect(err).NotTo(HaveOccurred())
		Expect(values).To(BeEmpty())
		values, err = TranslateRuntime(&v1alpha1.ConfigurationTemplateSpec{LinkType: consts.Infiniband, PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{Enabled: true, MaxReadRequest: 512}, RoceOptimized: &v1alpha1.RoceOptimizedSpec{Enabled: true, RoceMode: 2}}, observed)
		Expect(err).NotTo(HaveOccurred())
		Expect(values[0].MaxReadRequestBytes).To(HaveValue(Equal(uint32(512))))
		Expect(values[0].RoCEMode).To(BeNil())
	})
	It("rejects native numeric overflow rather than truncating it", func() {
		_, err := TranslateRuntime(&v1alpha1.ConfigurationTemplateSpec{RuntimePerformanceOptimized: &v1alpha1.RuntimePerformanceOptimizedSpec{Enabled: true, RxRingSize: 1 << 32}}, observed)
		Expect(err).To(MatchError(ContainSubstring("outside uint32 range")))
	})
})
