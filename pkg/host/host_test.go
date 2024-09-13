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
	"errors"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/host/mocks"
	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/jaypipes/pcidb"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("HostManager", func() {
	var (
		mockHostUtils mocks.HostUtils
		hostManager   HostManager
	)

	BeforeEach(func() {
		mockHostUtils = mocks.HostUtils{}
		hostManager = NewHostManager(&mockHostUtils)
	})

	Describe("DiscoverNicDevices", func() {
		Context("when GetPCIDevices fails", func() {
			It("should return nil and log an error", func() {
				mockHostUtils.On("GetPCIDevices").Return(nil, errors.New("get PCI devices error"))

				devices, err := hostManager.DiscoverNicDevices()
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

				devices, err := hostManager.DiscoverNicDevices()
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

				devices, err := hostManager.DiscoverNicDevices()
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

				devices, err := hostManager.DiscoverNicDevices()
				Expect(err).NotTo(HaveOccurred())
				Expect(devices).To(BeEmpty())
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should fails if GetPartAndSerialNumber fails", func() {
				mockHostUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
				mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").Return("", "", errors.New("serial number error"))

				devices, err := hostManager.DiscoverNicDevices()
				Expect(err).To(HaveOccurred())
				Expect(devices).To(BeNil())
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should log and skip devices if GetFirmwareVersionAndPSID fails", func() {
				mockHostUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
				mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").Return("part-number", "serial-number", nil)
				mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("", "", errors.New("firmware error"))

				devices, err := hostManager.DiscoverNicDevices()
				Expect(err).To(HaveOccurred())
				Expect(devices).To(BeNil())
				mockHostUtils.AssertExpectations(GinkgoT())
			})

			It("should discover and return devices successfully", func() {
				mockHostUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
				mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").Return("part-number", "serial-number", nil)
				mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("fw-version", "psid", nil)
				mockHostUtils.On("GetInterfaceName", "0000:00:00.0").Return("eth0")
				mockHostUtils.On("GetRDMADeviceName", "0000:00:00.0").Return("mlx5_0")

				devices, err := hostManager.DiscoverNicDevices()
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
			mockHostUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").Return("part-number", "serial-number", nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("fw-version", "psid", nil)
			mockHostUtils.On("GetInterfaceName", "0000:00:00.0").Return("eth0")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.0").Return("mlx5_0")

			mockHostUtils.On("IsSriovVF", "0000:00:00.1").Return(true)

			devices, err := hostManager.DiscoverNicDevices()
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
			mockHostUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").Return("part-number", "serial-number", nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("fw-version", "psid", nil)
			mockHostUtils.On("GetInterfaceName", "0000:00:00.0").Return("eth0")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.0").Return("mlx5_0")

			mockHostUtils.On("IsSriovVF", "0000:00:00.1").Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.1").Return("", "", errors.New("serial number error"))

			devices, err := hostManager.DiscoverNicDevices()
			Expect(err).To(HaveOccurred())
			Expect(devices).To(BeNil())
			mockHostUtils.AssertExpectations(GinkgoT())
		})

		It("should log and skip only a faulty device if GetFirmwareVersionAndPSID fails", func() {
			mockHostUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").Return("part-number", "serial-number", nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("fw-version", "psid", nil)
			mockHostUtils.On("GetInterfaceName", "0000:00:00.0").Return("eth0")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.0").Return("mlx5_0")

			mockHostUtils.On("IsSriovVF", "0000:00:00.1").Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.1").Return("part-number", "serial-number-2", nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.1").Return("", "", errors.New("firmware error"))

			devices, err := hostManager.DiscoverNicDevices()
			Expect(err).To(HaveOccurred())
			Expect(devices).To(BeNil())
			mockHostUtils.AssertExpectations(GinkgoT())
		})

		It("should discover and return devices successfully", func() {
			mockHostUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").Return("part-number", "serial-number", nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("fw-version", "psid", nil)
			mockHostUtils.On("GetInterfaceName", "0000:00:00.0").Return("eth0")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.0").Return("mlx5_0")

			mockHostUtils.On("IsSriovVF", "0000:00:00.1").Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.1").Return("part-number", "serial-number-2", nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.1").Return("fw-version", "psid", nil)
			mockHostUtils.On("GetInterfaceName", "0000:00:00.1").Return("eth1")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.1").Return("mlx5_1")

			devices, err := hostManager.DiscoverNicDevices()
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

			mockHostUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.0").Return("part-number", sameSerialNumber, nil)
			mockHostUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("fw-version", "psid", nil)
			mockHostUtils.On("GetInterfaceName", "0000:00:00.0").Return("eth0")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.0").Return("mlx5_0")

			mockHostUtils.On("IsSriovVF", "0000:00:00.1").Return(false)
			mockHostUtils.On("GetPartAndSerialNumber", "0000:00:00.1").Return("part-number", sameSerialNumber, nil)
			mockHostUtils.AssertNotCalled(GinkgoT(), "GetFirmwareVersionAndPSID", "0000:00:00.1")
			mockHostUtils.On("GetInterfaceName", "0000:00:00.1").Return("eth1")
			mockHostUtils.On("GetRDMADeviceName", "0000:00:00.1").Return("mlx5_1")

			devices, err := hostManager.DiscoverNicDevices()
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
})
