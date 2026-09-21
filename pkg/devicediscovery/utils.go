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
	"sort"
	"strconv"
	"strings"

	"github.com/Mellanox/rdmamap"
	"github.com/jaypipes/ghw"
	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/vishvananda/netlink"
	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

const pciDevicesPath = "/sys/bus/pci/devices"

const fwctlDevicesPath = "/dev/fwctl"

// physPortNameRegex matches physical port names like "p0", "p1" — the PF uplink interfaces.
// VF/SF representors have names like "pf0vf0", "pf0sf0" which do NOT match this pattern.
var physPortNameRegex = regexp.MustCompile(`^p\d+$`)

type DeviceDiscoveryUtils interface {
	// GetPCIDevices returns a list of PCI devices on the host
	GetPCIDevices() ([]*pci.Device, error)

	// GetVPD reads the kernel-exposed PCI VPD and retrieves Part Number, Serial Number, and Model Name.
	GetVPD(pciAddr string) (*types.VPD, error)

	// GetFirmwareVersionAndPSID retrieves the FW version and PSID of the device
	GetFirmwareVersionAndPSID(pciAddr string) (string, string, error)

	// GetRDMADeviceName returns a RDMA device name for the given PCI address
	GetRDMADeviceName(pciAddr string) string

	// GetInterfaceName returns a network interface name for the given PCI address
	GetInterfaceName(pciAddr string) string

	// GetFwctlDevice returns a fwctl character device path for the given PCI address.
	// Missing or unreadable sysfs state is logged and treated as no fwctl device.
	GetFwctlDevice(pciAddr string) string

	// IsSriovVF return true if the device is a SRIOV VF, false otherwise
	IsSriovVF(pciAddr string) bool

	// IsZeroTrust uses mlxprivhost tool to check if the device is in zero-trust mode
	IsZeroTrust(pciAddr string) (bool, error)

	// GetNetworkBayASIC reports whether the device is part of a ConnectX-9 Network Bay
	// ("orchid") card and, if so, its ASIC index (0 or 1). It reads the MGIR register's
	// ga / ga_valid fields via mlxreg. isOrchid is false when the device is a standalone
	// CX9 (ga_valid == 0) or when mlxreg is unavailable / its output cannot be parsed.
	// This never returns an error: detection failures must not block device discovery.
	GetNetworkBayASIC(pciAddr string) (asic int, isOrchid bool)
}

