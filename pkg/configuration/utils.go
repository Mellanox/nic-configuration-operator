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

package configuration

import (
	"bufio"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/dms"
)

var (
	linkSpeedRegexp  = regexp.MustCompile(`speed\s+([0-9.]+)gt/s`)
	maxReadReqRegexp = regexp.MustCompile(consts.MaxReadReqPrefix + `\s+(\d+)\s+bytes`)
	cableLenRegexp   = regexp.MustCompile(`(?i)cable[_ ]len\w*\s*:\s*(\d+)`)
	roceModeRegexp   = regexp.MustCompile(`(?:roce\s*v|mode\s+)(\d+)\b`)
)

// ConfigurationUtils is an interface that contains util functions related to NIC Configuration
type ConfigurationUtils interface {
	// GetLinkType return the link type of the net device (Ethernet / Infiniband)
	GetLinkType(name string) string
	// GetPCILinkSpeed return PCI bus speed in GT/s
	GetPCILinkSpeed(pciAddr string) (int, error)
	// GetMaxReadRequestSize returns MaxReadRequest size for PCI device
	GetMaxReadRequestSize(pciAddr string) (int, error)
	// GetQoSSettings returns trust and pfc settings for network interface
	GetQoSSettings(device *v1alpha1.NicDevice, interfaceName string) (*v1alpha1.QosSpec, error)

	// SetMaxReadRequestSize sets max read request size for PCI device
	SetMaxReadRequestSize(pciAddr string, maxReadRequestSize int) error
	// SetQoSSettings sets trust and PFC settings for a network interface
	SetQoSSettings(device *v1alpha1.NicDevice, spec *v1alpha1.QosSpec) error

	// GetRoceMode returns the current RoCE mode (1 or 2) for a network interface
	GetRoceMode(interfaceName string) (int, error)
	// SetRoceMode sets the RoCE mode (1 or 2) for a network interface
	SetRoceMode(interfaceName string, mode int) error

	// GetCableLen returns the configured cable length for a network interface
	GetCableLen(interfaceName string) (int, error)
	// SetCableLen sets the cable length for a network interface
	SetCableLen(interfaceName string, length int) error

	// GetECNEnabled returns whether ECN is enabled for roce_rp and roce_np on a given priority
	GetECNEnabled(interfaceName string, priority int) (rp bool, np bool, err error)
	// SetECNEnabled enables/disables ECN for roce_rp and roce_np on a given priority
	SetECNEnabled(interfaceName string, priority int, rp bool, np bool) error

	// GetPauseFrames returns whether global pause frames are enabled
	GetPauseFrames(interfaceName string) (bool, error)
	// SetPauseFrames enables/disables global pause frames (autoneg, rx, tx)
	SetPauseFrames(interfaceName string, enabled bool) error

	// GetRingSize returns the current rx and tx ring buffer sizes
	GetRingSize(interfaceName string) (rx int, tx int, err error)
	// SetRingSize sets the rx and tx ring buffer sizes
	SetRingSize(interfaceName string, rx int, tx int) error

	// GetCombinedChannels returns the current number of combined channels
	GetCombinedChannels(interfaceName string) (int, error)
	// SetCombinedChannels sets the number of combined channels
	SetCombinedChannels(interfaceName string, channels int) error

	// GetLRO returns whether Large Receive Offload is enabled
	GetLRO(interfaceName string) (bool, error)
	// SetLRO enables/disables Large Receive Offload
	SetLRO(interfaceName string, enabled bool) error

	// ResetNicFirmware resets NIC's firmware
	// Operation can be long, required context to be able to terminate by timeout
	// IB devices need to communicate with other nodes for confirmation
	ResetNicFirmware(ctx context.Context, pciAddr string) error
}

type configurationUtils struct {
	execInterface execUtils.Interface
	dmsManager    dms.DMSManager
}

