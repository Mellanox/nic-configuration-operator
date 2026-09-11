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
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/nvconfig"
	nvconfigmocks "github.com/Mellanox/nic-configuration-operator/pkg/nvconfig/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/spectrumx"
	spcxmocks "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

const pciAddress = "0000:3b:00.0"
const pciAddress2 = "0000:3b:00.1"
const testPCIXPath = "/nvidia/pci"

// legacyNVConfigUtils deliberately exposes only the public NVConfigUtils interface. It verifies
// that ConfigurationManager supports implementations without the optional context-aware extension.
type legacyNVConfigUtils struct {
	nvconfig.NVConfigUtils
}

type xpathNVConfigUtils struct {
	nvconfig.NVConfigUtils
	validationResults []xpathValidationResult
	validationCalls   []xpathValidationCall
	applyResult       *xpathApplyResult
	applyCalls        []xpathApplyCall
}

type xpathValidationResult struct {
	updateNeeded bool
	rebootNeeded bool
	err          error
}

type xpathValidationCall struct {
	ports      []v1alpha1.NicDevicePortSpec
	operations []dmscli.XPathOperation
}

type xpathApplyResult struct {
	status types.ApplyStatus
	err    error
}

type xpathApplyCall struct {
	port        v1alpha1.NicDevicePortSpec
	portCount   int
	params      map[string]string
	operations  []dmscli.XPathOperation
	withDefault bool
	force       bool
}

func (u *xpathNVConfigUtils) SetNvConfigParametersBatchWithContext(
	ctx context.Context,
	port v1alpha1.NicDevicePortSpec,
	params map[string]string,
	withDefault bool,
	force bool,
) (types.ApplyStatus, error) {
	return u.NVConfigUtils.(contextualNVConfigBatchSetter).
		SetNvConfigParametersBatchWithContext(ctx, port, params, withDefault, force)
}

func (u *xpathNVConfigUtils) ValidateNvConfigXPaths(
	ctx context.Context,
	ports []v1alpha1.NicDevicePortSpec,
	operations []dmscli.XPathOperation,
) (bool, bool, error) {
	if len(operations) == 0 {
		return false, false, nil
	}
	if len(u.validationResults) == 0 {
		return false, false, errors.New("unexpected XPath validation")
	}
	u.validationCalls = append(u.validationCalls, xpathValidationCall{ports, operations})
	result := u.validationResults[0]
	u.validationResults = u.validationResults[1:]
	return result.updateNeeded, result.rebootNeeded, result.err
}

func (u *xpathNVConfigUtils) SetNvConfigParametersBatchWithXPaths(
	ctx context.Context,
	port v1alpha1.NicDevicePortSpec,
	portCount int,
	params map[string]string,
	operations []dmscli.XPathOperation,
	withDefault bool,
	force bool,
) (types.ApplyStatus, error) {
	if u.applyResult == nil {
		if len(operations) > 0 {
			return types.ApplyStatusFailed, errors.New("unexpected XPath apply")
		}
		return u.NVConfigUtils.(contextualNVConfigBatchSetter).
			SetNvConfigParametersBatchWithContext(ctx, port, params, withDefault, force)
	}
	u.applyCalls = append(u.applyCalls, xpathApplyCall{
		port, portCount, params, operations, withDefault, force,
	})
	return u.applyResult.status, u.applyResult.err
}

func portSpec(pciAddr string) v1alpha1.NicDevicePortSpec {
	return v1alpha1.NicDevicePortSpec{PCI: pciAddr}
}

// okSystemConf stubs a matching validate_system_conf result (no mismatched params). Spread into a
// mock .Return(...) call: mockNV.On("ValidateSystemConf", ...).Return(okSystemConf()...).
func okSystemConf() []interface{} {
	return []interface{}{true, []string(nil), nil}
}

// mismatchSystemConf stubs a non-matching validate_system_conf result with the given MISMATCH params.
// Spread into a mock .Return(...) call: .Return(mismatchSystemConf("NUM_OF_PF")...).
func mismatchSystemConf(mismatchedParams ...string) []interface{} {
	return []interface{}{false, mismatchedParams, nil}
}

