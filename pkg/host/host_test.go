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

package host

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/host/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/jaypipes/pcidb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("HostManager", func() {
	var (
		mockHostUtils mocks.HostUtils
		manager       hostManager
	)

	BeforeEach(func() {
		mockHostUtils = mocks.HostUtils{}
		manager = hostManager{hostUtils: &mockHostUtils}
	})

	Describe("DiscoverNicDevices", func() {
		Context("when GetPCIDevices fails", func() {
			It("should return nil and log an error", func() {
				mockHostUtils.On("GetPCIDevices").
					Return(nil, errors.New("get PCI devices error"))

				devices, err := manager.DiscoverNicDevices()
				Expect(err).To(HaveOccurred())
				Expect(devices).To(BeNil())
				mockHostUtils.AssertExpectations(GinkgoT())
			})
		})

		Context("when working with non-mellanox devices", func() {
			It("should skip non-Mellanox devices", func() {
				mockHostUtils.On("GetPCIDevices").Return([]*pci.Device{
					{
						Address: "0000:00:00.0",
						Vendor:  &pcidb.Vendor{ID: "non-mellanox-vendor"},
						Product: &pcidb.Product{ID: "test-id", Name: "Non-Mellanox Device"},
						Class:   &pcidb.Class{ID: "02"},
					},
				}, nil)

				devices, err := manager.DiscoverNicDevices()
				Expect(err).NotTo(HaveOccurred())
				Expect(devices).To(BeEmpty())
				mockHostUtils.AssertExpectations(GinkgoT())
			})
		})

		Context("when working with non-network devices", func() {
			It("should skip non-Mellanox devices", func() {
				mockHostUtils.On("GetPCIDevices").Return([]*pci.Device{
					{
						Address: "0000:00:00.0",
						Vendor:  &pcidb.Vendor{ID: "non-mellanox-vendor"},
						Product: &pcidb.Product{ID: "test-id", Name: "Non-Mellanox Device"},
						Class:   &pcidb.Class{ID: "non-network-class"},
					},
				}, nil)

				devices, err := manager.DiscoverNicDevices()
				Expect(err).NotTo(HaveOccurred())
				Expect(devices).To(BeEmpty())
				mockHostUtils.AssertExpectations(GinkgoT())
			})
		})

		Context("when discovering a single mellanox device", func() {
			BeforeEach(func() {
				mockHostUtils.On("GetPCIDevices").Return([]*pci.Device{
					{
						Address: "0000:00:00.0",
						Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
						Product: &pcidb.Product{ID: "test-id", Name: "Mellanox Device"},
						Class:   &pcidb.Class{ID: "02"},
					},
				}, nil)
			})

			It("should log and skip devices if IsSriovVF returns true", func() {
				mockHostUtils.On("IsSriovVF", "0000:00:00.0").Return(true)

				devices, err := manager.DiscoverNicDevices()
				Expect(err).NotTo(HaveOccurred())
				Expect(devices).To(BeEmpty())
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should fails if GetPartAndSerialNumber fails", func() {
				mockHostUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
				mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").
					Return("", "", errors.New("serial number error"))

				devices, err := manager.DiscoverNicDevices()
				Expect(err).To(HaveOccurred())
				Expect(devices).To(BeNil())
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should log and skip devices if GetFirmwareVersionAndPSID fails", func() {
				mockHostUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
				mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").
					Return("part-number", "serial-number", nil)
				mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
					Return("", "", errors.New("firmware error"))

				devices, err := manager.DiscoverNicDevices()
				Expect(err).To(HaveOccurred())
				Expect(devices).To(BeNil())
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should discover and return devices successfully", func() {
				mockHostUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
				mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").
					Return("part-number", "serial-number", nil)
				mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
					Return("fw-version", "psid", nil)
				mockHostUtils.On("GetInterfaceName", "0000:00:00.0").
					Return("eth0")
				mockHostUtils.On("GetRDMADeviceName", "0000:00:00.0").
					Return("mlx5_0")

				devices, err := manager.DiscoverNicDevices()
				Expect(err).NotTo(HaveOccurred())

				expectedDeviceStatus := v1alpha1.NicDeviceStatus{
					Type:            "test-id",
					SerialNumber:    "serial-number",
					PartNumber:      "part-number",
					PSID:            "psid",
					FirmwareVersion: "fw-version",
					Ports: []v1alpha1.NicDevicePortSpec{
						{
							PCI:              "0000:00:00.0",
							NetworkInterface: "eth0",
							RdmaInterface:    "mlx5_0",
						},
					},
				}

				Expect(devices).To(HaveKey("serial-number"))
				Expect(devices["serial-number"]).To(Equal(expectedDeviceStatus))

				mockHostUtils.AssertExpectations(GinkgoT())
			})
		})
	})

	Context("when discovering several mellanox device", func() {
		BeforeEach(func() {
			mockHostUtils.On("GetPCIDevices").Return([]*pci.Device{
				{
					Address: "0000:00:00.0",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: "test-id", Name: "Mellanox Device"},
					Class:   &pcidb.Class{ID: "02"},
				},
				{
					Address: "0000:00:00.1",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: "test-id", Name: "Mellanox Device"},
					Class:   &pcidb.Class{ID: "02"},
				},
			}, nil)
		})

		It("should log and skip only a faulty device if IsSriovVF returns true", func() {
			mockHostUtils.On("IsSriovVF", "0000:00:00.0").
				Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").
				Return("part-number", "serial-number", nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
				Return("fw-version", "psid", nil)
			mockHostUtils.On("GetInterfaceName", "0000:00:00.0").
				Return("eth0")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.0").
				Return("mlx5_0")

			mockHostUtils.On("IsSriovVF", "0000:00:00.1").Return(true)

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			expectedDeviceStatus := v1alpha1.NicDeviceStatus{
				Type:            "test-id",
				SerialNumber:    "serial-number",
				PartNumber:      "part-number",
				PSID:            "psid",
				FirmwareVersion: "fw-version",
				Ports: []v1alpha1.NicDevicePortSpec{
					{
						PCI:              "0000:00:00.0",
						NetworkInterface: "eth0",
						RdmaInterface:    "mlx5_0",
					},
				},
			}

			Expect(devices).To(HaveKey("serial-number"))
			Expect(devices["serial-number"]).To(Equal(expectedDeviceStatus))
			mockHostUtils.AssertExpectations(GinkgoT())
		})

		It("should log and skip only a faulty device if GetPartAndSerialNumber fails", func() {
			mockHostUtils.On("IsSriovVF", "0000:00:00.0").
				Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").
				Return("part-number", "serial-number", nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
				Return("fw-version", "psid", nil)
			mockHostUtils.On("GetInterfaceName", "0000:00:00.0").
				Return("eth0")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.0").
				Return("mlx5_0")

			mockHostUtils.On("IsSriovVF", "0000:00:00.1").
				Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.1").
				Return("", "", errors.New("serial number error"))

			devices, err := manager.DiscoverNicDevices()
			Expect(err).To(HaveOccurred())
			Expect(devices).To(BeNil())
			mockHostUtils.AssertExpectations(GinkgoT())
		})

		It("should log and skip only a faulty device if GetFirmwareVersionAndPSID fails", func() {
			mockHostUtils.On("IsSriovVF", "0000:00:00.0").
				Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").
				Return("part-number", "serial-number", nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
				Return("fw-version", "psid", nil)
			mockHostUtils.On("GetInterfaceName", "0000:00:00.0").
				Return("eth0")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.0").
				Return("mlx5_0")

			mockHostUtils.On("IsSriovVF", "0000:00:00.1").
				Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.1").
				Return("part-number", "serial-number-2", nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.1").
				Return("", "", errors.New("firmware error"))

			devices, err := manager.DiscoverNicDevices()
			Expect(err).To(HaveOccurred())
			Expect(devices).To(BeNil())
			mockHostUtils.AssertExpectations(GinkgoT())
		})

		It("should discover and return devices successfully", func() {
			mockHostUtils.On("IsSriovVF", "0000:00:00.0").
				Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").
				Return("part-number", "serial-number", nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
				Return("fw-version", "psid", nil)
			mockHostUtils.On("GetInterfaceName", "0000:00:00.0").
				Return("eth0")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.0").
				Return("mlx5_0")

			mockHostUtils.On("IsSriovVF", "0000:00:00.1").
				Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.1").
				Return("part-number", "serial-number-2", nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.1").
				Return("fw-version", "psid", nil)
			mockHostUtils.On("GetInterfaceName", "0000:00:00.1").
				Return("eth1")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.1").
				Return("mlx5_1")

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())

			expectedDeviceStatus1 := v1alpha1.NicDeviceStatus{
				Type:            "test-id",
				SerialNumber:    "serial-number",
				PartNumber:      "part-number",
				PSID:            "psid",
				FirmwareVersion: "fw-version",
				Ports: []v1alpha1.NicDevicePortSpec{
					{
						PCI:              "0000:00:00.0",
						NetworkInterface: "eth0",
						RdmaInterface:    "mlx5_0",
					},
				},
			}

			expectedDeviceStatus2 := v1alpha1.NicDeviceStatus{
				Type:            "test-id",
				SerialNumber:    "serial-number-2",
				PartNumber:      "part-number",
				PSID:            "psid",
				FirmwareVersion: "fw-version",
				Ports: []v1alpha1.NicDevicePortSpec{
					{
						PCI:              "0000:00:00.1",
						NetworkInterface: "eth1",
						RdmaInterface:    "mlx5_1",
					},
				},
			}

			Expect(devices).To(HaveKey("serial-number"))
			Expect(devices["serial-number"]).To(Equal(expectedDeviceStatus1))
			Expect(devices["serial-number-2"]).To(Equal(expectedDeviceStatus2))

			mockHostUtils.AssertExpectations(GinkgoT())
		})
	})

	Context("when discovering two ports of a single NIC", func() {
		It("should combine them into a single device with two ports", func() {
			sameSerialNumber := "serial-number"

			mockHostUtils.On("GetPCIDevices").Return([]*pci.Device{
				{
					Address: "0000:00:00.0",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: "test-id", Name: "Mellanox Device"},
					Class:   &pcidb.Class{ID: "02"},
				},
				{
					Address: "0000:00:00.1",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: "test-id", Name: "Mellanox Device"},
					Class:   &pcidb.Class{ID: "02"},
				},
			}, nil)

			mockHostUtils.On("IsSriovVF", "0000:00:00.0").
				Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").
				Return("part-number", sameSerialNumber, nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
				Return("fw-version", "psid", nil)
			mockHostUtils.On("GetInterfaceName", "0000:00:00.0").
				Return("eth0")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.0").
				Return("mlx5_0")

			mockHostUtils.On("IsSriovVF", "0000:00:00.1").
				Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.1").
				Return("part-number", sameSerialNumber, nil)
			mockHostUtils.AssertNotCalled(GinkgoT(), "GetFirmwareVersionAndPSID", "0000:00:00.1")
			mockHostUtils.On("GetInterfaceName", "0000:00:00.1").
				Return("eth1")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.1").
				Return("mlx5_1")

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			expectedDeviceStatus := v1alpha1.NicDeviceStatus{
				Type:            "test-id",
				SerialNumber:    sameSerialNumber,
				PartNumber:      "part-number",
				PSID:            "psid",
				FirmwareVersion: "fw-version",
				Ports: []v1alpha1.NicDevicePortSpec{
					{
						PCI:              "0000:00:00.0",
						NetworkInterface: "eth0",
						RdmaInterface:    "mlx5_0",
					},
					{
						PCI:              "0000:00:00.1",
						NetworkInterface: "eth1",
						RdmaInterface:    "mlx5_1",
					},
				},
			}

			Expect(devices).To(HaveKey("serial-number"))
			Expect(devices[sameSerialNumber]).To(Equal(expectedDeviceStatus))

			mockHostUtils.AssertExpectations(GinkgoT())
		})
	})
	Describe("hostManager.ValidateDeviceNvSpec", func() {
		var (
			mockHostUtils        mocks.HostUtils
			mockConfigValidation mocks.ConfigValidation
			manager              hostManager
			ctx                  context.Context
			device               *v1alpha1.NicDevice
			pciAddress           string
		)

		BeforeEach(func() {
			mockHostUtils = mocks.HostUtils{}
			mockConfigValidation = mocks.ConfigValidation{}
			manager = hostManager{
				hostUtils:        &mockHostUtils,
				configValidation: &mockConfigValidation,
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
	Describe("hostManager.ApplyDeviceNvSpec", func() {
		var (
			mockHostUtils        mocks.HostUtils
			mockConfigValidation mocks.ConfigValidation
			manager              hostManager
			ctx                  context.Context
			device               *v1alpha1.NicDevice
			pciAddress           string
		)

		BeforeEach(func() {
			mockHostUtils = mocks.HostUtils{}
			mockConfigValidation = mocks.ConfigValidation{}
			manager = hostManager{
				hostUtils:        &mockHostUtils,
				configValidation: &mockConfigValidation,
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
					mockHostUtils.On("ResetNvConfig", pciAddress).Return(nil)
					mockHostUtils.
						On("SetNvConfigParameter", pciAddress, consts.AdvancedPCISettingsParam, consts.NvParamTrue).
						Return(nil)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeTrue())
					Expect(err).To(BeNil())

					mockHostUtils.AssertExpectations(GinkgoT())
				})

				It("should return error if ResetNvConfig fails", func() {
					resetErr := errors.New("failed to reset nv config")
					mockHostUtils.On("ResetNvConfig", pciAddress).Return(resetErr)

					reboot, err := manager.ApplyDeviceNvSpec(ctx, device)
					Expect(reboot).To(BeFalse())
					Expect(err).To(MatchError(resetErr))

					mockHostUtils.AssertExpectations(GinkgoT())
				})

				It("should return error if SetNvConfigParameter fails", func() {
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

					It("should return error if ResetNicFirmware fails", func() {
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
						Expect(reboot).To(BeFalse())
						Expect(err).To(MatchError(resetFirmwareErr))

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
