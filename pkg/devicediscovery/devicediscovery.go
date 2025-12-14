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
	"strconv"
	"strings"

	"context"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/nvconfig"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

type DeviceDiscovery interface {
	// DiscoverNicDevices discovers Nvidia NIC devices on the host and returns back a map of serial numbers to device statuses
	DiscoverNicDevices() (map[string]v1alpha1.NicDeviceStatus, error)
}

type deviceDiscovery struct {
	utils         DeviceDiscoveryUtils
	nvConfigUtils nvconfig.NVConfigUtils

	nodeName string
}

// DiscoverNicDevices uses host utils to discover Nvidia NIC devices on the host and returns back a map of serial numbers to device statuses
func (d deviceDiscovery) DiscoverNicDevices() (map[string]v1alpha1.NicDeviceStatus, error) {
	log.Log.Info("ConfigurationManager.DiscoverNicDevices()")

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

		vpd, err := d.utils.GetVPD(device.Address)
		if err != nil {
			log.Log.Error(err, "Failed to get device's part and serial numbers, skipping", "address", device.Address)
			continue
		}

		// Devices with the same serial number are ports of the same NIC, so grouping them
		deviceStatus, ok := devices[vpd.SerialNumber]

		if !ok {
			firmwareVersion, psid, err := d.utils.GetFirmwareVersionAndPSID(device.Address)
			if err != nil {
				log.Log.Error(err, "Failed to get device's firmware and PSID", "address", device.Address)
				return nil, err
			}

			// Trim the model name to the first word, e.g. ConnectX-6 or BlueField-2
			shortName := strings.SplitN(vpd.ModelName, " ", 2)[0]

			dpu := false
			if utils.IsBlueFieldDevice(device.Product.ID) {
				nvConfig, err := d.nvConfigUtils.QueryNvConfig(context.Background(), device.Address, consts.BF3OperationModeParam)
				if err != nil {
					log.Log.Error(err, "Failed to get BlueField device's operation mode", "address", device.Address)
					return nil, err
				}
				dpu = nvConfig.CurrentConfig[consts.BF3OperationModeParam][0] == consts.NvParamBF3DpuMode
			}

			deviceStatus = v1alpha1.NicDeviceStatus{
				Node:            d.nodeName,
				Type:            device.Product.ID,
				SerialNumber:    vpd.SerialNumber,
				PartNumber:      vpd.PartNumber,
				ModelName:       shortName,
				PSID:            psid,
				FirmwareVersion: firmwareVersion,
				SuperNIC:        utils.ContainsIgnoreCase(vpd.ModelName, consts.SuperNIC),
				DPU:             dpu,
				Ports:           []v1alpha1.NicDevicePortSpec{},
			}

			devices[vpd.SerialNumber] = deviceStatus
		}

		networkInterface := d.utils.GetInterfaceName(device.Address)
		rdmaInterface := d.utils.GetRDMADeviceName(device.Address)

		deviceStatus.Ports = append(deviceStatus.Ports, v1alpha1.NicDevicePortSpec{
			PCI:              device.Address,
			NetworkInterface: networkInterface,
			RdmaInterface:    rdmaInterface,
		})

		devices[deviceStatus.SerialNumber] = deviceStatus
	}

	log.Log.V(2).Info("Found devices", "devices", devices)

	return devices, nil
}

func NewDeviceDiscovery(nodeName string, nvConfigUtils nvconfig.NVConfigUtils) DeviceDiscovery {
	return &deviceDiscovery{nodeName: nodeName, utils: newDeviceDiscoveryUtils(), nvConfigUtils: nvConfigUtils}
}
