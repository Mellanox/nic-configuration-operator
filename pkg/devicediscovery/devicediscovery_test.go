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
	"os"

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
		mockUtils    mocks.DeviceDiscoveryUtils
		manager      *deviceDiscovery
		fwctlDevices map[string]string
	)

	BeforeEach(func() {
		Expect(os.Unsetenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR)).To(Succeed())
		mockUtils = mocks.DeviceDiscoveryUtils{}
		fwctlDevices = map[string]string{}
		mockUtils.On("GetFwctlDevice", mock.Anything).Return(func(pciAddr string) string {
			return fwctlDevices[pciAddr]
		}).Maybe()
		manager = &deviceDiscovery{utils: &mockUtils, nodeName: "test-node"}
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

			It("should skip devices if GetFirmwareVersionAndPSID fails and SKIP_DEVICE_ON_DISCOVERY_ERROR is true", func() {
				Expect(os.Setenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR, consts.LabelValueTrue)).To(Succeed())
				mockUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
				mockUtils.On("GetVPD", "0000:00:00.0").
					Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number", ModelName: ""}, nil)
				mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
					Return("", "", errors.New("firmware error"))

				devices, err := manager.DiscoverNicDevices()
				Expect(err).NotTo(HaveOccurred())
				Expect(devices).To(BeEmpty())
				Expect(manager.SkippedDevices()).To(HaveKey("0000:00:00"))
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

				Expect(devices).To(HaveKey("0000:00:00"))
				Expect(devices["0000:00:00"].Status).To(Equal(expectedDeviceStatus))
				// Verify that the returned device has the correct nodeName
				Expect(devices["0000:00:00"].Status.Node).To(Equal("test-node"))

				mockUtils.AssertExpectations(GinkgoT())
			})
		})
	})

	Context("when discovering several mellanox device", func() {
		BeforeEach(func() {
			// Two distinct physical NICs at different PCI devices.
			mockUtils.On("GetPCIDevices").Return([]*pci.Device{
				{
					Address: "0000:00:00.0",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: "test-id", Name: "Mellanox Device"},
					Class:   &pcidb.Class{ID: "02"},
				},
				{
					Address: "0000:01:00.0",
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

			mockUtils.On("IsSriovVF", "0000:01:00.0").Return(true)

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

			Expect(devices).To(HaveKey("0000:00:00"))
			Expect(devices["0000:00:00"].Status).To(Equal(expectedDeviceStatus))
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

			mockUtils.On("IsSriovVF", "0000:01:00.0").
				Return(false)
			mockUtils.On("GetVPD", "0000:01:00.0").
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

			mockUtils.On("IsSriovVF", "0000:01:00.0").
				Return(false)
			mockUtils.On("GetVPD", "0000:01:00.0").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number-2", ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:01:00.0").
				Return("", "", errors.New("firmware error"))

			devices, err := manager.DiscoverNicDevices()
			Expect(err).To(HaveOccurred())
			Expect(devices).To(BeNil())
			mockUtils.AssertExpectations(GinkgoT())
		})

		It("should skip only a faulty device if GetFirmwareVersionAndPSID fails and SKIP_DEVICE_ON_DISCOVERY_ERROR is true", func() {
			Expect(os.Setenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR, consts.LabelValueTrue)).To(Succeed())
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

			mockUtils.On("IsSriovVF", "0000:01:00.0").
				Return(false)
			mockUtils.On("GetVPD", "0000:01:00.0").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number-2", ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:01:00.0").
				Return("", "", errors.New("firmware error"))

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(1))
			Expect(devices).To(HaveKey("0000:00:00"))
			Expect(devices).NotTo(HaveKey("0000:01:00"))
			Expect(manager.SkippedDevices()).To(HaveKey("0000:01:00"))
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

			mockUtils.On("IsSriovVF", "0000:01:00.0").
				Return(false)
			mockUtils.On("GetVPD", "0000:01:00.0").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number-2", ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:01:00.0").
				Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0000:01:00.0").
				Return("eth1")
			mockUtils.On("GetRDMADeviceName", "0000:01:00.0").
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
						PCI:              "0000:01:00.0",
						NetworkInterface: "eth1",
						RdmaInterface:    "mlx5_1",
					},
				},
			}

			Expect(devices).To(HaveKey("0000:00:00"))
			Expect(devices["0000:00:00"].Status).To(Equal(expectedDeviceStatus1))
			Expect(devices).To(HaveKey("0000:01:00"))
			Expect(devices["0000:01:00"].Status).To(Equal(expectedDeviceStatus2))

			mockUtils.AssertExpectations(GinkgoT())
		})

	})

	Context("when discovering ConnectX-9 Network Bay (orchid) devices", func() {
		// stageCX9 sets up the common per-device discovery mock expectations for a CX9 device.
		stageCX9 := func(pci, serial, iface, rdma string) {
			mockUtils.On("IsSriovVF", pci).Return(false)
			mockUtils.On("GetVPD", pci).
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: serial, ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", pci).Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", pci).Return(iface)
			mockUtils.On("GetRDMADeviceName", pci).Return(rdma)
		}

		cx9Devices := func() []*pci.Device {
			return []*pci.Device{
				{
					Address: "0000:0b:00.0",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: consts.ConnectX9DeviceID, Name: "ConnectX-9"},
					Class:   &pcidb.Class{ID: "02"},
				},
				{
					Address: "0000:0e:00.0",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: consts.ConnectX9DeviceID, Name: "ConnectX-9"},
					Class:   &pcidb.Class{ID: "02"},
				},
			}
		}

		It("sets ASIC index and pairs PeerPCI for two orchid ASICs sharing a serial", func() {
			mockUtils.On("GetPCIDevices").Return(cx9Devices(), nil)
			stageCX9("0000:0b:00.0", "orchid-serial", "eth0", "mlx5_0")
			stageCX9("0000:0e:00.0", "orchid-serial", "eth1", "mlx5_1")
			mockUtils.On("GetNetworkBayASIC", "0000:0b:00.0").Return(0, true)
			mockUtils.On("GetNetworkBayASIC", "0000:0e:00.0").Return(1, true)

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())

			Expect(devices["0000:0b:00"].Status.NetworkBay).To(Equal(&v1alpha1.NicDeviceNetworkBayStatus{Asic: 0, PeerPCI: "0000:0e:00.0"}))
			Expect(devices["0000:0e:00"].Status.NetworkBay).To(Equal(&v1alpha1.NicDeviceNetworkBayStatus{Asic: 1, PeerPCI: "0000:0b:00.0"}))
		})

		It("leaves NetworkBay nil for a standalone (non-orchid) CX9", func() {
			mockUtils.On("GetPCIDevices").Return(cx9Devices()[:1], nil)
			stageCX9("0000:0b:00.0", "standalone-serial", "eth0", "mlx5_0")
			mockUtils.On("GetNetworkBayASIC", "0000:0b:00.0").Return(0, false)

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices["0000:0b:00"].Status.NetworkBay).To(BeNil())
		})

		It("leaves PeerPCI empty when only one ASIC of a bay is discovered", func() {
			mockUtils.On("GetPCIDevices").Return(cx9Devices()[:1], nil)
			stageCX9("0000:0b:00.0", "lonely-serial", "eth0", "mlx5_0")
			mockUtils.On("GetNetworkBayASIC", "0000:0b:00.0").Return(0, true)

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices["0000:0b:00"].Status.NetworkBay).To(Equal(&v1alpha1.NicDeviceNetworkBayStatus{Asic: 0, PeerPCI: ""}))
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

			Expect(devices).To(HaveKey("0000:00:00"))
			Expect(devices["0000:00:00"].Status).To(Equal(expectedDeviceStatus))
			_ = sameSerialNumber // kept for readability of mock setup above

			mockUtils.AssertExpectations(GinkgoT())
		})

		It("should skip the whole physical device when discovery fails after one port was added", func() {
			Expect(os.Setenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR, consts.LabelValueTrue)).To(Succeed())
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

			mockUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
			mockUtils.On("GetVPD", "0000:00:00.0").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: "serial-number", ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").
				Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0000:00:00.0").
				Return("eth0")
			mockUtils.On("GetRDMADeviceName", "0000:00:00.0").
				Return("mlx5_0")

			mockUtils.On("IsSriovVF", "0000:00:00.1").Return(false)
			mockUtils.On("GetVPD", "0000:00:00.1").
				Return(nil, errors.New("vpd error"))

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(BeEmpty())
			Expect(manager.SkippedDevices()).To(HaveKey("0000:00:00"))
			mockUtils.AssertExpectations(GinkgoT())
		})
	})

	Context("when multiple NICs share the same flashed serial number (HGX B300)", func() {
		It("should return one device per PCI device key, not collapse by SN", func() {
			sharedSerial := "shared-sn"

			mockUtils.On("GetPCIDevices").Return([]*pci.Device{
				{
					Address: "0000:3b:00.0",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: "cx9", Name: "ConnectX-9"},
					Class:   &pcidb.Class{ID: "02"},
				},
				{
					Address: "0000:3c:00.0",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: "cx9", Name: "ConnectX-9"},
					Class:   &pcidb.Class{ID: "02"},
				},
			}, nil)

			for _, addr := range []string{"0000:3b:00.0", "0000:3c:00.0"} {
				mockUtils.On("IsSriovVF", addr).Return(false)
				mockUtils.On("GetVPD", addr).
					Return(&types.VPD{PartNumber: "part-number", SerialNumber: sharedSerial, ModelName: ""}, nil)
				mockUtils.On("GetFirmwareVersionAndPSID", addr).Return("fw-version", "psid", nil)
				mockUtils.On("GetInterfaceName", addr).Return("eth0")
				mockUtils.On("GetRDMADeviceName", addr).Return("mlx5_0")
			}

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(2))
			Expect(devices).To(HaveKey("0000:3b:00"))
			Expect(devices).To(HaveKey("0000:3c:00"))
			Expect(devices["0000:3b:00"].Status.SerialNumber).To(Equal(sharedSerial))
			Expect(devices["0000:3c:00"].Status.SerialNumber).To(Equal(sharedSerial))
		})
	})

	Context("when a ConnectX-9 card exposes four PCI functions (2 eth + 2 IB)", func() {
		It("should group all four functions into a single device with four ports", func() {
			sn := "cx9-sn"
			pciDevs := make([]*pci.Device, 0, 4)
			for _, fn := range []string{"0", "1", "2", "3"} {
				pciDevs = append(pciDevs, &pci.Device{
					Address: "0001:03:00." + fn,
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: "cx9", Name: "ConnectX-9"},
					Class:   &pcidb.Class{ID: "02"},
				})
			}
			mockUtils.On("GetPCIDevices").Return(pciDevs, nil)

			for _, fn := range []string{"0", "1", "2", "3"} {
				addr := "0001:03:00." + fn
				mockUtils.On("IsSriovVF", addr).Return(false)
				mockUtils.On("GetVPD", addr).
					Return(&types.VPD{PartNumber: "part-number", SerialNumber: sn, ModelName: ""}, nil)
				mockUtils.On("GetInterfaceName", addr).Return("eth" + fn)
				mockUtils.On("GetRDMADeviceName", addr).Return("mlx5_" + fn)
			}
			// FW/PSID only queried on first function.
			mockUtils.On("GetFirmwareVersionAndPSID", "0001:03:00.0").Return("fw-version", "psid", nil)

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(1))
			Expect(devices).To(HaveKey("0001:03:00"))
			Expect(devices["0001:03:00"].Status.Ports).To(HaveLen(4))
		})
	})

	Context("when two cards have the same bus:dev but different PCI domains", func() {
		It("should return two distinct devices", func() {
			mockUtils.On("GetPCIDevices").Return([]*pci.Device{
				{
					Address: "0001:03:00.0",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: "cx9", Name: "ConnectX-9"},
					Class:   &pcidb.Class{ID: "02"},
				},
				{
					Address: "0004:03:00.0",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: "cx9", Name: "ConnectX-9"},
					Class:   &pcidb.Class{ID: "02"},
				},
			}, nil)

			mockUtils.On("IsSriovVF", "0001:03:00.0").Return(false)
			mockUtils.On("GetVPD", "0001:03:00.0").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: "sn-a", ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0001:03:00.0").Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0001:03:00.0").Return("eth0")
			mockUtils.On("GetRDMADeviceName", "0001:03:00.0").Return("mlx5_0")

			mockUtils.On("IsSriovVF", "0004:03:00.0").Return(false)
			mockUtils.On("GetVPD", "0004:03:00.0").
				Return(&types.VPD{PartNumber: "part-number", SerialNumber: "sn-b", ModelName: ""}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0004:03:00.0").Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0004:03:00.0").Return("eth1")
			mockUtils.On("GetRDMADeviceName", "0004:03:00.0").Return("mlx5_1")

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(2))
			Expect(devices).To(HaveKey("0001:03:00"))
			Expect(devices).To(HaveKey("0004:03:00"))
		})
	})

	Context("when parsing model name and SuperNIC flag", func() {
		It("should truncate model name at first comma and set SuperNIC", func() {
			fwctlDevices["0000:00:00.0"] = "/dev/fwctl/fwctl0"
			mockUtils.On("GetPCIDevices").Return([]*pci.Device{
				{
					Address: "0000:00:00.0",
					Vendor:  &pcidb.Vendor{ID: consts.MellanoxVendor},
					Product: &pcidb.Product{ID: "test-id", Name: "Mellanox Device"},
					Class:   &pcidb.Class{ID: "02"},
				},
			}, nil)
			mockUtils.On("IsSriovVF", "0000:00:00.0").Return(false)
			mockUtils.On("GetVPD", "0000:00:00.0").Return(&types.VPD{
				PartNumber:   "part-number",
				SerialNumber: "serial-number",
				ModelName:    "NVIDIA ConnectX-8 C8180 HHHL SuperNIC, 800Gbs XDR IB / 800GbE (default), Single-cage OSFP",
			}, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0000:00:00.0").Return("eth0")
			mockUtils.On("GetRDMADeviceName", "0000:00:00.0").Return("mlx5_0")

			manager := &deviceDiscovery{utils: &mockUtils, nvConfigUtils: nvmmocks.NewNVConfigUtils(GinkgoT()), nodeName: "test-node"}

			devicesByPCI, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devicesByPCI).To(HaveKey("0000:00:00"))
			discoveredDevice := devicesByPCI["0000:00:00"]
			Expect(discoveredDevice.Status.ModelName).To(Equal("NVIDIA ConnectX-8 C8180 HHHL SuperNIC"))
			Expect(discoveredDevice.Status.SuperNIC).To(BeTrue())
			Expect(discoveredDevice.Status.Ports).To(HaveLen(1))
			Expect(discoveredDevice.Status.Ports[0].FwctlDevice).To(Equal("/dev/fwctl/fwctl0"))
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
			mockUtils.On("IsZeroTrust", "0000:00:00.0").Return(false, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("fw-version", "psid", nil)
			mockUtils.On("GetInterfaceName", "0000:00:00.0").Return("eth0")
			mockUtils.On("GetRDMADeviceName", "0000:00:00.0").Return("mlx5_0")

			nvConfigUtilsMock := nvmmocks.NewNVConfigUtils(GinkgoT())
			nvConfigUtilsMock.On("QueryNvConfig", mock.Anything, v1alpha1.NicDevicePortSpec{PCI: "0000:00:00.0"}, []string{consts.BF3OperationModeParam}).Return(types.NvConfigQuery{
				CurrentConfig:  map[string][]string{consts.BF3OperationModeParam: {"enabled", consts.NvParamBF3DpuMode}},
				NextBootConfig: map[string][]string{},
				DefaultConfig:  map[string][]string{},
			}, nil)

			manager := &deviceDiscovery{utils: &mockUtils, nvConfigUtils: nvConfigUtilsMock, nodeName: "test-node"}

			devicesByPCI, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			discoveredDevice := devicesByPCI["0000:00:00"]
			Expect(discoveredDevice.Status.DPU).To(BeTrue())
		})

		It("should skip zero-trust device", func() {
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
			mockUtils.On("IsZeroTrust", "0000:00:00.0").Return(true, nil)

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(HaveLen(0))
			mockUtils.AssertExpectations(GinkgoT())
		})

		It("should return an error when querying operation mode fails by default", func() {
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
			mockUtils.On("IsZeroTrust", "0000:00:00.0").Return(false, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("fw-version", "psid", nil)

			nvConfigUtilsMock := nvmmocks.NewNVConfigUtils(GinkgoT())
			nvConfigUtilsMock.On("QueryNvConfig", mock.Anything, v1alpha1.NicDevicePortSpec{PCI: "0000:00:00.0"}, []string{consts.BF3OperationModeParam}).Return(types.NvConfigQuery{}, errors.New("operation mode error"))

			manager := &deviceDiscovery{utils: &mockUtils, nvConfigUtils: nvConfigUtilsMock, nodeName: "test-node"}

			devices, err := manager.DiscoverNicDevices()
			Expect(err).To(HaveOccurred())
			Expect(devices).To(BeNil())
			mockUtils.AssertExpectations(GinkgoT())
		})

		It("should skip the device when querying operation mode fails and SKIP_DEVICE_ON_DISCOVERY_ERROR is true", func() {
			Expect(os.Setenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR, consts.LabelValueTrue)).To(Succeed())
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
			mockUtils.On("IsZeroTrust", "0000:00:00.0").Return(false, nil)
			mockUtils.On("GetFirmwareVersionAndPSID", "0000:00:00.0").Return("fw-version", "psid", nil)

			nvConfigUtilsMock := nvmmocks.NewNVConfigUtils(GinkgoT())
			nvConfigUtilsMock.On("QueryNvConfig", mock.Anything, v1alpha1.NicDevicePortSpec{PCI: "0000:00:00.0"}, []string{consts.BF3OperationModeParam}).Return(types.NvConfigQuery{}, errors.New("operation mode error"))

			manager := &deviceDiscovery{utils: &mockUtils, nvConfigUtils: nvConfigUtilsMock, nodeName: "test-node"}

			devices, err := manager.DiscoverNicDevices()
			Expect(err).NotTo(HaveOccurred())
			Expect(devices).To(BeEmpty())
			Expect(manager.SkippedDevices()).To(HaveKey("0000:00:00"))
			mockUtils.AssertExpectations(GinkgoT())
		})
	})
})
