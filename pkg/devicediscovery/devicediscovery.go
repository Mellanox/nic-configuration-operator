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
	"context"
	"os"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/nvconfig"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

type DeviceDiscovery interface {
	// DiscoverNicDevices discovers Nvidia NIC devices on the host and returns back a map of
	// PCI device addresses (Domain:Bus:Device, function stripped) to NicDevice objects.
	DiscoverNicDevices() (map[string]v1alpha1.NicDevice, error)
}

type SkippedDeviceReporter interface {
	// SkippedDevices returns physical PCI device keys skipped during the last discovery pass.
	SkippedDevices() map[string]error
}

type IncompleteDeviceReporter interface {
	// IncompleteDevices returns physical PCI device keys omitted because discovery could not build complete status.
	IncompleteDevices() map[string]error
}

type deviceDiscovery struct {
	utils             DeviceDiscoveryUtils
	nvConfigUtils     nvconfig.NVConfigUtils
	skippedDevices    map[string]error
	incompleteDevices map[string]error

	nodeName string
}

// DiscoverNicDevices uses host utils to discover Nvidia NIC devices on the host and returns back a map of
// PCI device addresses (Domain:Bus:Device, function stripped) to NicDevice objects. PCI functions that share
// the same Domain:Bus:Device are ports of the same physical NIC and are merged into one NicDevice. Keying
// by PCI device address (rather than serial number) is required on systems where multiple cards share a
// flashed VPD image (e.g. embedded NICs on HGX B300).
func (d *deviceDiscovery) DiscoverNicDevices() (map[string]v1alpha1.NicDevice, error) {
	log.Log.Info("ConfigurationManager.DiscoverNicDevices()")
	d.skippedDevices = map[string]error{}
	d.incompleteDevices = map[string]error{}

	pciDevices, err := d.utils.GetPCIDevices()
	if err != nil {
		log.Log.Error(err, "Failed to get PCI devices")
		return nil, err
	}

	statuses := make(map[string]v1alpha1.NicDeviceStatus)
	skipDeviceOnDiscoveryError := os.Getenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR) == consts.LabelValueTrue

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

		pciKey := utils.PCIDeviceAddress(device.Address)
		if _, skipped := d.skippedDevices[pciKey]; skipped {
			log.Log.Info("Physical device already skipped during this discovery pass, skipping port", "pciKey", pciKey, "address", device.Address)
			continue
		}

		log.Log.Info("Found Mellanox device", "address", device.Address, "type", device.Product.Name)

		vpd, err := d.utils.GetVPD(device.Address)
		if err != nil {
			log.Log.Error(err, "Failed to get device's part and serial numbers, skipping", "address", device.Address)
			// A VPD failure makes the whole physical device incomplete for this pass.
			d.dropIncompleteDevice(statuses, pciKey, err)
			if skipDeviceOnDiscoveryError {
				d.skippedDevices[pciKey] = err
			}
			continue
		}

		isBlueField := utils.IsBlueFieldDevice(device.Product.ID)
		zeroTrust := false
		if isBlueField {
			zeroTrust, err = d.utils.IsZeroTrust(device.Address)
			if err != nil {
				log.Log.Error(err, "Failed to get device's zero-trust (host restriction) status. Proceed on premise of non-zero-trust", "address", device.Address)
			}
		}
		if zeroTrust {
			log.Log.Info("Device is zero-trust (host restriction) mode, skipping as it disallows any config change from host", "address", device.Address)
			continue
		}

		deviceStatus, ok := statuses[pciKey]
		fwctlDevice := d.utils.GetFwctlDevice(device.Address)

		if !ok {
			firmwareVersion, psid, err := d.utils.GetFirmwareVersionAndPSID(device.Address)
			if err != nil {
				log.Log.Error(err, "Failed to get device's firmware and PSID", "address", device.Address)
				if skipDeviceOnDiscoveryError {
					d.skipPhysicalDevice(statuses, pciKey, err)
					continue
				}
				return nil, err
			}

			// mlxvpd's IDTAG "Board Id" is a long marketing string like
			//   "NVIDIA ConnectX-9 C9180 HHHL SuperNIC, 800Gbs XDR IB / 800GbE (default), ..."
			// The portion before the first comma is the product name; everything after is
			// feature/packaging detail we don't want in the CR.
			modelName := strings.TrimSpace(strings.SplitN(vpd.ModelName, ",", 2)[0])

			dpu := false
			if isBlueField {
				port := v1alpha1.NicDevicePortSpec{PCI: device.Address, FwctlDevice: fwctlDevice}
				nvConfig, err := d.nvConfigUtils.QueryNvConfig(context.Background(), port, []string{consts.BF3OperationModeParam})
				if err != nil {
					log.Log.Error(err, "Failed to get BlueField device's operation mode", "address", device.Address)
					if skipDeviceOnDiscoveryError {
						d.skipPhysicalDevice(statuses, pciKey, err)
						continue
					}
					return nil, err
				}
				// For a field ENABLED(0) or DISABLED(1), the second parameter is the 0/1 value
				dpu = nvConfig.CurrentConfig[consts.BF3OperationModeParam][1] == consts.NvParamBF3DpuMode
			}

			deviceStatus = v1alpha1.NicDeviceStatus{
				Node:            d.nodeName,
				Type:            device.Product.ID,
				SerialNumber:    vpd.SerialNumber,
				PartNumber:      vpd.PartNumber,
				ModelName:       modelName,
				PSID:            psid,
				FirmwareVersion: firmwareVersion,
				SuperNIC:        utils.ContainsIgnoreCase(vpd.ModelName, consts.SuperNIC),
				DPU:             dpu,
				Ports:           []v1alpha1.NicDevicePortSpec{},
			}

			log.Log.Info("Discovered NIC device", "address", device.Address, "status", deviceStatus)
		}

		networkInterface := d.utils.GetInterfaceName(device.Address)
		rdmaInterface := d.utils.GetRDMADeviceName(device.Address)

		deviceStatus.Ports = append(deviceStatus.Ports, v1alpha1.NicDevicePortSpec{
			PCI:              device.Address,
			FwctlDevice:      fwctlDevice,
			NetworkInterface: networkInterface,
			RdmaInterface:    rdmaInterface,
		})

		// ConnectX-9 Network Bay ("orchid") detection. Orchid-ness is a fixed hardware
		// property, so detect it once when the device is first seen. The two ASICs of a
		// bay enumerate as separate PCI devices, so this runs per device.
		if !ok && device.Product.ID == consts.ConnectX9DeviceID {
			if asic, isOrchid := d.utils.GetNetworkBayASIC(device.Address); isOrchid {
				deviceStatus.NetworkBay = &v1alpha1.NicDeviceNetworkBayStatus{Asic: asic}
			}
		}

		statuses[pciKey] = deviceStatus
	}

	// Pair Network Bay siblings by serial number: the two ASICs of one orchid card share a
	// serial number. For each pair, record the other ASIC's PCI address as PeerPCI.
	pairNetworkBayDevices(statuses)

	devices := make(map[string]v1alpha1.NicDevice, len(statuses))
	for pciKey, status := range statuses {
		devices[pciKey] = v1alpha1.NicDevice{
			Status: status,
		}
	}

	log.Log.V(2).Info("Found devices", "devices", devices)

	return devices, nil
}