// GetLinkType return the link type of the net device (Ethernet / Infiniband)
func (d *configurationUtils) GetLinkType(name string) string {
	log.Log.Info("ConfigurationUtils.GetLinkType()", "name", name)
	link, err := netlink.LinkByName(name)
	if err != nil {
		log.Log.Error(err, "GetLinkType(): failed to get link", "device", name)
		return ""
	}
	return encapTypeToLinkType(link.Attrs().EncapType)
}

// GetPCILinkSpeed return PCI bus speed in GT/s
func (h *configurationUtils) GetPCILinkSpeed(pciAddr string) (int, error) {
	log.Log.Info("ConfigurationUtils.GetPCILinkSpeed()", "pciAddr", pciAddr)
	cmd := h.execInterface.Command("lspci", "-vv", "-s", pciAddr)
	output, err := cmd.CombinedOutput()
	log.Log.V(2).Info("GetPCILinkSpeed(): command output", "output", string(output))
	if err != nil {
		log.Log.Error(err, "GetPCILinkSpeed(): Failed to run lspci")
		return -1, err
	}

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
func (h *configurationUtils) GetMaxReadRequestSize(pciAddr string) (int, error) {
	log.Log.Info("ConfigurationUtils.GetMaxReadRequestSize()", "pciAddr", pciAddr)
	cmd := h.execInterface.Command("lspci", "-vv", "-s", pciAddr)
	output, err := cmd.CombinedOutput()
	log.Log.V(2).Info("GetMaxReadRequestSize(): command output", "output", string(output))
	if err != nil && len(output) == 0 {
		log.Log.Error(err, "GetMaxReadRequestSize(): Failed to run lspci")
		return -1, err
	}

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

// GetQoSSettings returns trust and pfc settings for network interface
func (h *configurationUtils) GetQoSSettings(device *v1alpha1.NicDevice, interfaceName string) (*v1alpha1.QosSpec, error) {
	log.Log.Info("ConfigurationUtils.GetTrustAndPFC()", "device", device.Name)

	dmsClient, err := h.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
	if err != nil {
		log.Log.Error(err, "GetTrustAndPFC(): failed to get DMS client", "device", device.Name)
		return nil, err
	}

	spec, err := dmsClient.GetQoSSettings(interfaceName)
	if err != nil {
		log.Log.Error(err, "GetTrustAndPFC(): failed to get QoS settings", "device", device.Name)
		return nil, err
	}

	return spec, nil
}

// ResetNicFirmware resets NIC's firmware
// Operation can be long, required context to be able to terminate by timeout
// IB devices need to communicate with other nodes for confirmation
func (h *configurationUtils) ResetNicFirmware(ctx context.Context, pciAddr string) error {
	log.Log.Info("ConfigurationUtils.ResetNicFirmware()", "pciAddr", pciAddr)

	cmd := h.execInterface.CommandContext(ctx, "mlxfwreset", "--device", pciAddr, "reset", "--yes")
	output, err := cmd.CombinedOutput()
	log.Log.V(2).Info("ResetNicFirmware(): command output", "output", string(output))
	if err != nil {
		log.Log.Error(err, "ResetNicFirmware(): Failed to run mlxfwreset")
		return err
	}
	return nil
}

// SetMaxReadRequestSize sets max read request size for PCI device
func (h *configurationUtils) SetMaxReadRequestSize(pciAddr string, maxReadRequestSize int) error {
	log.Log.Info("ConfigurationUtils.SetMaxReadRequestSize()", "pciAddr", pciAddr, "maxReadRequestSize", maxReadRequestSize)

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
	output, err := cmd.CombinedOutput()
	log.Log.V(2).Info("SetMaxReadRequestSize(): command output", "output", string(output))
	if err != nil {
		log.Log.Error(err, "SetMaxReadRequestSize(): Failed to run setpci")
		return err
	}
	return nil
}

// SetQoSSettings sets trust and PFC settings for a network interface
func (h *configurationUtils) SetQoSSettings(device *v1alpha1.NicDevice, spec *v1alpha1.QosSpec) error {
	log.Log.Info("ConfigurationUtils.SetTrustAndPFC()", "device", device.Name, "spec", spec)

	dmsClient, err := h.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
	if err != nil {
		log.Log.Error(err, "SetTrustAndPFC(): failed to get DMS client", "device", device.Name)
		return err
	}

	if err := dmsClient.SetQoSSettings(spec); err != nil {
		log.Log.Error(err, "SetTrustAndPFC(): failed to set QoS settings", "device", device.Name)
		return err
	}

	return nil
}

// nsenterWithFlags wraps a command with nsenter using the specified namespace flags.
func (h *configurationUtils) nsenterWithFlags(nsFlags []string, args ...string) execUtils.Cmd {
	fullArgs := append([]string{"-t", "1"}, nsFlags...)
	fullArgs = append(fullArgs, "--")
	fullArgs = append(fullArgs, args...)
	return h.execInterface.Command("nsenter", fullArgs...)
}

// nsenterCommand wraps a command with nsenter to run in the host's network namespace.
func (h *configurationUtils) nsenterCommand(args ...string) execUtils.Cmd {
	return h.nsenterWithFlags([]string{"-n"}, args...)
}

// nsenterMountNetCommand wraps a command with nsenter to run in the host's mount and network namespaces.
// Required for accessing host sysfs paths (e.g. /sys/class/net/...).
func (h *configurationUtils) nsenterMountNetCommand(args ...string) execUtils.Cmd {
	return h.nsenterWithFlags([]string{"-m", "-n"}, args...)
}

// GetRoceMode returns the current RoCE mode (1 or 2)
func (h *configurationUtils) GetRoceMode(interfaceName string) (int, error) {
	log.Log.V(2).Info("ConfigurationUtils.GetRoceMode()", "interface", interfaceName)
	cmd := h.nsenterCommand("cma_roce_mode", "-d", interfaceName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Log.Error(err, "GetRoceMode(): failed to run cma_roce_mode", "interface", interfaceName)
		return 0, err
	}
	outputStr := strings.ToLower(strings.TrimSpace(string(output)))
	if m := roceModeRegexp.FindStringSubmatch(outputStr); m != nil {
		mode, _ := strconv.Atoi(m[1])
		switch mode {
		case consts.RoceModeV1:
			return consts.RoceModeV1, nil
		case consts.RoceModeV2:
			return consts.RoceModeV2, nil
		}
	}
	return 0, fmt.Errorf("unexpected cma_roce_mode output: %s", outputStr)
}

// SetRoceMode sets the RoCE mode (1 or 2)
func (h *configurationUtils) SetRoceMode(interfaceName string, mode int) error {
	log.Log.Info("ConfigurationUtils.SetRoceMode()", "interface", interfaceName, "mode", mode)
	cmd := h.nsenterCommand("cma_roce_mode", "-d", interfaceName, "-m", strconv.Itoa(mode))
	_, err := cmd.CombinedOutput()
	if err != nil {
		log.Log.Error(err, "SetRoceMode(): failed to run cma_roce_mode", "interface", interfaceName, "mode", mode)
		return err
	}
	return nil
}

// GetCableLen returns the configured cable length
func (h *configurationUtils) GetCableLen(interfaceName string) (int, error) {
	log.Log.V(2).Info("ConfigurationUtils.GetCableLen()", "interface", interfaceName)
	cmd := h.nsenterCommand("mlnx_qos", "-i", interfaceName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Log.Error(err, "GetCableLen(): failed to run mlnx_qos", "interface", interfaceName)
		return 0, err
	}
	match := cableLenRegexp.FindStringSubmatch(string(output))
	if len(match) == 2 {
		val, err := strconv.Atoi(match[1])
		if err != nil {
			return 0, fmt.Errorf("failed to parse cable_len: %v", err)
		}
		return val, nil
	}
	return 0, fmt.Errorf("cable_len not found in mlnx_qos output")
}

// SetCableLen sets the cable length
func (h *configurationUtils) SetCableLen(interfaceName string, length int) error {
	log.Log.Info("ConfigurationUtils.SetCableLen()", "interface", interfaceName, "length", length)
	cmd := h.nsenterCommand("mlnx_qos", "-i", interfaceName, fmt.Sprintf("--cable_len=%d", length))
	_, err := cmd.CombinedOutput()
	if err != nil {
		log.Log.Error(err, "SetCableLen(): failed to run mlnx_qos", "interface", interfaceName, "length", length)
		return err
	}
	return nil
}

// ecnSysfsPath returns the sysfs path for ECN configuration.
// direction is "roce_rp" or "roce_np".
func ecnSysfsPath(interfaceName, direction string, priority int) string {
	return filepath.Join("/sys/class/net", interfaceName, "ecn", direction, "enable", strconv.Itoa(priority))
}

// GetECNEnabled returns whether ECN is enabled for roce_rp and roce_np on a given priority
func (h *configurationUtils) GetECNEnabled(interfaceName string, priority int) (bool, bool, error) {
	log.Log.V(2).Info("ConfigurationUtils.GetECNEnabled()", "interface", interfaceName, "priority", priority)

	readECN := func(direction string) (bool, error) {
		cmd := h.nsenterMountNetCommand("cat", ecnSysfsPath(interfaceName, direction, priority))
		data, err := cmd.CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("failed to read ECN %s: %v", direction, err)
		}
		return strings.TrimSpace(string(data)) == "1", nil
	}

	rp, err := readECN("roce_rp")
	if err != nil {
		return false, false, err
	}
	np, err := readECN("roce_np")
	if err != nil {
		return false, false, err
	}
	return rp, np, nil
}

// SetECNEnabled enables/disables ECN for roce_rp and roce_np on a given priority
func (h *configurationUtils) SetECNEnabled(interfaceName string, priority int, rp bool, np bool) error {
	log.Log.Info("ConfigurationUtils.SetECNEnabled()", "interface", interfaceName, "priority", priority, "rp", rp, "np", np)

	writeECN := func(direction string, enabled bool) error {
		val := "0"
		if enabled {
			val = "1"
		}
		cmd := h.nsenterMountNetCommand("tee", ecnSysfsPath(interfaceName, direction, priority))
		cmd.SetStdin(strings.NewReader(val))
		if _, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to write ECN %s: %v", direction, err)
		}
		return nil
	}

	if err := writeECN("roce_rp", rp); err != nil {
		return err
	}
	return writeECN("roce_np", np)
}

