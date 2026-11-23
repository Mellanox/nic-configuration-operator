/*
2024 NVIDIA CORPORATION & AFFILIATES
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
	"github.com/Mellanox/nic-configuration-operator/pkg/configuration/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
)

const testVal = "testVal"
const anotherTestVal = "anotherTestVal"

var _ = Describe("ConfigValidationImpl", func() {
	var (
		validator              *configValidationImpl
		mockConfigurationUtils mocks.ConfigurationUtils
	)

	BeforeEach(func() {
		mockConfigurationUtils = mocks.ConfigurationUtils{}
		validator = &configValidationImpl{utils: &mockConfigurationUtils}
	})

	Describe("ConstructNvParamMapFromTemplate", func() {
		It("should return default values if optional config is disabled", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Ethernet,
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:03:00.0"},
						{PCI: "0000:03:00.1"},
					},
				},
			}

			defaultValues := map[string][]string{
				consts.RoceCcPrioMaskP1Param: {"testRoceCcP1"},
				consts.CnpDscpP1Param:        {"testDscpP1"},
				consts.Cnp802pPrioP1Param:    {"test802PrioP1"},
				consts.RoceCcPrioMaskP2Param: {"testRoceCcP2"},
				consts.CnpDscpP2Param:        {"testDscpP2"},
				consts.Cnp802pPrioP2Param:    {"test802PrioP2"},
				consts.AtsEnabledParam:       {"testAts"},
			}
			query := types.NewNvConfigQuery()
			query.DefaultConfig = defaultValues
			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovEnabledParam, consts.NvParamFalse))
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovNumOfVfsParam, "0"))
			Expect(nvParams).To(HaveKeyWithValue(consts.RoceCcPrioMaskP1Param, "testRoceCcP1"))
			Expect(nvParams).To(HaveKeyWithValue(consts.CnpDscpP1Param, "testDscpP1"))
			Expect(nvParams).To(HaveKeyWithValue(consts.Cnp802pPrioP1Param, "test802PrioP1"))
			Expect(nvParams).To(HaveKeyWithValue(consts.RoceCcPrioMaskP2Param, "testRoceCcP2"))
			Expect(nvParams).To(HaveKeyWithValue(consts.CnpDscpP2Param, "testDscpP2"))
			Expect(nvParams).To(HaveKeyWithValue(consts.Cnp802pPrioP2Param, "test802PrioP2"))
			Expect(nvParams).To(HaveKeyWithValue(consts.AtsEnabledParam, "testAts"))
		})

		It("should check if dual-port device has LINK_TYPE_P2 param", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Ethernet,
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:03:00.0"},
						{PCI: "0000:03:00.1"},
					},
				},
			}

			defaultValues := map[string][]string{
				consts.LinkTypeP1Param: {"testLinkTypeP1"},
			}
			query := types.NewNvConfigQuery()
			query.DefaultConfig = defaultValues
			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.LinkTypeP1Param, consts.NvParamLinkTypeEthernet))
			Expect(nvParams).ToNot(HaveKey(consts.LinkTypeP2Param))
		})

		It("should not emit LINK_TYPE params when linkType is unset (Network Bay)", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:     0,
							NetworkBay: &v1alpha1.NetworkBaySpec{Conf: "3"},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:0b:00.0"},
						{PCI: "0000:0b:00.1"},
					},
				},
			}

			// Firmware exposes LINK_TYPE slots, but the template does not set linkType, so the
			// operator must not emit LINK_TYPE_P* (the system profile owns the link type).
			query := types.NewNvConfigQuery()
			query.DefaultConfig = map[string][]string{
				consts.LinkTypeP1Param: {"2"},
				consts.LinkTypeP2Param: {"2"},
			}
			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).ToNot(HaveKey(consts.LinkTypeP1Param))
			Expect(nvParams).ToNot(HaveKey(consts.LinkTypeP2Param))
		})

		It("should omit parameters for the second port if device is single port", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Ethernet,
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:03:00.0"},
					},
				},
			}
			defaultValues := map[string][]string{
				consts.RoceCcPrioMaskP1Param: {"testRoceCcP1"},
				consts.CnpDscpP1Param:        {"testDscpP1"},
				consts.Cnp802pPrioP1Param:    {"test802PrioP1"},
				consts.RoceCcPrioMaskP2Param: {"testRoceCcP2"},
				consts.CnpDscpP2Param:        {"testDscpP2"},
				consts.Cnp802pPrioP2Param:    {"test802PrioP2"},
				consts.AtsEnabledParam:       {"testAts"},
			}
			query := types.NewNvConfigQuery()
			query.DefaultConfig = defaultValues

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovEnabledParam, consts.NvParamFalse))
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovNumOfVfsParam, "0"))
			Expect(nvParams).To(HaveKeyWithValue(consts.AtsEnabledParam, "testAts"))
			Expect(nvParams).To(HaveKeyWithValue(consts.RoceCcPrioMaskP1Param, "testRoceCcP1"))
			Expect(nvParams).To(HaveKeyWithValue(consts.CnpDscpP1Param, "testDscpP1"))
			Expect(nvParams).To(HaveKeyWithValue(consts.Cnp802pPrioP1Param, "test802PrioP1"))
			Expect(nvParams).To(Not(HaveKey(consts.RoceCcPrioMaskP2Param)))
			Expect(nvParams).To(Not(HaveKey(consts.CnpDscpP2Param)))
			Expect(nvParams).To(Not(HaveKey(consts.Cnp802pPrioP2Param)))
		})

		It("should construct the correct nvparam map with optional optimizations enabled", func() {
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Ethernet,
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled:        true,
								MaxReadRequest: 1339,
							},
							GpuDirectOptimized: &v1alpha1.GpuDirectOptimizedSpec{
								Enabled: true,
								Env:     consts.EnvBaremetal,
							},
							RoceOptimized: &v1alpha1.RoceOptimizedSpec{
								Enabled: true,
								Qos: &v1alpha1.QosSpec{
									Trust: "testTrust",
									PFC:   "testPFC",
								},
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:03:00.0"},
						{PCI: "0000:03:00.1"},
					},
				},
			}

			query := types.NewNvConfigQuery()

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.AtsEnabledParam, "0"))
			Expect(nvParams).To(HaveKeyWithValue(consts.RoceCcPrioMaskP1Param, "255"))
			Expect(nvParams).To(HaveKeyWithValue(consts.CnpDscpP1Param, "48"))
			Expect(nvParams).To(HaveKeyWithValue(consts.Cnp802pPrioP1Param, "6"))
			Expect(nvParams).To(HaveKeyWithValue(consts.RoceCcPrioMaskP2Param, "255"))
			Expect(nvParams).To(HaveKeyWithValue(consts.CnpDscpP2Param, "48"))
			Expect(nvParams).To(HaveKeyWithValue(consts.Cnp802pPrioP2Param, "6"))
		})

		It("should not change NVConfig when only PCI performance optimization is enabled", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs: 0,
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled:        true,
								MaxReadRequest: 1024,
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:03:00.0"}},
				},
			}

			query := types.NewNvConfigQuery()
			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(Equal(map[string]string{
				consts.SriovEnabledParam:  consts.NvParamFalse,
				consts.SriovNumOfVfsParam: "0",
			}))
		})

		It("should return an error when GpuOptimized is enabled without PciPerformanceOptimized", func() {
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Ethernet,
							GpuDirectOptimized: &v1alpha1.GpuDirectOptimizedSpec{
								Enabled: true,
								Env:     consts.EnvBaremetal,
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:03:00.0"},
					},
				},
			}

			query := types.NewNvConfigQuery()

			_, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).To(MatchError("incorrect spec: GpuDirectOptimized should only be enabled together with PciPerformanceOptimized"))
		})
		It("should ignore raw config for the second port if device is single port", func() {
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Ethernet,
							RawNvConfig: []v1alpha1.NvConfigParam{
								{
									Name:  "TEST_P1",
									Value: "test",
								},
								{
									Name:  "TEST_P2",
									Value: "test",
								},
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:03:00.0"},
					},
				},
			}

			query := types.NewNvConfigQuery()

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue("TEST_P1", "test"))
			Expect(nvParams).NotTo(HaveKey("TEST_P2"))
		})
		It("should apply raw config for the second port if device is dual port", func() {
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Ethernet,
							RawNvConfig: []v1alpha1.NvConfigParam{
								{
									Name:  "TEST_P1",
									Value: "test",
								},
								{
									Name:  "TEST_P2",
									Value: "test",
								},
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:03:00.0"},
						{PCI: "0000:03:00.1"},
					},
				},
			}

			query := types.NewNvConfigQuery()

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue("TEST_P1", "test"))
			Expect(nvParams).To(HaveKeyWithValue("TEST_P2", "test"))
		})
		It("should emit link type and RoCE params for every port on a quad-port device", func() {
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Ethernet,
							RoceOptimized: &v1alpha1.RoceOptimizedSpec{
								Enabled: true,
							},
							RawNvConfig: []v1alpha1.NvConfigParam{
								{Name: "CUSTOM_PARAM_P1", Value: "raw1"},
								{Name: "CUSTOM_PARAM_P4", Value: "raw4"},
								{Name: "CUSTOM_PARAM_P5", Value: "raw5"},
								{Name: "CUSTOM_PARAM_NO_SUFFIX", Value: "rawN"},
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:03:00.0"},
						{PCI: "0000:03:00.1"},
						{PCI: "0000:03:00.2"},
						{PCI: "0000:03:00.3"},
					},
				},
			}

			query := types.NewNvConfigQuery()
			// Firmware exposes LINK_TYPE for all four ports.
			query.DefaultConfig = map[string][]string{
				"LINK_TYPE_P1": {"2"},
				"LINK_TYPE_P2": {"2"},
				"LINK_TYPE_P3": {"2"},
				"LINK_TYPE_P4": {"2"},
			}

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())

			// Link type emitted for every port with a firmware-declared slot.
			for _, name := range []string{"LINK_TYPE_P1", "LINK_TYPE_P2", "LINK_TYPE_P3", "LINK_TYPE_P4"} {
				Expect(nvParams).To(HaveKeyWithValue(name, consts.NvParamLinkTypeEthernet))
			}

			// RoCE optimization emitted for all four ports.
			for _, name := range []string{"ROCE_CC_PRIO_MASK_P1", "ROCE_CC_PRIO_MASK_P2", "ROCE_CC_PRIO_MASK_P3", "ROCE_CC_PRIO_MASK_P4"} {
				Expect(nvParams).To(HaveKeyWithValue(name, "255"))
			}
			for _, name := range []string{"CNP_DSCP_P1", "CNP_DSCP_P2", "CNP_DSCP_P3", "CNP_DSCP_P4"} {
				Expect(nvParams).To(HaveKeyWithValue(name, "48"))
			}
			for _, name := range []string{"CNP_802P_PRIO_P1", "CNP_802P_PRIO_P2", "CNP_802P_PRIO_P3", "CNP_802P_PRIO_P4"} {
				Expect(nvParams).To(HaveKeyWithValue(name, "6"))
			}

			// rawNvConfig: _P1..P4 and non-suffixed entries kept; _P5 dropped.
			Expect(nvParams).To(HaveKeyWithValue("CUSTOM_PARAM_P1", "raw1"))
			Expect(nvParams).To(HaveKeyWithValue("CUSTOM_PARAM_P4", "raw4"))
			Expect(nvParams).To(HaveKeyWithValue("CUSTOM_PARAM_NO_SUFFIX", "rawN"))
			Expect(nvParams).NotTo(HaveKey("CUSTOM_PARAM_P5"))
		})
		It("should skip LINK_TYPE_Pn for ports firmware does not declare a slot for", func() {
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Ethernet,
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:03:00.0"},
						{PCI: "0000:03:00.1"},
						{PCI: "0000:03:00.2"},
						{PCI: "0000:03:00.3"},
					},
				},
			}

			query := types.NewNvConfigQuery()
			// Firmware exposes only P1 and P3, not P2 or P4.
			query.DefaultConfig = map[string][]string{
				"LINK_TYPE_P1": {"2"},
				"LINK_TYPE_P3": {"2"},
			}

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P1", consts.NvParamLinkTypeEthernet))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P3", consts.NvParamLinkTypeEthernet))
			Expect(nvParams).NotTo(HaveKey("LINK_TYPE_P2"))
			Expect(nvParams).NotTo(HaveKey("LINK_TYPE_P4"))
		})
		It("should report an error when LinkType cannot be changed and template differs from the actual status", func() {
			mockConfigurationUtils.On("GetLinkType", mock.Anything).Return(consts.Ethernet)
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Infiniband,
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled: true,
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{
							PCI:              "0000:03:00.0",
							NetworkInterface: "enp3s0f0np0",
						},
						{
							PCI:              "0000:03:00.1",
							NetworkInterface: "enp3s0f1np1",
						},
					},
				},
			}

			query := types.NewNvConfigQuery()

			_, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).To(MatchError("incorrect spec: device does not support link type change, wrong link type provided in the template, should be: Ethernet"))
		})
		It("should not report an error when LinkType can be changed and template differs from the actual status", func() {
			mockConfigurationUtils.On("GetLinkType", mock.Anything).Return(consts.Ethernet)
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Infiniband,
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled: true,
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{
							PCI:              "0000:03:00.0",
							NetworkInterface: "enp3s0f0np0",
						},
						{
							PCI:              "0000:03:00.1",
							NetworkInterface: "enp3s0f1np1",
						},
					},
				},
			}

			defaultValues := map[string][]string{
				consts.LinkTypeP1Param: {consts.Ethernet},
			}
			query := types.NewNvConfigQuery()
			query.DefaultConfig = defaultValues

			_, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
		})
		It("should not report an error when LinkType cannot be changed and template matches the actual status", func() {
			mockConfigurationUtils.On("GetLinkType", mock.Anything).Return(consts.Infiniband)
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Infiniband,
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled: true,
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{
							PCI:              "0000:03:00.0",
							NetworkInterface: "enp3s0f0np0",
						},
						{
							PCI:              "0000:03:00.1",
							NetworkInterface: "enp3s0f1np1",
						},
					},
				},
			}

			query := types.NewNvConfigQuery()

			_, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
		})
		It("should return an error when RoceOptimized is enabled with linkType Infiniband", func() {
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Infiniband,
							RoceOptimized: &v1alpha1.RoceOptimizedSpec{
								Enabled: true,
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:03:00.0"},
					},
				},
			}

			query := types.NewNvConfigQuery()

			_, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).To(MatchError("incorrect spec: RoceOptimized settings can only be used with link type Ethernet"))
		})

		It("should take numeric values when both numeric values and string aliases are present in nv config query", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: consts.Ethernet,
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled: true,
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:03:00.0"},
						{PCI: "0000:03:00.1"},
					},
				},
			}

			defaultValues := map[string][]string{
				consts.AtsEnabledParam: {"testAtsEnabled", "0"},
			}
			query := types.NewNvConfigQuery()
			query.DefaultConfig = defaultValues

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovEnabledParam, consts.NvParamFalse))
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovNumOfVfsParam, "0"))
			Expect(nvParams).To(HaveKeyWithValue(consts.AtsEnabledParam, "0"))
		})
	})

	Describe("ConstructNvParamMapFromTemplate — raw override merge", func() {
		// newDevice builds a single-port device with an empty template, so ConstructNvParamMapFromTemplate
		// contributes only its unconditional SRIOV defaults; raw overrides are added on top.
		newDevice := func() *v1alpha1.NicDevice {
			return &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{},
					},
				},
				Status: v1alpha1.NicDeviceStatus{Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:03:00.0"}}},
			}
		}

		BeforeEach(func() {
			validator = &configValidationImpl{utils: &mockConfigurationUtils}
		})

		It("returns an empty map when the template is nil", func() {
			device := newDevice()
			device.Spec.Configuration.Template = nil
			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, types.NewNvConfigQuery())
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(BeEmpty())
		})

		It("lets rawNvConfig win over a template param (raw > template)", func() {
			device := newDevice()
			device.Spec.Configuration.Template.NumVfs = 4 // template sets SRIOV_EN=true, NUM_OF_VFS=4
			device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: consts.SriovNumOfVfsParam, Value: "16"}}

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, types.NewNvConfigQuery())
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovNumOfVfsParam, "16"))
		})

		It("drops a rawNvConfig _Pn param whose port is beyond the device port count", func() {
			device := newDevice() // single port
			device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
				{Name: "SOME_PARAM_P1", Value: "1"},
				{Name: "SOME_PARAM_P2", Value: "1"},
			}
			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, types.NewNvConfigQuery())
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKey("SOME_PARAM_P1"))
			Expect(nvParams).ToNot(HaveKey("SOME_PARAM_P2"))
		})
	})

	Describe("ValidateResetToDefault", func() {
		It("should return false, false if device is already reset in current and next boot", func() {
			nvConfigQuery := types.NewNvConfigQuery()
			nvConfigQuery.DefaultConfig["RandomParam"] = []string{testVal}
			nvConfigQuery.CurrentConfig["RandomParam"] = []string{testVal}
			nvConfigQuery.NextBootConfig["RandomParam"] = []string{testVal}

			nvConfigChangeRequired, rebootRequired, err := validator.ValidateResetToDefault(nvConfigQuery)
			Expect(nvConfigChangeRequired).To(Equal(false))
			Expect(rebootRequired).To(Equal(false))
			Expect(err).NotTo(HaveOccurred())
		})

		It("should return false, true if reset will complete after reboot", func() {
			nvConfigQuery := types.NewNvConfigQuery()
			nvConfigQuery.DefaultConfig["RandomParam"] = []string{testVal}
			nvConfigQuery.CurrentConfig["RandomParam"] = []string{anotherTestVal}
			nvConfigQuery.NextBootConfig["RandomParam"] = []string{testVal}

			nvConfigChangeRequired, rebootRequired, err := validator.ValidateResetToDefault(nvConfigQuery)
			Expect(nvConfigChangeRequired).To(Equal(false))
			Expect(rebootRequired).To(Equal(true))
			Expect(err).NotTo(HaveOccurred())
		})

		It("should return true, true if reset is required", func() {
			nvConfigQuery := types.NewNvConfigQuery()
			nvConfigQuery.DefaultConfig["RandomParam"] = []string{testVal}
			nvConfigQuery.CurrentConfig["RandomParam"] = []string{anotherTestVal}
			nvConfigQuery.NextBootConfig["RandomParam"] = []string{anotherTestVal}

			nvConfigChangeRequired, rebootRequired, err := validator.ValidateResetToDefault(nvConfigQuery)
			Expect(nvConfigChangeRequired).To(Equal(true))
			Expect(rebootRequired).To(Equal(true))
			Expect(err).NotTo(HaveOccurred())
		})
	})
	Describe("CalculateDesiredRuntimeConfig", func() {
		It("should return correct defaults when no optimizations are enabled", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							PciPerformanceOptimized: nil,
							RoceOptimized:           nil,
						},
					},
				},
			}

			result := templateRuntimeConfig(device.Spec.Configuration.Template)
			Expect(result.MaxReadRequestSize).To(Equal(0))
			Expect(result.Qos).To(BeNil())
		})

		It("should calculate maxReadRequestSize when PciPerformanceOptimized is enabled with MaxReadRequest", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled:        true,
								MaxReadRequest: 1024,
							},
							RoceOptimized: nil,
						},
					},
				},
			}

			result := templateRuntimeConfig(device.Spec.Configuration.Template)
			Expect(result.MaxReadRequestSize).To(Equal(1024))
			Expect(result.Qos).To(BeNil())
		})

		It("should default maxReadReqSize to 4096 when PciPerformanceOptimized is enabled without MaxReadRequest", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled:        true,
								MaxReadRequest: 0,
							},
							RoceOptimized: nil,
						},
					},
				},
			}

			result := templateRuntimeConfig(device.Spec.Configuration.Template)
			Expect(result.MaxReadRequestSize).To(Equal(4096))
			Expect(result.Qos).To(BeNil())
		})

		It("should calculate QoS when RoceOptimized is enabled with Qos", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							PciPerformanceOptimized: nil,
							RoceOptimized: &v1alpha1.RoceOptimizedSpec{
								Enabled: true,
								Qos: &v1alpha1.QosSpec{
									Trust: "dscp",
									PFC:   "0,1,0,1,0,0,0,0",
									ToS:   100,
								},
							},
						},
					},
				},
			}

			result := templateRuntimeConfig(device.Spec.Configuration.Template)
			Expect(result.MaxReadRequestSize).To(Equal(0))
			Expect(result.Qos).ToNot(BeNil())
			Expect(result.Qos.Trust).To(Equal("dscp"))
			Expect(result.Qos.PFC).To(Equal("0,1,0,1,0,0,0,0"))
			Expect(result.Qos.ToS).To(Equal(100))
		})

		It("should default QoS settings when RoceOptimized is enabled without Qos", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							PciPerformanceOptimized: nil,
							RoceOptimized: &v1alpha1.RoceOptimizedSpec{
								Enabled: true,
								Qos:     nil,
							},
						},
					},
				},
			}

			result := templateRuntimeConfig(device.Spec.Configuration.Template)
			Expect(result.MaxReadRequestSize).To(Equal(0))
			Expect(result.Qos).ToNot(BeNil())
			Expect(result.Qos.Trust).To(Equal("dscp"))
			Expect(result.Qos.PFC).To(Equal("0,0,0,1,0,0,0,0"))
			Expect(result.Qos.ToS).To(Equal(0))
		})

		It("should prioritize RoceOptimized settings over defaults when both optimizations are enabled", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled:        true,
								MaxReadRequest: 256,
							},
							RoceOptimized: &v1alpha1.RoceOptimizedSpec{
								Enabled: true,
								Qos: &v1alpha1.QosSpec{
									Trust: "customTrust",
									PFC:   "1,1,1,1,1,1,1,1",
								},
							},
						},
					},
				},
			}

			result := templateRuntimeConfig(device.Spec.Configuration.Template)
			Expect(result.MaxReadRequestSize).To(Equal(256))
			Expect(result.Qos).ToNot(BeNil())
			Expect(result.Qos.Trust).To(Equal("customTrust"))
			Expect(result.Qos.PFC).To(Equal("1,1,1,1,1,1,1,1"))
			Expect(result.Qos.ToS).To(Equal(0))
		})

		It("should not calculate desired QoS settings for an IB configuration", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							LinkType: consts.Infiniband,
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled:        true,
								MaxReadRequest: 256,
							},
							RoceOptimized: &v1alpha1.RoceOptimizedSpec{
								Enabled: true,
								Qos: &v1alpha1.QosSpec{
									Trust: "customTrust",
									PFC:   "1,1,1,1,1,1,1,1",
								},
							},
						},
					},
				},
			}

			result := templateRuntimeConfig(device.Spec.Configuration.Template)
			Expect(result.MaxReadRequestSize).To(Equal(256))
			Expect(result.Qos).To(BeNil())
		})
		It("should not calculate desired QoS settings if RoCE optimizations are disabled", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							LinkType: consts.Infiniband,
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled:        true,
								MaxReadRequest: 256,
							},
							RoceOptimized: &v1alpha1.RoceOptimizedSpec{
								Enabled: false,
							},
						},
					},
				},
			}

			result := templateRuntimeConfig(device.Spec.Configuration.Template)
			Expect(result.MaxReadRequestSize).To(Equal(256))
			Expect(result.Qos).To(BeNil())
		})

		It("should include RoceMode when set", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							RoceOptimized: &v1alpha1.RoceOptimizedSpec{
								Enabled:  true,
								RoceMode: consts.RoceModeV2,
							},
						},
					},
				},
			}

			result := templateRuntimeConfig(device.Spec.Configuration.Template)
			Expect(result.RoceMode).To(Equal(consts.RoceModeV2))
		})

		It("should include ECN, CableLen, and PauseFrames from QoS", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							RoceOptimized: &v1alpha1.RoceOptimizedSpec{
								Enabled: true,
								Qos: &v1alpha1.QosSpec{
									Trust:       "dscp",
									PFC:         "0,0,0,1,0,0,0,0",
									CableLen:    5,
									ECN:         &v1alpha1.ECNSpec{Enabled: true, Priority: 3},
									PauseFrames: &v1alpha1.PauseFramesSpec{Enabled: false},
								},
							},
						},
					},
				},
			}

			result := templateRuntimeConfig(device.Spec.Configuration.Template)
			Expect(result.Qos).ToNot(BeNil())
			Expect(result.Qos.CableLen).To(Equal(5))
			Expect(result.Qos.ECN).ToNot(BeNil())
			Expect(result.Qos.ECN.Enabled).To(BeTrue())
			Expect(result.Qos.ECN.Priority).To(Equal(3))
			Expect(result.Qos.PauseFrames).ToNot(BeNil())
			Expect(result.Qos.PauseFrames.Enabled).To(BeFalse())
		})

		It("should not include RuntimePerf for IB devices", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							LinkType: consts.Infiniband,
							RuntimePerformanceOptimized: &v1alpha1.RuntimePerformanceOptimizedSpec{
								Enabled:    true,
								RxRingSize: 1024,
							},
						},
					},
				},
			}

			result := templateRuntimeConfig(device.Spec.Configuration.Template)
			Expect(result.RuntimePerf).To(BeNil())
		})

		It("should include RuntimePerf for Ethernet devices", func() {
			lroTrue := true
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							RuntimePerformanceOptimized: &v1alpha1.RuntimePerformanceOptimizedSpec{
								Enabled:          true,
								RxRingSize:       1024,
								TxRingSize:       512,
								CombinedChannels: 8,
								LRO:              &lroTrue,
							},
						},
					},
				},
			}

			result := templateRuntimeConfig(device.Spec.Configuration.Template)
			Expect(result.RuntimePerf).ToNot(BeNil())
			Expect(result.RuntimePerf.RxRingSize).To(Equal(1024))
			Expect(result.RuntimePerf.TxRingSize).To(Equal(512))
			Expect(result.RuntimePerf.CombinedChannels).To(Equal(8))
			Expect(*result.RuntimePerf.LRO).To(BeTrue())
		})
	})

})