var _ = Describe("ConfigurationManager", func() {
	Describe("buildNVConfigApplyDiff", func() {
		It("separates changed, unchanged, and unsupported parameters", func() {
			diff := buildNVConfigApplyDiff(types.NvConfigQuery{
				NextBootConfig: map[string][]string{
					"CHANGED":   {"old"},
					"UNCHANGED": {"requested"},
				},
			}, map[string]string{
				"CHANGED":     "requested",
				"UNCHANGED":   "requested",
				"UNSUPPORTED": "requested",
			}, false, false)

			Expect(diff.changed).To(Equal(map[string]string{"CHANGED": "requested"}))
			Expect(diff.unchanged).To(Equal([]string{"UNCHANGED"}))
			Expect(diff.unsupported).To(Equal([]string{"UNSUPPORTED"}))
		})

		It("classifies every desired parameter as changed when force is enabled", func() {
			desired := map[string]string{"A": "1", "B": "2"}
			diff := buildNVConfigApplyDiff(types.NewNvConfigQuery(), desired, false, true)

			Expect(diff.changed).To(Equal(desired))
			Expect(diff.unchanged).To(BeEmpty())
			Expect(diff.unsupported).To(BeEmpty())
		})

		It("classifies matching supported parameters as changed when with-default is enabled", func() {
			diff := buildNVConfigApplyDiff(types.NvConfigQuery{
				NextBootConfig: map[string][]string{"MATCHING": {"requested"}},
			}, map[string]string{
				"MATCHING":    "requested",
				"UNSUPPORTED": "requested",
			}, true, false)

			Expect(diff.changed).To(Equal(map[string]string{"MATCHING": "requested"}))
			Expect(diff.unchanged).To(BeEmpty())
			Expect(diff.unsupported).To(Equal([]string{"UNSUPPORTED"}))
		})

		It("combines changes found on any queried port", func() {
			changed, hasUnsupported := buildCombinedNVConfigApplyDiff(map[string]types.NvConfigQuery{
				"0000:3b:00.0": {
					NextBootConfig: map[string][]string{"A": {"requested"}, "B": {"old"}},
				},
				"0000:3b:00.1": {
					NextBootConfig: map[string][]string{"A": {"old"}, "B": {"requested"}},
				},
			}, map[string]string{"A": "requested", "B": "requested"}, false, false, false)

			Expect(changed).To(Equal(map[string]string{"A": "requested", "B": "requested"}))
			Expect(hasUnsupported).To(BeFalse())
		})

		It("includes matching raw values when a typed operation can overwrite them", func() {
			changed, hasUnsupported := buildCombinedNVConfigApplyDiff(map[string]types.NvConfigQuery{
				"0000:3b:00.0": {
					NextBootConfig: map[string][]string{"RAW_OVERRIDE": {"requested"}},
				},
			}, map[string]string{"RAW_OVERRIDE": "requested"}, false, false, true)

			Expect(changed).To(Equal(map[string]string{"RAW_OVERRIDE": "requested"}))
			Expect(hasUnsupported).To(BeFalse())
		})
	})

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
						mockNV.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress), map[string]string{"param1": "value2"}, false, false).
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
						mockNV.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress), map[string]string{"param1": "value3"}, false, false).
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
						mockNV.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress2), map[string]string{"TRACER_ENABLED": "1"}, false, false).
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
					mockNV.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress), map[string]string{"param1": "newValue3", "param2": "newValue3"}, false, false).
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

		Context("with Spectrum-X optimization", func() {
			var mockSpcXMgr *spcxmocks.SpectrumXManager

			BeforeEach(func() {
				mockSpcXMgr = spcxmocks.NewSpectrumXManager(GinkgoT())
				manager.spectrumXConfigManager = mockSpcXMgr
				device.Spec.Configuration.Template.SpectrumXOptimized = &v1alpha1.SpectrumXOptimizedSpec{Enabled: true}
			})

			It("requires a matching configure plan before checking or applying runtime configuration", func() {
				planErr := errors.New("configure plan is stale")
				mockSpcXMgr.On("GetPreparedPlan", device, spectrumx.PlanStageConfigure).Return(nil, planErr)

				result, err := manager.ApplyRuntimeConfiguration(ctx, device)

				Expect(result.Status).To(Equal(types.ApplyStatusFailed))
				Expect(err).To(MatchError(ContainSubstring("matching doSPCX configure plan")))
				Expect(err).To(MatchError(ContainSubstring(planErr.Error())))
				mockConfigValidation.AssertNotCalled(GinkgoT(), "RuntimeConfigApplied", mock.Anything)
			})

			It("uses the prepared doSPCX plan when it matches", func() {
				mockSpcXMgr.On("GetPreparedPlan", device, spectrumx.PlanStageConfigure).
					Return(&spectrumx.Plan{}, nil)
				mockConfigValidation.On("RuntimeConfigApplied", device).Return(true, nil)

				result, err := manager.ApplyRuntimeConfiguration(ctx, device)

				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
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
			xpathUtils           *xpathNVConfigUtils
			mockSpcXMgr          *spcxmocks.SpectrumXManager
			mockConfigValidation mocks.ConfigValidation
			manager              configurationManager
			ctx                  context.Context
			device               *v1alpha1.NicDevice
			preparedPlan         *spectrumx.Plan
		)

		BeforeEach(func() {
			mockNVConfigUtils = nvconfigmocks.NewNVConfigUtils(GinkgoT())
			xpathUtils = &xpathNVConfigUtils{NVConfigUtils: mockNVConfigUtils}
			mockSpcXMgr = spcxmocks.NewSpectrumXManager(GinkgoT())
			mockConfigValidation = mocks.ConfigValidation{}
			manager = configurationManager{
				configValidation:       &mockConfigValidation,
				nvConfigUtils:          xpathUtils,
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
			preparedPlan = &spectrumx.Plan{}
			mockSpcXMgr.On("GetPreparedPlan", device, spectrumx.PlanStagePrepare).
				Return(func(*v1alpha1.NicDevice, spectrumx.PlanStage) *spectrumx.Plan {
					return preparedPlan
				}, nil).Maybe()
		})

		Describe("ValidateDeviceNvSpec", func() {
			// The template-derived native parameter map is validated independently from the doSPCX XPath plan.
			It("requires update+reboot when a native desired param mismatches next boot", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"1"}}}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)

				updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())
			})

			It("requires no update when every native desired param matches next boot and current", func() {
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

			DescribeTable("validates doSPCX phases across the breakout barrier",
				func(results []xpathValidationResult, expectedUpdate, expectedReboot bool, expectedCalls int) {
					preparedPlan.Breakout = []dmscli.XPathOperation{{
						Path: testPCIXPath, Values: map[string]any{"num-pfs": 2},
					}}
					preparedPlan.PostBreakout = []dmscli.XPathOperation{{
						Path: "/nvidia/link/type", Values: map[string]any{"value": "ETH"},
					}}
					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(types.NvConfigQuery{}, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
						Return(map[string]string{}, nil)
					xpathUtils.validationResults = results

					updateNeeded, rebootNeeded, _, err := manager.ValidateDeviceNvSpec(ctx, device)

					Expect(err).NotTo(HaveOccurred())
					Expect(updateNeeded).To(Equal(expectedUpdate))
					Expect(rebootNeeded).To(Equal(expectedReboot))
					Expect(xpathUtils.validationCalls).To(HaveLen(expectedCalls))
					Expect(xpathUtils.validationCalls[0]).To(Equal(xpathValidationCall{
						ports: []v1alpha1.NicDevicePortSpec{portSpec(pciAddress)}, operations: preparedPlan.Breakout,
					}))
				},
				Entry("breakout mismatch", []xpathValidationResult{{updateNeeded: true, rebootNeeded: true}}, true, true, 1),
				Entry("breakout pending only", []xpathValidationResult{{rebootNeeded: true}}, false, true, 1),
				Entry("post-breakout mismatch", []xpathValidationResult{{}, {updateNeeded: true, rebootNeeded: true}}, true, true, 2),
			)

			It("returns the error when ConstructNvParamMapFromTemplate fails", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(nil, errors.New("config not found"))

				_, _, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("config not found"))
			})

			It("rejects Network Bay before querying device state", func() {
				device.Spec.Configuration.Template.NetworkBay = &v1alpha1.NetworkBaySpec{Conf: "conf3"}

				updateNeeded, rebootNeeded, unsupported, err := manager.ValidateDeviceNvSpec(ctx, device)

				Expect(err).To(MatchError(ContainSubstring(
					"networkBay cannot currently be combined with spectrumXOptimized")))
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeFalse())
				Expect(unsupported).To(BeNil())
				mockNVConfigUtils.AssertNotCalled(GinkgoT(), "QueryNvConfig", mock.Anything, mock.Anything)
			})
		})

		Describe("ApplyNVConfiguration", func() {
			It("requires a matching prepare plan before querying or applying NV configuration", func() {
				missingPlanManager := spcxmocks.NewSpectrumXManager(GinkgoT())
				missingPlanManager.On("GetPreparedPlan", device, spectrumx.PlanStagePrepare).
					Return(nil, errors.New("prepare plan is missing"))
				manager.spectrumXConfigManager = missingPlanManager

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})

				Expect(result.Status).To(Equal(types.ApplyStatusFailed))
				Expect(err).To(MatchError(ContainSubstring("matching doSPCX prepare plan")))
				mockNVConfigUtils.AssertNotCalled(GinkgoT(), "QueryNvConfig", mock.Anything, mock.Anything)
			})

			It("rejects a Spectrum-X apply before raw changes when typed XPath support is unavailable", func() {
				manager.nvConfigUtils = legacyNVConfigUtils{NVConfigUtils: mockNVConfigUtils}

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})

				Expect(result.Status).To(Equal(types.ApplyStatusFailed))
				Expect(err).To(MatchError(ContainSubstring("doSPCX NVConfig is not supported")))
				mockNVConfigUtils.AssertNotCalled(GinkgoT(), "QueryNvConfig", mock.Anything, mock.Anything)
			})

			It("applies native parameters and the active doSPCX phase in one batch", func() {
				preparedPlan.Breakout = []dmscli.XPathOperation{{
					Path: testPCIXPath, Values: map[string]any{"num-pfs": 2, "rde-disable": true},
				}}
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"RAW_PARAM": {"1"}}}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"RAW_PARAM": "2"}, nil)

				xpathUtils.validationResults = []xpathValidationResult{{updateNeeded: true, rebootNeeded: true}}
				xpathUtils.applyResult = &xpathApplyResult{status: types.ApplyStatusSuccess}

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})

				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
				Expect(xpathUtils.applyCalls).To(Equal([]xpathApplyCall{{
					port: portSpec(pciAddress), portCount: 1,
					params: map[string]string{"RAW_PARAM": "2"}, operations: preparedPlan.Breakout,
				}}))
			})

			It("fails when a successful primary-target apply leaves native drift on a secondary PCI function", func() {
				device.Status.Ports = append(device.Status.Ports, portSpec(pciAddress2))
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress2), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"1"}}}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				xpathUtils.applyResult = &xpathApplyResult{status: types.ApplyStatusSuccess}

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})

				Expect(result.Status).To(Equal(types.ApplyStatusFailed))
				Expect(err).To(MatchError(ContainSubstring(
					"did not stage native NVConfig on secondary PCI function \"0000:3b:00.1\"")))
			})

			It("fails when a successful primary-target apply leaves typed drift on a secondary PCI function", func() {
				device.Status.Ports = append(device.Status.Ports, portSpec(pciAddress2))
				preparedPlan.Breakout = []dmscli.XPathOperation{{
					Path: testPCIXPath, Values: map[string]any{"num-pfs": 2},
				}}
				for _, port := range device.Status.Ports {
					mockNVConfigUtils.On("QueryNvConfig", ctx, port, []string(nil)).Return(types.NvConfigQuery{}, nil)
				}
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{}, nil)
				xpathUtils.validationResults = []xpathValidationResult{
					{updateNeeded: true, rebootNeeded: true},
					{updateNeeded: true, rebootNeeded: true},
				}
				xpathUtils.applyResult = &xpathApplyResult{status: types.ApplyStatusSuccess}

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})

				Expect(result.Status).To(Equal(types.ApplyStatusFailed))
				Expect(err).To(MatchError(ContainSubstring(
					"did not stage typed NVConfig on every secondary PCI function")))
				Expect(xpathUtils.validationCalls).To(HaveLen(2))
				Expect(xpathUtils.validationCalls[1].ports).To(Equal(
					[]v1alpha1.NicDevicePortSpec{portSpec(pciAddress2)}))
			})

			It("rejects rawNvConfig before querying or applying device state", func() {
				device.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{
					Name: "ROCE_ADAPTIVE_ROUTING_EN", Value: "0",
				}}

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})

				Expect(result.Status).To(Equal(types.ApplyStatusFailed))
				Expect(err).To(MatchError(ContainSubstring(
					"rawNvConfig cannot currently be combined with spectrumXOptimized")))
				mockNVConfigUtils.AssertNotCalled(GinkgoT(), "QueryNvConfig", mock.Anything, mock.Anything)
			})

			It("force applies the complete plan once through the primary target with every available DMS port", func() {
				device.Status.Ports = append(device.Status.Ports, portSpec(pciAddress2))
				preparedPlan.Breakout = []dmscli.XPathOperation{{
					Path: testPCIXPath, Values: map[string]any{"num-pfs": 2},
				}}
				preparedPlan.PostBreakout = []dmscli.XPathOperation{{
					Path: "/nvidia/link/type", Values: map[string]any{"value": "ETH"},
				}}
				for _, port := range device.Status.Ports {
					mockNVConfigUtils.On("QueryNvConfig", ctx, port, []string(nil)).
						Return(types.NvConfigQuery{}, nil)
				}
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{}, nil)
				xpathUtils.applyResult = &xpathApplyResult{status: types.ApplyStatusSuccess}
				xpathUtils.validationResults = []xpathValidationResult{{}}

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})

				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
				Expect(xpathUtils.validationCalls).To(Equal([]xpathValidationCall{{
					ports: []v1alpha1.NicDevicePortSpec{portSpec(pciAddress2)},
					operations: append(
						append([]dmscli.XPathOperation{}, preparedPlan.Breakout...), preparedPlan.PostBreakout...),
				}}))
				Expect(xpathUtils.applyCalls).To(HaveLen(1))
				Expect(xpathUtils.applyCalls[0].portCount).To(Equal(2))
				Expect(xpathUtils.applyCalls[0].operations).To(Equal(
					append(preparedPlan.Breakout, preparedPlan.PostBreakout...)))
				Expect(xpathUtils.applyCalls[0].force).To(BeTrue())
			})

			DescribeTable("selects post-breakout operations after the barrier",
				func(options types.ConfigurationOptions, includeBreakout bool) {
					preparedPlan.Breakout = []dmscli.XPathOperation{{
						Path: testPCIXPath, Values: map[string]any{"num-pfs": 2},
					}}
					preparedPlan.PostBreakout = []dmscli.XPathOperation{{
						Path: "/nvidia/link/type", Values: map[string]any{"value": "ETH"},
					}}
					mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).
						Return(types.NvConfigQuery{}, nil)
					mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
						Return(map[string]string{}, nil)
					xpathUtils.validationResults = []xpathValidationResult{
						{}, {updateNeeded: true, rebootNeeded: true},
					}
					xpathUtils.applyResult = &xpathApplyResult{status: types.ApplyStatusSuccess}

					result, err := manager.ApplyNVConfiguration(ctx, device, &options)

					Expect(err).NotTo(HaveOccurred())
					Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
					Expect(result.RebootRequired).To(BeTrue())
					expected := preparedPlan.PostBreakout
					if includeBreakout {
						expected = append(preparedPlan.Breakout, preparedPlan.PostBreakout...)
					}
					Expect(xpathUtils.applyCalls[0].operations).To(Equal(expected))
					Expect(xpathUtils.applyCalls[0].withDefault).To(Equal(options.WithDefault))
				},
				Entry("normally", types.ConfigurationOptions{}, false),
				Entry("with defaults", types.ConfigurationOptions{WithDefault: true}, true),
			)

			It("force=false applies the native desired params present in the query that differ", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"1"}, "LINK_TYPE_P1": {"2"}}}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "2"}, nil)
				// NUM_OF_PF differs (1 != 2) and is applied; LINK_TYPE_P1 already matches and is skipped.
				mockNVConfigUtils.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress), map[string]string{"NUM_OF_PF": "2"}, false, false).
					Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("falls back to the public batch method when the context-aware extension is absent", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.Enabled = false
				manager.nvConfigUtils = legacyNVConfigUtils{NVConfigUtils: mockNVConfigUtils}
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"1"}}}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatch", portSpec(pciAddress),
					map[string]string{"NUM_OF_PF": "2"}, false, false).Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})

				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("force=true applies all native desired params in a single --force batch", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "2"}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress),
					map[string]string{"NUM_OF_PF": "2", "LINK_TYPE_P1": "2", "LINK_TYPE_P2": "2"}, false, true).Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("preserves per-PF native apply for non-Spectrum-X devices", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.Enabled = false
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{{PCI: pciAddress}, {PCI: pciAddress2}}
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress2), []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress), map[string]string{"NUM_OF_PF": "2"}, false, true).
					Return(types.ApplyStatusSuccess, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress2), map[string]string{"NUM_OF_PF": "2"}, false, true).
					Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
				mockNVConfigUtils.AssertCalled(GinkgoT(), "SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress2), map[string]string{"NUM_OF_PF": "2"}, false, true)
			})

			It("propagates WithDefault=true to the native batch", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"1"}}}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress), map[string]string{"NUM_OF_PF": "2"}, true, false).
					Return(types.ApplyStatusSuccess, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{WithDefault: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("returns NothingToDo when WithDefault=true but DMS reports no reset is required", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: map[string][]string{"NUM_OF_PF": {"2"}}}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress), map[string]string{"NUM_OF_PF": "2"}, true, false).
					Return(types.ApplyStatusNothingToDo, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{WithDefault: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
				Expect(result.RebootRequired).To(BeFalse())
			})

			It("returns NothingToDo when Force and WithDefault are true but DMS reports no reset is required", func() {
				mockNVConfigUtils.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(types.NvConfigQuery{}, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, mock.Anything).
					Return(map[string]string{"NUM_OF_PF": "2"}, nil)
				mockNVConfigUtils.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress), map[string]string{"NUM_OF_PF": "2"}, true, true).
					Return(types.ApplyStatusNothingToDo, nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{Force: true, WithDefault: true})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
				Expect(result.RebootRequired).To(BeFalse())
			})

			It("returns NothingToDo when the native desired params already match", func() {
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
				mockNV.On("ValidateSystemConf", ctx, portSpec(pciAddress), "conf3", 0).
					Return(mismatchSystemConf("BOARD_CONFIGURATION_MODE")...)
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
					Return(map[string]string{}, nil)

				configUpdate, reboot, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(configUpdate).To(BeTrue())
				Expect(reboot).To(BeTrue())
			})

			It("fails closed when validate_system_conf reports a mismatch but no MISMATCH rows are parsed", func() {
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				// Result says NOT match, but no recognized MISMATCH rows — must not look like a match.
				mockNV.On("ValidateSystemConf", ctx, portSpec(pciAddress), "conf3", 0).
					Return(false, []string(nil), nil)

				_, _, _, err := manager.ValidateDeviceNvSpec(ctx, device)
				Expect(err).To(HaveOccurred())
			})

			It("requires nothing when both regular config and system_conf match", func() {
				mockNV.On("ValidateSystemConf", ctx, portSpec(pciAddress), "conf3", 0).
					Return(okSystemConf()...)
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
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
				mockNV.On("ValidateSystemConf", ctx, portSpec(pciAddress), "conf3", 0).
					Return(mismatchSystemConf("NUM_OF_PF")...)
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(portConfig, nil)
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
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				mockConfigValidation.On("ValidateResetToDefault", nvConfig).Return(false, false, nil)
				mockNV.AssertNotCalled(GinkgoT(), "ValidateSystemConf", mock.Anything, mock.Anything, mock.Anything, mock.Anything)

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
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				mockNV.On("ValidateSystemConf", ctx, portSpec(pciAddress), "conf3", 0).
					Return(mismatchSystemConf("BOARD_CONFIGURATION_MODE")...)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
					Return(map[string]string{}, nil)
				mockNV.On("SetSystemConf", ctx, portSpec(pciAddress), "conf3", 0, false).Return(nil)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("passes --force to set_system_conf when Force is set", func() {
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				mockNV.On("ValidateSystemConf", ctx, portSpec(pciAddress), "conf3", 0).
					Return(mismatchSystemConf("BOARD_CONFIGURATION_MODE")...)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, nvConfig).
					Return(map[string]string{}, nil)
				mockNV.On("SetSystemConf", ctx, portSpec(pciAddress), "conf3", 0, true).Return(nil)

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
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(portConfig, nil)
				mockNV.On("ValidateSystemConf", ctx, portSpec(pciAddress), "conf3", 0).
					Return(mismatchSystemConf("BOARD_CONFIGURATION_MODE")...)
				mockConfigValidation.On("ConstructNvParamMapFromTemplate", device, portConfig).
					Return(map[string]string{"param1": "value2"}, nil)
				setSystemConfCall := mockNV.On("SetSystemConf", ctx, portSpec(pciAddress), "conf3", 0, false).Return(nil)
				// set_system_conf is the baseline and must be staged before the override batch.
				mockNV.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress), map[string]string{"param1": "value2"}, false, false).
					Return(types.ApplyStatusSuccess, nil).NotBefore(setSystemConfCall)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})

			It("resets and never touches system_conf when ResetToDefault is set", func() {
				// Guards against the reset/set_system_conf reboot loop: reset runs, set_system_conf does not.
				device.Spec.Configuration.ResetToDefault = true
				mockNV.On("QueryNvConfig", ctx, portSpec(pciAddress), []string(nil)).Return(nvConfig, nil)
				mockNV.On("ResetNvConfig", portSpec(pciAddress)).Return(nil)
				mockNV.AssertNotCalled(GinkgoT(), "SetSystemConf", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)

				result, err := manager.ApplyNVConfiguration(ctx, device, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RebootRequired).To(BeTrue())
			})
		})

		// Full-flow suite: real configValidation with mocked ConfigurationUtils and NVConfigUtils
		// so the whole chain — system_conf validate, rawNvConfig > template merge inside
		// ConstructNvParamMapFromTemplate, coverage check, and apply — runs end to end. Each case drives BOTH
		// ValidateDeviceNvSpec and ApplyNVConfiguration and asserts the concrete SetSystemConf /
		// SetNvConfigParametersBatch calls. ConstructNvParamMapFromTemplate always emits SRIOV_EN=0 /
		// NUM_OF_VFS=0 for an empty template, so every fixture stages those to keep them out of the assertions.
		Describe("Network Bay system_conf full flow (validate + apply)", func() {
			var (
				fullFlowHostUtils mocks.ConfigurationUtils
				fullFlowNV        *nvconfigmocks.NVConfigUtils
				fullFlowManager   configurationManager
				fullFlowCtx       context.Context
				fullFlowDevice    *v1alpha1.NicDevice
			)

			BeforeEach(func() {
				fullFlowHostUtils = mocks.ConfigurationUtils{}
				fullFlowNV = nvconfigmocks.NewNVConfigUtils(GinkgoT())
				fullFlowManager = configurationManager{
					configurationUtils: &fullFlowHostUtils,
					configValidation:   newConfigValidation(&fullFlowHostUtils, nil),
					nvConfigUtils:      fullFlowNV,
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

			// 1) system_conf valid, no overrides → nothing to do, no set_system_conf.
			It("1: valid system_conf with no overrides converges without applying anything", func() {
				staged := sriovStaged(nil)
				fullFlowNV.On("QueryNvConfig", fullFlowCtx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: staged, CurrentConfig: staged}, nil)
				fullFlowNV.On("ValidateSystemConf", fullFlowCtx, portSpec(pciAddress), "conf3", 0).Return(okSystemConf()...)

				updateNeeded, rebootNeeded, _, err := fullFlowManager.ValidateDeviceNvSpec(fullFlowCtx, fullFlowDevice)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeFalse())

				result, err := fullFlowManager.ApplyNVConfiguration(fullFlowCtx, fullFlowDevice, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
				Expect(result.RebootRequired).To(BeFalse())
				fullFlowNV.AssertNotCalled(GinkgoT(), "SetSystemConf", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
				fullFlowNV.AssertNotCalled(GinkgoT(), "SetNvConfigParametersBatchWithContext", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			})

			// 2) system_conf mismatch, no overrides → set_system_conf re-applied.
			It("2: system_conf mismatch with no overrides re-applies set_system_conf", func() {
				staged := sriovStaged(nil)
				fullFlowNV.On("QueryNvConfig", fullFlowCtx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: staged, CurrentConfig: staged}, nil)
				fullFlowNV.On("ValidateSystemConf", fullFlowCtx, portSpec(pciAddress), "conf3", 0).
					Return(mismatchSystemConf("BOARD_CONFIGURATION_MODE")...)
				fullFlowNV.On("SetSystemConf", fullFlowCtx, portSpec(pciAddress), "conf3", 0, false).Return(nil)

				updateNeeded, rebootNeeded, _, err := fullFlowManager.ValidateDeviceNvSpec(fullFlowCtx, fullFlowDevice)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())

				result, err := fullFlowManager.ApplyNVConfiguration(fullFlowCtx, fullFlowDevice, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
				fullFlowNV.AssertNotCalled(GinkgoT(), "SetNvConfigParametersBatchWithContext", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			})

			// 3) system_conf mismatch, rawNvConfig covers it and the value is already staged → converged.
			It("3: system_conf mismatch fully covered by an already-applied rawNvConfig override converges", func() {
				fullFlowDevice.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "NUM_OF_PF", Value: "8"}}
				staged := sriovStaged(map[string][]string{"NUM_OF_PF": {"8"}})
				fullFlowNV.On("QueryNvConfig", fullFlowCtx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: staged, CurrentConfig: staged}, nil)
				fullFlowNV.On("ValidateSystemConf", fullFlowCtx, portSpec(pciAddress), "conf3", 0).
					Return(mismatchSystemConf("NUM_OF_PF")...)

				updateNeeded, rebootNeeded, _, err := fullFlowManager.ValidateDeviceNvSpec(fullFlowCtx, fullFlowDevice)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeFalse())
				Expect(rebootNeeded).To(BeFalse())

				result, err := fullFlowManager.ApplyNVConfiguration(fullFlowCtx, fullFlowDevice, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusNothingToDo))
				fullFlowNV.AssertNotCalled(GinkgoT(), "SetSystemConf", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
				fullFlowNV.AssertNotCalled(GinkgoT(), "SetNvConfigParametersBatchWithContext", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			})

			// 4) system_conf mismatch covered by rawNvConfig by name, but the override value is not yet staged →
			//    no set_system_conf, but the rawNvConfig param is applied.
			It("4: rawNvConfig covers the mismatch by name but differs in value — applies the raw param, not set_system_conf", func() {
				fullFlowDevice.Spec.Configuration.Template.RawNvConfig = []v1alpha1.NvConfigParam{{Name: "NUM_OF_PF", Value: "8"}}
				staged := sriovStaged(map[string][]string{"NUM_OF_PF": {"1"}})
				fullFlowNV.On("QueryNvConfig", fullFlowCtx, portSpec(pciAddress), []string(nil)).Return(
					types.NvConfigQuery{NextBootConfig: staged, CurrentConfig: staged}, nil)
				fullFlowNV.On("ValidateSystemConf", fullFlowCtx, portSpec(pciAddress), "conf3", 0).
					Return(mismatchSystemConf("NUM_OF_PF")...)
				fullFlowNV.On("SetNvConfigParametersBatchWithContext", mock.Anything, portSpec(pciAddress), map[string]string{"NUM_OF_PF": "8"}, false, false).
					Return(types.ApplyStatusSuccess, nil)

				updateNeeded, rebootNeeded, _, err := fullFlowManager.ValidateDeviceNvSpec(fullFlowCtx, fullFlowDevice)
				Expect(err).NotTo(HaveOccurred())
				Expect(updateNeeded).To(BeTrue())
				Expect(rebootNeeded).To(BeTrue())

				result, err := fullFlowManager.ApplyNVConfiguration(fullFlowCtx, fullFlowDevice, &types.ConfigurationOptions{})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.Status).To(Equal(types.ApplyStatusSuccess))
				Expect(result.RebootRequired).To(BeTrue())
				fullFlowNV.AssertNotCalled(GinkgoT(), "SetSystemConf", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			})

		})
	})
})
