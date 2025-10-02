/*
2025 NVIDIA CORPORATION & AFFILIATES
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

package devicediscovery

import (
	"errors"

	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/jaypipes/pcidb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/devicediscovery/mocks"
	nvmmocks "github.com/Mellanox/nic-configuration-operator/pkg/nvconfig/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

var _ = Describe("DeviceDiscovery", func() {
	var (
		mockUtils mocks.DeviceDiscoveryUtils
		manager   DeviceDiscovery
	)

	BeforeEach(func() {
		mockUtils = mocks.DeviceDiscoveryUtils{}
		manager = deviceDiscovery{utils: &mockUtils, nodeName: "test-node"}
	})

	Describe("NewDeviceDiscovery", func() {
		It("should create a DeviceDiscovery with nodeName set correctly", func() {
			nodeName := "test-node"
			manager := NewDeviceDiscovery(nodeName, nvmmocks.NewNVConfigUtils(GinkgoT()))

			// Cast to the concrete type to access the nodeName field
			concreteManager, ok := manager.(*deviceDiscovery)
			Expect(ok).To(BeTrue())
			Expect(concreteManager.nodeName).To(Equal(nodeName))
		})

		It("should not allow empty nodeName", func() {
			nodeName := ""
			manager := NewDeviceDiscovery(nodeName, nvmmocks.NewNVConfigUtils(GinkgoT()))

			// Cast to the concrete type to access the nodeName field
			concreteManager, ok := manager.(*deviceDiscovery)
			Expect(ok).To(BeTrue())
			Expect(concreteManager.nodeName).To(BeEmpty())
		})
	})

	Describe("DiscoverNicDevices", func() {
		Context("when GetPCIDevices fails", func() {
			It("should return nil and log an error", func() {
				mockUtils.On("GetPCIDevices").
					Return(nil, errors.New("get PCI devices error"))

				devices, err := manager.DiscoverNicDevices()
				Expect(err).To(HaveOccurred())
				Expect(devices).To(BeNil())
				mockUtils.AssertExpectations(GinkgoT())
			})
		})

		Context("when working with non-mellanox devices", func() {
			It("should skip non-Mellanox devices", func() {
				mockUtils.On("GetPCIDevices").Return([]*pci.Device{
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
				mockUtils.AssertExpectations(GinkgoT())
			})
		})

		Context("when working with non-network devices", func() {
			It("should skip non-Mellanox devices", func() {
				mockUtils.On("GetPCIDevices").Return([]*pci.Device{
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
				mockUtils.AssertExpectations(GinkgoT())
			})
		})

		Context("when discovering a single mellanox device", func() {
			BeforeEach(func() {
				mockUtils.On("GetPCIDevices").Return([]*pci.Device{
					{
						Address: "0000:00:00.0",
						Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
						Product: &pcidb.Product{ID: "test-id", Name: "Mellanox Device"},
						Class:   &pcidb.Class{ID: "02"},
					},
				}, nil)
			})

			It("should log and skip devices if IsSriovVF returns true", func() {
				mockUtils.On("IsSriovVF", "0000:00:00.0").Return(true)

				devices, err := manager.DiscoverNicDevices()
				Expect(err).NotTo(HaveOccurred())
				Expect(devices).To(BeEmpty())
				mockUtils.AssertExpectations(GinkgoT())
			})

			It("should not fail if GetPartAndSerialNumber fails", func() {
				mockUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
				mockUtils.On("GetVPD", "0000:00:00.0").
					Return(nil, errors.New("serial number error"))

				devices, err := manager.DiscoverNicDevices()
				Expect(err).NotTo(HaveOccurred())
				Expect(devices).To(BeEmpty())
				mockUtils.AssertExpectations(GinkgoT())
			})

			It("should log and skip devices if GetFirmwareVersionAndPSID fails", func() {
				mockUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
				mockUtils.On("GetVPD", "0000:00:00.0").
					Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number", ModelName: ""}, nil)
				mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
					Return("", "", errors.New("firmware error"))

				devices, err := manager.DiscoverNicDevices()
				Expect(err).To(HaveOccurred())
				Expect(devices).To(BeNil())
				mockUtils.AssertExpectations(GinkgoT())
			})

			It("should discover and return devices successfully with nodeName", func() {
				mockUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
				mockUtils.On("GetVPD", "0000:00:00.0").
					Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number", ModelName: ""}, nil)
				mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
					Return("fw-version", "psid", nil)
				mockUtils.On("GetInterfaceName", "0000:00:00.0").
					Return("eth0")
				mockUtils.On("GetRDMADeviceName", "0000:00:00.0").
					Return("mlx5_0")

				devices, err := manager.DiscoverNicDevices()
				Expect(err).NotTo(HaveOccurred())

				expectedDeviceStatus := v1alpha1.NicDeviceStatus{
					Node:            "test-node",
					Type:            "test-id",
					SerialNumber:    "serial-number",
					PartNumber:      "part-number",
					ModelName:       "",
					PSID:            "psid",
					FirmwareVersion: "fw-version",
					SuperNIC:        false,
					DPU:             false,
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
				// Verify that the returned device has the correct nodeName
				Expect(devices["serial-number"].Node).To(Equal("test-node"))

				mockUtils.AssertExpectations(GinkgoT())
			})
		})
	})

	Context("when discovering several mellanox device", func() {
		BeforeEach(func() {
			mockUtils.On("GetPCIDevices").Return([]*pci.Device{
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
			mockUtils.On("IsSriovVF", "0000:00:00.0").
				Return(false)
			mockUtils.On("GetVPD", "0000:00:00.0").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number", ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
				Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0000:00:00.0").
				Return("eth0")
			mockUtils.On("GetRDMADeviceName", "0000:00:00.0").
				Return("mlx5_0")

			mockUtils.On("IsSriovVF", "0000:00:00.1").Return(true)

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			expectedDeviceStatus := v1alpha1.NicDeviceStatus{
				Node:            "test-node",
				Type:            "test-id",
				SerialNumber:    "serial-number",
				PartNumber:      "part-number",
				ModelName:       "",
				PSID:            "psid",
				FirmwareVersion: "fw-version",
				SuperNIC:        false,
				DPU:             false,
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
			mockUtils.AssertExpectations(GinkgoT())
		})

		It("should log and skip only a faulty device if GetVPD fails", func() {
			mockUtils.On("IsSriovVF", "0000:00:00.0").
				Return(false)
			mockUtils.On("GetVPD", "0000:00:00.0").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number", ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
				Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0000:00:00.0").
				Return("eth0")
			mockUtils.On("GetRDMADeviceName", "0000:00:00.0").
				Return("mlx5_0")

			mockUtils.On("IsSriovVF", "0000:00:00.1").
				Return(false)
			mockUtils.On("GetVPD", "0000:00:00.1").
				Return(nil, errors.New("serial number error"))

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(1))
			mockUtils.AssertExpectations(GinkgoT())
		})

		It("should log and skip only a faulty device if GetFirmwareVersionAndPSID fails", func() {
			mockUtils.On("IsSriovVF", "0000:00:00.0").
				Return(false)
			mockUtils.On("GetVPD", "0000:00:00.0").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number", ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
				Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0000:00:00.0").
				Return("eth0")
			mockUtils.On("GetRDMADeviceName", "0000:00:00.0").
				Return("mlx5_0")

			mockUtils.On("IsSriovVF", "0000:00:00.1").
				Return(false)
			mockUtils.On("GetVPD", "0000:00:00.1").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number-2", ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.1").
				Return("", "", errors.New("firmware error"))

			devices, err := manager.DiscoverNicDevices()
			Expect(err).To(HaveOccurred())
			Expect(devices).To(BeNil())
			mockUtils.AssertExpectations(GinkgoT())
		})

		It("should discover and return devices successfully", func() {
			mockUtils.On("IsSriovVF", "0000:00:00.0").
				Return(false)
			mockUtils.On("GetVPD", "0000:00:00.0").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number", ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
				Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0000:00:00.0").
				Return("eth0")
			mockUtils.On("GetRDMADeviceName", "0000:00:00.0").
				Return("mlx5_0")

			mockUtils.On("IsSriovVF", "0000:00:00.1").
				Return(false)
			mockUtils.On("GetVPD", "0000:00:00.1").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number-2", ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.1").
				Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0000:00:00.1").
				Return("eth1")
			mockUtils.On("GetRDMADeviceName", "0000:00:00.1").
				Return("mlx5_1")

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())

			expectedDeviceStatus1 := v1alpha1.NicDeviceStatus{
				Node:            "test-node",
				Type:            "test-id",
				SerialNumber:    "serial-number",
				PartNumber:      "part-number",
				ModelName:       "",
				PSID:            "psid",
				FirmwareVersion: "fw-version",
				SuperNIC:        false,
				DPU:             false,
				Ports: []v1alpha1.NicDevicePortSpec{
					{
						PCI:              "0000:00:00.0",
						NetworkInterface: "eth0",
						RdmaInterface:    "mlx5_0",
					},
				},
			}

			expectedDeviceStatus2 := v1alpha1.NicDeviceStatus{
				Node:            "test-node",
				Type:            "test-id",
				SerialNumber:    "serial-number-2",
				PartNumber:      "part-number",
				ModelName:       "",
				PSID:            "psid",
				FirmwareVersion: "fw-version",
				SuperNIC:        false,
				DPU:             false,
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

			mockUtils.AssertExpectations(GinkgoT())
		})
	})

	Context("when discovering two ports of a single NIC", func() {
		It("should combine them into a single device with two ports", func() {
			sameSerialNumber := "serial-number"

			mockUtils.On("GetPCIDevices").Return([]*pci.Device{
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

			mockUtils.On("IsSriovVF", "0000:00:00.0").
				Return(false)
			mockUtils.On("GetVPD", "0000:00:00.0").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: sameSerialNumber, ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
				Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0000:00:00.0").
				Return("eth0")
			mockUtils.On("GetRDMADeviceName", "0000:00:00.0").
				Return("mlx5_0")

			mockUtils.On("IsSriovVF", "0000:00:00.1").
				Return(false)
			mockUtils.On("GetVPD", "0000:00:00.1").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: sameSerialNumber, ModelName: ""}, nil)
			mockUtils.AssertNotCalled(GinkgoT(), "GetFirmwareVersionAndPSID", "0000:00:00.1")
			mockUtils.On("GetInterfaceName", "0000:00:00.1").
				Return("eth1")
			mockUtils.On("GetRDMADeviceName", "0000:00:00.1").
				Return("mlx5_1")

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			expectedDeviceStatus := v1alpha1.NicDeviceStatus{
				Node:            "test-node",
				Type:            "test-id",
				SerialNumber:    sameSerialNumber,
				PartNumber:      "part-number",
				ModelName:       "",
				PSID:            "psid",
				FirmwareVersion: "fw-version",
				SuperNIC:        false,
				DPU:             false,
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

			mockUtils.AssertExpectations(GinkgoT())
		})
	})

	Context("when parsing model name and SuperNIC flag", func() {
		It("should shorten model name and set SuperNIC", func() {
			mockUtils.On("GetPCIDevices").Return([]*pci.Device{
				{
					Address: "0000:00:00.0",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: "test-id", Name: "Mellanox Device"},
					Class:   &pcidb.Class{ID: "02"},
				},
			}, nil)
			mockUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
			mockUtils.On("GetVPD", "0000:00:00.0").Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number", ModelName: "ConnectX-8 SuperNIC"}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0000:00:00.0").Return("eth0")
			mockUtils.On("GetRDMADeviceName", "0000:00:00.0").Return("mlx5_0")

			manager := deviceDiscovery{utils: &mockUtils, nvConfigUtils: nvmmocks.NewNVConfigUtils(GinkgoT()), nodeName: "test-node"}

			devicesBySerial, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devicesBySerial).To(HaveKey("serial-number"))
			discoveredDeviceStatus := devicesBySerial["serial-number"]
			Expect(discoveredDeviceStatus.ModelName).To(Equal("ConnectX-8"))
			Expect(discoveredDeviceStatus.SuperNIC).To(BeTrue())
		})
	})

	Context("when detecting DPU mode on BlueField", func() {
		It("should set DPU=true when BF3_OPERATION_MODE is DPU", func() {
			mockUtils.On("GetPCIDevices").Return([]*pci.Device{
				{
					Address: "0000:00:00.0",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: consts.BlueField3DeviceID, Name: "BlueField-3"},
					Class:   &pcidb.Class{ID: "02"},
				},
			}, nil)
			mockUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
			mockUtils.On("GetVPD", "0000:00:00.0").Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number", ModelName: "BlueField-3"}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0000:00:00.0").Return("eth0")
			mockUtils.On("GetRDMADeviceName", "0000:00:00.0").Return("mlx5_0")

			nvConfigUtilsMock := nvmmocks.NewNVConfigUtils(GinkgoT())
			nvConfigUtilsMock.On("QueryNvConfig", mock.Anything, "0000:00:00.0", consts.BF3OperationModeParam).Return(types.NvConfigQuery{
				CurrentConfig:  map[string][]string{consts.BF3OperationModeParam: {consts.NvParamBF3DpuMode}},
				NextBootConfig: map[string][]string{},
				DefaultConfig:  map[string][]string{},
			}, nil)

			manager := deviceDiscovery{utils: &mockUtils, nvConfigUtils: nvConfigUtilsMock, nodeName: "test-node"}

			devicesBySerial, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			discoveredDeviceStatus := devicesBySerial["serial-number"]
			Expect(discoveredDeviceStatus.DPU).To(BeTrue())
		})
	})
})
