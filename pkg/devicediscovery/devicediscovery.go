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
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"strconv"
)

type DeviceDiscovery interface {
	// DiscoverNicDevices discovers Nvidia NIC devices on the host and returns back a map of serial numbers to device statuses
	DiscoverNicDevices() (map[string]v1alpha1.NicDeviceStatus, error)
}

type deviceDiscovery struct {
	utils DeviceDiscoveryUtils

	nodeName string
}

// DiscoverNicDevices uses host utils to discover Nvidia NIC devices on the host and returns back a map of serial numbers to device statuses
func (d deviceDiscovery) DiscoverNicDevices() (map[string]v1alpha1.NicDeviceStatus, error) {
	log.Log.Info("HostManager.DiscoverNicDevices()")

	pciDevices, err := d.utils.GetPCIDevices()
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

		if d.utils.IsSriovVF(device.Address) {
			log.Log.V(2).Info("Device is an SRIOV VF, skipping", "address", device.Address)
			continue
		}

		log.Log.Info("Found Mellanox device", "address", device.Address, "type", device.Product.Name)

		partNumber, serialNumber, err := d.utils.GetPartAndSerialNumber(device.Address)
		if err != nil {
			log.Log.Error(err, "Failed to get device's part and serial numbers", "address", device.Address)
			return nil, err
		}

		// Devices with the same serial number are ports of the same NIC, so grouping them
		deviceStatus, ok := devices[serialNumber]

		if !ok {
			firmwareVersion, psid, err := d.utils.GetFirmwareVersionAndPSID(device.Address)
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

		networkInterface := d.utils.GetInterfaceName(device.Address)
		rdmaInterface := d.utils.GetRDMADeviceName(device.Address)

		deviceStatus.Ports = append(deviceStatus.Ports, v1alpha1.NicDevicePortSpec{
			PCI:              device.Address,
			NetworkInterface: networkInterface,
			RdmaInterface:    rdmaInterface,
		})

		deviceStatus.Node = d.nodeName
		devices[deviceStatus.SerialNumber] = deviceStatus
	}

	return devices, nil
}

func NewDeviceDiscovery(nodeName string) DeviceDiscovery {
	return &deviceDiscovery{utils: newDeviceDiscoveryUtils()}
}
