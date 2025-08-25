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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/configuration/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	nvconfigmocks "github.com/Mellanox/nic-configuration-operator/pkg/nvconfig/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

const pciAddress = "0000:3b:00.0"

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
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, "").
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, "").
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, "").
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, "").
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, "").
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, "").
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, "").
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, "").
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, "").
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, "").
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, "").
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
		})
	})
	Describe("configurationManager.ApplyDeviceNvSpec", func() {
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
					},
				},
				Status: v1alpha1.NicDeviceStatus{
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: pciAddress},
					},
				},
			}
		})

		Describe("ApplyDeviceNvSpec", func() {
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
					mockNV.On("QueryNvConfig", ctx, pciAddress, "").
						Return(nvConfig, nil)

					mockNV.On("ResetNvConfig", pciAddress).Return(nil)
					mockNV.
						On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
						Return(nil)
					mockNV.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, mock.Anything)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockNV.AssertExpectations(GinkgoT())
				})

				It("should reset NV config and restore the BF3 operation mode successfully", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3NicMode}},
						NextBootConfig: map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3DpuMode}},
						DefaultConfig:  map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3DpuMode}},
					}
					mockNV.On("QueryNvConfig", ctx, pciAddress, "").
						Return(nvConfig, nil)

					mockNV.On("ResetNvConfig", pciAddress).Return(nil)
					mockNV.
						On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
						Return(nil)
					mockNV.
						On("SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, consts.NvParamBF3NicMode).
						Return(nil)
					mockNV.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, consts.NvParamBF3DpuMode)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockNV.AssertExpectations(GinkgoT())
				})

				It("should return error if ResetNvConfig fails", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}},
					}
					mockNV.On("QueryNvConfig", ctx, pciAddress, "").
						Return(nvConfig, nil)

					resetErr := errors.New("failed to reset nv config")
					mockNV.On("ResetNvConfig", pciAddress).Return(resetErr)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeFalse())
					Expect(err).To(MatchError(resetErr))

					mockNV.AssertExpectations(GinkgoT())
				})

				It("should return error if SetNvConfigParameter fails", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}},
					}
					mockNV.On("QueryNvConfig", ctx, pciAddress, "").
						Return(nvConfig, nil)

					mockNV.On("ResetNvConfig", pciAddress).Return(nil)
					setParamErr := errors.New("failed to set nv config parameter")
					mockNV.
						On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
						Return(setParamErr)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeFalse())
					Expect(err).To(MatchError(setParamErr))

					mockNV.AssertExpectations(GinkgoT())
				})
			})

			Context("when ResetToDefault is false", func() {
				Context("when QueryNvConfig returns an error", func() {
					It("should return false and the error", func() {
						queryErr := errors.New("failed to query nv config")
						mockNV.On("QueryNvConfig", ctx, pciAddress, "").Return(types.NewNvConfigQuery(), queryErr)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeFalse())
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

						mockNV.On("QueryNvConfig", ctx, pciAddress, "").
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(false)
						mockNV.
							On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
							Return(nil)
						mockHostUtils.On("ResetNicFirmware", mock.Anything, pciAddress).
							Return(nil)
						mockNV.On("QueryNvConfig", ctx, pciAddress, "").
							Return(nvConfig, nil)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeTrue())
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
						mockNV.On("QueryNvConfig", ctx, pciAddress, "").Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(false)
						setParamErr := errors.New("failed to set nv config parameter")
						mockNV.
							On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
							Return(setParamErr)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeFalse())
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
						mockNV.On("QueryNvConfig", ctx, pciAddress, "").Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(false)
						mockNV.On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
							Return(nil)
						resetFirmwareErr := errors.New("failed to reset NIC firmware")
						mockHostUtils.On("ResetNicFirmware", mock.Anything, pciAddress).Return(resetFirmwareErr)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeTrue())
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

						mockNV.On("QueryNvConfig", ctx, pciAddress, "").
							Return(nvConfig, nil).Times(1)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(false)
						mockNV.On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
							Return(nil)
						mockHostUtils.On("ResetNicFirmware", mock.Anything, pciAddress).
							Return(nil)
						secondQueryErr := errors.New("failed to query nv config again")
						mockNV.On("QueryNvConfig", ctx, pciAddress, "").
							Return(types.NewNvConfigQuery(), secondQueryErr)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeFalse())
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

						mockNV.On("QueryNvConfig", ctx, pciAddress, "").
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeTrue())
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

						mockNV.On("QueryNvConfig", ctx, pciAddress, "").
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)
						mockNV.On("SetNvConfigParameter", pciAddress, "param1", "value2").
							Return(nil)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeTrue())
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

						mockNV.On("QueryNvConfig", ctx, pciAddress, "").
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(nil, constructErr)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeFalse())
						Expect(err).To(MatchError(constructErr))

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should return error if desiredConfig has a parameter not in NextBootConfig", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value1", "param2": "value2"}

						mockNV.On("QueryNvConfig", ctx, pciAddress, "").
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)

						expectedErr := types.IncorrectSpecError(
							fmt.Sprintf("Parameter %s unsupported for device %s", "param2", device.Name))

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeFalse())
						Expect(err).To(MatchError(expectedErr))

						mockNV.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should return error if SetNvConfigParameter fails while applying params", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value3"}

						mockNV.On("QueryNvConfig", ctx, pciAddress, "").
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)
						setParamErr := errors.New("failed to set param1")
						mockNV.On("SetNvConfigParameter", pciAddress, "param1", "value3").
							Return(setParamErr)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeFalse())
						Expect(err).To(MatchError(setParamErr))

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

					mockNV.On("QueryNvConfig", ctx, pciAddress, "").
						Return(nvConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockNV.On("SetNvConfigParameter", pciAddress, "param1", "newValue3").
						Return(nil)
					mockNV.On("SetNvConfigParameter", pciAddress, "param2", "newValue3").
						Return(nil)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockNV.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})

			Context("when no parameters need to be applied", func() {
				It("should return true without applying any parameters", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}},
					}
					desiredConfig := map[string]string{"param1": "value1"}

					mockNV.On("QueryNvConfig", ctx, pciAddress, "").
						Return(nvConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockNV.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})
		})
	})

	Describe("configurationManager.ApplyDeviceRuntimeSpec", func() {
		var (
			mockHostUtils        mocks.ConfigurationUtils
			mockConfigValidation mocks.ConfigValidation
			mockNV               *nvconfigmocks.NVConfigUtils
			manager              configurationManager
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

				err := manager.ApplyDeviceRuntimeSpec(device)
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

				err := manager.ApplyDeviceRuntimeSpec(device)
				Expect(err).To(MatchError(checkErr))

				mockHostUtils.AssertNotCalled(GinkgoT(), "SetMaxReadRequestSize", mock.Anything, mock.Anything)
				mockHostUtils.AssertNotCalled(GinkgoT(), "SetTrustAndPFC", mock.Anything, mock.Anything, mock.Anything)
				mockConfigValidation.AssertExpectations(GinkgoT())
			})
		})

		Context("when applying max read request size", func() {
			BeforeEach(func() {
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(false, nil)
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(2048, "", "")
			})

			It("should apply max read request size successfully", func() {
				mockHostUtils.On("SetMaxReadRequestSize", pciAddress, 2048).Return(nil)

				err := manager.ApplyDeviceRuntimeSpec(device)
				Expect(err).To(BeNil())

				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})

			It("should return error if SetMaxReadRequestSize fails", func() {
				setErr := errors.New("failed to set max read request size")
				mockHostUtils.On("SetMaxReadRequestSize", pciAddress, 2048).Return(setErr)

				err := manager.ApplyDeviceRuntimeSpec(device)
				Expect(err).To(MatchError(setErr))

				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})
		})

		Context("when applying QoS settings", func() {
			BeforeEach(func() {
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(false, nil)
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(0, "trust", "pfc")
			})

			It("should apply QoS settings successfully", func() {
				mockHostUtils.On("SetTrustAndPFC", device, "trust", "pfc").Return(nil)

				err := manager.ApplyDeviceRuntimeSpec(device)
				Expect(err).To(BeNil())

				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})

			It("should return error if SetTrustAndPFC fails", func() {
				setErr := errors.New("failed to set QoS settings")
				mockHostUtils.On("SetTrustAndPFC", device, "trust", "pfc").Return(setErr)

				err := manager.ApplyDeviceRuntimeSpec(device)
				Expect(err).To(MatchError(setErr))

				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})
		})

		Context("when applying both max read request size and QoS settings", func() {
			BeforeEach(func() {
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(false, nil)
				mockConfigValidation.On("CalculateDesiredRuntimeConfig", device).Return(2048, "trust", "pfc")
			})

			It("should apply both settings successfully", func() {
				mockHostUtils.On("SetMaxReadRequestSize", pciAddress, 2048).Return(nil)
				mockHostUtils.On("SetTrustAndPFC", device, "trust", "pfc").Return(nil)

				err := manager.ApplyDeviceRuntimeSpec(device)
				Expect(err).To(BeNil())

				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})

			It("should return error if SetMaxReadRequestSize fails", func() {
				setErr := errors.New("failed to set max read request size")
				mockHostUtils.On("SetMaxReadRequestSize", pciAddress, 2048).Return(setErr)

				err := manager.ApplyDeviceRuntimeSpec(device)
				Expect(err).To(MatchError(setErr))

				mockHostUtils.AssertNotCalled(GinkgoT(), "SetTrustAndPFC", mock.Anything, mock.Anything, mock.Anything)
				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})

			It("should return error if SetTrustAndPFC fails", func() {
				mockHostUtils.On("SetMaxReadRequestSize", pciAddress, 2048).Return(nil)
				setErr := errors.New("failed to set QoS settings")
				mockHostUtils.On("SetTrustAndPFC", device, "trust", "pfc").Return(setErr)

				err := manager.ApplyDeviceRuntimeSpec(device)
				Expect(err).To(MatchError(setErr))

				mockHostUtils.AssertExpectations(GinkgoT())
				mockConfigValidation.AssertExpectations(GinkgoT())
			})
		})
	})
})
