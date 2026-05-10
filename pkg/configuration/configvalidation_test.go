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
		baseDevice := func(linkType v1alpha1.LinkTypeEnum, ports int) *v1alpha1.NicDevice {
			portSpecs := make([]v1alpha1.NicDevicePortSpec, ports)
			for i := 0; i < ports; i++ {
				portSpecs[i] = v1alpha1.NicDevicePortSpec{PCI: fmt.Sprintf("0000:03:00.%d", i)}
			}
			return &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NumVfs:   0,
							LinkType: linkType,
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{Ports: portSpecs},
			}
		}

		It("emits SRIOV, LINK_TYPE per port, and sentinels for disabled RoCE/Ats/MaxAcc", func() {
			nvParams, err := validator.ConstructNvParamMapFromTemplate(baseDevice(consts.Ethernet, 2))
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovEnabledParam, consts.NvParamFalse))
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovNumOfVfsParam, "0"))
			Expect(nvParams).To(HaveKeyWithValue(consts.LinkTypeP1Param, consts.NvParamLinkTypeEthernet))
			Expect(nvParams).To(HaveKeyWithValue(consts.LinkTypeP2Param, consts.NvParamLinkTypeEthernet))
			// Disabled optional features emit the factory-default sentinel so the
			// resolver fills in the FW default at apply time — see ResolveFactoryDefaults.
			for _, name := range []string{consts.RoceCcPrioMaskP1Param, consts.RoceCcPrioMaskP2Param,
				consts.CnpDscpP1Param, consts.CnpDscpP2Param,
				consts.Cnp802pPrioP1Param, consts.Cnp802pPrioP2Param,
				consts.AtsEnabledParam, consts.MaxAccOutReadParam} {
				Expect(nvParams).To(HaveKeyWithValue(name, consts.NvParamFactoryDefault))
			}
			// ADVANCED_PCI_SETTINGS is only emitted when MAX_ACC_OUT_READ has a concrete value.
			Expect(nvParams).NotTo(HaveKey(consts.AdvancedPCISettingsParam))
		})

		It("emits SRIOV enabled values when NumVfs > 0", func() {
			device := baseDevice(consts.Ethernet, 1)
			device.Spec.Configuration.Template.NumVfs = 16

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovEnabledParam, consts.NvParamTrue))
			Expect(nvParams).To(HaveKeyWithValue(consts.SriovNumOfVfsParam, "16"))
		})

		It("emits LINK_TYPE_Pn for every port unconditionally (no firmware gate)", func() {
			// Four-port device; function must emit four LINK_TYPE entries regardless
			// of what the firmware may or may not expose.
			nvParams, err := validator.ConstructNvParamMapFromTemplate(baseDevice(consts.Ethernet, 4))
			Expect(err).NotTo(HaveOccurred())
			for _, name := range []string{"LINK_TYPE_P1", "LINK_TYPE_P2", "LINK_TYPE_P3", "LINK_TYPE_P4"} {
				Expect(nvParams).To(HaveKeyWithValue(name, consts.NvParamLinkTypeEthernet))
			}
		})

		It("emits IB link type when template requests it", func() {
			nvParams, err := validator.ConstructNvParamMapFromTemplate(baseDevice(consts.Infiniband, 2))
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.LinkTypeP1Param, consts.NvParamLinkTypeInfiniband))
			Expect(nvParams).To(HaveKeyWithValue(consts.LinkTypeP2Param, consts.NvParamLinkTypeInfiniband))
		})

		It("emits INTERNAL_CPU_OFFLOAD_ENGINE=NIC mode for BlueField devices", func() {
			device := baseDevice(consts.Ethernet, 2)
			device.Status.Type = consts.BlueField3DeviceID

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.BF3OperationModeParam, consts.NvParamBF3NicMode))
		})

		It("does not emit INTERNAL_CPU_OFFLOAD_ENGINE for non-BlueField devices", func() {
			device := baseDevice(consts.Ethernet, 2)
			device.Status.Type = "1021" // ConnectX-7 device ID

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).NotTo(HaveKey(consts.BF3OperationModeParam))
		})

		It("emits MAX_ACC_OUT_READ and ADVANCED_PCI_SETTINGS when PciPerf specifies a non-zero value", func() {
			device := baseDevice(consts.Ethernet, 1)
			device.Spec.Configuration.Template.PciPerformanceOptimized = &v1alpha1.PciPerformanceOptimizedSpec{
				Enabled:       true,
				MaxAccOutRead: 1337,
			}

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.MaxAccOutReadParam, "1337"))
			// ADVANCED_PCI_SETTINGS=1 is included in the same batch so mlxconfig can
			// unlock visibility of MAX_ACC_OUT_READ in one --with_default set call.
			Expect(nvParams).To(HaveKeyWithValue(consts.AdvancedPCISettingsParam, consts.NvParamTrue))
		})

		It("emits sentinel for MAX_ACC_OUT_READ when PciPerf is enabled with auto (value 0)", func() {
			device := baseDevice(consts.Ethernet, 1)
			device.Spec.Configuration.Template.PciPerformanceOptimized = &v1alpha1.PciPerformanceOptimizedSpec{
				Enabled: true,
			}

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.MaxAccOutReadParam, consts.NvParamFactoryDefault))
			Expect(nvParams).NotTo(HaveKey(consts.AdvancedPCISettingsParam))
		})

		It("emits RoCE params per port when RoceOptimized is enabled", func() {
			device := baseDevice(consts.Ethernet, 2)
			device.Spec.Configuration.Template.RoceOptimized = &v1alpha1.RoceOptimizedSpec{Enabled: true}

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device)
			Expect(err).NotTo(HaveOccurred())
			for _, name := range []string{consts.RoceCcPrioMaskP1Param, consts.RoceCcPrioMaskP2Param} {
				Expect(nvParams).To(HaveKeyWithValue(name, "255"))
			}
			for _, name := range []string{consts.CnpDscpP1Param, consts.CnpDscpP2Param} {
				Expect(nvParams).To(HaveKeyWithValue(name, "4"))
			}
			for _, name := range []string{consts.Cnp802pPrioP1Param, consts.Cnp802pPrioP2Param} {
				Expect(nvParams).To(HaveKeyWithValue(name, "6"))
			}
		})

		It("emits ATS_ENABLED=0 when GpuDirectOptimized is enabled", func() {
			device := baseDevice(consts.Ethernet, 1)
			device.Spec.Configuration.Template.PciPerformanceOptimized = &v1alpha1.PciPerformanceOptimizedSpec{Enabled: true, MaxAccOutRead: 1}
			device.Spec.Configuration.Template.GpuDirectOptimized = &v1alpha1.GpuDirectOptimizedSpec{
				Enabled: true,
				Env:     consts.EnvBaremetal,
			}

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue(consts.AtsEnabledParam, consts.NvParamFalse))
		})

		It("returns an error when GpuDirectOptimized is enabled without PciPerformanceOptimized", func() {
			device := baseDevice(consts.Ethernet, 1)
			device.Spec.Configuration.Template.GpuDirectOptimized = &v1alpha1.GpuDirectOptimizedSpec{
				Enabled: true,
				Env:     consts.EnvBaremetal,
			}

			_, err := validator.ConstructNvParamMapFromTemplate(device)
			Expect(err).To(MatchError("incorrect spec: GpuDirectOptimized should only be enabled together with PciPerformanceOptimized"))
		})

		It("returns an error when RoceOptimized is enabled with linkType Infiniband", func() {
			device := baseDevice(consts.Infiniband, 1)
			device.Spec.Configuration.Template.RoceOptimized = &v1alpha1.RoceOptimizedSpec{Enabled: true}

			_, err := validator.ConstructNvParamMapFromTemplate(device)
			Expect(err).To(MatchError("incorrect spec: RoceOptimized settings can only be used with link type Ethernet"))
		})

		It("passes rawNvConfig entries through and filters _Pn overrides beyond portCount", func() {
			device := baseDevice(consts.Ethernet, 1)
			device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
				{Name: "TEST_P1", Value: "keep"},
				{Name: "TEST_P2", Value: "drop"}, // beyond portCount
				{Name: "GENERIC", Value: "keep"},
			}

			nvParams, err := validator.ConstructNvParamMapFromTemplate(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(nvParams).To(HaveKeyWithValue("TEST_P1", "keep"))
			Expect(nvParams).To(HaveKeyWithValue("GENERIC", "keep"))
			Expect(nvParams).NotTo(HaveKey("TEST_P2"))
		})
	})

	Describe("ResolveFactoryDefaults", func() {
		It("replaces sentinels with the numeric default from DefaultConfig", func() {
			desired := map[string]string{
				consts.AtsEnabledParam:       consts.NvParamFactoryDefault,
				consts.RoceCcPrioMaskP1Param: consts.NvParamFactoryDefault,
			}
			portConfig := types.NewNvConfigQuery()
			// Bracketed values store [alias, numeric]; numeric slot is last.
			portConfig.DefaultConfig[consts.AtsEnabledParam] = []string{"false", "0"}
			portConfig.DefaultConfig[consts.RoceCcPrioMaskP1Param] = []string{"128"}

			resolved := validator.ResolveFactoryDefaults(desired, portConfig)
			Expect(resolved).To(HaveKeyWithValue(consts.AtsEnabledParam, "0"))
			Expect(resolved).To(HaveKeyWithValue(consts.RoceCcPrioMaskP1Param, "128"))
		})

		It("passes concrete values through unchanged", func() {
			desired := map[string]string{
				consts.SriovEnabledParam:  consts.NvParamTrue,
				consts.SriovNumOfVfsParam: "16",
				consts.LinkTypeP1Param:    consts.NvParamLinkTypeEthernet,
			}
			portConfig := types.NewNvConfigQuery()

			resolved := validator.ResolveFactoryDefaults(desired, portConfig)
			Expect(resolved).To(Equal(desired))
		})

		It("drops sentinel entries whose names are not in DefaultConfig", func() {
			desired := map[string]string{
				consts.AtsEnabledParam:   consts.NvParamFactoryDefault,
				consts.SriovEnabledParam: consts.NvParamFalse,
			}
			portConfig := types.NewNvConfigQuery()
			// DefaultConfig does not contain AtsEnabledParam — FW doesn't expose it.

			resolved := validator.ResolveFactoryDefaults(desired, portConfig)
			Expect(resolved).NotTo(HaveKey(consts.AtsEnabledParam))
			Expect(resolved).To(HaveKeyWithValue(consts.SriovEnabledParam, consts.NvParamFalse))
		})

		It("does not mutate the input map", func() {
			desired := map[string]string{
				consts.AtsEnabledParam: consts.NvParamFactoryDefault,
			}
			portConfig := types.NewNvConfigQuery()
			portConfig.DefaultConfig[consts.AtsEnabledParam] = []string{"0"}

			_ = validator.ResolveFactoryDefaults(desired, portConfig)
			Expect(desired).To(HaveKeyWithValue(consts.AtsEnabledParam, consts.NvParamFactoryDefault))
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

			maxReadRequestSize, qos := validator.CalculateDesiredRuntimeConfig(device)
			Expect(maxReadRequestSize).To(Equal(0))
			Expect(qos).To(BeNil())
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

			maxReadRequestSize, qos := validator.CalculateDesiredRuntimeConfig(device)
			Expect(maxReadRequestSize).To(Equal(1024))
			Expect(qos).To(BeNil())
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

			maxReadRequestSize, qos := validator.CalculateDesiredRuntimeConfig(device)
			Expect(maxReadRequestSize).To(Equal(4096))
			Expect(qos).To(BeNil())
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

			maxReadRequestSize, qos := validator.CalculateDesiredRuntimeConfig(device)
			Expect(maxReadRequestSize).To(Equal(0))
			Expect(qos).ToNot(BeNil())
			Expect(qos.Trust).To(Equal("dscp"))
			Expect(qos.PFC).To(Equal("0,1,0,1,0,0,0,0"))
			Expect(qos.ToS).To(Equal(100))
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

			maxReadRequestSize, qos := validator.CalculateDesiredRuntimeConfig(device)
			Expect(maxReadRequestSize).To(Equal(0))
			Expect(qos).ToNot(BeNil())
			Expect(qos.Trust).To(Equal("dscp"))
			Expect(qos.PFC).To(Equal("0,0,0,1,0,0,0,0"))
			Expect(qos.ToS).To(Equal(0))
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

			maxReadRequestSize, qos := validator.CalculateDesiredRuntimeConfig(device)
			Expect(maxReadRequestSize).To(Equal(256))
			Expect(qos).ToNot(BeNil())
			Expect(qos.Trust).To(Equal("customTrust"))
			Expect(qos.PFC).To(Equal("1,1,1,1,1,1,1,1"))
			Expect(qos.ToS).To(Equal(0))
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

			maxReadRequestSize, qos := validator.CalculateDesiredRuntimeConfig(device)
			Expect(maxReadRequestSize).To(Equal(256))
			Expect(qos).To(BeNil())
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

			maxReadRequestSize, qos := validator.CalculateDesiredRuntimeConfig(device)
			Expect(maxReadRequestSize).To(Equal(256))
			Expect(qos).To(BeNil())
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
				desiredMaxReadReqSize, desiredQos := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desiredMaxReadReqSize, nil)
				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.1").Return(desiredMaxReadReqSize, nil)

				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(desiredQos, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, "interface1").Return(desiredQos, nil)
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
				desiredMaxReadReqSize, desiredQos := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desiredMaxReadReqSize+128, nil)

				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(desiredQos, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, "interface1").Return(desiredQos, nil)

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

				desiredMaxReadReqSize, desiredQos := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desiredMaxReadReqSize, nil)
				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.1").Return(desiredMaxReadReqSize+256, nil)

				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(desiredQos, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, "interface1").Return(desiredQos, nil)
			})

			It("should return false with no error", func() {
				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("when trust setting does not match on the first port", func() {
			BeforeEach(func() {
				desiredMaxReadReqSize, desiredQos := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desiredMaxReadReqSize, nil)
				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.1").Return(desiredMaxReadReqSize, nil)

				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(&v1alpha1.QosSpec{Trust: "differentTrust", PFC: desiredQos.PFC}, nil)
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
				desiredMaxReadReqSize, desiredQos := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desiredMaxReadReqSize, nil)
				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.1").Return(desiredMaxReadReqSize, nil)

				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(&v1alpha1.QosSpec{Trust: desiredQos.Trust, PFC: "differentPfc"}, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, "interface1").Return(&v1alpha1.QosSpec{Trust: desiredQos.Trust, PFC: "differentPfc"}, nil)
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

				_, _ = validator.CalculateDesiredRuntimeConfig(device)

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
				desiredMaxReadReqSize, _ := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desiredMaxReadReqSize, nil)
				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.1").Return(desiredMaxReadReqSize, nil)

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

				desiredMaxReadReqSize, desiredQos := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desiredMaxReadReqSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(desiredQos, nil)
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

				desiredMaxReadReqSize, desiredQos := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desiredMaxReadReqSize, nil)
				mockConfigurationUtils.On("GetQoSSettings", device, "interface0").Return(&v1alpha1.QosSpec{Trust: "differentTrust", PFC: desiredQos.PFC}, nil)
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

				desiredMaxReadReqSize, _ := validator.CalculateDesiredRuntimeConfig(device)

				mockConfigurationUtils.On("GetMaxReadRequestSize", "0000:03:00.0").Return(desiredMaxReadReqSize, nil)
			})

			It("should return an error", func() {
				applied, err = validator.RuntimeConfigApplied(device)
				Expect(err).To(MatchError("cannot apply QoS settings for device port 0000:03:00.0, network interface is missing"))
				Expect(applied).To(BeFalse())
			})
		})
	})
})
