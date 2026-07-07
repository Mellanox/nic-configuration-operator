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
	"errors"
	"fmt"

	"github.com/Mellanox/nic-configuration-operator/pkg/configuration/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	spcxmocks "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/mocks"
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
		It("should not use queried defaults when optional config is disabled", func() {
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

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovEnabledParam, consts.NvParamFalse))
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovNumOfVfsParam, "0"))
			Expect(nvParams).To(HaveKeyWithValue(consts.LinkTypeP1Param, consts.NvParamLinkTypeEthernet))
			Expect(nvParams).To(HaveKeyWithValue(consts.LinkTypeP2Param, consts.NvParamLinkTypeEthernet))
			Expect(nvParams).NotTo(HaveKey(consts.RoceCcPrioMaskP1Param))
			Expect(nvParams).NotTo(HaveKey(consts.CnpDscpP1Param))
			Expect(nvParams).NotTo(HaveKey(consts.Cnp802pPrioP1Param))
			Expect(nvParams).NotTo(HaveKey(consts.AtsEnabledParam))
		})

		It("should emit LINK_TYPE params for all discovered ports when link type change is supported", func() {
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

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.LinkTypeP1Param, consts.NvParamLinkTypeEthernet))
			Expect(nvParams).To(HaveKeyWithValue(consts.LinkTypeP2Param, consts.NvParamLinkTypeEthernet))
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

			// Even when link type change is supported, the template does not set linkType, so the
			// operator must not emit LINK_TYPE_P* (set_system_conf owns the link type).
			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
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
			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovEnabledParam, consts.NvParamFalse))
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovNumOfVfsParam, "0"))
			Expect(nvParams).To(HaveKeyWithValue(consts.LinkTypeP1Param, consts.NvParamLinkTypeEthernet))
			Expect(nvParams).To(Not(HaveKey(consts.LinkTypeP2Param)))
			Expect(nvParams).To(Not(HaveKey(consts.AtsEnabledParam)))
			Expect(nvParams).To(Not(HaveKey(consts.RoceCcPrioMaskP1Param)))
			Expect(nvParams).To(Not(HaveKey(consts.CnpDscpP1Param)))
			Expect(nvParams).To(Not(HaveKey(consts.Cnp802pPrioP1Param)))
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

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).NotTo(HaveKey(consts.MaxAccOutReadParam))
			Expect(nvParams).To(HaveKeyWithValue(consts.AtsEnabledParam, "0"))
			Expect(nvParams).To(HaveKeyWithValue(consts.RoceCcPrioMaskP1Param, "255"))
			Expect(nvParams).To(HaveKeyWithValue(consts.CnpDscpP1Param, "4"))
			Expect(nvParams).To(HaveKeyWithValue(consts.Cnp802pPrioP1Param, "6"))
			Expect(nvParams).To(HaveKeyWithValue(consts.RoceCcPrioMaskP2Param, "255"))
			Expect(nvParams).To(HaveKeyWithValue(consts.CnpDscpP2Param, "4"))
			Expect(nvParams).To(HaveKeyWithValue(consts.Cnp802pPrioP2Param, "6"))
		})

		It("should ignore MaxAccOutRead when pciPerformanceOptimized is enabled", func() {
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

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).NotTo(HaveKey(consts.MaxAccOutReadParam))
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

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
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

			_, err := validator.ConstructNvParamMapFromTemplate(device, true)
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

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
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

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue("TEST_P1", "test"))
			Expect(nvParams).To(HaveKeyWithValue("TEST_P2", "test"))
		})
		It("should extrapolate port-suffixed params when NUM_OF_PF expands the effective port count", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs: 0,
							RawNvConfig: []v1alpha1.NvConfigParam{
								{Name: consts.NumOfPfParam, Value: "4"},
								{Name: "LINK_TYPE_P1", Value: consts.NvParamLinkTypeEthernet},
								{Name: "CUSTOM_PARAM_P1", Value: "base"},
								{Name: "CUSTOM_PARAM_P3", Value: "explicit"},
								{Name: "CUSTOM_PARAM_P5", Value: "drop"},
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:03:00.0"}},
				},
			}

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.NumOfPfParam, "4"))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P1", consts.NvParamLinkTypeEthernet))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P2", consts.NvParamLinkTypeEthernet))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P3", consts.NvParamLinkTypeEthernet))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P4", consts.NvParamLinkTypeEthernet))
			Expect(nvParams).To(HaveKeyWithValue("CUSTOM_PARAM_P1", "base"))
			Expect(nvParams).To(HaveKeyWithValue("CUSTOM_PARAM_P2", "base"))
			Expect(nvParams).To(HaveKeyWithValue("CUSTOM_PARAM_P3", "explicit"))
			Expect(nvParams).To(HaveKeyWithValue("CUSTOM_PARAM_P4", "base"))
			Expect(nvParams).NotTo(HaveKey("CUSTOM_PARAM_P5"))
		})
		It("should extrapolate from the lowest existing port when P1 is absent", func() {
			device := &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							RawNvConfig: []v1alpha1.NvConfigParam{
								{Name: consts.NumOfPfParam, Value: "5"},
								{Name: "LINK_TYPE_P2", Value: "2"},
								{Name: "LINK_TYPE_P3", Value: "1"},
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:03:00.0"}},
				},
			}

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, false)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P1", "2"))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P2", "2"))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P3", "1"))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P4", "2"))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P5", "2"))
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

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())

			// Link type emitted for every discovered port when LINK_TYPE_P1 is supported.
			for _, name := range []string{"LINK_TYPE_P1", "LINK_TYPE_P2", "LINK_TYPE_P3", "LINK_TYPE_P4"} {
				Expect(nvParams).To(HaveKeyWithValue(name, consts.NvParamLinkTypeEthernet))
			}

			// RoCE optimization emitted for all four ports.
			for _, name := range []string{"ROCE_CC_PRIO_MASK_P1", "ROCE_CC_PRIO_MASK_P2", "ROCE_CC_PRIO_MASK_P3", "ROCE_CC_PRIO_MASK_P4"} {
				Expect(nvParams).To(HaveKeyWithValue(name, "255"))
			}
			for _, name := range []string{"CNP_DSCP_P1", "CNP_DSCP_P2", "CNP_DSCP_P3", "CNP_DSCP_P4"} {
				Expect(nvParams).To(HaveKeyWithValue(name, "4"))
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
		It("should emit LINK_TYPE_Pn without requiring a full query for each port slot", func() {
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

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P1", consts.NvParamLinkTypeEthernet))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P2", consts.NvParamLinkTypeEthernet))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P3", consts.NvParamLinkTypeEthernet))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P4", consts.NvParamLinkTypeEthernet))
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

			_, err := validator.ConstructNvParamMapFromTemplate(device, false)
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

			_, err := validator.ConstructNvParamMapFromTemplate(device, true)
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

			_, err := validator.ConstructNvParamMapFromTemplate(device, false)
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

			_, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).To(MatchError("incorrect spec: RoceOptimized settings can only be used with link type Ethernet"))
		})

		It("should not emit deprecated MaxAccOutRead values", func() {
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

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovEnabledParam, consts.NvParamFalse))
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovNumOfVfsParam, "0"))
			Expect(nvParams).NotTo(HaveKey(consts.MaxAccOutReadParam))
		})
	})

	Describe("ConstructNvParamMapFromTemplate — override merge & priority", func() {
		var mockSpcXMgr *spcxmocks.SpectrumXManager

		// newDevice builds a single-port device with an empty template, so ConstructNvParamMapFromTemplate
		// contributes only its unconditional SRIOV defaults; the override layers under test are added on top.
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
			mockSpcXMgr = spcxmocks.NewSpectrumXManager(GinkgoT())
			validator = &configValidationImpl{utils: &mockConfigurationUtils, spectrumXConfigManager: mockSpcXMgr}
		})

		It("returns an empty map when the template is nil", func() {
			device := newDevice()
			device.Spec.Configuration.Template = nil
			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(BeEmpty())
		})

		It("merges Spectrum-X breakout and postBreakout params", func() {
			device := newDevice()
			device.Spec.Configuration.Template.SpectrumXOptimized = &v1alpha1.SpectrumXOptimizedSpec{Enabled: true}
			mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
			mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(map[string]string{"LINK_TYPE_P1": "2"}, nil)

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue("NUM_OF_PF", "2"))
			Expect(nvParams).To(HaveKeyWithValue("LINK_TYPE_P1", "2"))
		})

		It("lets rawNvConfig win over a Spectrum-X param (raw > Spectrum-X)", func() {
			device := newDevice()
			device.Spec.Configuration.Template.SpectrumXOptimized = &v1alpha1.SpectrumXOptimizedSpec{Enabled: true}
			device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "NUM_OF_PF", Value: "8"}}
			mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
			mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue("NUM_OF_PF", "8"))
		})

		It("lets rawNvConfig win over a template param (raw > template)", func() {
			device := newDevice()
			device.Spec.Configuration.Template.NumVfs = 4 // template sets SRIOV_EN=true, NUM_OF_VFS=4
			device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: consts.SriovNumOfVfsParam, Value: "16"}}

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovNumOfVfsParam, "16"))
		})

		It("lets a rawNvConfig concrete index override a lower-priority Spectrum-X index", func() {
			// rawNvConfig range syntax is rejected by CEL; both layers use concrete per-index keys, so
			// priority is a plain key-collision (raw > Spectrum-X).
			device := newDevice()
			device.Spec.Configuration.Template.SpectrumXOptimized = &v1alpha1.SpectrumXOptimizedSpec{Enabled: true}
			device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "MODULE_SPLIT_M0[2]", Value: "5"}}
			mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"MODULE_SPLIT_M0[2]": "1", "MODULE_SPLIT_M0[3]": "1"}, nil)
			mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue("MODULE_SPLIT_M0[2]", "5"))
			Expect(nvParams).To(HaveKeyWithValue("MODULE_SPLIT_M0[3]", "1"))
		})

		It("drops a rawNvConfig _Pn param whose port is beyond the device port count", func() {
			device := newDevice() // single port
			device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
				{Name: "SOME_PARAM_P1", Value: "1"},
				{Name: "SOME_PARAM_P2", Value: "1"},
			}
			nvParams, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKey("SOME_PARAM_P1"))
			Expect(nvParams).ToNot(HaveKey("SOME_PARAM_P2"))
		})

		It("propagates an error from GetBreakoutMlxConfig", func() {
			device := newDevice()
			device.Spec.Configuration.Template.SpectrumXOptimized = &v1alpha1.SpectrumXOptimizedSpec{Enabled: true}
			mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(nil, errors.New("config not found"))

			_, err := validator.ConstructNvParamMapFromTemplate(device, true)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("config not found"))
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
				mockConfigurationUtils.On("GetPauseFrames", mock.Anything).Return(false, false, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("should return false when pause frames do not match", func() {
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetPauseFrames", "interface0").Return(true, true, nil)

				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})

			It("should return false when one pause frame direction drifts", func() {
				device.Spec.Configuration.Template.RoceOptimized.Qos.PauseFrames.Enabled = true
				desired := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", mock.Anything).Return(desired.MaxReadRequestSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, mock.Anything).Return(desired.Qos, nil)
				mockConfigurationUtils.On("GetPauseFrames", "interface0").Return(true, false, nil)

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
