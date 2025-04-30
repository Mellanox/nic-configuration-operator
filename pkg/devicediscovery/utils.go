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
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mellanox/rdmamap"
	"github.com/jaypipes/ghw"
	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/vishvananda/netlink"
	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

const pciDevicesPath = "/sys/bus/pci/devices"

type DeviceDiscoveryUtils interface {
	// GetPCIDevices returns a list of PCI devices on the host
	GetPCIDevices() ([]*pci.Device, error)

	// GetPartAndSerialNumber uses mstvpd util to retrieve Part and Serial numbers of the PCI device
	GetPartAndSerialNumber(pciAddr string) (string, string, error)
	// GetFirmwareVersionAndPSID uses mstflint tool to retrieve FW version and PSID of the device
	GetFirmwareVersionAndPSID(pciAddr string) (string, string, error)

	// GetRDMADeviceName returns a RDMA device name for the given PCI address
	GetRDMADeviceName(pciAddr string) string
	// GetInterfaceName returns a network interface name for the given PCI address
	GetInterfaceName(pciAddr string) string

	// IsSriovVF return true if the device is a SRIOV VF, false otherwise
	IsSriovVF(pciAddr string) bool
}

type deviceDiscoveryUtils struct {
	execInterface execUtils.Interface
}

// GetPCIDevices returns a list of PCI devices on the host
func (d *deviceDiscoveryUtils) GetPCIDevices() ([]*pci.Device, error) {
	pciRegistry, err := ghw.PCI()
	if err != nil {
		log.Log.Error(err, "GetPCIDevices(): Failed to read PCI devices")
		return nil, err
	}

	return pciRegistry.Devices, nil
}

// GetPartAndSerialNumber uses mstvpd util to retrieve Part and Serial numbers of the PCI device
func (d *deviceDiscoveryUtils) GetPartAndSerialNumber(pciAddr string) (string, string, error) {
	log.Log.Info("HostUtils.GetPartAndSerialNumber()", "pciAddr", pciAddr)
	cmd := d.execInterface.Command("mstvpd", pciAddr)
	output, err := utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "GetPartAndSerialNumber(): Failed to run mstvpd")
		return "", "", err
	}

	// Parse the output for PN and SN
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	var partNumber, serialNumber string

	for scanner.Scan() {
		line := strings.ToLower(scanner.Text())

		if strings.HasPrefix(line, consts.PartNumberPrefix) {
			partNumber = strings.TrimSpace(strings.TrimPrefix(line, consts.PartNumberPrefix))
		}
		if strings.HasPrefix(line, consts.SerialNumberPrefix) {
			serialNumber = strings.TrimSpace(strings.TrimPrefix(line, consts.SerialNumberPrefix))
		}
	}

	if err := scanner.Err(); err != nil {
		log.Log.Error(err, "GetPartAndSerialNumber(): Error reading mstvpd output")
		return "", "", err
	}

	if partNumber == "" || serialNumber == "" {
		return "", "", fmt.Errorf("GetPartAndSerialNumber(): part number (%v) or serial number (%v) is empty", partNumber, serialNumber)
	}

	return partNumber, serialNumber, nil
}

// GetFirmwareVersionAndPSID uses mstflint tool to retrieve FW version and PSID of the device
func (d *deviceDiscoveryUtils) GetFirmwareVersionAndPSID(pciAddr string) (string, string, error) {
	log.Log.Info("HostUtils.GetFirmwareVersionAndPSID()", "pciAddr", pciAddr)
	cmd := d.execInterface.Command("mstflint", "-d", pciAddr, "q")
	output, err := utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "GetFirmwareVersionAndPSID(): Failed to run mstflint")
		return "", "", err
	}

	// Parse the output for PN and SN
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	var firmwareVersion, PSID string

	for scanner.Scan() {
		line := strings.ToLower(scanner.Text())

		if strings.HasPrefix(line, consts.FirmwareVersionPrefix) {
			firmwareVersion = strings.TrimSpace(strings.TrimPrefix(line, consts.FirmwareVersionPrefix))
		}
		if strings.HasPrefix(line, consts.PSIDPrefix) {
			PSID = strings.TrimSpace(strings.TrimPrefix(line, consts.PSIDPrefix))
		}
	}

	if err := scanner.Err(); err != nil {
		log.Log.Error(err, "GetFirmwareVersionAndPSID(): Error reading mstflint output")
		return "", "", err
	}

	if firmwareVersion == "" || PSID == "" {
		return "", "", fmt.Errorf("GetFirmwareVersionAndPSID(): firmware version (%v) or PSID (%v) is empty", firmwareVersion, PSID)
	}

	return firmwareVersion, PSID, nil
}

// GetRDMADeviceName returns a RDMA device name for the given PCI address
func (d *deviceDiscoveryUtils) GetRDMADeviceName(pciAddr string) string {
	log.Log.Info("HostUtils.GetRDMADeviceName()", "pciAddr", pciAddr)

	rdmaDevices := rdmamap.GetRdmaDevicesForPcidev(pciAddr)

	if len(rdmaDevices) < 1 {
		log.Log.Info("GetRDMADeviceName(): No RDMA device found for device", "address", pciAddr)
		return ""
	}

	log.Log.V(1).Info("Rdma device", "pciAddr", pciAddr, "name", rdmaDevices[0])
	return rdmaDevices[0]
}

// GetInterfaceName returns a network interface name for the given PCI address
func (d *deviceDiscoveryUtils) GetInterfaceName(pciAddr string) string {
	log.Log.Info("HostUtils.GetInterfaceName()", "pciAddr", pciAddr)

	names, err := getNetNames(pciAddr)
	if err != nil || len(names) < 1 {
		log.Log.Error(err, "GetInterfaceName(): failed to get interface name")
		return ""
	}
	log.Log.Info("Interface name", "pciAddr", pciAddr, "name", names[0])
	return names[0]
}

// IsSriovVF return true if the device is a SRIOV VF, false otherwise
func (d *deviceDiscoveryUtils) IsSriovVF(pciAddr string) bool {
	log.Log.Info("HostUtils.IsSriovVF()", "pciAddr", pciAddr)

	totalVfFilePath := filepath.Join(pciDevicesPath, pciAddr, "physfn")
	if _, err := os.Stat(totalVfFilePath); err != nil {
		return false
	}
	return true
}

func getNetNames(pciAddr string) ([]string, error) {
	netDir := filepath.Join(pciDevicesPath, pciAddr, "net")
	if _, err := os.Lstat(netDir); err != nil {
		return nil, fmt.Errorf("GetNetNames(): no net directory under pci device %s: %q", pciAddr, err)
	}

	fInfos, err := os.ReadDir(netDir)
	if err != nil {
		return nil, fmt.Errorf("GetNetNames(): failed to read net directory %s: %q", netDir, err)
	}

	names := make([]string, 0)
	for _, f := range fInfos {
		names = append(names, f.Name())
	}

	return names, nil
}

func newDeviceDiscoveryUtils() DeviceDiscoveryUtils {
	return &deviceDiscoveryUtils{execInterface: execUtils.New()}
}
