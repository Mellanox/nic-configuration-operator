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
	"time"

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

const mlxvpdMaxAttempts = 3

// mlxvpdBackoff is a var (not const) so tests can override it to keep runs fast.
var mlxvpdBackoff = 500 * time.Millisecond

// runCommandWithRetry runs `name args...` up to maxAttempts times, sleeping backoff
// between failed attempts. Returns combined stdout+stderr from the last attempt and
// its error. A fresh Cmd is built each iteration (execUtils.Cmd is single-use).
func runCommandWithRetry(execInterface execUtils.Interface, name string, args []string, maxAttempts int, backoff time.Duration) ([]byte, error) {
	var output []byte
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		output, err = execInterface.Command(name, args...).CombinedOutput()
		log.Log.V(2).Info("command output", "command", name, "attempt", attempt, "output", string(output))
		if err == nil {
			return output, nil
		}
		if attempt < maxAttempts {
			log.Log.V(1).Info("command failed, retrying", "command", name, "attempt", attempt, "error", err)
			time.Sleep(backoff)
		}
	}
	return output, err
}

// physPortNameRegex matches physical port names like "p0", "p1" — the PF uplink interfaces.
// VF/SF representors have names like "pf0vf0", "pf0sf0" which do NOT match this pattern.
var physPortNameRegex = regexp.MustCompile(`^p\d+$`)

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

// vpdOutputPatterns is the set of line-regexes used to extract fields from one
// VPD tool's output. Each pattern must have a single capture group for the value.
type vpdOutputPatterns struct {
	partNumber   *regexp.Regexp
	serialNumber *regexp.Regexp
	modelName    *regexp.Regexp
}

// mlxvpd output is a 3-column table: "  PN             Part Number             <value>"
var mlxvpdPatterns = vpdOutputPatterns{
	partNumber:   regexp.MustCompile(`^\s*` + consts.PartNumberPrefix + `\s+` + consts.PartNumberDescription + `\s+(.+)$`),
	serialNumber: regexp.MustCompile(`^\s*` + consts.SerialNumberPrefix + `\s+` + consts.SerialNumberDescription + `\s+(.+)$`),
	modelName:    regexp.MustCompile(`^\s*` + consts.ModelNamePrefix + `\s+` + consts.ModelNameDescription + `\s+(.+)$`),
}

// mstvpd output is "KEY: <value>" with model name under "ID:" (no Board Id / IDTAG column).
var mstvpdPatterns = vpdOutputPatterns{
	partNumber:   regexp.MustCompile(`^\s*PN:\s+(.+)$`),
	serialNumber: regexp.MustCompile(`^\s*SN:\s+(.+)$`),
	modelName:    regexp.MustCompile(`^\s*ID:\s+(.+)$`),
}

// GetVPD retrieves Part Number, Serial Number, and Model Name for a PCI device.
// Primary: mlxvpd (MFT). Fallback: mstvpd (mstflint) — used when mlxvpd fails all
// retries or its output is unparseable. MFT 4.36 segfaults on BlueField-4 PF0;
// mstvpd reads /sys/bus/pci/devices/<pci>/vpd directly and is hardware-agnostic.
func (d *deviceDiscoveryUtils) GetVPD(pciAddr string) (*types.VPD, error) {
	log.Log.Info("HostUtils.GetVPD()", "pciAddr", pciAddr)

	vpd, mlxvpdErr := d.getVPDViaMlxvpd(pciAddr)
	if mlxvpdErr == nil {
		return vpd, nil
	}
	log.Log.Info("GetVPD(): mlxvpd failed, falling back to mstvpd", "pciAddr", pciAddr, "error", mlxvpdErr.Error())

	vpd, mstvpdErr := d.getVPDViaMstvpd(pciAddr)
	if mstvpdErr != nil {
		return nil, fmt.Errorf("both mlxvpd and mstvpd failed: mlxvpd: %w; mstvpd: %v", mlxvpdErr, mstvpdErr)
	}
	return vpd, nil
}

