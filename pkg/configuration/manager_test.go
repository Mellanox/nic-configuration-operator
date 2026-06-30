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
	"github.com/Mellanox/nic-configuration-operator/pkg/nvconfig"
	nvconfigmocks "github.com/Mellanox/nic-configuration-operator/pkg/nvconfig/mocks"
	spcxmocks "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

const pciAddress = "0000:3b:00.0"
const pciAddress2 = "0000:3b:00.1"

// okSystemConfResult builds a matching validate_system_conf result with the given OK params.
func okSystemConfResult(okParams ...string) *nvconfig.SystemConfValidationResult {
	res := &nvconfig.SystemConfValidationResult{Matches: true}
	for _, p := range okParams {
		res.Entries = append(res.Entries, nvconfig.SystemConfEntry{Param: p, Status: nvconfig.SystemConfStatusOK, Actual: "ok"})
	}
	return res
}

// mismatchSystemConfResult builds a non-matching validate_system_conf result with the given MISMATCH params.
func mismatchSystemConfResult(mismatchedParams ...string) *nvconfig.SystemConfValidationResult {
	res := &nvconfig.SystemConfValidationResult{Matches: false}
	for _, p := range mismatchedParams {
		res.Entries = append(res.Entries, nvconfig.SystemConfEntry{Param: p, Status: nvconfig.SystemConfStatusMismatch, Expected: "e", Actual: "a"})
	}
	return res
}

// baseOnlyNVConfig implements nvconfig.NVConfigUtils but NOT the optional nvconfig.SystemConfValidator
// extension, modelling a library consumer's custom implementation. Used to assert the manager fails
// closed for a Network Bay template instead of silently skipping set_system_conf.
type baseOnlyNVConfig struct{}

