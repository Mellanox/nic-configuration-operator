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
	"regexp"
	"strings"

	"github.com/Mellanox/rdmamap"
	"github.com/jaypipes/ghw"
	"github.com/jaypipes/ghw/pkg/pci"
	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

const pciDevicesPath = "/sys/bus/pci/devices"

type DeviceDiscoveryUtils interface {
	// GetPCIDevices returns a list of PCI devices on the host
	GetPCIDevices() ([]*pci.Device, error)

	// GetVPD uses mlxvpd util to retrieve Part Number, Serial Number, Model Name of the PCI device
	GetVPD(pciAddr string) (*types.VPD, error)

	// GetFirmwareVersionAndPSID uses flint tool to retrieve FW version and PSID of the device
	GetFirmwareVersionAndPSID(pciAddr string) (string, string, error)

	// GetRDMADeviceName returns a RDMA device name for the given PCI address
	GetRDMADeviceName(pciAddr string) string

	// GetInterfaceName returns a network interface name for the given PCI address
	GetInterfaceName(pciAddr string) string

	// IsSriovVF return true if the device is a SRIOV VF, false otherwise
	IsSriovVF(pciAddr string) bool

	// IsZeroTrust uses mlxprivhost tool to check if the device is in zero-trust mode
	IsZeroTrust(pciAddr string) (bool, error)
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

// GetVPD uses mlxvpd util to retrieve Part Number, Serial Number, Model Name of the PCI device
func (d *deviceDiscoveryUtils) GetVPD(pciAddr string) (*types.VPD, error) {
	log.Log.Info("HostUtils.GetPartAndSerialNumber()", "pciAddr", pciAddr)
	cmd := d.execInterface.Command("mlxvpd", "-d", pciAddr)
	output, err := utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "GetPartAndSerialNumber(): Failed to run mlxvpd")
		return nil, err
	}

	// Parse the output for PN, SN and IDTAG
	// The output format is tabular with columns: VPD-KEYWORD, DESCRIPTION, VALUE
	// Example line: "  PN             Part Number             MCX623106AE-CDAT"
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	var partNumber, serialNumber, modelName string

	// Compile regex patterns for each VPD field we care about
	// Pattern: keyword + whitespace + description + whitespace + value (capture group)
	pnRegex := regexp.MustCompile(`^\s*` + consts.PartNumberPrefix + `\s+` + consts.PartNumberDescription + `\s+(.+)$`)
	snRegex := regexp.MustCompile(`^\s*` + consts.SerialNumberPrefix + `\s+` + consts.SerialNumberDescription + `\s+(.+)$`)
	modelRegex := regexp.MustCompile(`^\s*` + consts.ModelNamePrefix + `\s+` + consts.ModelNameDescription + `\s+(.+)$`)

	for scanner.Scan() {
		line := scanner.Text()

		if matches := pnRegex.FindStringSubmatch(line); len(matches) > 1 {
			partNumber = strings.TrimSpace(matches[1])
		} else if matches := snRegex.FindStringSubmatch(line); len(matches) > 1 {
			serialNumber = strings.TrimSpace(matches[1])
		} else if matches := modelRegex.FindStringSubmatch(line); len(matches) > 1 {
			modelName = strings.TrimSpace(matches[1])
		}
	}

	if err := scanner.Err(); err != nil {
		log.Log.Error(err, "GetPartAndSerialNumber(): Error reading mlxvpd output")
		return nil, err
	}

	if partNumber == "" || serialNumber == "" {
		return nil, fmt.Errorf("GetPartAndSerialNumber(): part number (%v) or serial number (%v) is empty", partNumber, serialNumber)
	}

	return &types.VPD{
		PartNumber:   partNumber,
		SerialNumber: serialNumber,
		ModelName:    modelName,
	}, nil
}

// GetFirmwareVersionAndPSID uses flint tool to retrieve FW version and PSID of the device
func (d *deviceDiscoveryUtils) GetFirmwareVersionAndPSID(pciAddr string) (string, string, error) {
	log.Log.Info("HostUtils.GetFirmwareVersionAndPSID()", "pciAddr", pciAddr)
	cmd := d.execInterface.Command("flint", "-d", pciAddr, "q")
	output, err := utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "GetFirmwareVersionAndPSID(): Failed to run flint")
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
		log.Log.Error(err, "GetFirmwareVersionAndPSID(): Error reading flint output")
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

// IsZeroTrust uses mlxprivhost tool to check if the BlueField device is in zero-trust mode
func (d *deviceDiscoveryUtils) IsZeroTrust(pciAddr string) (bool, error) {
	log.Log.Info("HostUtils.IsZeroTrust()", "pciAddr", pciAddr)
	// Check if the device is in restricted (zero-trust) mode
	cmd := d.execInterface.Command("mlxprivhost", "-d", pciAddr, "q")
	output, err := utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "IsZeroTrust(): Failed to run mlxprivhost")
		return false, err
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))

	for scanner.Scan() {
		line := strings.ToLower(scanner.Text())

		if strings.HasPrefix(line, consts.ZeroTrustHostConfigPrefix) {
			if strings.Contains(line, consts.HostRestrictionLevelRestricted) {
				return true, nil
			} else if strings.Contains(line, consts.HostRestrictionLevelPrivileged) {
				return false, nil
			}
		}
	}

	return false, fmt.Errorf("IsZeroTrustDevice(): failed to parse mlxprivhost output")
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

// NewDeviceDiscoveryUtils creates a new DeviceDiscoveryUtils instance
func NewDeviceDiscoveryUtils() DeviceDiscoveryUtils {
	return &deviceDiscoveryUtils{execInterface: execUtils.New()}
}