func (d *deviceDiscoveryUtils) getVPDViaMlxvpd(pciAddr string) (*types.VPD, error) {
	output, err := runCommandWithRetry(d.execInterface, "mlxvpd", []string{"-d", pciAddr}, mlxvpdMaxAttempts, mlxvpdBackoff)
	if err != nil {
		return nil, fmt.Errorf("mlxvpd failed after %d attempts: %w", mlxvpdMaxAttempts, err)
	}
	return parseVPDOutput(output, mlxvpdPatterns)
}

func (d *deviceDiscoveryUtils) getVPDViaMstvpd(pciAddr string) (*types.VPD, error) {
	output, err := d.execInterface.Command("mstvpd", pciAddr).CombinedOutput()
	log.Log.V(2).Info("command output", "command", "mstvpd", "output", string(output))
	if err != nil {
		return nil, fmt.Errorf("mstvpd failed: %w", err)
	}
	return parseVPDOutput(output, mstvpdPatterns)
}

// parseVPDOutput scans VPD tool output line-by-line, extracting the first
// capture group of each matching regex. Returns an error if PN or SN is missing.
func parseVPDOutput(output []byte, p vpdOutputPatterns) (*types.VPD, error) {
	var partNumber, serialNumber, modelName string

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if m := p.partNumber.FindStringSubmatch(line); len(m) > 1 {
			partNumber = strings.TrimSpace(m[1])
		} else if m := p.serialNumber.FindStringSubmatch(line); len(m) > 1 {
			serialNumber = strings.TrimSpace(m[1])
		} else if m := p.modelName.FindStringSubmatch(line); len(m) > 1 {
			modelName = strings.TrimSpace(m[1])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading VPD output: %w", err)
	}
	if partNumber == "" || serialNumber == "" {
		return nil, fmt.Errorf("VPD output missing part number (%q) or serial number (%q)", partNumber, serialNumber)
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

// isPhysicalPort checks if a network interface under a PCI device is a physical port (PF uplink)
// rather than a VF/SF representor. It reads the phys_port_name sysfs attribute:
// - PF uplinks have phys_port_name like "p0", "p1"
// - Representors have phys_port_name like "pf0vf0", "pf0sf0"
// Returns true if the interface is a physical port or if phys_port_name cannot be determined
// (backward compatibility for devices that don't expose this attribute).
func isPhysicalPort(basePath, pciAddr, ifaceName string) bool {
	physPortNamePath := filepath.Join(basePath, pciAddr, "net", ifaceName, "phys_port_name")
	data, err := os.ReadFile(physPortNamePath)
	if err != nil {
		// File doesn't exist or can't be read — assume it's a physical port for backward compatibility
		return true
	}
	portName := strings.TrimSpace(string(data))
	if portName == "" {
		return true
	}
	return physPortNameRegex.MatchString(portName)
}

func getNetNames(pciAddr string) ([]string, error) {
	return getNetNamesFromPath(pciDevicesPath, pciAddr)
}

func getNetNamesFromPath(basePath, pciAddr string) ([]string, error) {
	netDir := filepath.Join(basePath, pciAddr, "net")
	if _, err := os.Lstat(netDir); err != nil {
		return nil, fmt.Errorf("GetNetNames(): no net directory under pci device %s: %q", pciAddr, err)
	}

	fInfos, err := os.ReadDir(netDir)
	if err != nil {
		return nil, fmt.Errorf("GetNetNames(): failed to read net directory %s: %q", netDir, err)
	}

	names := make([]string, 0)
	for _, f := range fInfos {
		name := f.Name()
		if isPhysicalPort(basePath, pciAddr, name) {
			names = append(names, name)
		}
	}

	return names, nil
}

// NewDeviceDiscoveryUtils creates a new DeviceDiscoveryUtils instance
func NewDeviceDiscoveryUtils() DeviceDiscoveryUtils {
	return &deviceDiscoveryUtils{execInterface: execUtils.New()}
}
