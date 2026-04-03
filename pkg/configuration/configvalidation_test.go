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
	"fmt"

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
								MaxAccOutRead:  1337,
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
			Expect(nvParams).To(HaveKeyWithValue(consts.MaxAccOutReadParam, "1337"))
			Expect(nvParams).To(HaveKeyWithValue(consts.AtsEnabledParam, "0"))
			Expect(nvParams).To(HaveKeyWithValue(consts.RoceCcPrioMaskP1Param, "255"))
			Expect(nvParams).To(HaveKeyWithValue(consts.CnpDscpP1Param, "4"))
			Expect(nvParams).To(HaveKeyWithValue(consts.Cnp802pPrioP1Param, "6"))
			Expect(nvParams).To(HaveKeyWithValue(consts.RoceCcPrioMaskP2Param, "255"))
			Expect(nvParams).To(HaveKeyWithValue(consts.CnpDscpP2Param, "4"))
			Expect(nvParams).To(HaveKeyWithValue(consts.Cnp802pPrioP2Param, "6"))
		})

		It("should skip the MaxAccOutRead if the default is not 0", func() {
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

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
					},
				},
			}

			defaultValues := map[string][]string{
				consts.MaxAccOutReadParam: {"notZero"},
			}
			query := types.NewNvConfigQuery()
			query.DefaultConfig = defaultValues

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).NotTo(HaveKeyWithValue(consts.MaxAccOutReadParam, consts.NvParamZero))
		})

		It("should apply MaxAccOutRead if the default is 0", func() {
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

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
					},
				},
			}

			defaultValues := map[string][]string{
				consts.MaxAccOutReadParam: {consts.NvParamZero},
			}
			currentValues := map[string][]string{
				consts.AdvancedPCISettingsParam: {consts.NvParamTrue},
			}
			query := types.NewNvConfigQuery()
			query.DefaultConfig = defaultValues
			query.CurrentConfig = currentValues

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.MaxAccOutReadParam, consts.NvParamZero))
		})

		It("should not apply MaxAccOutRead if the default is unavailable", func() {
			mockConfigurationUtils.On("GetPCILinkSpeed", mock.Anything).Return(16, nil)

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
					},
				},
			}

			// MAX_ACC_OUT_READ param is unavailable if ADVANCED_PCI_SETTINGS is disabled
			query := types.NewNvConfigQuery()

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).ToNot(HaveKeyWithValue(consts.MaxAccOutReadParam, consts.NvParamZero))
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
				consts.MaxAccOutReadParam: {"testMaxAccOutRead", "0"},
			}
			currentValues := map[string][]string{
				consts.AdvancedPCISettingsParam: {"testAdvancedPCISettings", "1"},
			}
			query := types.NewNvConfigQuery()
			query.DefaultConfig = defaultValues
			query.CurrentConfig = currentValues

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, query)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovEnabledParam, consts.NvParamFalse))
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovNumOfVfsParam, "0"))
			Expect(nvParams).To(HaveKeyWithValue(consts.MaxAccOutReadParam, "0"))
		})
	})

	Describe("ValidateResetToDefault", func() {
		It("should return false, false if device is already reset in current and next boot", func() {
			nvConfigQuery := types.NewNvConfigQuery()
			nvConfigQuery.CurrentConfig[consts.AdvancedPCISettingsParam] = []string{consts.NvParamTrue}
			nvConfigQuery.NextBootConfig[consts.AdvancedPCISettingsParam] = []string{consts.NvParamTrue}

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
			nvConfigQuery.CurrentConfig[consts.AdvancedPCISettingsParam] = []string{consts.NvParamTrue}
			nvConfigQuery.NextBootConfig[consts.AdvancedPCISettingsParam] = []string{consts.NvParamTrue}

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
			nvConfigQuery.CurrentConfig[consts.AdvancedPCISettingsParam] = []string{consts.NvParamTrue}
			nvConfigQuery.NextBootConfig[consts.AdvancedPCISettingsParam] = []string{consts.NvParamTrue}

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

			result := validator.CalculateDesiredRuntimeConfig(device)
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

			result := validator.CalculateDesiredRuntimeConfig(device)
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

			result := validator.CalculateDesiredRuntimeConfig(device)
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

			result := validator.CalculateDesiredRuntimeConfig(device)
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

			result := validator.CalculateDesiredRuntimeConfig(device)
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

			result := validator.CalculateDesiredRuntimeConfig(device)
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

			result := validator.CalculateDesiredRuntimeConfig(device)
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

			result := validator.CalculateDesiredRuntimeConfig(device)
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

			result := validator.CalculateDesiredRuntimeConfig(device)
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

			result := validator.CalculateDesiredRuntimeConfig(device)
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

			result := validator.CalculateDesiredRuntimeConfig(device)
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

			result := validator.CalculateDesiredRuntimeConfig(device)
			Expect(result.RuntimePerf).ToNot(BeNil())
			Expect(result.RuntimePerf.RxRingSize).To(Equal(1024))
			Expect(result.RuntimePerf.TxRingSize).To(Equal(512))
			Expect(result.RuntimePerf.CombinedChannels).To(Equal(8))
			Expect(*result.RuntimePerf.LRO).To(BeTrue())
		})
	})

	Describe("RuntimeConfigApplied", func() {
		var (
			device  *v1alpha1.NicDevice
			applied bool
			err     error
		)

		BeforeEach(func() {
			device = &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							RoceOptimized: &v1alpha1.RoceOptimizedSpec{Enabled: true},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: "0000:03:00.0", NetworkInterface: "interface0"},
						{PCI: "0000:03:00.1", NetworkInterface: "interface1"},
					},
				},
			}
		})

		Context("when desired runtime config is applied correctly on all ports", func() {
			BeforeEach(func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.1").Return(desired.MaxReadRequestSize, nil)

				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, "interface1").Return(desired.Qos, nil)
			})

			It("should return true with no error", func() {
				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})
		})

		Context("when desiredMaxReadRequestSize does not match on the first port", func() {
			BeforeEach(func() {
				device := device
				device.Spec = v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled:        true,
								MaxReadRequest: 2048,
							},
						},
					},
				}
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desired.MaxReadRequestSize+128, nil)

				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, "interface1").Return(desired.Qos, nil)

				// The second port should not be called since the first port already fails
			})

			It("should return false with no error", func() {
				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("when desiredMaxReadRequestSize does not match on the second port", func() {
			BeforeEach(func() {
				device := device
				device.Spec = v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled:        true,
								MaxReadRequest: 2048,
							},
						},
					},
				}

				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.1").Return(desired.MaxReadRequestSize+256, nil)

				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, "interface1").Return(desired.Qos, nil)
			})

			It("should return false with no error", func() {
				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("when trust setting does not match on the first port", func() {
			BeforeEach(func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.1").Return(desired.MaxReadRequestSize, nil)

				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(&v1alpha1.QosSpec{Trust: "differentTrust", PFC: desired.Qos.PFC}, nil)
				// The second port should not be called since the first port already fails
			})

			It("should return false with no error", func() {
				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("when PFC setting does not match on the second port", func() {
			BeforeEach(func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.1").Return(desired.MaxReadRequestSize, nil)

				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(&v1alpha1.QosSpec{Trust: desired.Qos.Trust, PFC: "differentPfc"}, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, "interface1").Return(&v1alpha1.QosSpec{Trust: desired.Qos.Trust, PFC: "differentPfc"}, nil)
			})

			It("should return false with no error", func() {
				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("when GetMaxReadRequestSize returns an error", func() {
			BeforeEach(func() {
				device := device
				device.Spec = v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled:        true,
								MaxReadRequest: 2048,
							},
						},
					},
				}

				_ = validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(0, fmt.Errorf("command failed"))
			})

			It("should return false with the error", func() {
				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("command failed"))
				Expect(applied).To(BeFalse())
			})
		})

		Context("when GetQoSSettings returns an error on the first port", func() {
			BeforeEach(func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.1").Return(desired.MaxReadRequestSize, nil)

				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(&v1alpha1.QosSpec{}, fmt.Errorf("failed to get trust and pfc"))
			})

			It("should return false with the error", func() {
				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to get trust and pfc"))
				Expect(applied).To(BeFalse())
			})
		})

		Context("when device has a single port and all settings are applied correctly", func() {
			BeforeEach(func() {
				device := device
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:03:00.0", NetworkInterface: "interface0"},
				}

				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(desired.Qos, nil)
			})

			It("should return true with no error", func() {
				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})
		})

		Context("when device has a single port and trust setting does not match", func() {
			BeforeEach(func() {
				device := device
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:03:00.0", NetworkInterface: "interface0"},
				}

				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(&v1alpha1.QosSpec{Trust: "differentTrust", PFC: desired.Qos.PFC}, nil)
			})

			It("should return false with no error", func() {
				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("when device's port doesn't have a network interface", func() {
			BeforeEach(func() {
				device := device
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:03:00.0", NetworkInterface: ""},
				}

				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desired.MaxReadRequestSize, nil)
			})

			It("should return an error", func() {
				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).To(MatchError("cannot validate QoS settings for device port 0000:03:00.0, network interface is missing"))
				Expect(applied).To(BeFalse())
			})
		})

		Context("when validating RoCE mode", func() {
			BeforeEach(func() {
				device.Spec.Configuration.Template.RoceOptimized.RoceMode = consts.RoceModeV2
			})

			It("should return true when RoCE mode matches", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetRoceMode", mock.Anything).Return(consts.RoceModeV2, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("should return false when RoCE mode does not match", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetRoceMode", "interface0").Return(consts.RoceModeV1, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("when validating ECN settings", func() {
			BeforeEach(func() {
				device.Spec.Configuration.Template.RoceOptimized.Qos = &v1alpha1.QosSpec{
					Trust: "dscp",
					PFC:   "0,0,0,1,0,0,0,0",
					ECN:   &v1alpha1.ECNSpec{Enabled: true, Priority: 3},
				}
			})

			It("should return true when ECN is enabled and matches", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetECNEnabled", mock.Anything, 3).Return(true, true, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("should return false when ECN is enabled but not applied", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetECNEnabled", "interface0", 3).Return(false, false, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("when validating ECN disabled", func() {
			BeforeEach(func() {
				device.Spec.Configuration.Template.RoceOptimized.Qos = &v1alpha1.QosSpec{
					Trust: "dscp",
					PFC:   "0,0,0,1,0,0,0,0",
					ECN:   &v1alpha1.ECNSpec{Enabled: false, Priority: 3},
				}
			})

			It("should return true when ECN is disabled and matches", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetECNEnabled", mock.Anything, 3).Return(false, false, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("should return false when ECN should be disabled but is still enabled", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetECNEnabled", "interface0", 3).Return(true, true, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("when validating pause frames", func() {
			BeforeEach(func() {
				device.Spec.Configuration.Template.RoceOptimized.Qos = &v1alpha1.QosSpec{
					Trust:       "dscp",
					PFC:         "0,0,0,1,0,0,0,0",
					PauseFrames: &v1alpha1.PauseFramesSpec{Enabled: false},
				}
			})

			It("should return true when pause frames match", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetPauseFrames", mock.Anything).Return(false, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("should return false when pause frames do not match", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetPauseFrames", "interface0").Return(true, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("when validating runtime performance settings", func() {
			var lroTrue = true

			BeforeEach(func() {
				device.Spec.Configuration.Template.RuntimePerformanceOptimized = &v1alpha1.RuntimePerformanceOptimizedSpec{
					Enabled:          true,
					RxRingSize:       1024,
					TxRingSize:       512,
					CombinedChannels: 8,
					LRO:              &lroTrue,
				}
			})

			It("should return true when all runtime perf settings match", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetRingSize", mock.Anything).Return(1024, 512, nil)
				mockConfigurationUtils.On("GetCombinedChannels", mock.Anything).Return(8, nil)
				mockConfigurationUtils.On("GetLRO", mock.Anything).Return(true, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("should return false when ring size does not match", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetRingSize", "interface0").Return(256, 512, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})

			It("should return false when combined channels does not match", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetRingSize", mock.Anything).Return(1024, 512, nil)
				mockConfigurationUtils.On("GetCombinedChannels", "interface0").Return(4, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})

			It("should return false when LRO does not match", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetRingSize", mock.Anything).Return(1024, 512, nil)
				mockConfigurationUtils.On("GetCombinedChannels", mock.Anything).Return(8, nil)
				mockConfigurationUtils.On("GetLRO", "interface0").Return(false, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})

			It("should return error when GetRingSize fails", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetRingSize", "interface0").Return(0, 0, fmt.Errorf("ethtool failed"))

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).To(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("when validating cable length", func() {
			BeforeEach(func() {
				device.Spec.Configuration.Template.RoceOptimized.Qos = &v1alpha1.QosSpec{
					Trust:    "dscp",
					PFC:      "0,0,0,1,0,0,0,0",
					CableLen: 10,
				}
			})

			It("should return true when cable length matches", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetCableLen", mock.Anything).Return(10, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("should return false when cable length does not match", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetCableLen", "interface0").Return(5, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})
	})
})
