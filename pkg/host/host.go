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
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
)

// HostManager contains logic for managing NIC devices on the host
type HostManager interface {
	// DiscoverNicDevices discovers Nvidia NIC devices on the host and returns back a map of serial numbers to device statuses
	DiscoverNicDevices() (map[string]v1alpha1.NicDeviceStatus, error)
	// ValidateDeviceNvSpec will validate device's non-volatile spec against already applied configuration on the host
	// returns bool - nv config update required
	// returns bool - reboot required
	// returns error - there are errors in device's spec
	ValidateDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, bool, error)
	// ApplyDeviceNvSpec calculates device's missing nv spec configuration and applies it to the device on the host
	// returns bool - reboot required
	// returns error - there were errors while applying nv configuration
	ApplyDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, error)
	// ApplyDeviceRuntimeSpec calculates device's missing runtime spec configuration and applies it to the device on the host
	// returns error - there were errors while applying nv configuration
	ApplyDeviceRuntimeSpec(device *v1alpha1.NicDevice) error
}

type hostManager struct {
	hostUtils HostUtils
}

// DiscoverNicDevices uses host utils to discover Nvidia NIC devices on the host and returns back a map of serial numbers to device statuses
func (h hostManager) DiscoverNicDevices() (map[string]v1alpha1.NicDeviceStatus, error) {
	log.Log.Info("HostManager.DiscoverNicDevices()")

	pciDevices, err := h.hostUtils.GetPCIDevices()
	if err != nil {
		log.Log.Error(err, "Failed to get PCI devices")
		return nil, err
	}

	// Map of Serial Number to nic device
	devices := make(map[string]v1alpha1.NicDeviceStatus)

	for _, device := range pciDevices {
		if device.Vendor.ID != consts.MellanoxVendor {
			continue
		}

		devClass, err := strconv.ParseInt(device.Class.ID, 16, 64)
		if err != nil {
			log.Log.Error(err, "DiscoverSriovDevices(): unable to parse device class, skipping",
				"device", device)
			continue
		}
		if devClass != consts.NetClass {
			log.Log.V(2).Info("Device is not a network device, skipping", "address", device)
			continue
		}

		if h.hostUtils.IsSriovVF(device.Address) {
			log.Log.V(2).Info("Device is an SRIOV VF, skipping", "address", device.Address)
			continue
		}

		log.Log.Info("Found Mellanox device", "address", device.Address, "type", device.Product.Name)

		partNumber, serialNumber, err := h.hostUtils.GetPartAndSerialNumber(device.Address)
		if err != nil {
			log.Log.Error(err, "Failed to get device's part and serial numbers", "address", device.Address)
			return nil, err
		}

		// Devices with the same serial number are ports of the same NIC, so grouping them
		deviceStatus, ok := devices[serialNumber]

		if !ok {
			firmwareVersion, psid, err := h.hostUtils.GetFirmwareVersionAndPSID(device.Address)
			if err != nil {
				log.Log.Error(err, "Failed to get device's firmware and PSID", "address", device.Address)
				return nil, err
			}

			deviceStatus = v1alpha1.NicDeviceStatus{
				Type:            device.Product.ID,
				SerialNumber:    serialNumber,
				PartNumber:      partNumber,
				PSID:            psid,
				FirmwareVersion: firmwareVersion,
				Ports:           []v1alpha1.NicDevicePortSpec{},
			}

			devices[serialNumber] = deviceStatus
		}

		networkInterface := h.hostUtils.GetInterfaceName(device.Address)
		rdmaInterface := h.hostUtils.GetRDMADeviceName(device.Address)

		deviceStatus.Ports = append(deviceStatus.Ports, v1alpha1.NicDevicePortSpec{
			PCI:              device.Address,
			NetworkInterface: networkInterface,
			RdmaInterface:    rdmaInterface,
		})

		devices[deviceStatus.SerialNumber] = deviceStatus
	}

	return devices, nil
}

// ValidateDeviceNvSpec will validate device's non-volatile spec against already applied configuration on the host
// returns bool - nv config update required
// returns bool - reboot required
// returns error - there are errors in device's spec
func (h hostManager) ValidateDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, bool, error) {
	return false, false, nil
}

// ApplyDeviceNvSpec calculates device's missing nv spec configuration and applies it to the device on the host
// returns bool - reboot required
// returns error - there were errors while applying nv configuration
func (h hostManager) ApplyDeviceNvSpec(ctx context.Context, device *v1alpha1.NicDevice) (bool, error) {
	// TODO first set ADVANCED_PCI_SETTINGS=true
	// TODO then fwreset
	// TODO then recalculate list of commands
	// TODO then apply commands
	return false, nil
}

// ApplyDeviceRuntimeSpec calculates device's missing runtime spec configuration and applies it to the device on the host
// returns error - there were errors while applying nv configuration
func (h hostManager) ApplyDeviceRuntimeSpec(device *v1alpha1.NicDevice) error {
	// TODO check lastAppliedSpec
	return nil
}

func NewHostManager(hostUtils HostUtils) HostManager {
	return hostManager{hostUtils: hostUtils}
}
