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
	"fmt"
	"slices"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/configuration/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	nvconfigmocks "github.com/Mellanox/nic-configuration-operator/pkg/nvconfig/mocks"
	spcxmocks "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

const pciAddress = "0000:3b:00.0"
const pciAddress2 = "0000:3b:00.1"

var _ = Describe("ConfigurationManager", func() {
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
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(types.NewNvConfigQuery(), queryErr)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ValidateResetToDefault", nvConfig).
						Return(true, false, nil)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ValidateResetToDefault", nvConfig).
						Return(false, false, validationErr)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(nil, constructErr)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(false)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(false)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(false)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeTrue())
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})

			Context("when AdvancedPCISettingsEnabled is true and a parameter is missing in CurrentConfig", func() {
				It("should return an IncorrectSpecError", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param2": {"value2"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}, "param2": {"value2"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}, "param2": {"default2"}},
					}
					desiredConfig := map[string]string{"param1": "value1", "param2": "value2"}

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)

					expectedErr := types.IncorrectSpecError(
						fmt.Sprintf("Parameter %s unsupported for device %s", "param1", device.Name))

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(MatchError(expectedErr))

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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
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

					// First port config (used for ConstructNvParamMapFromTemplate and AdvancedPCISettingsEnabled)
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig0, nil)
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress2, []string(nil)).Return(nvConfig1, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig0).Return(map[string]string{"TRACER_ENABLED": "1"}, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig0).Return(false)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig0, nil)
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress2, []string(nil)).Return(nvConfig1, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig0).Return(map[string]string{"TRACER_ENABLED": "1"}, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig0).Return(false)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).To(BeNil())
					Expect(configUpdate).To(BeTrue())
					Expect(reboot).To(BeTrue())

					mockNVConfigUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})

				It("returns IncorrectSpecError when advanced enabled and a port misses param in current", func() {
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig0, nil)
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress2, []string(nil)).Return(nvConfig1, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig0).Return(map[string]string{"TRACER_ENABLED": "1"}, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig0).Return(true)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(types.IsIncorrectSpecError(err)).To(BeTrue())

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

				It("should reset NV config and set AdvancedPCISettings parameter successfully", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}},
					}
					mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)

					mockNV.On("ResetNvConfig", pciAddress).Return(nil)
					mockNV.
						On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
						Return(nil)
					mockNV.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, mock.Anything)

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
					mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)

					mockNV.On("ResetNvConfig", pciAddress).Return(nil)
					mockNV.
						On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
						Return(nil)
					mockNV.
						On("SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, consts.NvParamBF3NicMode).
						Return(nil)
					mockNV.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, consts.NvParamBF3DpuMode)

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
					mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)

					resetErr := errors.New("failed to reset nv config")
					mockNV.On("ResetNvConfig", pciAddress).Return(resetErr)

					result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
					Expect(result.RebootRequired).To(BeFalse())
					Expect(err).To(MatchError(resetErr))

					mockNV.AssertExpectations(GinkgoT())
				})

				It("should return error if SetNvConfigParameter fails", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}},
					}
					mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)

					mockNV.On("ResetNvConfig", pciAddress).Return(nil)
					setParamErr := errors.New("failed to set nv config parameter")
					mockNV.
						On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
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
						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(types.NewNvConfigQuery(), queryErr)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(result.RebootRequired).To(BeFalse())
						Expect(err).To(MatchError(queryErr))

						mockNV.AssertExpectations(GinkgoT())
					})
				})

				Context("when AdvancedPCISettingsEnabled is false", func() {
					It("should set AdvancedPCISettingsParam and reset NIC firmware successfully", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value1"}

						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(false)
						mockNV.
							On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
							Return(nil)
						mockHostUtils.On("ResetNicFirmware", mock.Anything, pciAddress).
							Return(nil)
						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(result.RebootRequired).To(BeFalse())
						Expect(err).To(BeNil())

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should return error if SetNvConfigParameter fails", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(false)
						setParamErr := errors.New("failed to set nv config parameter")
						mockNV.
							On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
							Return(setParamErr)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(result.RebootRequired).To(BeFalse())
						Expect(err).To(MatchError(setParamErr))

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should request reboot if ResetNicFirmware fails", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(false)
						mockNV.On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
							Return(nil)
						resetFirmwareErr := errors.New("failed to reset NIC firmware")
						mockHostUtils.On("ResetNicFirmware", mock.Anything, pciAddress).Return(resetFirmwareErr)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(result.RebootRequired).To(BeTrue())
						Expect(err).To(BeNil())

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should return error if second QueryNvConfig fails", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}

						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
							Return(nvConfig, nil).Times(1)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(false)
						mockNV.On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
							Return(nil)
						mockHostUtils.On("ResetNicFirmware", mock.Anything, pciAddress).
							Return(nil)
						secondQueryErr := errors.New("failed to query nv config again")
						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
							Return(types.NewNvConfigQuery(), secondQueryErr)

						result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
						Expect(result.RebootRequired).To(BeFalse())
						Expect(err).To(MatchError(secondQueryErr))

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})
				})

				Context("when AdvancedPCISettingsEnabled is true", func() {
					It("should construct desiredConfig and apply no changes if desiredConfig matches NextBootConfig", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value1"}

						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
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

						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)
						mockNV.On("SetNvConfigParametersBatch", pciAddress, map[string]string{"param1": "value2"}, false).
							Return(nil)

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

						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
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

						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
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

						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)
						setParamErr := errors.New("failed to set param1")
						mockNV.On("SetNvConfigParametersBatch", pciAddress, map[string]string{"param1": "value3"}, false).
							Return(setParamErr)

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

						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig0, nil)
						mockNV.On("QueryNvConfig", ctx, pciAddress2, []string(nil)).Return(nvConfig1, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig0).Return(true)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig0).Return(map[string]string{"TRACER_ENABLED": "1"}, nil)
						// Only port2 should be updated
						mockNV.On("SetNvConfigParametersBatch", pciAddress2, map[string]string{"TRACER_ENABLED": "1"}, false).Return(nil)

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

						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig0, nil)
						mockNV.On("QueryNvConfig", ctx, pciAddress2, []string(nil)).Return(nvConfig1, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig0).Return(true)
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

					mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockNV.On("SetNvConfigParametersBatch", pciAddress, map[string]string{"param1": "newValue3", "param2": "newValue3"}, false).
						Return(nil)

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

					mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)
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
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(2048, nil)
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
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(0, &v1alpha1.QosSpec{Trust: "trust", PFC: "pfc"})
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
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(2048, &v1alpha1.QosSpec{Trust: "trust", PFC: "pfc"})
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
	})

	Describe("SpectrumX NV Configuration", func() {
		var (
			mockNVConfigUtils *nvconfigmocks.NVConfigUtils
			mockSpcXMgr       *spcxmocks.SpectrumXManager
			manager           configurationManager
			ctx               context.Context
			device            *v1alpha1.NicDevice
		)

		BeforeEach(func() {
			mockNVConfigUtils = nvconfigmocks.NewNVConfigUtils(GinkgoT())
			mockSpcXMgr = spcxmocks.NewSpectrumXManager(GinkgoT())
			manager = configurationManager{
				nvConfigUtils:          mockNVConfigUtils,
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
			It("returns update+reboot when breakout params mismatch", func() {
				breakoutParams := map[string]string{"NUM_OF_PF": "2", "NUM_OF_PLANES_P1": "0"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, mock.Anything).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"1"}, "NUM_OF_PLANES_P1": {"0"}}}, nil)

				updateNeeded, rebootNeeded, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())
			})

			It("returns update+reboot when postBreakout params mismatch", func() {
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"NUM_OF_PF"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"LINK_TYPE_P1"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"LINK_TYPE_P1": {"1"}}}, nil)

				updateNeeded, rebootNeeded, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())
			})

			It("returns no update when all params match", func() {
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"NUM_OF_PF"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"LINK_TYPE_P1"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"LINK_TYPE_P1": {"2"}}}, nil)

				updateNeeded, rebootNeeded, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeFalse())
			})

			It("returns error when GetBreakoutMlxConfig fails", func() {
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(nil, errors.New("config not found"))

				_, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("config not found"))
			})

			It("returns error when QueryNvConfig fails during breakout check", func() {
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, mock.Anything).Return(
					types.NvConfigQuery{}, errors.New("query failed"))

				_, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("query failed"))
			})

			It("skips breakout check when no breakout params and checks postBreakout", func() {
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(nil, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"LINK_TYPE_P1"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"LINK_TYPE_P1": {"1"}}}, nil)

				updateNeeded, rebootNeeded, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())
			})

			It("returns update+reboot when rawNvConfig params mismatch", func() {
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
					{Name: "CUSTOM_PARAM_P1", Value: "42"},
				}
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"NUM_OF_PF"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, mock.MatchedBy(func(names []string) bool {
					return len(names) == 2 && slices.Contains(names, "LINK_TYPE_P1") && slices.Contains(names, "CUSTOM_PARAM_P1")
				})).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"LINK_TYPE_P1": {"2"}, "CUSTOM_PARAM_P1": {"0"}}}, nil)

				updateNeeded, rebootNeeded, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())
			})

			It("returns no update when all params including rawNvConfig match", func() {
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
					{Name: "CUSTOM_PARAM_P1", Value: "42"},
				}
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"NUM_OF_PF"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, mock.MatchedBy(func(names []string) bool {
					return len(names) == 2 && slices.Contains(names, "LINK_TYPE_P1") && slices.Contains(names, "CUSTOM_PARAM_P1")
				})).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"LINK_TYPE_P1": {"2"}, "CUSTOM_PARAM_P1": {"42"}}}, nil)

				updateNeeded, rebootNeeded, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeFalse())
			})

			It("rawNvConfig overrides conflicting postBreakout param in validation", func() {
				// postBreakout wants LINK_TYPE_P1=2, but rawNvConfig overrides to LINK_TYPE_P1=1
				// current config has LINK_TYPE_P1=1, so merged desired (1) matches current (1) → no mismatch
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
					{Name: "LINK_TYPE_P1", Value: "1"},
				}
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"NUM_OF_PF"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"LINK_TYPE_P1"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"LINK_TYPE_P1": {"1"}}}, nil)

				updateNeeded, rebootNeeded, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeFalse())
			})

			It("rawNvConfig overrides breakout param and is checked in breakout phase", func() {
				// rawNvConfig overrides NUM_OF_PF which is a breakout param
				// current has NUM_OF_PF=2, rawNvConfig sets it to 4 → mismatch in breakout phase
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
					{Name: "NUM_OF_PF", Value: "4"},
				}
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				// After merge, breakout has NUM_OF_PF=4 (rawNvConfig override)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"NUM_OF_PF"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)

				updateNeeded, rebootNeeded, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())
			})
		})

		Describe("ApplyNVConfiguration (SpectrumX path)", func() {
			It("applies breakout only and returns PartiallyApplied when breakout mismatches", func() {
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, mock.Anything).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"1"}}}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress, breakoutParams, true).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusPartiallyApplied))
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("applies merged params when breakout matches but postBreakout mismatches", func() {
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"NUM_OF_PF"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"LINK_TYPE_P1"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"LINK_TYPE_P1": {"1"}}}, nil)
				merged := map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "2"}
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress, merged, true).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("returns NothingToDo when both match", func() {
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"NUM_OF_PF"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"LINK_TYPE_P1"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"LINK_TYPE_P1": {"2"}}}, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
			})

			It("returns error when SetNvConfigParametersBatch fails", func() {
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, mock.Anything).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"1"}}}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress, breakoutParams, true).Return(errors.New("set failed"))

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).To(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusFailed))
			})

			It("merges rawNvConfig into postBreakout and applies", func() {
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
					{Name: "CUSTOM_PARAM_P1", Value: "42"},
				}
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"NUM_OF_PF"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, mock.MatchedBy(func(names []string) bool {
					return len(names) == 2 && slices.Contains(names, "LINK_TYPE_P1") && slices.Contains(names, "CUSTOM_PARAM_P1")
				})).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"LINK_TYPE_P1": {"2"}, "CUSTOM_PARAM_P1": {"0"}}}, nil)
				merged := map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "2", "CUSTOM_PARAM_P1": "42"}
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress, merged, true).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("rawNvConfig overrides conflicting postBreakout param", func() {
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
					{Name: "LINK_TYPE_P1", Value: "1"},
				}
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"NUM_OF_PF"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				// After merge, LINK_TYPE_P1 should be "1" (rawNvConfig override)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"LINK_TYPE_P1"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"LINK_TYPE_P1": {"2"}}}, nil)
				// The merged set should have rawNvConfig's value "1" for LINK_TYPE_P1
				merged := map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "1"}
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress, merged, true).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("rawNvConfig overrides breakout param and applies in breakout phase", func() {
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
					{Name: "NUM_OF_PF", Value: "4"},
				}
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				// After merge, breakout has NUM_OF_PF=4 (rawNvConfig override), which mismatches current
				mergedBreakout := map[string]string{"NUM_OF_PF": "4"}
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"NUM_OF_PF"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress, mergedBreakout, true).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusPartiallyApplied))
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("filters _P2 rawNvConfig params for single-port device", func() {
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{
					{Name: "CUSTOM_PARAM_P1", Value: "42"},
					{Name: "CUSTOM_PARAM_P2", Value: "42"},
				}
				breakoutParams := map[string]string{"NUM_OF_PF": "2"}
				postBreakoutParams := map[string]string{"LINK_TYPE_P1": "2"}
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakoutParams, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(postBreakoutParams, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string{"NUM_OF_PF"}).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				// Only LINK_TYPE_P1 and CUSTOM_PARAM_P1 should be checked (P2 filtered for single-port)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, mock.MatchedBy(func(names []string) bool {
					return len(names) == 2 && slices.Contains(names, "LINK_TYPE_P1") && slices.Contains(names, "CUSTOM_PARAM_P1") && !slices.Contains(names, "CUSTOM_PARAM_P2")
				})).Return(
					types.NvConfigQuery{CurrentConfig: map[string][]string{"LINK_TYPE_P1": {"2"}, "CUSTOM_PARAM_P1": {"0"}}}, nil)
				merged := map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "2", "CUSTOM_PARAM_P1": "42"}
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress, merged, true).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})
		})
	})
})
