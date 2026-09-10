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
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/configuration/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/nvconfig"
	nvconfigmocks "github.com/Mellanox/nic-configuration-operator/pkg/nvconfig/mocks"
	spcxmocks "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

const pciAddress = "0000:3b:00.0"
const pciAddress2 = "0000:3b:00.1"

func portSpec(pciAddr string) v1alpha1.NicDevicePortSpec {
	return v1alpha1.NicDevicePortSpec{PCI: pciAddr}
}

type systemConfParamsProviderMock struct {
	mock.Mock
}

var _ nvconfig.SystemConfParamsProvider = (*systemConfParamsProviderMock)(nil)

func (m *systemConfParamsProviderMock) GetSystemConfParams(
	ctx context.Context, port v1alpha1.NicDevicePortSpec, conf string, asic int) (map[string]string, error) {
	args := m.Called(ctx, port, conf, asic)
	params, _ := args.Get(0).(map[string]string)
	return params, args.Error(1)
}

var _ = Describe("ConfigurationManager", func() {
	DescribeTable("compares normalized mlxconfig values",
		func(actual, desired string, expected bool) {
			Expect(mlxConfigValuesEqual(actual, desired)).To(Equal(expected))
		},
		Entry("case-insensitive symbolic values", "ETH", "eth", true),
		Entry("prefixed and bare hexadecimal values", "0xFF", "FF", true),
		Entry("hexadecimal and decimal values", "0x02", "2", true),
		Entry("decimal values with leading zeroes", "004", "4", true),
		Entry("different values", "1", "2", false),
	)

	Describe("extrapolatePortParamsFromNumOfPF", func() {
		It("copies P1 values to missing ports while preserving explicit overrides", func() {
			params := map[string]string{"NUM_OF_PF": "3", "LINK_TYPE_P1": "2", "LINK_TYPE_P3": "1"}

			extrapolatePortParamsFromNumOfPF(params)

			Expect(params).To(Equal(map[string]string{
				"NUM_OF_PF": "3", "LINK_TYPE_P1": "2", "LINK_TYPE_P2": "2", "LINK_TYPE_P3": "1",
			}))
		})

		It("does not extrapolate from a higher port when P1 is absent", func() {
			params := map[string]string{"NUM_OF_PF": "3", "LINK_TYPE_P2": "2"}

			extrapolatePortParamsFromNumOfPF(params)

			Expect(params).To(Equal(map[string]string{"NUM_OF_PF": "3", "LINK_TYPE_P2": "2"}))
		})
	})

	Describe("configurationManager.ValidateDeviceNvSpec", func() {
		var (
			mockHostUtils        mocks.ConfigurationUtils
			mockConfigValidation mocks.ConfigValidation
			mockNVConfigUtils    *nvconfigmocks.NVConfigUtils
			manager              configurationManager
			ctx                  context.Context
			device               *v1alpha1.NicDevice
		)

		BeforeEach(func() {
			mockHostUtils = mocks.ConfigurationUtils{}
			mockConfigValidation = mocks.ConfigValidation{}
			mockNVConfigUtils = nvconfigmocks.NewNVConfigUtils(GinkgoT())
			manager = configurationManager{
				configurationUtils: &mockHostUtils,
				configValidation:   &mockConfigValidation,
				nvConfigUtils:      mockNVConfigUtils,
			}
			ctx = context.TODO()

			device = &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						ResetToDefault: false,
						Template:       &v1alpha1.ConfigurationTemplateSpec{},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: pciAddress},
					},
				},
			}
		})

		Describe("ValidateDeviceNvSpec", func() {
			Context("when QueryNvConfig returns an error", func() {
				It("should return false, false, and the error", func() {
					queryErr := errors.New("failed to query nv config")
					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(types.NewNvConfigQuery(), queryErr)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(MatchError(queryErr))

					mockNVConfigUtils.AssertExpectations(GinkgoT())
				})
			})

			Context("when ResetToDefault is true", func() {
				BeforeEach(func() {
					device.Spec.Configuration.ResetToDefault = true
				})

				It("should call ValidateResetToDefault and return its results", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}},
					}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ValidateResetToDefault", nvConfig).
						Return(true, false, nil)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeTrue())
					Expect(reboot).To(BeFalse())
					Expect(err).To(BeNil())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})

				It("should return an error if ValidateResetToDefault fails", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{},
						NextBootConfig: map[string][]string{},
						DefaultConfig:  map[string][]string{},
					}
					validationErr := errors.New("validation failed")

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ValidateResetToDefault", nvConfig).
						Return(false, false, validationErr)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(MatchError(validationErr))

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})

			Context("when ConstructNvParamMapFromTemplate returns an error", func() {
				It("should return false, false, and the error", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{},
						NextBootConfig: map[string][]string{},
						DefaultConfig:  map[string][]string{},
					}
					constructErr := errors.New("failed to construct desired config")

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(nil, constructErr)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(MatchError(constructErr))

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})

			//nolint:dupl
			Context("when desiredConfig fully matches current and next config", func() {
				It("should return false, false, nil", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1"}, "param2": {"value2"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}, "param2": {"value2"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}, "param2": {"default2"}},
					}
					desiredConfig := map[string]string{"param1": "value1", "param2": "value2"}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(BeNil())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})

			//nolint:dupl
			Context("when desiredConfig fully matches next but not current config", func() {
				It("should return false, true, nil", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"oldValue1"}, "param2": {"value2"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}, "param2": {"value2"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}, "param2": {"default2"}},
					}
					desiredConfig := map[string]string{"param1": "value1", "param2": "value2"}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})

			//nolint:dupl
			Context("when desiredConfig does not fully match next boot config", func() {
				It("should return true, true, nil", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"oldValue1"}, "param2": {"value2"}},
						NextBootConfig: map[string][]string{"param1": {"wrongValue"}, "param2": {"value2"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}, "param2": {"default2"}},
					}
					desiredConfig := map[string]string{"param1": "value1", "param2": "value2"}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeTrue())
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})

			Context("when a desired param is missing from CurrentConfig but present in NextBootConfig", func() {
				It("should return false, true, nil (reboot only)", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param2": {"value2"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}, "param2": {"value2"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}, "param2": {"default2"}},
					}
					desiredConfig := map[string]string{"param1": "value1", "param2": "value2"}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})

			Context("when a desired param is missing from NextBootConfig (unsupported)", func() {
				It("should treat it as unsupported, skip it, and surface the param name", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param2": {"value2"}},
						NextBootConfig: map[string][]string{"param2": {"value2"}},
						DefaultConfig:  map[string][]string{"param2": {"default2"}},
					}
					// param1 is hidden on this device (e.g. ADVANCED_PCI_SETTINGS off)
					desiredConfig := map[string]string{"param1": "value1", "param2": "value2"}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)

					configUpdate, reboot, unsupported, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(unsupported).To(Equal([]string{"param1"}))
					Expect(err).To(BeNil())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})

				It("still flags mismatched supported params as configUpdateNeeded and reports unsupported ones", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param2": {"oldValue2"}},
						NextBootConfig: map[string][]string{"param2": {"oldValue2"}},
						DefaultConfig:  map[string][]string{"param2": {"default2"}},
					}
					// param1 is unsupported; param2 needs to change
					desiredConfig := map[string]string{"param1": "value1", "param2": "value2"}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)

					configUpdate, reboot, unsupported, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeTrue())
					Expect(reboot).To(BeTrue())
					Expect(unsupported).To(Equal([]string{"param1"}))
					Expect(err).To(BeNil())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})

			Context("when desired config contains string aliases", func() {
				It("should accept lowercase parameters", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1", "1"}, "param2": {"value2", "2"}},
						NextBootConfig: map[string][]string{"param1": {"value1", "1"}, "param2": {"value2", "2"}},
						DefaultConfig:  map[string][]string{"param1": {"default1", "1"}, "param2": {"default2", "2"}},
					}
					desiredConfig := map[string]string{"param1": "value1", "param2": "2"}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(BeNil())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
				It("should accept mixed-case parameters", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1", "1"}, "param2": {"value2", "2"}},
						NextBootConfig: map[string][]string{"param1": {"value1", "1"}, "param2": {"value2", "2"}},
						DefaultConfig:  map[string][]string{"param1": {"default1", "1"}, "param2": {"default2", "2"}},
					}
					desiredConfig := map[string]string{"param1": "VaLuE1", "param2": "valUE2"}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(BeNil())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
				It("should process not matching parameters", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1", "1"}, "param2": {"value2", "2"}},
						NextBootConfig: map[string][]string{"param1": {"value1", "1"}, "param2": {"value2", "2"}},
						DefaultConfig:  map[string][]string{"param1": {"default1", "1"}, "param2": {"default2", "2"}},
					}
					desiredConfig := map[string]string{"param1": "value3", "param2": "val4"}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeTrue())
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})

			Context("when handling per-port TRACER_ENABLED across two ports", func() {
				It("requires reboot when one port's current differs but next matches", func() {
					// two ports
					device.Status.Ports = []v1alpha1.NicDevicePortSpec{{PCI: pciAddress}, {PCI: pciAddress2}}

					// First port config (used for ConstructNvParamMapFromTemplate)
					nvConfig0 := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"TRACER_ENABLED": {"1"}},
						NextBootConfig: map[string][]string{"TRACER_ENABLED": {"1"}},
						DefaultConfig:  map[string][]string{},
					}
					// Second port already matches desired in current and next
					nvConfig1 := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"TRACER_ENABLED": {"0"}},
						NextBootConfig: map[string][]string{"TRACER_ENABLED": {"1"}},
						DefaultConfig:  map[string][]string{},
					}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig0, nil)
					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress2), []string(nil)).Return(nvConfig1, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig0).Return(map[string]string{"TRACER_ENABLED": "1"}, nil)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).To(BeNil())
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeTrue())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})

				It("requires config update when a port's next boot mismatches desired", func() {
					device.Status.Ports = []v1alpha1.NicDevicePortSpec{{PCI: pciAddress}, {PCI: pciAddress2}}

					nvConfig0 := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"TRACER_ENABLED": {"0"}},
						NextBootConfig: map[string][]string{"TRACER_ENABLED": {"1"}},
						DefaultConfig:  map[string][]string{},
					}
					nvConfig1 := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"TRACER_ENABLED": {"0"}},
						NextBootConfig: map[string][]string{"TRACER_ENABLED": {"0"}},
						DefaultConfig:  map[string][]string{},
					}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig0, nil)
					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress2), []string(nil)).Return(nvConfig1, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig0).Return(map[string]string{"TRACER_ENABLED": "1"}, nil)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).To(BeNil())
					Expect(configUpdate).To(BeTrue())
					Expect(reboot).To(BeTrue())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})

				It("requires reboot when a port misses param in current but matches next boot", func() {
					device.Status.Ports = []v1alpha1.NicDevicePortSpec{{PCI: pciAddress}, {PCI: pciAddress2}}

					nvConfig0 := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"TRACER_ENABLED": {"1"}},
						NextBootConfig: map[string][]string{"TRACER_ENABLED": {"1"}},
						DefaultConfig:  map[string][]string{},
					}
					nvConfig1 := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{}, // TRACER_ENABLED missing in current
						NextBootConfig: map[string][]string{"TRACER_ENABLED": {"1"}},
						DefaultConfig:  map[string][]string{},
					}

					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig0, nil)
					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress2), []string(nil)).Return(nvConfig1, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig0).Return(map[string]string{"TRACER_ENABLED": "1"}, nil)

					configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).To(BeNil())
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeTrue())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})
		})
	})
	Describe("configurationManager.ApplyNVConfiguration", func() {
		var (
			mockHostUtils        mocks.ConfigurationUtils
			mockConfigValidation mocks.ConfigValidation
			mockNV               *nvconfigmocks.NVConfigUtils
			manager              configurationManager
			ctx                  context.Context
			device               *v1alpha1.NicDevice
		)

		BeforeEach(func() {
			mockHostUtils = mocks.ConfigurationUtils{}
			mockConfigValidation = mocks.ConfigValidation{}
			mockNV = nvconfigmocks.NewNVConfigUtils(GinkgoT())
			manager = configurationManager{
				configurationUtils: &mockHostUtils,
				configValidation:   &mockConfigValidation,
				nvConfigUtils:      mockNV,
			}
			ctx = context.TODO()

			device = &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						ResetToDefault: false,
						Template:       &v1alpha1.ConfigurationTemplateSpec{},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: pciAddress},
					},
				},
			}
		})

		Describe("ApplyNVConfiguration", func() {
			Context("when ResetToDefault is true", func() {
				BeforeEach(func() {
					device.Spec.Configuration.ResetToDefault = true
				})

				It("should reset NV config successfully on a non-BF3 device", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}},
					}
					mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)

					mockNV.On("ResetNvConfig", portSpec(pciAddress)).Return(nil)
					mockNV.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", portSpec(pciAddress), consts.BF3OperationModeParam, mock.Anything)
					mockNV.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", portSpec(pciAddress), consts.AdvancedPCISettingsParam, mock.Anything)

					result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
					Expect(result.RebootRequired).To(BeTrue())
					Expect(err).To(BeNil())

					mockNV.AssertExpectations(GinkgoT())
				})

				It("should reset NV config and restore the BF3 operation mode successfully", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3NicMode}},
						NextBootConfig: map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3DpuMode}},
						DefaultConfig:  map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3DpuMode}},
					}
					mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)

					mockNV.On("ResetNvConfig", portSpec(pciAddress)).Return(nil)
					mockNV.
						On("SetNvConfigParameter", portSpec(pciAddress), consts.BF3OperationModeParam, consts.NvParamBF3NicMode).
						Return(nil)
					mockNV.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", portSpec(pciAddress), consts.BF3OperationModeParam, consts.NvParamBF3DpuMode)
					mockNV.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", portSpec(pciAddress), consts.AdvancedPCISettingsParam, mock.Anything)

					result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
					Expect(result.RebootRequired).To(BeTrue())
					Expect(err).To(BeNil())

					mockNV.AssertExpectations(GinkgoT())
				})

				It("should return error if ResetNvConfig fails", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}},
					}
					mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)

					resetErr := errors.New("failed to reset nv config")
					mockNV.On("ResetNvConfig", portSpec(pciAddress)).Return(resetErr)

					result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
					Expect(result.RebootRequired).To(BeFalse())
					Expect(err).To(MatchError(resetErr))

					mockNV.AssertExpectations(GinkgoT())
				})

				It("should return error if BF3 mode restore fails", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3NicMode}},
						NextBootConfig: map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3DpuMode}},
						DefaultConfig:  map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3DpuMode}},
					}
					mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)

					mockNV.On("ResetNvConfig", portSpec(pciAddress)).Return(nil)
					setParamErr := errors.New("failed to set nv config parameter")
					mockNV.
						On("SetNvConfigParameter", portSpec(pciAddress), consts.BF3OperationModeParam, consts.NvParamBF3NicMode).
						Return(setParamErr)

					result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
					Expect(result.RebootRequired).To(BeFalse())
					Expect(err).To(MatchError(setParamErr))

					mockNV.AssertExpectations(GinkgoT())
				})
			})

			Context("when ResetToDefault is false", func() {
				Context("when QueryNvConfig returns an error", func() {
					It("should return false and the error", func() {
						queryErr := errors.New("failed to query nv config")
						mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(types.NewNvConfigQuery(), queryErr)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(result.RebootRequired).To(BeFalse())
						Expect(err).To(MatchError(queryErr))

						mockNV.AssertExpectations(GinkgoT())
					})
				})

				Context("when applying the desired template config", func() {
					It("should construct desiredConfig and apply no changes if desiredConfig matches NextBootConfig", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value1"}

						mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(result.RebootRequired).To(BeFalse())
						Expect(err).To(BeNil())

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should construct desiredConfig and apply necessary changes successfully", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value2"}

						mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)
						mockNV.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"param1": "value2"}, false, false).
							Return(types.ApplyStatusSuccess, nil)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(result.RebootRequired).To(BeTrue())
						Expect(err).To(BeNil())

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should return error if ConstructNvParamMapFromTemplate fails", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						constructErr := errors.New("failed to construct desired config")

						mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(nil, constructErr)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(result.RebootRequired).To(BeFalse())
						Expect(err).To(MatchError(constructErr))

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should skip unsupported parameters and return partially applied", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value1", "param2": "value2"}

						mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(result.RebootRequired).To(BeFalse())
						Expect(result.Status).To(Equal(types.ApplyStatusPartiallyApplied))
						Expect(err).To(BeNil())

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should return error if SetNvConfigParametersBatch fails while applying params", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value3"}

						mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)
						setParamErr := errors.New("failed to set param1")
						mockNV.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"param1": "value3"}, false, false).
							Return(types.ApplyStatusFailed, setParamErr)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(result.RebootRequired).To(BeFalse())
						Expect(err).To(MatchError(setParamErr))

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("applies per-port TRACER_ENABLED only to ports that need it", func() {
						// two ports
						device.Status.Ports = []v1alpha1.NicDevicePortSpec{{PCI: pciAddress}, {PCI: pciAddress2}}

						// First port already has desired in next boot
						nvConfig0 := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"TRACER_ENABLED": {"1"}},
							NextBootConfig: map[string][]string{"TRACER_ENABLED": {"1"}},
							DefaultConfig:  map[string][]string{},
						}
						// Second port needs update
						nvConfig1 := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"TRACER_ENABLED": {"0"}},
							NextBootConfig: map[string][]string{"TRACER_ENABLED": {"0"}},
							DefaultConfig:  map[string][]string{},
						}

						mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig0, nil)
						mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress2), []string(nil)).Return(nvConfig1, nil)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig0).Return(map[string]string{"TRACER_ENABLED": "1"}, nil)
						// Only port2 should be updated
						mockNV.On("SetNvConfigParametersBatch", portSpec(pciAddress2), map[string]string{"TRACER_ENABLED": "1"}, false, false).
							Return(types.ApplyStatusSuccess, nil)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(err).To(BeNil())
						Expect(result.RebootRequired).To(BeTrue())

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("returns partially applied when a port lacks TRACER_ENABLED in NextBoot", func() {
						device.Status.Ports = []v1alpha1.NicDevicePortSpec{{PCI: pciAddress}, {PCI: pciAddress2}}

						nvConfig0 := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"TRACER_ENABLED": {"1"}},
							NextBootConfig: map[string][]string{"TRACER_ENABLED": {"1"}},
							DefaultConfig:  map[string][]string{},
						}
						nvConfig1 := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"OTHER": {"x"}},
							NextBootConfig: map[string][]string{"OTHER": {"x"}}, // TRACER_ENABLED missing entirely
							DefaultConfig:  map[string][]string{},
						}

						mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig0, nil)
						mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress2), []string(nil)).Return(nvConfig1, nil)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig0).Return(map[string]string{"TRACER_ENABLED": "1"}, nil)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(result.RebootRequired).To(BeFalse())
						Expect(result.Status).To(Equal(types.ApplyStatusPartiallyApplied))
						Expect(err).To(BeNil())

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})
				})
			})

			Context("when applying multiple parameters", func() {
				It("should apply all parameters successfully and require a reboot", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"oldValue1"}, "param2": {"oldValue2"}},
						NextBootConfig: map[string][]string{"param1": {"newValue1"}, "param2": {"newValue2"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}, "param2": {"default2"}},
					}
					desiredConfig := map[string]string{"param1": "newValue3", "param2": "newValue3"}

					mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockNV.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"param1": "newValue3", "param2": "newValue3"}, false, false).
						Return(types.ApplyStatusSuccess, nil)

					result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
					Expect(result.RebootRequired).To(BeTrue())
					Expect(err).To(BeNil())

					mockNV.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})

			Context("when no parameters need to be applied", func() {
				It("should return nothing-to-do without applying any parameters", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}},
					}
					desiredConfig := map[string]string{"param1": "value1"}

					mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)

					result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
					Expect(result.RebootRequired).To(BeFalse())
					Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
					Expect(err).To(BeNil())

					mockNV.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})
		})
	})

	Describe("configurationManager.ApplyRuntimeConfiguration", func() {
		var (
			mockHostUtils        mocks.ConfigurationUtils
			mockConfigValidation mocks.ConfigValidation
			mockNV               *nvconfigmocks.NVConfigUtils
			manager              configurationManager
			ctx                  context.Context
			device               *v1alpha1.NicDevice
		)

		BeforeEach(func() {
			mockHostUtils = mocks.ConfigurationUtils{}
			mockConfigValidation = mocks.ConfigValidation{}
			mockNV = nvconfigmocks.NewNVConfigUtils(GinkgoT())
			manager = configurationManager{
				configurationUtils: &mockHostUtils,
				configValidation:   &mockConfigValidation,
				nvConfigUtils:      mockNV,
			}
			ctx = context.TODO()

			device = &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
								Enabled:        true,
								MaxReadRequest: 2048,
							},
							RoceOptimized: &v1alpha1.RoceOptimizedSpec{
								Enabled: true,
							},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: pciAddress, NetworkInterface: "eth0"},
					},
				},
			}
		})

		Context("when runtime config is already applied", func() {
			It("should return nil without applying any changes", func() {
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(true, nil)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(BeNil())

				mockHostUtils.AssertNotCalled(GinkgoT(), "SetMaxReadRequestSize", mock.Anything, mock.Anything)
				mockHostUtils.AssertNotCalled(GinkgoT(), "SetTrustAndPFC", mock.Anything, mock.Anything, mock.Anything)
				mockConfigValidation.AssertExpectations(GinkgoT())
			})
		})

		Context("when RuntimeConfigApplied returns an error", func() {
			It("should return the error", func() {
				checkErr := errors.New("failed to check runtime config")
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(false, checkErr)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(MatchError(checkErr))

				mockHostUtils.AssertNotCalled(GinkgoT(), "SetMaxReadRequestSize", mock.Anything, mock.Anything)
				mockHostUtils.AssertNotCalled(GinkgoT(), "SetTrustAndPFC", mock.Anything, mock.Anything, mock.Anything)
				mockConfigValidation.AssertExpectations(GinkgoT())
			})
		})

		Context("when applying max read request size", func() {
			BeforeEach(func() {
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(false, nil)
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(types.DesiredRuntimeConfig{MaxReadRequestSize: 2048})
			})

			It("should apply max read request size successfully", func() {
				mockHostUtils.On("SetMaxReadRequestSize", pciAddress, 2048).Return(nil)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(BeNil())

				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})

			It("should return error if SetMaxReadRequestSize fails", func() {
				setErr := errors.New("failed to set max read request size")
				mockHostUtils.On("SetMaxReadRequestSize", pciAddress, 2048).Return(setErr)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(MatchError(setErr))

				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})
		})

		Context("when applying QoS settings", func() {
			BeforeEach(func() {
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(false, nil)
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(types.DesiredRuntimeConfig{Qos: &v1alpha1.QosSpec{Trust: "trust", PFC: "pfc"}})
			})

			It("should apply QoS settings successfully", func() {
				mockHostUtils.On("SetQoSSettings", device, &v1alpha1.QosSpec{Trust: "trust", PFC: "pfc"}).Return(nil)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(BeNil())

				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})

			It("should return error if SetTrustAndPFC fails", func() {
				setErr := errors.New("failed to set QoS settings")
				mockHostUtils.On("SetQoSSettings", device, &v1alpha1.QosSpec{Trust: "trust", PFC: "pfc"}).Return(setErr)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(MatchError(setErr))

				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})
		})

		Context("when applying both max read request size and QoS settings", func() {
			BeforeEach(func() {
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(false, nil)
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(types.DesiredRuntimeConfig{MaxReadRequestSize: 2048, Qos: &v1alpha1.QosSpec{Trust: "trust", PFC: "pfc"}})
			})

			It("should apply both settings successfully", func() {
				mockHostUtils.On("SetMaxReadRequestSize", pciAddress, 2048).Return(nil)
				mockHostUtils.On("SetQoSSettings", device, &v1alpha1.QosSpec{Trust: "trust", PFC: "pfc"}).Return(nil)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(BeNil())

				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})

			It("should return error if SetMaxReadRequestSize fails", func() {
				setErr := errors.New("failed to set max read request size")
				mockHostUtils.On("SetMaxReadRequestSize", pciAddress, 2048).Return(setErr)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(MatchError(setErr))

				mockHostUtils.AssertNotCalled(GinkgoT(), "SetTrustAndPFC", mock.Anything, mock.Anything, mock.Anything)
				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})

			It("should return error if SetTrustAndPFC fails", func() {
				mockHostUtils.On("SetMaxReadRequestSize", pciAddress, 2048).Return(nil)
				setErr := errors.New("failed to set QoS settings")
				mockHostUtils.On("SetQoSSettings", device, &v1alpha1.QosSpec{Trust: "trust", PFC: "pfc"}).Return(setErr)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(MatchError(setErr))

				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})
		})

		Context("when applying per-port RoCE mode", func() {
			BeforeEach(func() {
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(false, nil)
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(types.DesiredRuntimeConfig{RoceMode: consts.RoceModeV2})
			})

			It("should apply RoCE mode successfully", func() {
				mockHostUtils.On("SetRoceMode", "eth0", consts.RoceModeV2).Return(nil)

				result, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(BeNil())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should return error if SetRoceMode fails", func() {
				setErr := errors.New("failed to set roce mode")
				mockHostUtils.On("SetRoceMode", "eth0", consts.RoceModeV2).Return(setErr)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(MatchError(setErr))
				mockHostUtils.AssertExpectations(GinkgoT())
			})
		})

		Context("when applying per-port ECN settings", func() {
			BeforeEach(func() {
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(false, nil)
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(types.DesiredRuntimeConfig{
					Qos: &v1alpha1.QosSpec{ECN: &v1alpha1.ECNSpec{Enabled: true, Priority: 3}},
				})
			})

			It("should apply ECN successfully", func() {
				mockHostUtils.On("SetECNEnabled", "eth0", 3, true, true).Return(nil)

				result, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(BeNil())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should return error if SetECNEnabled fails", func() {
				setErr := errors.New("failed to set ECN")
				mockHostUtils.On("SetECNEnabled", "eth0", 3, true, true).Return(setErr)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(MatchError(setErr))
				mockHostUtils.AssertExpectations(GinkgoT())
			})
		})

		Context("when applying per-port pause frames", func() {
			BeforeEach(func() {
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(false, nil)
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(types.DesiredRuntimeConfig{
					Qos: &v1alpha1.QosSpec{PauseFrames: &v1alpha1.PauseFramesSpec{Enabled: true}},
				})
			})

			It("should apply pause frames successfully", func() {
				mockHostUtils.On("SetPauseFrames", "eth0", true).Return(nil)

				result, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(BeNil())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should return error if SetPauseFrames fails", func() {
				setErr := errors.New("failed to set pause frames")
				mockHostUtils.On("SetPauseFrames", "eth0", true).Return(setErr)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(MatchError(setErr))
				mockHostUtils.AssertExpectations(GinkgoT())
			})
		})

		Context("when applying per-port runtime performance settings", func() {
			lroEnabled := true

			BeforeEach(func() {
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(false, nil)
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(types.DesiredRuntimeConfig{
					RuntimePerf: &v1alpha1.RuntimePerformanceOptimizedSpec{
						Enabled:          true,
						RxRingSize:       4096,
						TxRingSize:       4096,
						CombinedChannels: 8,
						LRO:              &lroEnabled,
					},
				})
			})

			It("should apply all runtime perf settings successfully", func() {
				mockHostUtils.On("SetRingSize", "eth0", 4096, 4096).Return(nil)
				mockHostUtils.On("GetCombinedChannels", "eth0").Return(4, nil)
				mockHostUtils.On("SetCombinedChannels", "eth0", 8).Return(nil)
				mockHostUtils.On("SetLRO", "eth0", true).Return(nil)

				result, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(BeNil())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should skip combined channels when driver does not support it", func() {
				mockHostUtils.On("SetRingSize", "eth0", 4096, 4096).Return(nil)
				mockHostUtils.On("GetCombinedChannels", "eth0").Return(0, nil)
				mockHostUtils.On("SetLRO", "eth0", true).Return(nil)

				result, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(BeNil())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				mockHostUtils.AssertNotCalled(GinkgoT(), "SetCombinedChannels", mock.Anything, mock.Anything)
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should return error if SetRingSize fails", func() {
				setErr := errors.New("failed to set ring size")
				mockHostUtils.On("SetRingSize", "eth0", 4096, 4096).Return(setErr)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(MatchError(setErr))
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should return error if SetCombinedChannels fails", func() {
				mockHostUtils.On("SetRingSize", "eth0", 4096, 4096).Return(nil)
				mockHostUtils.On("GetCombinedChannels", "eth0").Return(4, nil)
				setErr := errors.New("failed to set combined channels")
				mockHostUtils.On("SetCombinedChannels", "eth0", 8).Return(setErr)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(MatchError(setErr))
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should return error if SetLRO fails", func() {
				mockHostUtils.On("SetRingSize", "eth0", 4096, 4096).Return(nil)
				mockHostUtils.On("GetCombinedChannels", "eth0").Return(4, nil)
				mockHostUtils.On("SetCombinedChannels", "eth0", 8).Return(nil)
				setErr := errors.New("failed to set LRO")
				mockHostUtils.On("SetLRO", "eth0", true).Return(setErr)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(MatchError(setErr))
				mockHostUtils.AssertExpectations(GinkgoT())
			})
		})

		Context("when applying per-port cable length", func() {
			BeforeEach(func() {
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(false, nil)
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(types.DesiredRuntimeConfig{
					Qos: &v1alpha1.QosSpec{CableLen: 3},
				})
			})

			It("should apply cable length successfully", func() {
				mockHostUtils.On("SetCableLen", "eth0", 3).Return(nil)

				result, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(BeNil())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should return error if SetCableLen fails", func() {
				setErr := errors.New("failed to set cable length")
				mockHostUtils.On("SetCableLen", "eth0", 3).Return(setErr)

				_, err := manager.ApplyRuntimeConfiguration(ctx, device)
				Expect(err).To(MatchError(setErr))
				mockHostUtils.AssertExpectations(GinkgoT())
			})
		})
	})

	Describe("SpectrumX NV Configuration", func() {
		var (
			mockNVConfigUtils    *nvconfigmocks.NVConfigUtils
			mockSystemConfParams *systemConfParamsProviderMock
			mockSpcXMgr          *spcxmocks.SpectrumXManager
			mockConfigValidation mocks.ConfigValidation
			manager              configurationManager
			ctx                  context.Context
			device               *v1alpha1.NicDevice
		)

		BeforeEach(func() {
			mockNVConfigUtils = nvconfigmocks.NewNVConfigUtils(GinkgoT())
			mockSystemConfParams = &systemConfParamsProviderMock{}
			mockSpcXMgr = spcxmocks.NewSpectrumXManager(GinkgoT())
			mockConfigValidation = mocks.ConfigValidation{}
			manager = configurationManager{
				configValidation:       &mockConfigValidation,
				nvConfigUtils:          mockNVConfigUtils,
				systemConfParams:       mockSystemConfParams,
				spectrumXConfigManager: mockSpcXMgr,
			}
			ctx = context.TODO()
			device = &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							SpectrumXOptimized: &v1alpha1.SpectrumXOptimizedSpec{Enabled: true},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{{PCI: pciAddress}},
				},
			}
		})

		Describe("ValidateDeviceNvSpec", func() {
			// The combined override map (breakout + postBreakout + template + raw) is built by
			// ConstructNvParamMapFromTemplate; the manager validates the returned map against the device's
			// next-boot config — the same source the apply path uses. These tests mock the combined map
			// directly; the merge/expansion/priority itself is covered in the configValidation suite.
			It("requires update+reboot when a combined param mismatches next boot", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"1"}}}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)

				updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())
			})

			It("requires no update when every combined param matches next boot and current", func() {
				matched := map[string][]string{"NUM_OF_PF": {"2"}, "LINK_TYPE_P1": {"2"}}
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: matched, CurrentConfig: matched}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "2"}, nil)

				updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeFalse())
			})

			It("requires reboot (not update) when a param is staged for next boot but not yet current", func() {
				// Regression for the staged-not-rebooted case: next boot already has the desired value but
				// current does not, so we must report RebootRequired, not loop with NothingToDo.
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{
						NextBootConfig: map[string][]string{"NUM_OF_PF": {"2"}},
						CurrentConfig:  map[string][]string{"NUM_OF_PF": {"1"}},
					}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)

				updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeTrue())
			})

			It("returns the error when ConstructNvParamMapFromTemplate fails", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(nil, errors.New("config not found"))

				_, _, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("config not found"))
			})

			Context("with a Network Bay system profile", func() {
				BeforeEach(func() {
					device.Spec.Configuration.Template.NetworkBay = &v1alpha1.NetworkBaySpec{Conf: "conf3"}
					device.Status.NetworkBay = &v1alpha1.NicDeviceNetworkBayStatus{Asic: 0}
				})

				It("lets an explicit parameter override the system profile value", func() {
					matched := map[string][]string{"NUM_OF_PF": {"2"}}
					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
						types.NvConfigQuery{NextBootConfig: matched, CurrentConfig: matched}, nil)
					mockSystemConfParams.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
						Return(map[string]string{"NUM_OF_PF": "4"}, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
						Return(map[string]string{"NUM_OF_PF": "2"}, nil)

					updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).NotTo(HaveOccurred())
					Expect(updateNeeded).To(BeFalse())
					Expect(rebootNeeded).To(BeFalse())
				})

				It("flags a system profile parameter whose value differs", func() {
					matched := map[string][]string{"NUM_OF_PF": {"2"}, "BOARD_CONFIGURATION_MODE": {"1"}}
					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
						types.NvConfigQuery{NextBootConfig: matched, CurrentConfig: matched}, nil)
					mockSystemConfParams.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
						Return(map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
						Return(map[string]string{"NUM_OF_PF": "2"}, nil)

					updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).NotTo(HaveOccurred())
					Expect(updateNeeded).To(BeTrue())
					Expect(rebootNeeded).To(BeTrue())
				})

				It("value-checks each expanded profile index", func() {
					// The combined map already has the range expanded to concrete indices; [2] differs from
					// next boot, so validation must still report an update even though all four rows are covered.
					expanded := map[string]string{
						"MODULE_SPLIT_M0[0]": "1", "MODULE_SPLIT_M0[1]": "1",
						"MODULE_SPLIT_M0[2]": "1", "MODULE_SPLIT_M0[3]": "1",
					}
					nextBoot := map[string][]string{
						"MODULE_SPLIT_M0[0]": {"1"}, "MODULE_SPLIT_M0[1]": {"1"},
						"MODULE_SPLIT_M0[2]": {"0"}, "MODULE_SPLIT_M0[3]": {"1"},
					}
					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
						types.NvConfigQuery{NextBootConfig: nextBoot, CurrentConfig: nextBoot}, nil)
					mockSystemConfParams.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
						Return(expanded, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(expanded, nil)

					updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).NotTo(HaveOccurred())
					Expect(updateNeeded).To(BeTrue())
					Expect(rebootNeeded).To(BeTrue())
				})

				It("converges when every expanded profile index already matches", func() {
					matched := map[string][]string{
						"MODULE_SPLIT_M0[0]": {"1"}, "MODULE_SPLIT_M0[1]": {"1"},
						"MODULE_SPLIT_M0[2]": {"1"}, "MODULE_SPLIT_M0[3]": {"1"},
					}
					expanded := map[string]string{
						"MODULE_SPLIT_M0[0]": "1", "MODULE_SPLIT_M0[1]": "1",
						"MODULE_SPLIT_M0[2]": "1", "MODULE_SPLIT_M0[3]": "1",
					}
					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
						types.NvConfigQuery{NextBootConfig: matched, CurrentConfig: matched}, nil)
					mockSystemConfParams.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
						Return(expanded, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(expanded, nil)

					updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).NotTo(HaveOccurred())
					Expect(updateNeeded).To(BeFalse())
					Expect(rebootNeeded).To(BeFalse())
				})

			})
		})

		Describe("ApplyNVConfiguration", func() {
			It("force=false applies the combined params present in the query that differ", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"1"}, "LINK_TYPE_P1": {"2"}}}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "2"}, nil)
				// NUM_OF_PF differs (1 != 2) and is applied; LINK_TYPE_P1 already matches and is skipped.
				mockNVConfigUtils.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"NUM_OF_PF": "2"}, false, false).
					Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("force=true applies all combined params in a single --force batch", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "2"}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", portSpec(pciAddress),
					map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "2", "LINK_TYPE_P2": "2"}, false, true).Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("force=true applies the batch to every PF, not just the first", func() {
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{{PCI: pciAddress}, {PCI: pciAddress2}}
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress2), []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"NUM_OF_PF": "2"}, false, true).
					Return(types.ApplyStatusSuccess, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", portSpec(pciAddress2), map[string]string{"NUM_OF_PF": "2"}, false, true).
					Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
				mockNVConfigUtils.AssertCalled(GinkgoT(), "SetNvConfigParametersBatch", portSpec(pciAddress2), map[string]string{"NUM_OF_PF": "2"}, false, true)
			})

			It("propagates WithDefault=true to the combined batch", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"1"}}}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"NUM_OF_PF": "2"}, true, false).
					Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{WithDefault: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("returns NothingToDo when WithDefault=true but mlxconfig reports no changed params", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"NUM_OF_PF": "2"}, true, false).
					Return(types.ApplyStatusNothingToDo, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{WithDefault: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
				Expect(result.RebootRequired).To(BeFalse())
			})

			It("returns NothingToDo when Force and WithDefault are true but mlxconfig reports no changed params", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"NUM_OF_PF": "2"}, true, true).
					Return(types.ApplyStatusNothingToDo, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true, WithDefault: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
				Expect(result.RebootRequired).To(BeFalse())
			})

			It("returns NothingToDo when the combined params already match", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
				Expect(result.RebootRequired).To(BeFalse())
			})

			It("returns error when ConstructNvParamMapFromTemplate fails", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(nil, errors.New("config not found"))

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).To(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusFailed))
			})

			Context("with a Network Bay system profile", func() {
				BeforeEach(func() {
					device.Spec.Configuration.Template.NetworkBay = &v1alpha1.NetworkBaySpec{Conf: "conf3"}
					device.Status.NetworkBay = &v1alpha1.NicDeviceNetworkBayStatus{Asic: 0}
				})

				It("folds profile parameters into the regular force batch", func() {
					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(types.NvConfigQuery{}, nil)
					mockSystemConfParams.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
						Return(map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
						Return(map[string]string{"NUM_OF_PF": "2"}, nil)
					mockNVConfigUtils.On("SetNvConfigParametersBatch", portSpec(pciAddress),
						map[string]string{"BOARD_CONFIGURATION_MODE": "0", "NUM_OF_PF": "2"}, false, true).
						Return(types.ApplyStatusSuccess, nil)

					result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
					Expect(err).NotTo(HaveOccurred())
					Expect(result.RebootRequired).To(BeTrue())
				})

				It("lets an explicit value replace a profile value in the regular batch", func() {
					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(types.NvConfigQuery{}, nil)
					mockSystemConfParams.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
						Return(map[string]string{"NUM_OF_PF": "4"}, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
						Return(map[string]string{"NUM_OF_PF": "2"}, nil)
					mockNVConfigUtils.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"NUM_OF_PF": "2"}, false, true).
						Return(types.ApplyStatusSuccess, nil)

					result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
					Expect(err).NotTo(HaveOccurred())
					Expect(result.RebootRequired).To(BeTrue())
				})
			})
		})
	})

	Describe("Network Bay system profile", func() {
		var (
			mockHostUtils        mocks.ConfigurationUtils
			mockConfigValidation mocks.ConfigValidation
			mockNV               *nvconfigmocks.NVConfigUtils
			mockSystemConf       *systemConfParamsProviderMock
			manager              configurationManager
			ctx                  context.Context
			device               *v1alpha1.NicDevice
			nvConfig             types.NvConfigQuery
		)

		BeforeEach(func() {
			mockHostUtils = mocks.ConfigurationUtils{}
			mockConfigValidation = mocks.ConfigValidation{}
			mockNV = nvconfigmocks.NewNVConfigUtils(GinkgoT())
			mockSystemConf = &systemConfParamsProviderMock{}
			manager = configurationManager{
				configurationUtils: &mockHostUtils,
				configValidation:   &mockConfigValidation,
				nvConfigUtils:      mockNV,
				systemConfParams:   mockSystemConf,
			}
			ctx = context.TODO()

			device = &v1alpha1.NicDevice{
				Spec: v1alpha1.NicDeviceSpec{
					Configuration: &v1alpha1.NicDeviceConfigurationSpec{
						Template: &v1alpha1.ConfigurationTemplateSpec{
							NetworkBay: &v1alpha1.NetworkBaySpec{Conf: "conf3"},
						},
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports:      []v1alpha1.NicDevicePortSpec{{PCI: pciAddress}},
					NetworkBay: &v1alpha1.NicDeviceNetworkBayStatus{Asic: 0},
				},
			}

			nvConfig = types.NvConfigQuery{
				CurrentConfig:  map[string][]string{"param1": {"value1"}},
				NextBootConfig: map[string][]string{"param1": {"value1"}},
				DefaultConfig:  map[string][]string{"param1": {"default1"}},
			}
		})

		Describe("ValidateDeviceNvSpec", func() {
			It("requires update+reboot when a profile parameter differs", func() {
				nvConfig.CurrentConfig["BOARD_CONFIGURATION_MODE"] = []string{"1"}
				nvConfig.NextBootConfig["BOARD_CONFIGURATION_MODE"] = []string{"1"}
				mockSystemConf.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, nil)
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
					Return(map[string]string{}, nil)

				configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(configUpdate).To(BeTrue())
				Expect(reboot).To(BeTrue())
			})

			It("returns an error when the profile cannot be resolved", func() {
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				mockSystemConf.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
					Return(nil, errors.New("profile not found"))

				_, _, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).To(MatchError("profile not found"))
			})

			It("requires nothing when a hexadecimal profile value matches after normalization", func() {
				nvConfig.CurrentConfig["MODULE_SPLIT_M0[4]"] = []string{"0xFF"}
				nvConfig.NextBootConfig["MODULE_SPLIT_M0[4]"] = []string{"0xFF"}
				mockSystemConf.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"MODULE_SPLIT_M0[4]": "FF"}, nil)
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
					Return(map[string]string{}, nil)

				configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(configUpdate).To(BeFalse())
				Expect(reboot).To(BeFalse())
			})

			It("uses a rawNvConfig value instead of the profile value", func() {
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "NUM_OF_PF", Value: "8"}}
				portConfig := types.NvConfigQuery{
					CurrentConfig:  map[string][]string{"NUM_OF_PF": {"8"}},
					NextBootConfig: map[string][]string{"NUM_OF_PF": {"8"}},
					DefaultConfig:  map[string][]string{"NUM_OF_PF": {"1"}},
				}
				mockSystemConf.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"NUM_OF_PF": "4"}, nil)
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(portConfig, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, portConfig).
					Return(map[string]string{"NUM_OF_PF": "8"}, nil)

				configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(configUpdate).To(BeFalse())
				Expect(reboot).To(BeFalse())
			})

			It("does not resolve the profile when ResetToDefault is set", func() {
				device.Spec.Configuration.ResetToDefault = true
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				mockConfigValidation.On("ValidateResetToDefault", nvConfig).Return(false, false, nil)

				configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(configUpdate).To(BeFalse())
				Expect(reboot).To(BeFalse())
				mockSystemConf.AssertNotCalled(GinkgoT(), "GetSystemConfParams", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			})
		})

		Describe("ApplyNVConfiguration", func() {
			It("applies a profile parameter through the regular mlxconfig batch", func() {
				nvConfig.CurrentConfig["BOARD_CONFIGURATION_MODE"] = []string{"1"}
				nvConfig.NextBootConfig["BOARD_CONFIGURATION_MODE"] = []string{"1"}
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				mockSystemConf.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
					Return(map[string]string{}, nil)
				mockNV.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, false, false).
					Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("applies profile parameters to every target using the general target scope", func() {
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{{PCI: pciAddress}, {PCI: pciAddress2}}
				portConfig := types.NvConfigQuery{
					CurrentConfig:  map[string][]string{"BOARD_CONFIGURATION_MODE": {"1"}},
					NextBootConfig: map[string][]string{"BOARD_CONFIGURATION_MODE": {"1"}},
				}
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(portConfig, nil)
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress2), []string(nil)).Return(portConfig, nil)
				mockSystemConf.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, portConfig).
					Return(map[string]string{}, nil)
				mockNV.On("SetNvConfigParametersBatch", portSpec(pciAddress),
					map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, false, false).Return(types.ApplyStatusSuccess, nil)
				mockNV.On("SetNvConfigParametersBatch", portSpec(pciAddress2),
					map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, false, false).Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})

				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("passes force to the regular batch containing profile parameters", func() {
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				mockSystemConf.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
					Return(map[string]string{}, nil)
				mockNV.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, false, true).
					Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("combines profile and explicit parameters in one batch", func() {
				portConfig := types.NvConfigQuery{
					CurrentConfig:  map[string][]string{"param1": {"value1"}, "BOARD_CONFIGURATION_MODE": {"1"}},
					NextBootConfig: map[string][]string{"param1": {"value1"}, "BOARD_CONFIGURATION_MODE": {"1"}},
					DefaultConfig:  map[string][]string{"param1": {"default1"}},
				}
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(portConfig, nil)
				mockSystemConf.On("GetSystemConfParams", ctx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, portConfig).
					Return(map[string]string{"param1": "value2"}, nil)
				mockNV.On("SetNvConfigParametersBatch", portSpec(pciAddress),
					map[string]string{"BOARD_CONFIGURATION_MODE": "0", "param1": "value2"}, false, false).
					Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("resets without resolving the profile when ResetToDefault is set", func() {
				device.Spec.Configuration.ResetToDefault = true
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				mockNV.On("ResetNvConfig", portSpec(pciAddress)).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
				mockSystemConf.AssertNotCalled(GinkgoT(), "GetSystemConfParams", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			})
		})

		// Full-flow suite: real configValidation (mocked ConfigurationUtils + SpectrumXManager + NVConfigUtils)
		// so the whole chain — system profile resolution, priority merging, validation, and apply — runs end
		// to end. Each case drives both ValidateDeviceNvSpec and ApplyNVConfiguration and asserts the concrete
		// SetNvConfigParametersBatch calls. ConstructNvParamMapFromTemplate always emits SRIOV_EN=0 /
		// NUM_OF_VFS=0 for an empty template, so every fixture stages those to keep them out of the assertions.
		Describe("Network Bay system profile full flow (validate + apply)", func() {
			var (
				fullFlowHostUtils  mocks.ConfigurationUtils
				fullFlowNV         *nvconfigmocks.NVConfigUtils
				fullFlowSystemConf *systemConfParamsProviderMock
				fullFlowSpcX       *spcxmocks.SpectrumXManager
				fullFlowManager    configurationManager
				fullFlowCtx        context.Context
				fullFlowDevice     *v1alpha1.NicDevice
			)

			BeforeEach(func() {
				fullFlowHostUtils = mocks.ConfigurationUtils{}
				fullFlowNV = nvconfigmocks.NewNVConfigUtils(GinkgoT())
				fullFlowSystemConf = &systemConfParamsProviderMock{}
				fullFlowSpcX = spcxmocks.NewSpectrumXManager(GinkgoT())
				fullFlowManager = configurationManager{
					configurationUtils:     &fullFlowHostUtils,
					configValidation:       newConfigValidation(&fullFlowHostUtils, nil, fullFlowSpcX),
					nvConfigUtils:          fullFlowNV,
					systemConfParams:       fullFlowSystemConf,
					spectrumXConfigManager: fullFlowSpcX,
				}
				fullFlowCtx = context.TODO()
				fullFlowDevice = &v1alpha1.NicDevice{
					Spec: v1alpha1.NicDeviceSpec{
						Configuration: &v1alpha1.NicDeviceConfigurationSpec{
							Template: &v1alpha1.ConfigurationTemplateSpec{
								NetworkBay: &v1alpha1.NetworkBaySpec{Conf: "conf3"},
							},
						},
					},
					Status: v1alpha1.NicDeviceStatus{
						Ports:      []v1alpha1.NicDevicePortSpec{{PCI: pciAddress}},
						NetworkBay: &v1alpha1.NicDeviceNetworkBayStatus{Asic: 0},
					},
				}
			})

			// sriovStaged returns the SRIOV defaults ConstructNvParamMapFromTemplate emits for an empty
			// template, staged in both next boot and current so they never drive update/reboot/apply.
			sriovStaged := func(extra map[string][]string) map[string][]string {
				m := map[string][]string{consts.SriovEnabledParam: {"0"}, consts.SriovNumOfVfsParam: {"0"}}
				for k, v := range extra {
					m[k] = v
				}
				return m
			}

			// 1) Empty profile layer and no overrides beyond staged defaults: nothing to do.
			It("1: a profile with no additional params converges without applying anything", func() {
				staged := sriovStaged(nil)
				fullFlowNV.On("QueryNvConfig", fullFlowCtx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: staged, CurrentConfig: staged}, nil)
				fullFlowSystemConf.On("GetSystemConfParams", fullFlowCtx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{}, nil)

				updateNeeded, rebootNeeded, _, err := fullFlowManager.ValidateDeviceNvSpec(fullFlowCtx, fullFlowDevice)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeFalse())

				result, err := fullFlowManager.ApplyNVConfiguration(fullFlowCtx, fullFlowDevice, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
				Expect(result.RebootRequired).To(BeFalse())
				fullFlowNV.AssertNotCalled(GinkgoT(), "SetNvConfigParametersBatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			})

			// 2) A differing profile value is staged through the ordinary mlxconfig batch.
			It("2: a differing profile value is applied through the regular batch", func() {
				staged := sriovStaged(map[string][]string{"BOARD_CONFIGURATION_MODE": {"1"}})
				fullFlowNV.On("QueryNvConfig", fullFlowCtx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: staged, CurrentConfig: staged}, nil)
				fullFlowSystemConf.On("GetSystemConfParams", fullFlowCtx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, nil)
				fullFlowNV.On("SetNvConfigParametersBatch", portSpec(pciAddress),
					map[string]string{"BOARD_CONFIGURATION_MODE": "0"}, false, false).Return(types.ApplyStatusSuccess, nil)

				updateNeeded, rebootNeeded, _, err := fullFlowManager.ValidateDeviceNvSpec(fullFlowCtx, fullFlowDevice)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())

				result, err := fullFlowManager.ApplyNVConfiguration(fullFlowCtx, fullFlowDevice, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})

			// 3) rawNvConfig replaces a profile value and is already staged: converged.
			It("3: an already-applied rawNvConfig override replaces the profile value", func() {
				fullFlowDevice.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "NUM_OF_PF", Value: "8"}}
				staged := sriovStaged(map[string][]string{"NUM_OF_PF": {"8"}})
				fullFlowNV.On("QueryNvConfig", fullFlowCtx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: staged, CurrentConfig: staged}, nil)
				fullFlowSystemConf.On("GetSystemConfParams", fullFlowCtx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"NUM_OF_PF": "4"}, nil)

				updateNeeded, rebootNeeded, _, err := fullFlowManager.ValidateDeviceNvSpec(fullFlowCtx, fullFlowDevice)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeFalse())

				result, err := fullFlowManager.ApplyNVConfiguration(fullFlowCtx, fullFlowDevice, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
				fullFlowNV.AssertNotCalled(GinkgoT(), "SetNvConfigParametersBatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			})

			// 4) A rawNvConfig value wins over the profile and is applied when not yet staged.
			It("4: a differing rawNvConfig override is applied instead of the profile value", func() {
				fullFlowDevice.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "NUM_OF_PF", Value: "8"}}
				staged := sriovStaged(map[string][]string{"NUM_OF_PF": {"1"}})
				fullFlowNV.On("QueryNvConfig", fullFlowCtx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: staged, CurrentConfig: staged}, nil)
				fullFlowSystemConf.On("GetSystemConfParams", fullFlowCtx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"NUM_OF_PF": "4"}, nil)
				fullFlowNV.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"NUM_OF_PF": "8"}, false, false).
					Return(types.ApplyStatusSuccess, nil)

				updateNeeded, rebootNeeded, _, err := fullFlowManager.ValidateDeviceNvSpec(fullFlowCtx, fullFlowDevice)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())

				result, err := fullFlowManager.ApplyNVConfiguration(fullFlowCtx, fullFlowDevice, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})

			// 5) Spectrum-X replaces a profile value and is already staged: converged.
			It("5: an already-applied Spectrum-X value replaces the profile value", func() {
				fullFlowDevice.Spec.Configuration.Template.SpectrumXOptimized = &v1alpha1.SpectrumXOptimizedSpec{Enabled: true}
				fullFlowSpcX.On("GetBreakoutMlxConfig", fullFlowDevice).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				fullFlowSpcX.On("GetPostBreakoutMlxConfig", fullFlowDevice).Return(nil, nil)
				staged := sriovStaged(map[string][]string{"NUM_OF_PF": {"2"}})
				fullFlowNV.On("QueryNvConfig", fullFlowCtx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: staged, CurrentConfig: staged}, nil)
				fullFlowSystemConf.On("GetSystemConfParams", fullFlowCtx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"NUM_OF_PF": "4"}, nil)

				updateNeeded, rebootNeeded, _, err := fullFlowManager.ValidateDeviceNvSpec(fullFlowCtx, fullFlowDevice)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeFalse())

				result, err := fullFlowManager.ApplyNVConfiguration(fullFlowCtx, fullFlowDevice, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
				fullFlowNV.AssertNotCalled(GinkgoT(), "SetNvConfigParametersBatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			})

			// 6) Spectrum-X params for the mismatched profile params are ALSO overridden by rawNvConfig (raw wins):
			//    one already applied (NUM_OF_PF), one differs (LINK_TYPE_P1) so the raw value — not the Spectrum-X
			//    value — is what gets applied.
			It("6: rawNvConfig overrides Spectrum-X for the mismatched params and the raw value wins on apply", func() {
				fullFlowDevice.Spec.Configuration.Template.SpectrumXOptimized = &v1alpha1.SpectrumXOptimizedSpec{Enabled: true}
				fullFlowDevice.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
					{Name: "NUM_OF_PF", Value: "8"},
					{Name: "LINK_TYPE_P1", Value: "2"},
				}
				fullFlowSpcX.On("GetBreakoutMlxConfig", fullFlowDevice).Return(map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "1"}, nil)
				fullFlowSpcX.On("GetPostBreakoutMlxConfig", fullFlowDevice).Return(nil, nil)
				staged := sriovStaged(map[string][]string{"NUM_OF_PF": {"8"}, "LINK_TYPE_P1": {"1"}})
				fullFlowNV.On("QueryNvConfig", fullFlowCtx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: staged, CurrentConfig: staged}, nil)
				fullFlowSystemConf.On("GetSystemConfParams", fullFlowCtx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"NUM_OF_PF": "4", "LINK_TYPE_P1": "3"}, nil)
				// Only LINK_TYPE_P1 differs from next boot; the raw value 2 wins over the Spectrum-X value 1.
				fullFlowNV.On("SetNvConfigParametersBatch", portSpec(pciAddress), map[string]string{"LINK_TYPE_P1": "2"}, false, false).
					Return(types.ApplyStatusSuccess, nil)

				updateNeeded, rebootNeeded, _, err := fullFlowManager.ValidateDeviceNvSpec(fullFlowCtx, fullFlowDevice)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())

				result, err := fullFlowManager.ApplyNVConfiguration(fullFlowCtx, fullFlowDevice, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})

			// 7) Same as (6) but a template-derived param (NUM_OF_VFS from NumVfs) is also partly overridden by
			//    rawNvConfig — proving raw wins over BOTH Spectrum-X (LINK_TYPE_P1) and the template (NUM_OF_VFS).
			It("7: rawNvConfig wins over both Spectrum-X and template params in the combined apply batch", func() {
				fullFlowDevice.Spec.Configuration.Template.NumVfs = 4 // template: SRIOV_EN=1, NUM_OF_VFS=4
				fullFlowDevice.Spec.Configuration.Template.SpectrumXOptimized = &v1alpha1.SpectrumXOptimizedSpec{Enabled: true}
				fullFlowDevice.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
					{Name: "NUM_OF_PF", Value: "8"},
					{Name: "LINK_TYPE_P1", Value: "2"},
					{Name: consts.SriovNumOfVfsParam, Value: "16"}, // raw overrides the template's NUM_OF_VFS=4
				}
				fullFlowSpcX.On("GetBreakoutMlxConfig", fullFlowDevice).Return(map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "1"}, nil)
				fullFlowSpcX.On("GetPostBreakoutMlxConfig", fullFlowDevice).Return(nil, nil)
				staged := map[string][]string{
					consts.SriovEnabledParam:  {"1"},
					consts.SriovNumOfVfsParam: {"4"}, // template value staged; raw wants 16
					"NUM_OF_PF":               {"8"},
					"LINK_TYPE_P1":            {"1"}, // Spectrum-X value staged; raw wants 2
				}
				fullFlowNV.On("QueryNvConfig", fullFlowCtx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: staged, CurrentConfig: staged}, nil)
				fullFlowSystemConf.On("GetSystemConfParams", fullFlowCtx, portSpec(pciAddress), "conf3", 0).
					Return(map[string]string{"NUM_OF_PF": "4", "LINK_TYPE_P1": "3"}, nil)
				// Raw wins on both: LINK_TYPE_P1 over Spectrum-X (1→2) and NUM_OF_VFS over template (4→16).
				fullFlowNV.On("SetNvConfigParametersBatch", portSpec(pciAddress),
					map[string]string{"LINK_TYPE_P1": "2", consts.SriovNumOfVfsParam: "16"}, false, false).Return(types.ApplyStatusSuccess, nil)

				updateNeeded, rebootNeeded, _, err := fullFlowManager.ValidateDeviceNvSpec(fullFlowCtx, fullFlowDevice)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())

				result, err := fullFlowManager.ApplyNVConfiguration(fullFlowCtx, fullFlowDevice, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})
		})
	})
})
