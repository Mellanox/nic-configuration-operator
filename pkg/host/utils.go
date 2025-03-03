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
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Mellanox/rdmamap"
	"github.com/jaypipes/ghw"
	"github.com/jaypipes/ghw/pkg/pci"
	"github.com/vishvananda/netlink"
	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

const pciDevicesPath = "/sys/bus/pci/devices"
const arrayPrefix = "Array"

// HostUtils is an interface that contains util functions that perform operations on the actual host
type HostUtils interface {
	// GetPCIDevices returns a list of PCI devices on the host
	GetPCIDevices() ([]*pci.Device, error)
	// GetPartAndSerialNumber uses mstvpd util to retrieve Part and Serial numbers of the PCI device
	GetPartAndSerialNumber(pciAddr string) (string, string, error)
	// GetFirmwareVersionAndPSID uses mstflint tool to retrieve FW version and PSID of the device
	GetFirmwareVersionAndPSID(pciAddr string) (string, string, error)
	// GetPCILinkSpeed return PCI bus speed in GT/s
	GetPCILinkSpeed(pciAddr string) (int, error)
	// GetMaxReadRequestSize returns MaxReadRequest size for PCI device
	GetMaxReadRequestSize(pciAddr string) (int, error)
	// GetTrustAndPFC returns trust and pfc settings for network interface
	GetTrustAndPFC(interfaceName string) (string, string, error)
	// GetRDMADeviceName returns a RDMA device name for the given PCI address
	GetRDMADeviceName(pciAddr string) string
	// GetInterfaceName returns a network interface name for the given PCI address
	GetInterfaceName(pciAddr string) string
	// GetLinkType return the link type of the net device (Ethernet / Infiniband)
	GetLinkType(name string) string
	// IsSriovVF return true if the device is a SRIOV VF, false otherwise
	IsSriovVF(pciAddr string) bool
	// QueryNvConfig queries nv config for a mellanox device and returns default, current and next boot configs
	QueryNvConfig(ctx context.Context, pciAddr string) (types.NvConfigQuery, error)
	// SetNvConfigParameter sets a nv config parameter for a mellanox device
	SetNvConfigParameter(pciAddr string, paramName string, paramValue string) error
	// ResetNvConfig resets NIC's nv config
	ResetNvConfig(pciAddr string) error
	// ResetNicFirmware resets NIC's firmware
	// Operation can be long, required context to be able to terminate by timeout
	// IB devices need to communicate with other nodes for confirmation
	ResetNicFirmware(ctx context.Context, pciAddr string) error
	// SetMaxReadRequestSize sets max read request size for PCI device
	SetMaxReadRequestSize(pciAddr string, maxReadRequestSize int) error
	// SetTrustAndPFC sets trust and PFC settings for a network interface
	SetTrustAndPFC(interfaceName string, trust string, pfc string) error
	// ScheduleReboot schedules reboot on the host
	ScheduleReboot() error
	// GetOfedVersion retrieves installed OFED version
	GetOfedVersion() string
	// GetHostUptimeSeconds returns the host uptime in seconds
	GetHostUptimeSeconds() (time.Duration, error)
}

type hostUtils struct {
	execInterface execUtils.Interface
}

// GetPCIDevices returns a list of PCI devices on the host
func (h *hostUtils) GetPCIDevices() ([]*pci.Device, error) {
	pciRegistry, err := ghw.PCI()
	if err != nil {
		log.Log.Error(err, "GetPCIDevices(): Failed to read PCI devices")
		return nil, err
	}

	return pciRegistry.Devices, nil
}