// GetPauseFrames returns whether global pause frames are enabled (both RX and TX are on)
func (h *configurationUtils) GetPauseFrames(interfaceName string) (bool, error) {
	log.Log.V(2).Info("ConfigurationUtils.GetPauseFrames()", "interface", interfaceName)
	cmd := h.nsenterCommand("ethtool", "-a", interfaceName)
	output, err := cmd.CombinedOutput()
	if err != nil && len(output) == 0 {
		log.Log.Error(err, "GetPauseFrames(): failed to run ethtool -a", "interface", interfaceName)
		return false, err
	}
	lines := strings.Split(string(output), "\n")
	// Only check RX and TX — autoneg may be unsupported (always off) on some drivers (e.g. mlx5),
	// which would cause an infinite re-apply loop if included in the check.
	// Track each independently to avoid false positives from partial output.
	rxFound, txFound := false, false
	rxOn, txOn := false, false
	for _, line := range lines {
		lower := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(lower, "rx:") {
			rxFound = true
			rxOn = strings.Contains(lower, "on")
		} else if strings.HasPrefix(lower, "tx:") {
			txFound = true
			txOn = strings.Contains(lower, "on")
		}
	}
	if !rxFound || !txFound {
		return false, fmt.Errorf("RX/TX pause frame status not found in ethtool -a output for %s", interfaceName)
	}
	// Use || so that a partial state (e.g. rx=off, tx=on) is treated as "enabled",
	// ensuring Enabled=false triggers a full disable even from a partial state.
	return rxOn || txOn, nil
}

