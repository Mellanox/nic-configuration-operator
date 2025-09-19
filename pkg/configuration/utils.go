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
	"regexp"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/dms"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
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
	output, err := utils.RunCommand(cmd)
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
func (h *configurationUtils) GetMaxReadRequestSize(pciAddr string) (int, error) {
	log.Log.Info("ConfigurationUtils.GetMaxReadRequestSize()", "pciAddr", pciAddr)
	cmd := h.execInterface.Command("lspci", "-vv", "-s", pciAddr)
	output, err := utils.RunCommand(cmd)
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
	_, err := utils.RunCommand(cmd)
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
	_, err := utils.RunCommand(cmd)
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

func encapTypeToLinkType(encapType string) string {
	if encapType == "ether" {
		return consts.Ethernet
	} else if encapType == "infiniband" {
		return consts.Infiniband
	}
	return ""
}

func newConfigurationUtils(dmsManager dms.DMSManager) ConfigurationUtils {
	return &configurationUtils{
		execInterface: execUtils.New(),
		dmsManager:    dmsManager,
	}
}