func (d *deviceDiscovery) skipPhysicalDevice(statuses map[string]v1alpha1.NicDeviceStatus, pciKey string, err error) {
	d.skippedDevices[pciKey] = err
	d.dropIncompleteDevice(statuses, pciKey, err)
}

func (d *deviceDiscovery) dropIncompleteDevice(statuses map[string]v1alpha1.NicDeviceStatus, pciKey string, err error) {
	d.incompleteDevices[pciKey] = err
	delete(statuses, pciKey)
}

func (d *deviceDiscovery) SkippedDevices() map[string]error {
	skippedDevices := make(map[string]error, len(d.skippedDevices))
	for pciKey, err := range d.skippedDevices {
		skippedDevices[pciKey] = err
	}
	return skippedDevices
}

func (d *deviceDiscovery) IncompleteDevices() map[string]error {
	incompleteDevices := make(map[string]error, len(d.incompleteDevices))
	for pciKey, err := range d.incompleteDevices {
		incompleteDevices[pciKey] = err
	}
	return incompleteDevices
}

// pairNetworkBayDevices resolves PeerPCI for ConnectX-9 Network Bay siblings. The two ASICs of a
// single orchid card share a serial number; this groups orchid device statuses by serial and, for
// each group of exactly two, sets each device's NetworkBay.PeerPCI to the other's first-port PCI.
// Groups that are not exactly two (e.g. a serial reused across cards, or only one ASIC discovered)
// are left with PeerPCI unset. NetworkBay is a pointer, so mutating through it updates the stored
// status in place.
func pairNetworkBayDevices(statuses map[string]v1alpha1.NicDeviceStatus) {
	bySerial := make(map[string][]string)
	for key, status := range statuses {
		if status.NetworkBay != nil {
			bySerial[status.SerialNumber] = append(bySerial[status.SerialNumber], key)
		}
	}

	for serial, keys := range bySerial {
		if len(keys) != 2 {
			log.Log.Info("Network Bay device group does not contain exactly two ASICs, leaving PeerPCI unset",
				"serialNumber", serial, "count", len(keys))
			continue
		}
		a, b := statuses[keys[0]], statuses[keys[1]]
		if len(a.Ports) == 0 || len(b.Ports) == 0 {
			continue
		}
		a.NetworkBay.PeerPCI = b.Ports[0].PCI
		b.NetworkBay.PeerPCI = a.Ports[0].PCI
	}
}

func NewDeviceDiscovery(nodeName string, nvConfigUtils nvconfig.NVConfigUtils) DeviceDiscovery {
	return &deviceDiscovery{nodeName: nodeName, utils: NewDeviceDiscoveryUtils(), nvConfigUtils: nvConfigUtils}
}