// SetPauseFrames enables/disables global pause frames
func (h *configurationUtils) SetPauseFrames(interfaceName string, enabled bool) error {
	log.Log.Info("ConfigurationUtils.SetPauseFrames()", "interface", interfaceName, "enabled", enabled)
	state := boolToOnOff(enabled)
	cmd := h.nsenterCommand("ethtool", "-A", interfaceName, "autoneg", state, "rx", state, "tx", state)
	output, err := cmd.CombinedOutput()
	if err != nil && len(output) == 0 {
		log.Log.Error(err, "SetPauseFrames(): failed to run ethtool -A", "interface", interfaceName)
		return err
	}
	if err != nil {
		// ethtool -A may exit non-zero when autoneg is unsupported but rx/tx are applied successfully
		log.Log.V(2).Info("SetPauseFrames(): ethtool -A returned non-zero exit but produced output, continuing", "interface", interfaceName, "output", string(output))
	}
	return nil
}

// GetRingSize returns the current rx and tx ring buffer sizes
func (h *configurationUtils) GetRingSize(interfaceName string) (int, int, error) {
	log.Log.V(2).Info("ConfigurationUtils.GetRingSize()", "interface", interfaceName)
	cmd := h.nsenterCommand("ethtool", "-g", interfaceName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Log.Error(err, "GetRingSize(): failed to run ethtool -g", "interface", interfaceName)
		return 0, 0, err
	}
	// Parse "Current hardware settings" section
	inCurrent := false
	rx, tx := 0, 0
	foundRX, foundTX := false, false
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(strings.ToLower(line), "current hardware settings") {
			inCurrent = true
			continue
		}
		if inCurrent {
			if strings.HasPrefix(line, "RX:") {
				val := strings.TrimSpace(strings.TrimPrefix(line, "RX:"))
				rx, err = strconv.Atoi(val)
				if err != nil {
					return 0, 0, fmt.Errorf("failed to parse RX ring size: %v", err)
				}
				foundRX = true
			} else if strings.HasPrefix(line, "TX:") {
				val := strings.TrimSpace(strings.TrimPrefix(line, "TX:"))
				tx, err = strconv.Atoi(val)
				if err != nil {
					return 0, 0, fmt.Errorf("failed to parse TX ring size: %v", err)
				}
				foundTX = true
				break
			}
		}
	}
	if !inCurrent {
		return 0, 0, fmt.Errorf("current hardware settings not found in ethtool -g output for %s", interfaceName)
	}
	if !foundRX {
		return 0, 0, fmt.Errorf("RX ring size not found in ethtool -g output for %s", interfaceName)
	}
	if !foundTX {
		return 0, 0, fmt.Errorf("TX ring size not found in ethtool -g output for %s", interfaceName)
	}
	return rx, tx, nil
}