// GetPartAndSerialNumber uses mstvpd util to retrieve Part and Serial numbers of the PCI device
func (h *hostUtils) GetPartAndSerialNumber(pciAddr string) (string, string, error) {
	log.Log.Info("HostUtils.GetPartAndSerialNumber()", "pciAddr", pciAddr)
	cmd := h.execInterface.Command("mstvpd", pciAddr)
	output, err := cmd.Output()
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
func (h *hostUtils) GetFirmwareVersionAndPSID(pciAddr string) (string, string, error) {
	log.Log.Info("HostUtils.GetFirmwareVersionAndPSID()", "pciAddr", pciAddr)
	cmd := h.execInterface.Command("mstflint", "-d", pciAddr, "q")
	output, err := cmd.Output()
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

// GetPCILinkSpeed return PCI bus speed in GT/s
func (h *hostUtils) GetPCILinkSpeed(pciAddr string) (int, error) {
	log.Log.Info("HostUtils.GetPCILinkSpeed()", "pciAddr", pciAddr)
	cmd := h.execInterface.Command("lspci", "-vv", "-s", pciAddr)
	output, err := cmd.Output()
	if err != nil {
		log.Log.Error(err, "GetPCILinkSpeed(): Failed to run lspci")
		return -1, err
	}

	linkSpeedRegexp := regexp.MustCompile(`speed\s+([0-9.]+)gt/s`)

	// Parse the output for LnkSta
	scanner := bufio.NewScanner(strings.NewReader(string(output)))

	for scanner.Scan() {
		line := strings.TrimSpace(strings.ToLower(scanner.Text()))

		if !strings.HasPrefix(line, consts.LinkStatsPrefix) {
			continue
		}

		match := linkSpeedRegexp.FindStringSubmatch(line)
		if len(match) == 2 {
			speedValue, err := strconv.Atoi(match[1])
			if err != nil {
				log.Log.Error(err, "failed to parse link speed value", "pciAddr", pciAddr)
				return -1, err
			}

			return speedValue, nil
		}
	}

	if err := scanner.Err(); err != nil {
		log.Log.Error(err, "GetPCILinkSpeed(): Error reading lspci output")
		return -1, err
	}

	return -1, nil
}

// GetMaxReadRequestSize returns MaxReadRequest size for PCI device
func (h *hostUtils) GetMaxReadRequestSize(pciAddr string) (int, error) {
	log.Log.Info("HostUtils.GetMaxReadRequestSize()", "pciAddr", pciAddr)
	cmd := h.execInterface.Command("lspci", "-vv", "-s", pciAddr)
	output, err := cmd.Output()
	if err != nil && len(output) == 0 {
		log.Log.Error(err, "GetMaxReadRequestSize(): Failed to run lspci")
		return -1, err
	}

	maxReadReqRegexp := regexp.MustCompile(consts.MaxReadReqPrefix + `\s+(\d+)\s+bytes`)

	// Parse the output for LnkSta
	scanner := bufio.NewScanner(strings.NewReader(string(output)))

	for scanner.Scan() {
		line := strings.TrimSpace(strings.ToLower(scanner.Text()))

		if strings.Contains(line, consts.MaxReadReqPrefix) {
			match := maxReadReqRegexp.FindStringSubmatch(line)
			if len(match) != 2 {
				continue
			}

			maxReqReqSize, err := strconv.Atoi(match[1])
			if err != nil {
				log.Log.Error(err, "failed to parse max read req size", "pciAddr", pciAddr)
				return -1, err
			}

			return maxReqReqSize, nil
		}
	}

	if err := scanner.Err(); err != nil {
		log.Log.Error(err, "GetMaxReadRequestSize(): Error reading lspci output")
		return -1, err
	}

	return -1, nil
}

// GetTrustAndPFC returns trust and pfc settings for network interface
func (h *hostUtils) GetTrustAndPFC(interfaceName string) (string, string, error) {
	log.Log.Info("HostUtils.GetTrustAndPFC()", "interface", interfaceName)
	cmd := h.execInterface.Command("mlnx_qos", "-i", interfaceName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("failed to run mlnx_qos: %s", output)
		log.Log.Error(err, "GetTrustAndPFC(): Failed to run mlnx_qos")
		return "", "", err
	}

	// Parse the output for trust and pfc states
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	var trust, pfc string

	for scanner.Scan() {
		line := strings.TrimSpace(strings.ToLower(scanner.Text()))

		if strings.HasPrefix(line, consts.TrustStatePrefix) {
			trust = strings.TrimSpace(strings.TrimPrefix(line, consts.TrustStatePrefix))
		}
		if strings.HasPrefix(line, consts.PfcEnabledPrefix) {
			pfc = strings.Replace(strings.TrimSpace(strings.TrimPrefix(line, consts.PfcEnabledPrefix)), "   ", ",", -1)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Log.Error(err, "GetTrustAndPFC(): Error reading mlnx_qos output")
		return "", "", err
	}

	if trust == "" || pfc == "" {
		return "", "", fmt.Errorf("GetTrustAndPFC(): trust (%v) or pfc (%v) is empty", trust, pfc)
	}

	return trust, pfc, nil
}

// GetLinkType return the link type of the net device (Ethernet / Infiniband)
func (h *hostUtils) GetLinkType(name string) string {
	log.Log.Info("HostUtils.GetLinkType()", "name", name)
	link, err := netlink.LinkByName(name)
	if err != nil {
		log.Log.Error(err, "GetLinkType(): failed to get link", "device", name)
		return ""
	}
	return encapTypeToLinkType(link.Attrs().EncapType)
}

func encapTypeToLinkType(encapType string) string {
	if encapType == "ether" {
		return consts.Ethernet
	} else if encapType == "infiniband" {
		return consts.Infiniband
	}
	return ""
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

// GetInterfaceName returns a network interface name for the given PCI address
func (h *hostUtils) GetInterfaceName(pciAddr string) string {
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
func (h *hostUtils) IsSriovVF(pciAddr string) bool {
	log.Log.Info("HostUtils.IsSriovVF()", "pciAddr", pciAddr)

	totalVfFilePath := filepath.Join(pciDevicesPath, pciAddr, "physfn")
	if _, err := os.Stat(totalVfFilePath); err != nil {
		return false
	}
	return true
}

// GetRDMADeviceName returns a RDMA device name for the given PCI address
func (h *hostUtils) GetRDMADeviceName(pciAddr string) string {
	log.Log.Info("HostUtils.GetRDMADeviceName()", "pciAddr", pciAddr)

	rdmaDevices := rdmamap.GetRdmaDevicesForPcidev(pciAddr)

	if len(rdmaDevices) < 1 {
		log.Log.Info("GetRDMADeviceName(): No RDMA device found for device", "address", pciAddr)
		return ""
	}

	log.Log.V(1).Info("Rdma device", "pciAddr", pciAddr, "name", rdmaDevices[0])
	return rdmaDevices[0]
}

// queryMLXConfig runs a query on mlxconfig to parse out default, current and nextboot configurations
// might run recursively to expand array parameters' values
func (h *hostUtils) queryMLXConfig(ctx context.Context, query types.NvConfigQuery, pciAddr string, additionalParameter string) error {
	log.Log.Info(fmt.Sprintf("mlxconfig -d %s query %s", pciAddr, additionalParameter)) //TODO change verbosity
	valueInBracketsRegex := regexp.MustCompile(`^(.*?)\(([^)]*)\)$`)

	var cmd execUtils.Cmd
	if additionalParameter == "" {
		cmd = h.execInterface.CommandContext(ctx, "mlxconfig", "-d", pciAddr, "-e", "query")
	} else {
		cmd = h.execInterface.CommandContext(ctx, "mlxconfig", "-d", pciAddr, "-e", "query", additionalParameter)
	}
	output, err := cmd.Output()
	if err != nil {
		log.Log.Error(err, "queryMLXConfig(): Failed to run mlxconfig", "output", string(output))
		return err
	}

	inConfigSection := false
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()

		// Trim leading and trailing whitespace
		line = strings.TrimSpace(line)

		// Skip empty lines
		if line == "" {
			continue
		}
		// Check for the start of the configurations section
		if strings.HasPrefix(line, "Configurations:") {
			inConfigSection = true
			continue
		}

		// If in configurations section, parse the additionalParameters
		if inConfigSection {
			// Check if we have reached the end of the configurations section
			// In this example, we'll assume the configurations end when the scanner reaches EOF
			// Alternatively, you can check for specific markers or conditions

			if strings.HasPrefix(line, "*") {
				line = strings.TrimPrefix(line, "*")
				line = strings.TrimSpace(line)
			}

			// Replace multiple spaces with a single tab character
			spaceRe := regexp.MustCompile(`\s{2,}`)
			line = spaceRe.ReplaceAllString(line, "\t")

			fields := strings.Split(line, "\t")
			if len(fields) != 4 {
				// Line does not contain additionalParameters and values, skipping
				continue
			}

			for i := range fields {
				fields[i] = strings.TrimSpace(fields[i])
			}

			paramName := fields[0]
			defaultVal := fields[1]
			currentVal := fields[2]
			nextBootVal := fields[3]

			// If the parameter value is an array, we want to extract values for all indices
			if strings.HasPrefix(defaultVal, arrayPrefix) {
				err = h.queryMLXConfig(ctx, query, pciAddr, paramName+strings.TrimPrefix(defaultVal, arrayPrefix))
				if err != nil {
					return err
				}
				continue
			}

			// If the actual value is wrapped in brackets, we want to save both it and its string alias
			match := valueInBracketsRegex.FindStringSubmatch(defaultVal)
			if len(match) == 3 {
				for i, v := range match {
					match[i] = strings.ToLower(v)
				}
				query.DefaultConfig[paramName] = match[1:]

				match = valueInBracketsRegex.FindStringSubmatch(currentVal)
				for i, v := range match {
					match[i] = strings.ToLower(v)
				}
				query.CurrentConfig[paramName] = match[1:]

				match = valueInBracketsRegex.FindStringSubmatch(nextBootVal)
				for i, v := range match {
					match[i] = strings.ToLower(v)
				}
				query.NextBootConfig[paramName] = match[1:]
			} else {
				query.DefaultConfig[paramName] = []string{defaultVal}
				query.CurrentConfig[paramName] = []string{currentVal}
				query.NextBootConfig[paramName] = []string{nextBootVal}
			}

		}
	}

	return nil
}

// QueryNvConfig queries nv config for a mellanox device and returns default, current and next boot configs
func (h *hostUtils) QueryNvConfig(ctx context.Context, pciAddr string) (types.NvConfigQuery, error) {
	log.Log.Info("HostUtils.QueryNvConfig()", "pciAddr", pciAddr)

	query := types.NewNvConfigQuery()

	err := h.queryMLXConfig(ctx, query, pciAddr, "")
	if err != nil {
		log.Log.Error(err, "Failed to parse mlxconfig query output", "device", pciAddr)
	}

	return query, err
}

// SetNvConfigParameter sets a nv config parameter for a mellanox device
func (h *hostUtils) SetNvConfigParameter(pciAddr string, paramName string, paramValue string) error {
	log.Log.Info("HostUtils.SetNvConfigParameter()", "pciAddr", pciAddr, "paramName", paramName, "paramValue", paramValue)

	cmd := h.execInterface.Command("mlxconfig", "-d", pciAddr, "--yes", "set", paramName+"="+paramValue)
	_, err := cmd.Output()
	if err != nil {
		log.Log.Error(err, "SetNvConfigParameter(): Failed to run mlxconfig")
		return err
	}
	return nil
}

// ResetNvConfig resets NIC's nv config
func (h *hostUtils) ResetNvConfig(pciAddr string) error {
	log.Log.Info("HostUtils.ResetNvConfig()", "pciAddr", pciAddr)

	cmd := h.execInterface.Command("mlxconfig", "-d", pciAddr, "--yes", "reset")
	_, err := cmd.Output()
	if err != nil {
		log.Log.Error(err, "ResetNvConfig(): Failed to run mlxconfig")
		return err
	}
	return nil
}

// ResetNicFirmware resets NIC's firmware
// Operation can be long, required context to be able to terminate by timeout
// IB devices need to communicate with other nodes for confirmation
func (h *hostUtils) ResetNicFirmware(ctx context.Context, pciAddr string) error {
	log.Log.Info("HostUtils.ResetNicFirmware()", "pciAddr", pciAddr)

	cmd := h.execInterface.CommandContext(ctx, "mlxfwreset", "--device", pciAddr, "reset", "--yes")
	_, err := cmd.Output()
	if err != nil {
		log.Log.Error(err, "ResetNicFirmware(): Failed to run mlxfwreset")
		return err
	}
	return nil
}

// SetMaxReadRequestSize sets max read request size for PCI device
func (h *hostUtils) SetMaxReadRequestSize(pciAddr string, maxReadRequestSize int) error {
	log.Log.Info("HostUtils.SetMaxReadRequestSize()", "pciAddr", pciAddr, "maxReadRequestSize", maxReadRequestSize)

	// Meaning of the value is explained here:
	// https://enterprise-support.nvidia.com/s/article/understanding-pcie-configuration-for-maximum-performance#PCIe-Max-Read-Request
	readReqSizeToIndex := map[int]int{
		128:  0,
		256:  1,
		512:  2,
		1024: 3,
		2048: 4,
		4096: 5,
	}

	valueToApply, found := readReqSizeToIndex[maxReadRequestSize]
	if !found {
		err := fmt.Errorf("unsupported maxReadRequestSize (%d) for pci device (%s). Acceptable values are powers of 2 from 128 to 4096", maxReadRequestSize, pciAddr)
		log.Log.Error(err, "failed to set maxReadRequestSize", "pciAddr", pciAddr, "maxReadRequestSize", maxReadRequestSize)
		return err
	}

	cmd := h.execInterface.Command("setpci", "-s", pciAddr, fmt.Sprintf("CAP_EXP+08.w=%d000:F000", valueToApply))
	_, err := cmd.Output()
	if err != nil {
		log.Log.Error(err, "SetMaxReadRequestSize(): Failed to run setpci")
		return err
	}
	return nil
}

// SetTrustAndPFC sets trust and PFC settings for a network interface
func (h *hostUtils) SetTrustAndPFC(interfaceName string, trust string, pfc string) error {
	log.Log.Info("HostUtils.SetTrustAndPFC()", "interfaceName", interfaceName, "trust", trust, "pfc", pfc)

	cmd := h.execInterface.Command("mlnx_qos", "-i", interfaceName, "--trust", trust, "--pfc", pfc)
	output, err := cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("failed to run mlnx_qos: %s", output)
		log.Log.Error(err, "SetTrustAndPFC(): Failed to run mlnx_qos")
		return err
	}
	return nil
}

func (h *hostUtils) ScheduleReboot() error {
	log.Log.Info("HostUtils.ScheduleReboot()")
	root, err := os.Open("/")
	if err != nil {
		log.Log.Error(err, "ScheduleReboot(): Failed to os.Open")
		return err
	}

	if err := syscall.Chroot(consts.HostPath); err != nil {
		err := root.Close()
		if err != nil {
			log.Log.Error(err, "ScheduleReboot(): Failed to syscall.Chroot")
			return err
		}
		return err
	}

	defer func() {
		if err := root.Close(); err != nil {
			log.Log.Error(err, "ScheduleReboot(): Failed to os.Close")
			return
		}
		if err := root.Chdir(); err != nil {
			log.Log.Error(err, "ScheduleReboot(): Failed to os.Chdir")
			return
		}
		if err = syscall.Chroot("."); err != nil {
			log.Log.Error(err, "ScheduleReboot(): Failed to syscall.Chroot")
		}
	}()

	cmd := h.execInterface.Command("shutdown", "-r", "now")
	_, err = cmd.Output()
	if err != nil {
		log.Log.Error(err, "ScheduleReboot(): Failed to run shutdown -r now")
		return err
	}
	return nil
}

// GetOfedVersion retrieves installed OFED version
func (h *hostUtils) GetOfedVersion() string {
	log.Log.Info("HostUtils.GetOfedVersion()")
	versionBytes, err := os.ReadFile(filepath.Join(consts.HostPath, consts.Mlx5ModuleVersionPath))
	if err != nil {
		log.Log.Error(err, "GetOfedVersion(): failed to read mlx5_core version file, OFED isn't installed")
		return ""
	}
	version := strings.TrimSuffix(string(versionBytes), "\n")
	log.Log.Info("HostUtils.GetOfedVersion(): OFED version", "version", version)
	return version
}

// GetHostUptimeSeconds returns the host uptime in seconds
func (h *hostUtils) GetHostUptimeSeconds() (time.Duration, error) {
	log.Log.V(2).Info("HostUtils.GetHostUptimeSeconds()")
	output, err := os.ReadFile("/proc/uptime")
	if err != nil {
		log.Log.Error(err, "HostUtils.GetHostUptimeSeconds(): failed to read the system's uptime")
		return 0, err
	}
	uptimeStr := strings.Split(string(output), " ")[0]
	uptimeSeconds, _ := strconv.ParseFloat(uptimeStr, 64)

	return time.Duration(uptimeSeconds) * time.Second, nil
}

func NewHostUtils() HostUtils {
	return &hostUtils{execInterface: execUtils.New()}
}

func isBluefieldDevice(productType string) bool {
	// TODO implement
	return false
}