type deviceDiscoveryUtils struct {
	execInterface  execUtils.Interface
	getDevlinkInfo func(bus, device string) (map[string]string, error)
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

// GetFirmwareVersionAndPSID uses flint to retrieve the FW version and PSID of the device.
// If flint cannot query the device, it falls back to the kernel devlink interface.
func (d *deviceDiscoveryUtils) GetFirmwareVersionAndPSID(pciAddr string) (string, string, error) {
	log.Log.Info("HostUtils.GetFirmwareVersionAndPSID()", "pciAddr", pciAddr)
	firmwareVersion, psid, flintErr := d.getFirmwareVersionAndPSIDViaFlint(pciAddr)
	if flintErr == nil {
		return firmwareVersion, psid, nil
	}

	log.Log.Info("GetFirmwareVersionAndPSID(): flint failed, falling back to devlink", "pciAddr", pciAddr, "error", flintErr.Error())
	firmwareVersion, psid, devlinkErr := d.getFirmwareVersionAndPSIDViaDevlink(pciAddr)
	if devlinkErr != nil {
		return "", "", fmt.Errorf("failed to get firmware version and PSID: flint: %w; devlink: %w", flintErr, devlinkErr)
	}

	return firmwareVersion, psid, nil
}

func (d *deviceDiscoveryUtils) getFirmwareVersionAndPSIDViaFlint(pciAddr string) (string, string, error) {
	cmd := d.execInterface.Command("flint", "-d", pciAddr, "q")
	output, err := utils.RunCommand(cmd)
	if err != nil {
		return "", "", fmt.Errorf("failed to run flint: %w", err)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	var firmwareVersion, psid string

	for scanner.Scan() {
		line := strings.ToLower(scanner.Text())

		if strings.HasPrefix(line, consts.FirmwareVersionPrefix) {
			firmwareVersion = strings.TrimSpace(strings.TrimPrefix(line, consts.FirmwareVersionPrefix))
		}
		if strings.HasPrefix(line, consts.PSIDPrefix) {
			psid = strings.TrimSpace(strings.TrimPrefix(line, consts.PSIDPrefix))
		}
	}

	if err := scanner.Err(); err != nil {
		return "", "", fmt.Errorf("failed to read flint output: %w", err)
	}

	if firmwareVersion == "" || psid == "" {
		return "", "", fmt.Errorf("flint output has empty firmware version (%q) or PSID (%q)", firmwareVersion, psid)
	}

	return firmwareVersion, psid, nil
}

func (d *deviceDiscoveryUtils) getFirmwareVersionAndPSIDViaDevlink(pciAddr string) (string, string, error) {
	getDevlinkInfo := d.getDevlinkInfo
	if getDevlinkInfo == nil {
		getDevlinkInfo = netlink.DevlinkGetDeviceInfoByNameAsMap
	}

	info, err := getDevlinkInfo("pci", pciAddr)
	if err != nil {
		return "", "", fmt.Errorf("failed to query devlink device pci/%s: %w", pciAddr, err)
	}

	firmwareVersion := strings.TrimSpace(info["fw.version"])
	if firmwareVersion == "" {
		firmwareVersion = strings.TrimSpace(info["fw"])
	}
	psid := strings.TrimSpace(info["fw.psid"])
	if firmwareVersion == "" || psid == "" {
		return "", "", fmt.Errorf("devlink info has empty firmware version (%q) or PSID (%q)", firmwareVersion, psid)
	}

	return strings.ToLower(firmwareVersion), strings.ToLower(psid), nil
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

// GetFwctlDevice returns a fwctl character device path for the given PCI address.
func (d *deviceDiscoveryUtils) GetFwctlDevice(pciAddr string) string {
	log.Log.Info("DeviceDiscoveryUtils.GetFwctlDevice()", "pciAddr", pciAddr)

	fwctlDevice, err := getFwctlDeviceFromPath(pciDevicesPath, pciAddr)
	if err != nil {
		log.Log.V(1).Info("GetFwctlDevice(): fwctl device not found", "pciAddr", pciAddr, "error", err.Error())
		return ""
	}

	log.Log.Info("fwctl device", "pciAddr", pciAddr, "device", fwctlDevice)
	return fwctlDevice
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

// GetNetworkBayASIC reports whether the device is part of a ConnectX-9 Network Bay card and,
// if so, its ASIC index. See the interface doc for semantics.
func (d *deviceDiscoveryUtils) GetNetworkBayASIC(pciAddr string) (int, bool) {
	log.Log.Info("HostUtils.GetNetworkBayASIC()", "pciAddr", pciAddr)

	output, err := d.execInterface.Command("mlxreg", "-d", pciAddr, "--reg_name", "MGIR", "-g").CombinedOutput()
	log.Log.V(2).Info("command output", "command", "mlxreg", "pciAddr", pciAddr, "output", string(output))
	if err != nil {
		// mlxreg may be unavailable or the register unsupported on non-orchid hardware.
		// Per design, log and skip silently — never block device discovery.
		log.Log.V(1).Info("GetNetworkBayASIC(): mlxreg failed, treating device as non-orchid", "pciAddr", pciAddr, "error", err.Error())
		return 0, false
	}

	gaValid, ga, ok := parseMGIRGa(output)
	if !ok {
		log.Log.V(1).Info("GetNetworkBayASIC(): could not parse MGIR ga/ga_valid, treating device as non-orchid", "pciAddr", pciAddr)
		return 0, false
	}
	if !gaValid {
		// ga_valid == 0 → device is a standalone CX9, not part of a Network Bay.
		return 0, false
	}
	return ga, true
}

// mgirFieldRegex matches a `<key> | 0x<hex>` line from `mlxreg --reg_name MGIR -g` output.
var mgirFieldRegex = regexp.MustCompile(`^\s*(\w+)\s*\|\s*0x([0-9a-fA-F]+)\s*$`)

// parseMGIRGa extracts the ga and ga_valid fields from MGIR register dump output.
// Returns gaValid (ga_valid != 0), ga (numeric value), and ok=true only when both
// fields were found and parsed.
func parseMGIRGa(output []byte) (gaValid bool, ga int, ok bool) {
	var foundGa, foundGaValid bool
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		m := mgirFieldRegex.FindStringSubmatch(scanner.Text())
		if len(m) != 3 {
			continue
		}
		// ga / ga_valid are small flag fields; parse with bitSize 32 so the int conversion
		// below cannot overflow on 32-bit platforms (an out-of-range value fails to parse
		// and is simply skipped).
		val, err := strconv.ParseInt(m[2], 16, 32)
		if err != nil {
			continue
		}
		switch m[1] {
		case "ga":
			ga = int(val)
			foundGa = true
		case "ga_valid":
			gaValid = val != 0
			foundGaValid = true
		}
	}
	return gaValid, ga, foundGa && foundGaValid
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

func getFwctlDeviceFromPath(pciDevicesBasePath, pciAddr string) (string, error) {
	fwctlDir := filepath.Join(pciDevicesBasePath, pciAddr, "fwctl")
	entries, err := os.ReadDir(fwctlDir)
	if err != nil {
		return "", fmt.Errorf("GetFwctlDevice(): failed to read fwctl directory %s: %w", fwctlDir, err)
	}

	deviceNames := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "fwctl") {
			deviceNames = append(deviceNames, name)
		}
	}
	if len(deviceNames) == 0 {
		return "", fmt.Errorf("GetFwctlDevice(): no fwctl entries under pci device %s", pciAddr)
	}
	sort.Strings(deviceNames)

	return filepath.Join(fwctlDevicesPath, deviceNames[0]), nil
}

// NewDeviceDiscoveryUtils creates a new DeviceDiscoveryUtils instance
func NewDeviceDiscoveryUtils() DeviceDiscoveryUtils {
	return &deviceDiscoveryUtils{
		execInterface:  execUtils.New(),
		getDevlinkInfo: netlink.DevlinkGetDeviceInfoByNameAsMap,
	}
}