// SetRingSize sets the rx and tx ring buffer sizes. Zero values are skipped.
func (h *configurationUtils) SetRingSize(interfaceName string, rx int, tx int) error {
	log.Log.Info("ConfigurationUtils.SetRingSize()", "interface", interfaceName, "rx", rx, "tx", tx)
	if rx == 0 && tx == 0 {
		return nil
	}
	args := []string{"ethtool", "-G", interfaceName}
	if rx != 0 {
		args = append(args, "rx", strconv.Itoa(rx))
	}
	if tx != 0 {
		args = append(args, "tx", strconv.Itoa(tx))
	}
	cmd := h.nsenterCommand(args...)
	_, err := cmd.CombinedOutput()
	if err != nil {
		log.Log.Error(err, "SetRingSize(): failed to run ethtool -G", "interface", interfaceName)
		return err
	}
	return nil
}

// GetCombinedChannels returns the current number of combined channels
func (h *configurationUtils) GetCombinedChannels(interfaceName string) (int, error) {
	log.Log.V(2).Info("ConfigurationUtils.GetCombinedChannels()", "interface", interfaceName)
	cmd := h.nsenterCommand("ethtool", "-l", interfaceName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// ethtool -l may fail (with or without error output) on drivers that don't support channel config
		log.Log.V(2).Info("GetCombinedChannels(): ethtool -l failed, driver may not support it", "interface", interfaceName, "err", err)
		return 0, nil
	}
	// Parse "Current hardware settings" section
	inCurrent := false
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.Contains(strings.ToLower(line), "current hardware settings") {
			inCurrent = true
			continue
		}
		if inCurrent && strings.HasPrefix(line, "Combined:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Combined:"))
			n, err := strconv.Atoi(val)
			if err != nil {
				return 0, fmt.Errorf("failed to parse combined channels: %v", err)
			}
			return n, nil
		}
	}
	if !inCurrent {
		// Treat unexpected output format the same as tool failure — driver may not support it
		log.Log.V(2).Info("GetCombinedChannels(): current hardware settings not found in ethtool -l output, driver may not support it", "interface", interfaceName)
		return 0, nil
	}
	// Combined channels may not be exposed by all drivers; return 0 to let the caller decide
	log.Log.V(2).Info("GetCombinedChannels(): combined channels not found in ethtool output, driver may not support it", "interface", interfaceName)
	return 0, nil
}