func (baseOnlyNVConfig) QueryNvConfig(context.Context, string, []string) (types.NvConfigQuery, error) {
	return types.NvConfigQuery{}, nil
}
func (baseOnlyNVConfig) SetNvConfigParameter(string, string, string) error { return nil }
func (baseOnlyNVConfig) SetNvConfigParametersBatch(string, map[string]string, bool, bool) error {
	return nil
}
func (baseOnlyNVConfig) ResetNvConfig(string) error                                     { return nil }
func (baseOnlyNVConfig) SetSystemConf(context.Context, string, string, int, bool) error { return nil }
func (baseOnlyNVConfig) ValidateSystemConf(context.Context, string, string, int) (bool, error) {
	return true, nil
}

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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig0, nil)
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress2, []string(nil)).Return(nvConfig1, nil)
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig0, nil)
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress2, []string(nil)).Return(nvConfig1, nil)
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

					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig0, nil)
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress2, []string(nil)).Return(nvConfig1, nil)
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
					mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)

					mockNV.On("ResetNvConfig", pciAddress).Return(nil)
					mockNV.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, mock.Anything)
					mockNV.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, mock.Anything)

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
						On("SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, consts.NvParamBF3NicMode).
						Return(nil)
					mockNV.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, consts.NvParamBF3DpuMode)
					mockNV.AssertNotCalled(GinkgoT(), "SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, mock.Anything)

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

				It("should return error if BF3 mode restore fails", func() {
					nvConfig := types.NvConfigQuery{
						CurrentConfig:  map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3NicMode}},
						NextBootConfig: map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3DpuMode}},
						DefaultConfig:  map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3DpuMode}},
					}
					mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
						Return(nvConfig, nil)

					mockNV.On("ResetNvConfig", pciAddress).Return(nil)
					setParamErr := errors.New("failed to set nv config parameter")
					mockNV.
						On("SetNvConfigParameter", pciAddress, consts.BF3OperationModeParam, consts.NvParamBF3NicMode).
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

				Context("when applying the desired template config", func() {
					It("should construct desiredConfig and apply no changes if desiredConfig matches NextBootConfig", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value1"}

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

					It("should construct desiredConfig and apply necessary changes successfully", func() {
						nvConfig := types.NvConfigQuery{
							CurrentConfig:  map[string][]string{"param1": {"value1"}},
							NextBootConfig: map[string][]string{"param1": {"value1"}},
							DefaultConfig:  map[string][]string{"param1": {"default1"}},
						}
						desiredConfig := map[string]string{"param1": "value2"}

						mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).
							Return(nvConfig, nil)
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)
						mockNV.On("SetNvConfigParametersBatch", pciAddress, map[string]string{"param1": "value2"}, false, false).
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
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
							Return(desiredConfig, nil)
						setParamErr := errors.New("failed to set param1")
						mockNV.On("SetNvConfigParametersBatch", pciAddress, map[string]string{"param1": "value3"}, false, false).
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
						mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig0).Return(map[string]string{"TRACER_ENABLED": "1"}, nil)
						// Only port2 should be updated
						mockNV.On("SetNvConfigParametersBatch", pciAddress2, map[string]string{"TRACER_ENABLED": "1"}, false, false).Return(nil)

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
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
						Return(desiredConfig, nil)
					mockNV.On("SetNvConfigParametersBatch", pciAddress, map[string]string{"param1": "newValue3", "param2": "newValue3"}, false, false).
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
			mockSpcXMgr          *spcxmocks.SpectrumXManager
			mockConfigValidation mocks.ConfigValidation
			manager              configurationManager
			ctx                  context.Context
			device               *v1alpha1.NicDevice
		)

		BeforeEach(func() {
			mockNVConfigUtils = nvconfigmocks.NewNVConfigUtils(GinkgoT())
			mockSpcXMgr = spcxmocks.NewSpectrumXManager(GinkgoT())
			mockConfigValidation = mocks.ConfigValidation{}
			manager = configurationManager{
				configValidation:       &mockConfigValidation,
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
			// Validation builds the combined override map (breakout + postBreakout + template + raw) and
			// checks it against the device's next-boot config — the same source the apply path uses.
			It("requires update+reboot when breakout params mismatch", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"1"}}}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)

				updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())
			})

			It("requires update+reboot when postBreakout params mismatch", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"2"}, "LINK_TYPE_P1": {"1"}}}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(map[string]string{"LINK_TYPE_P1": "2"}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)

				updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())
			})

			It("requires no update when breakout, postBreakout and template all match next boot and current", func() {
				matched := map[string][]string{"NUM_OF_PF": {"2"}, "LINK_TYPE_P1": {"2"}}
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: matched, CurrentConfig: matched}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(map[string]string{"LINK_TYPE_P1": "2"}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)

				updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeFalse())
			})

			It("requires update+reboot when a template/rawNvConfig param mismatches", func() {
				// rawNvConfig params that are neither breakout nor postBreakout land in the desired config
				// (ConstructNvParamMapFromTemplate merges them) and are validated against the device next boot.
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"CUSTOM_PARAM_P1": {"0"}}}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{}, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(map[string]string{}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"CUSTOM_PARAM_P1": "42"}, nil)

				updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())
			})

			It("requires reboot (not update) when a param is staged for next boot but not yet current", func() {
				// Regression for the staged-not-rebooted case: next boot already has the desired value but
				// current does not, so we must report RebootRequired, not loop with NothingToDo.
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(
					types.NvConfigQuery{
						NextBootConfig: map[string][]string{"NUM_OF_PF": {"2"}},
						CurrentConfig:  map[string][]string{"NUM_OF_PF": {"1"}},
					}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)

				updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeTrue())
			})

			It("returns error when GetBreakoutMlxConfig fails", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(nil, errors.New("config not found"))

				_, _, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("config not found"))
			})

			Context("with Network Bay system_conf", func() {
				BeforeEach(func() {
					device.Spec.Configuration.Template.NetworkBay = &v1alpha1.NetworkBaySpec{Conf: "conf3"}
					device.Status.NetworkBay = &v1alpha1.NicDeviceNetworkBayStatus{Asic: 0}
				})

				It("does not flag drift when every system_conf mismatch is covered by a breakout param", func() {
					matched := map[string][]string{"NUM_OF_PF": {"2"}}
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(
						types.NvConfigQuery{NextBootConfig: matched, CurrentConfig: matched}, nil)
					mockNVConfigUtils.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
						Return(mismatchSystemConfResult("NUM_OF_PF"), nil)
					mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
					mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)

					updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).NotTo(HaveOccurred())
					Expect(updateNeeded).To(BeFalse())
					Expect(rebootNeeded).To(BeFalse())
				})

				It("flags drift when a system_conf mismatch is covered by no higher-priority param", func() {
					matched := map[string][]string{"NUM_OF_PF": {"2"}}
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(
						types.NvConfigQuery{NextBootConfig: matched, CurrentConfig: matched}, nil)
					mockNVConfigUtils.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
						Return(mismatchSystemConfResult("BOARD_CONFIGURATION_MODE"), nil)
					mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
					mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)

					updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).NotTo(HaveOccurred())
					Expect(updateNeeded).To(BeTrue())
					Expect(rebootNeeded).To(BeTrue())
				})

				It("value-checks the expanded indices of a range override (covers system_conf, but still drifts on a wrong index)", func() {
					// rawNvConfig MODULE_SPLIT_M0[0..3]=1 expands to [0]..[3]=1: it covers the four system_conf
					// mismatch rows by name, AND each index is value-validated against next boot. Here [2] is
					// wrong, so validation must still report an update — the range must not silently suppress drift.
					device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "MODULE_SPLIT_M0[0..3]", Value: "1"}}
					nextBoot := map[string][]string{
						"MODULE_SPLIT_M0[0]": {"1"}, "MODULE_SPLIT_M0[1]": {"1"},
						"MODULE_SPLIT_M0[2]": {"0"}, "MODULE_SPLIT_M0[3]": {"1"},
					}
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(
						types.NvConfigQuery{NextBootConfig: nextBoot, CurrentConfig: nextBoot}, nil)
					mockNVConfigUtils.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
						Return(mismatchSystemConfResult("MODULE_SPLIT_M0[0]", "MODULE_SPLIT_M0[1]", "MODULE_SPLIT_M0[2]", "MODULE_SPLIT_M0[3]"), nil)
					mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{}, nil)
					mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
						Return(map[string]string{"MODULE_SPLIT_M0[0..3]": "1"}, nil)

					updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).NotTo(HaveOccurred())
					Expect(updateNeeded).To(BeTrue())
					Expect(rebootNeeded).To(BeTrue())
				})

				It("converges when every expanded range index already matches and covers the system_conf rows", func() {
					device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "MODULE_SPLIT_M0[0..3]", Value: "1"}}
					matched := map[string][]string{
						"MODULE_SPLIT_M0[0]": {"1"}, "MODULE_SPLIT_M0[1]": {"1"},
						"MODULE_SPLIT_M0[2]": {"1"}, "MODULE_SPLIT_M0[3]": {"1"},
					}
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(
						types.NvConfigQuery{NextBootConfig: matched, CurrentConfig: matched}, nil)
					mockNVConfigUtils.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
						Return(mismatchSystemConfResult("MODULE_SPLIT_M0[0]", "MODULE_SPLIT_M0[1]", "MODULE_SPLIT_M0[2]", "MODULE_SPLIT_M0[3]"), nil)
					mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{}, nil)
					mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
						Return(map[string]string{"MODULE_SPLIT_M0[0..3]": "1"}, nil)

					updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).NotTo(HaveOccurred())
					Expect(updateNeeded).To(BeFalse())
					Expect(rebootNeeded).To(BeFalse())
				})

				It("returns an error for an oversized rawNvConfig range rather than materializing it", func() {
					device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "X[0..1000000000]", Value: "1"}}
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(types.NvConfigQuery{}, nil)
					mockNVConfigUtils.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
						Return(okSystemConfResult("NUM_OF_PF"), nil)
					mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{}, nil)
					mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
						Return(map[string]string{"X[0..1000000000]": "1"}, nil)

					_, _, _, err := manager.ValidateDeviceNvSpec(ctx, device)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("exceeds limit"))
				})
			})
		})

		Describe("ApplyNVConfiguration", func() {
			It("force=false applies the combined params present in the query that differ", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"1"}, "LINK_TYPE_P1": {"2"}}}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(map[string]string{"LINK_TYPE_P1": "2"}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)
				// NUM_OF_PF differs (1 \!= 2) and is applied; LINK_TYPE_P1 already matches and is skipped.
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress, map[string]string{"NUM_OF_PF": "2"}, false, false).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("force=true applies all combined params in a single --force batch", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(map[string]string{"LINK_TYPE_P1": "2"}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress,
					map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "2"}, false, true).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("force=true applies the batch to every PF, not just the first", func() {
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{{PCI: pciAddress}, {PCI: pciAddress2}}
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress2, []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress, map[string]string{"NUM_OF_PF": "2"}, false, true).Return(nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress2, map[string]string{"NUM_OF_PF": "2"}, false, true).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
				mockNVConfigUtils.AssertCalled(GinkgoT(), "SetNvConfigParametersBatch", pciAddress2, map[string]string{"NUM_OF_PF": "2"}, false, true)
			})

			It("a high-priority rawNvConfig range overrides a lower-priority concrete key (range over specific)", func() {
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "MODULE_SPLIT_M0[0..3]", Value: "1"}}
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(types.NvConfigQuery{}, nil)
				// Spectrum-X (lower priority) sets the concrete index to 0xff; raw range must win at [2].
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"MODULE_SPLIT_M0[2]": "0xff"}, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress,
					map[string]string{"MODULE_SPLIT_M0[0]": "1", "MODULE_SPLIT_M0[1]": "1", "MODULE_SPLIT_M0[2]": "1", "MODULE_SPLIT_M0[3]": "1"}, false, true).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("a high-priority rawNvConfig concrete key overrides a lower-priority range (specific over range)", func() {
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "MODULE_SPLIT_M0[2]", Value: "5"}}
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(types.NvConfigQuery{}, nil)
				// Spectrum-X (lower priority) sets the whole range to 1; raw specific [2]=5 must win at [2].
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"MODULE_SPLIT_M0[0..3]": "1"}, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress,
					map[string]string{"MODULE_SPLIT_M0[0]": "1", "MODULE_SPLIT_M0[1]": "1", "MODULE_SPLIT_M0[2]": "5", "MODULE_SPLIT_M0[3]": "1"}, false, true).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("rejects an aggregate range expansion that exceeds the total cap", func() {
				// Many in-bounds ranges that together exceed maxExpandedParams must be rejected, not materialized.
				rawParams := []v1alpha1.NvConfigParam{}
				breakout := map[string]string{}
				for i := 0; i < 20; i++ {
					breakout[fmt.Sprintf("PARAM_%d[0..255]", i)] = "1"
				}
				device.Spec.Configuration.Template.RawNvConfig = rawParams
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(breakout, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("refusing to materialize"))
				Expect(result.Status).To(Equal(types.ApplyStatusFailed))
			})

			It("propagates WithDefault=true to the combined batch", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"1"}}}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress, map[string]string{"NUM_OF_PF": "2"}, true, false).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{WithDefault: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("returns NothingToDo when the combined params already match", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
				Expect(result.RebootRequired).To(BeFalse())
			})

			It("returns error when GetBreakoutMlxConfig fails", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(nil, errors.New("config not found"))

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).To(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusFailed))
			})

			Context("with Network Bay system_conf", func() {
				BeforeEach(func() {
					device.Spec.Configuration.Template.NetworkBay = &v1alpha1.NetworkBaySpec{Conf: "conf3"}
					device.Status.NetworkBay = &v1alpha1.NicDeviceNetworkBayStatus{Asic: 0}
				})

				It("applies set_system_conf when the combined params do not cover a mismatch", func() {
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(types.NvConfigQuery{}, nil)
					mockNVConfigUtils.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
						Return(mismatchSystemConfResult("BOARD_CONFIGURATION_MODE"), nil)
					mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
					mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)
					mockNVConfigUtils.On("SetSystemConf", ctx, pciAddress, "conf3", 0, true).Return(nil)
					mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress, map[string]string{"NUM_OF_PF": "2"}, false, true).Return(nil)

					result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
					Expect(err).NotTo(HaveOccurred())
					Expect(result.RebootRequired).To(BeTrue())
				})

				It("does not apply set_system_conf when the combined params cover all mismatches", func() {
					mockNVConfigUtils.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(types.NvConfigQuery{}, nil)
					mockNVConfigUtils.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
						Return(mismatchSystemConfResult("NUM_OF_PF"), nil)
					mockSpcXMgr.On("GetBreakoutMlxConfig", device).Return(map[string]string{"NUM_OF_PF": "2"}, nil)
					mockSpcXMgr.On("GetPostBreakoutMlxConfig", device).Return(nil, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).Return(map[string]string{}, nil)
					mockNVConfigUtils.On("SetNvConfigParametersBatch", pciAddress, map[string]string{"NUM_OF_PF": "2"}, false, true).Return(nil)
					mockNVConfigUtils.AssertNotCalled(GinkgoT(), "SetSystemConf", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)

					result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
					Expect(err).NotTo(HaveOccurred())
					Expect(result.RebootRequired).To(BeTrue())
				})
			})
		})
	})

	Describe("Network Bay system_conf", func() {
		var (
			mockHostUtils        mocks.ConfigurationUtils
			mockConfigValidation mocks.ConfigValidation
			mockNV               *nvconfigmocks.NVConfigUtils
			manager              configurationManager
			ctx                  context.Context
			device               *v1alpha1.NicDevice
			nvConfig             types.NvConfigQuery
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
			It("requires update+reboot when system_conf has an unexplained mismatch", func() {
				// system_conf mismatched params are gathered first, then checked against the desired
				// (template + rawNvConfig) config — empty here, so BOARD_CONFIGURATION_MODE is uncovered drift.
				mockNV.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
					Return(mismatchSystemConfResult("BOARD_CONFIGURATION_MODE"), nil)
				mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
					Return(map[string]string{}, nil)

				configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(configUpdate).To(BeTrue())
				Expect(reboot).To(BeTrue())
			})

			It("fails closed when validate_system_conf reports a mismatch but no MISMATCH rows are parsed", func() {
				mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig, nil)
				// Result says NOT match, but no recognized MISMATCH rows — must not look like a match.
				mockNV.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
					Return(&nvconfig.SystemConfValidationResult{Matches: false}, nil)

				_, _, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).To(HaveOccurred())
			})

			It("fails closed when nvConfigUtils does not implement SystemConfValidator", func() {
				manager.nvConfigUtils = baseOnlyNVConfig{}

				_, _, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("SystemConfValidator"))
			})

			It("requires nothing when both regular config and system_conf match", func() {
				mockNV.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
					Return(okSystemConfResult("NUM_OF_PF"), nil)
				mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
					Return(map[string]string{}, nil)

				configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(configUpdate).To(BeFalse())
				Expect(reboot).To(BeFalse())
			})

			It("ignores a system_conf mismatch that rawNvConfig deliberately overrides", func() {
				// NUM_OF_PF is part of conf3 but the template overrides it, so the reported MISMATCH
				// is intentional. system_conf must not flag drift; the regular validation handles the value.
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "NUM_OF_PF", Value: "8"}}
				portConfig := types.NvConfigQuery{
					CurrentConfig:  map[string][]string{"NUM_OF_PF": {"8"}},
					NextBootConfig: map[string][]string{"NUM_OF_PF": {"8"}},
					DefaultConfig:  map[string][]string{"NUM_OF_PF": {"1"}},
				}
				mockNV.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
					Return(mismatchSystemConfResult("NUM_OF_PF"), nil)
				mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(portConfig, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, portConfig).
					Return(map[string]string{"NUM_OF_PF": "8"}, nil)

				configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(configUpdate).To(BeFalse())
				Expect(reboot).To(BeFalse())
			})

			It("does not manage system_conf when ResetToDefault is set (avoids a reboot loop)", func() {
				// ResetToDefault wipes nv config, so set_system_conf must not be validated/applied for the
				// same device — otherwise it would re-stage and get wiped every reconcile. validate_system_conf
				// must not be called; the reset validation path runs instead.
				device.Spec.Configuration.ResetToDefault = true
				mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig, nil)
				mockConfigValidation.On("ValidateResetToDefault", nvConfig).Return(false, false, nil)
				mockNV.AssertNotCalled(GinkgoT(), "ValidateSystemConfDetailed", mock.Anything, mock.Anything, mock.Anything, mock.Anything)

				configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(configUpdate).To(BeFalse())
				Expect(reboot).To(BeFalse())
			})
		})

		Describe("ApplyNVConfiguration", func() {
			// Apply checks system_conf coverage: it stages set_system_conf only when the combined override
			// params do not cover every mismatched profile param.
			It("stages set_system_conf when the combined params do not cover a mismatch", func() {
				mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig, nil)
				mockNV.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
					Return(mismatchSystemConfResult("BOARD_CONFIGURATION_MODE"), nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
					Return(map[string]string{}, nil)
				mockNV.On("SetSystemConf", ctx, pciAddress, "conf3", 0, false).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("passes --force to set_system_conf when Force is set", func() {
				mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig, nil)
				mockNV.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
					Return(mismatchSystemConfResult("BOARD_CONFIGURATION_MODE"), nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
					Return(map[string]string{}, nil)
				mockNV.On("SetSystemConf", ctx, pciAddress, "conf3", 0, true).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("stages set_system_conf before the regular nv param batch", func() {
				portConfig := types.NvConfigQuery{
					CurrentConfig:  map[string][]string{"param1": {"value1"}},
					NextBootConfig: map[string][]string{"param1": {"value1"}},
					DefaultConfig:  map[string][]string{"param1": {"default1"}},
				}
				mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(portConfig, nil)
				mockNV.On("ValidateSystemConfDetailed", ctx, pciAddress, "conf3", 0).
					Return(mismatchSystemConfResult("BOARD_CONFIGURATION_MODE"), nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, portConfig).
					Return(map[string]string{"param1": "value2"}, nil)
				setSystemConfCall := mockNV.On("SetSystemConf", ctx, pciAddress, "conf3", 0, false).Return(nil)
				// set_system_conf is the baseline and must be staged before the override batch.
				mockNV.On("SetNvConfigParametersBatch", pciAddress, map[string]string{"param1": "value2"}, false, false).
					Return(nil).NotBefore(setSystemConfCall)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("resets and never touches system_conf when ResetToDefault is set", func() {
				// Guards against the reset/set_system_conf reboot loop: reset runs, set_system_conf does not.
				device.Spec.Configuration.ResetToDefault = true
				mockNV.On("QueryNvConfig", ctx, pciAddress, []string(nil)).Return(nvConfig, nil)
				mockNV.On("ResetNvConfig", pciAddress).Return(nil)
				mockNV.AssertNotCalled(GinkgoT(), "SetSystemConf", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})
		})
	})
})
