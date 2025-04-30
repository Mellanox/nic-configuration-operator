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
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

var _ = Describe("ConfigurationManager", func() {
	Describe("configurationManager.ValidateDeviceNvSpec", func() {
		var (
			mockHostUtils        mocks.ConfigurationUtils
			mockConfigValidation mocks.ConfigValidation
			manager              configurationManager
			ctx                  context.Context
			device               *v1alpha1.NicDevice
			pciAddress           string
		)

		BeforeEach(func() {
			mockHostUtils = mocks.ConfigurationUtils{}
			mockConfigValidation = mocks.ConfigValidation{}
			manager = configurationManager{
				configurationUtils: &mockHostUtils,
				configValidation:   &mockConfigValidation,
			}
			ctx = context.TODO()
			pciAddress = "0000:3b:00.0"

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
					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(types.NewNvConfigQuery(), queryErr)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(MatchError(queryErr))

					mockHostUtils.AssertExpectations(GinkgoT())
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

					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)
					mockConfigValidation.On("ValidateResetToDefault", nvConfig).
						Return(true, false, nil)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeTrue())
					Expect(reboot).To(BeFalse())
					Expect(err).To(BeNil())

					mockHostUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})

				It("should return an error if ValidateResetToDefault fails", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{},
						NextBootConfig: map[string][]string{},
						DefaultConfig:  map[string][]string{},
					}
					validationErr := errors.New("validation failed")

					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)
					mockConfigValidation.On("ValidateResetToDefault", nvConfig).
						Return(false, false, validationErr)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(MatchError(validationErr))

					mockHostUtils.AssertExpectations(GinkgoT())
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

					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(nil, constructErr)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(MatchError(constructErr))

					mockHostUtils.AssertExpectations(GinkgoT())
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

					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(false)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(BeNil())

					mockHostUtils.AssertExpectations(GinkgoT())
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

					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(false)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockHostUtils.AssertExpectations(GinkgoT())
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

					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(false)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeTrue())
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockHostUtils.AssertExpectations(GinkgoT())
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

					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
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

					mockHostUtils.AssertExpectations(GinkgoT())
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

					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(BeNil())

					mockHostUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
				It("should accept mixed-case parameters", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1", "1"}, "param2": {"value2", "2"}},
						NextBootConfig: map[string][]string{"param1": {"value1", "1"}, "param2": {"value2", "2"}},
						DefaultConfig:  map[string][]string{"param1": {"default1", "1"}, "param2": {"default2", "2"}},
					}
					desiredConfig := map[string]string{"param1": "VaLuE1", "param2": "valUE2"}

					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeFalse())
					Expect(reboot).To(BeFalse())
					Expect(err).To(BeNil())

					mockHostUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
				It("should process not matching parameters", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1", "1"}, "param2": {"value2", "2"}},
						NextBootConfig: map[string][]string{"param1": {"value1", "1"}, "param2": {"value2", "2"}},
						DefaultConfig:  map[string][]string{"param1": {"default1", "1"}, "param2": {"default2", "2"}},
					}
					desiredConfig := map[string]string{"param1": "value3", "param2": "val4"}

					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)

					configUpdate, reboot, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(configUpdate).To(BeTrue())
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockHostUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})
		})
	})
	Describe("configurationManager.ApplyDeviceNvSpec", func() {
		var (
			mockHostUtils        mocks.ConfigurationUtils
			mockConfigValidation mocks.ConfigValidation
			manager              configurationManager
			ctx                  context.Context
			device               *v1alpha1.NicDevice
			pciAddress           string
		)

		BeforeEach(func() {
			mockHostUtils = mocks.ConfigurationUtils{}
			mockConfigValidation = mocks.ConfigValidation{}
			manager = configurationManager{
				configurationUtils: &mockHostUtils,
				configValidation:   &mockConfigValidation,
			}
			ctx = context.TODO()
			pciAddress = "0000:3b:00.0"

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
					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)

					mockHostUtils.On("ResetNvConfig", pciAddress).Return(nil)
					mockHostUtils.
						On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
						Return(nil)
					mockHostUtils.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, mock.Anything)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockHostUtils.AssertExpectations(GinkgoT())
				})

				It("should reset NV config and restore the BF3 operation mode successfully", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3NicMode}},
						NextBootConfig: map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3DpuMode}},
						DefaultConfig:  map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3DpuMode}},
					}
					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)

					mockHostUtils.On("ResetNvConfig", pciAddress).Return(nil)
					mockHostUtils.
						On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
						Return(nil)
					mockHostUtils.
						On("SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, consts.NvParamBF3NicMode).
						Return(nil)
					mockHostUtils.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, consts.NvParamBF3DpuMode)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockHostUtils.AssertExpectations(GinkgoT())
				})

				It("should return error if ResetNvConfig fails", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}},
					}
					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)

					resetErr := errors.New("failed to reset nv config")
					mockHostUtils.On("ResetNvConfig", pciAddress).Return(resetErr)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeFalse())
					Expect(err).To(MatchError(resetErr))

					mockHostUtils.AssertExpectations(GinkgoT())
				})

				It("should return error if SetNvConfigParameter fails", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{"param1": {"value1"}},
						NextBootConfig: map[string][]string{"param1": {"value1"}},
						DefaultConfig:  map[string][]string{"param1": {"default1"}},
					}
					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)

					mockHostUtils.On("ResetNvConfig", pciAddress).Return(nil)
					setParamErr := errors.New("failed to set nv config parameter")
					mockHostUtils.
						On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
						Return(setParamErr)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeFalse())
					Expect(err).To(MatchError(setParamErr))

					mockHostUtils.AssertExpectations(GinkgoT())
				})
			})

			Context("when ResetToDefault is false", func() {
				Context("when QueryNvConfig returns an error", func() {
					It("should return false and the error", func() {
						queryErr := errors.New("failed to query nv config")
						mockHostUtils.On("QueryNvConfig", ctx, pciAddress).Return(types.NewNvConfigQuery(), queryErr)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeFalse())
						Expect(err).To(MatchError(queryErr))

						mockHostUtils.AssertExpectations(GinkgoT())
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

						mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(false)
						mockHostUtils.
							On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
							Return(nil)
						mockHostUtils.On("ResetNicFirmware", ctx, pciAddress).
							Return(nil)
						mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
							Return(nvConfig, nil)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeTrue())
						Expect(err).To(BeNil())

						mockHostUtils.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should return error if SetNvConfigParameter fails", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						mockHostUtils.On("QueryNvConfig", ctx, pciAddress).Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(false)
						setParamErr := errors.New("failed to set nv config parameter")
						mockHostUtils.
							On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
							Return(setParamErr)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeFalse())
						Expect(err).To(MatchError(setParamErr))

						mockHostUtils.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should request reboot if ResetNicFirmware fails", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						mockHostUtils.On("QueryNvConfig", ctx, pciAddress).Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(false)
						mockHostUtils.On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
							Return(nil)
						resetFirmwareErr := errors.New("failed to reset NIC firmware")
						mockHostUtils.On("ResetNicFirmware", ctx, pciAddress).Return(resetFirmwareErr)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeTrue())
						Expect(err).To(BeNil())

						mockHostUtils.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should return error if second QueryNvConfig fails", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}

						mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
							Return(nvConfig, nil).Times(1)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(false)
						mockHostUtils.On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
							Return(nil)
						mockHostUtils.On("ResetNicFirmware", ctx, pciAddress).
							Return(nil)
						secondQueryErr := errors.New("failed to query nv config again")
						mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
							Return(types.NewNvConfigQuery(), secondQueryErr)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeFalse())
						Expect(err).To(MatchError(secondQueryErr))

						mockHostUtils.AssertExpectations(GinkgoT())
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

						mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeTrue())
						Expect(err).To(BeNil())

						mockHostUtils.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should construct desiredConfig and apply necessary changes successfully", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value2"}

						mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)
						mockHostUtils.On("SetNvConfigParameter", pciAddress, "param1", "value2").
							Return(nil)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeTrue())
						Expect(err).To(BeNil())

						mockHostUtils.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should return error if ConstructNvParamMapFromTemplate fails", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						constructErr := errors.New("failed to construct desired config")

						mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(nil, constructErr)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeFalse())
						Expect(err).To(MatchError(constructErr))

						mockHostUtils.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should return error if desiredConfig has a parameter not in NextBootConfig", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value1", "param2": "value2"}

						mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
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

						mockHostUtils.AssertExpectations(GinkgoT())
						mockConfigValidation.AssertExpectations(GinkgoT())
					})

					It("should return error if SetNvConfigParameter fails while applying params", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value3"}

						mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
							Return(nvConfig, nil)
						mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
							Return(true)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)
						setParamErr := errors.New("failed to set param1")
						mockHostUtils.On("SetNvConfigParameter", pciAddress, "param1", "value3").
							Return(setParamErr)

						reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
						Expect(reboot).To(BeFalse())
						Expect(err).To(MatchError(setParamErr))

						mockHostUtils.AssertExpectations(GinkgoT())
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

					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockHostUtils.On("SetNvConfigParameter", pciAddress, "param1", "newValue3").
						Return(nil)
					mockHostUtils.On("SetNvConfigParameter", pciAddress, "param2", "newValue3").
						Return(nil)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockHostUtils.AssertExpectations(GinkgoT())
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

					mockHostUtils.On("QueryNvConfig", ctx, pciAddress).
						Return(nvConfig, nil)
					mockConfigValidation.On("AdvancedPCISettingsEnabled", nvConfig).
						Return(true)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockHostUtils.AssertExpectations(GinkgoT())
					mockConfigValidation.AssertExpectations(GinkgoT())
				})
			})
		})
	})
})
