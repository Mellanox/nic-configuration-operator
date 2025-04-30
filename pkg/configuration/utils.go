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

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

const (
	arrayPrefix = "Array"
)

// ConfigurationUtils is an interface that contains util functions related to NIC Configuration
type ConfigurationUtils interface {
	// GetLinkType return the link type of the net device (Ethernet / Infiniband)
	GetLinkType(name string) string
	// GetPCILinkSpeed return PCI bus speed in GT/s
	GetPCILinkSpeed(pciAddr string) (int, error)
	// GetMaxReadRequestSize returns MaxReadRequest size for PCI device
	GetMaxReadRequestSize(pciAddr string) (int, error)
	// GetTrustAndPFC returns trust and pfc settings for network interface
	GetTrustAndPFC(interfaceName string) (string, string, error)

	// SetMaxReadRequestSize sets max read request size for PCI device
	SetMaxReadRequestSize(pciAddr string, maxReadRequestSize int) error
	// SetTrustAndPFC sets trust and PFC settings for a network interface
	SetTrustAndPFC(interfaceName string, trust string, pfc string) error

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
}

type configurationUtils struct {
	execInterface execUtils.Interface
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

// GetTrustAndPFC returns trust and pfc settings for network interface
func (h *configurationUtils) GetTrustAndPFC(interfaceName string) (string, string, error) {
	log.Log.Info("ConfigurationUtils.GetTrustAndPFC()", "interface", interfaceName)
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

// queryMLXConfig runs a query on mlxconfig to parse out default, current and nextboot configurations
// might run recursively to expand array parameters' values
func (h *configurationUtils) queryMLXConfig(ctx context.Context, query types.NvConfigQuery, pciAddr string, additionalParameter string) error {
	log.Log.Info(fmt.Sprintf("mlxconfig -d %s query %s", pciAddr, additionalParameter)) // TODO change verbosity
	valueInBracketsRegex := regexp.MustCompile(`^(.*?)\(([^)]*)\)$`)

	var cmd execUtils.Cmd
	if additionalParameter == "" {
		cmd = h.execInterface.CommandContext(ctx, "mlxconfig", "-d", pciAddr, "-e", "query")
	} else {
		cmd = h.execInterface.CommandContext(ctx, "mlxconfig", "-d", pciAddr, "-e", "query", additionalParameter)
	}
	output, err := utils.RunCommand(cmd)
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
func (h *configurationUtils) QueryNvConfig(ctx context.Context, pciAddr string) (types.NvConfigQuery, error) {
	log.Log.Info("ConfigurationUtils.QueryNvConfig()", "pciAddr", pciAddr)

	query := types.NewNvConfigQuery()

	err := h.queryMLXConfig(ctx, query, pciAddr, "")
	if err != nil {
		log.Log.Error(err, "Failed to parse mlxconfig query output", "device", pciAddr)
	}

	return query, err
}

// SetNvConfigParameter sets a nv config parameter for a mellanox device
func (h *configurationUtils) SetNvConfigParameter(pciAddr string, paramName string, paramValue string) error {
	log.Log.Info("ConfigurationUtils.SetNvConfigParameter()", "pciAddr", pciAddr, "paramName", paramName, "paramValue", paramValue)

	cmd := h.execInterface.Command("mlxconfig", "-d", pciAddr, "--yes", "set", paramName+"="+paramValue)
	output, err := utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "SetNvConfigParameter(): Failed to run mlxconfig", "output", string(output))
		return err
	}
	return nil
}

// ResetNvConfig resets NIC's nv config
func (h *configurationUtils) ResetNvConfig(pciAddr string) error {
	log.Log.Info("ConfigurationUtils.ResetNvConfig()", "pciAddr", pciAddr)

	cmd := h.execInterface.Command("mlxconfig", "-d", pciAddr, "--yes", "reset")
	output, err := utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "ResetNvConfig(): Failed to run mlxconfig", "output", string(output))
		return err
	}
	return nil
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

// SetTrustAndPFC sets trust and PFC settings for a network interface
func (h *configurationUtils) SetTrustAndPFC(interfaceName string, trust string, pfc string) error {
	log.Log.Info("ConfigurationUtils.SetTrustAndPFC()", "interfaceName", interfaceName, "trust", trust, "pfc", pfc)

	cmd := h.execInterface.Command("mlnx_qos", "-i", interfaceName, "--trust", trust, "--pfc", pfc)
	output, err := cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("failed to run mlnx_qos: %s", output)
		log.Log.Error(err, "SetTrustAndPFC(): Failed to run mlnx_qos")
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

func newConfigurationUtils() ConfigurationUtils {
	return &configurationUtils{execInterface: execUtils.New()}
}