// SetCombinedChannels sets the number of combined channels
func (h *configurationUtils) SetCombinedChannels(interfaceName string, channels int) error {
	log.Log.Info("ConfigurationUtils.SetCombinedChannels()", "interface", interfaceName, "channels", channels)
	cmd := h.nsenterCommand("ethtool", "-L", interfaceName, "combined", strconv.Itoa(channels))
	_, err := cmd.CombinedOutput()
	if err != nil {
		log.Log.Error(err, "SetCombinedChannels(): failed to run ethtool -L", "interface", interfaceName)
		return err
	}
	return nil
}

// GetLRO returns whether Large Receive Offload is enabled
func (h *configurationUtils) GetLRO(interfaceName string) (bool, error) {
	log.Log.V(2).Info("ConfigurationUtils.GetLRO()", "interface", interfaceName)
	cmd := h.nsenterCommand("ethtool", "-k", interfaceName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Log.Error(err, "GetLRO(): failed to run ethtool -k", "interface", interfaceName)
		return false, err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "large-receive-offload:") {
			return strings.Contains(line, "on"), nil
		}
	}
	return false, fmt.Errorf("LRO status not found in ethtool output")
}

// SetLRO enables/disables Large Receive Offload
func (h *configurationUtils) SetLRO(interfaceName string, enabled bool) error {
	log.Log.Info("ConfigurationUtils.SetLRO()", "interface", interfaceName, "enabled", enabled)
	cmd := h.nsenterCommand("ethtool", "-K", interfaceName, "lro", boolToOnOff(enabled))
	_, err := cmd.CombinedOutput()
	if err != nil {
		log.Log.Error(err, "SetLRO(): failed to run ethtool -K", "interface", interfaceName)
		return err
	}
	return nil
}

func boolToOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func encapTypeToLinkType(encapType string) string {
	switch encapType {
	case "ether":
		return consts.Ethernet
	case "infiniband":
		return consts.Infiniband
	default:
		return ""
	}
}

func newConfigurationUtils(dmsManager dms.DMSManager) ConfigurationUtils {
	return &configurationUtils{
		execInterface: execUtils.New(),
		dmsManager:    dmsManager,
	}
}
