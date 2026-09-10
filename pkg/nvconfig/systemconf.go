/*
2026 NVIDIA CORPORATION & AFFILIATES
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

package nvconfig

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

const (
	maxSystemConfOutputSize = 1024 * 1024
	maxSystemConfRangeSize  = 4096
)

var systemConfAsicHeader = regexp.MustCompile(`^ASIC\[([0-9]+)\]\s+Description:$`)
var systemConfParamRange = regexp.MustCompile(`^(.+)\[([0-9]+)\.\.([0-9]+)\]$`)

// SystemConfParamsProvider resolves a named mlxconfig system configuration into ordinary
// mlxconfig parameter assignments for one ASIC.
type SystemConfParamsProvider interface {
	GetSystemConfParams(ctx context.Context, port v1alpha1.NicDevicePortSpec, conf string, asic int) (map[string]string, error)
}

var _ SystemConfParamsProvider = (*nvConfigUtils)(nil)

// GetSystemConfParams queries the profiles available to a device and returns the selected ASIC's
// assignments. Array ranges are expanded so callers can merge individual keys with explicit values.
func (h *nvConfigUtils) GetSystemConfParams(
	ctx context.Context, port v1alpha1.NicDevicePortSpec, conf string, asic int) (map[string]string, error) {
	targetDevice := resolveDevice(port)
	log.Log.Info("ConfigurationUtils.GetSystemConfParams()", "pciAddr", port.PCI, "targetDevice", targetDevice, "conf", conf, "asic", asic)

	output, err := h.execInterface.CommandContext(ctx, "mlxconfig", "-d", targetDevice, "show_system_conf").CombinedOutput()
	log.Log.V(2).Info("command output", "command", "mlxconfig show_system_conf", "pciAddr", port.PCI, "targetDevice", targetDevice, "output", string(output))
	if err != nil {
		log.Log.Error(err, "GetSystemConfParams(): Failed to run mlxconfig", "pciAddr", port.PCI, "targetDevice", targetDevice)
		return nil, fmt.Errorf("failed to show mlxconfig system configurations: %w: %s", err, strings.TrimSpace(string(output)))
	}

	params, err := parseShowSystemConf(output, conf, asic)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve system configuration %s[%d]: %w", conf, asic, err)
	}
	return params, nil
}

func parseShowSystemConf(output []byte, conf string, asic int) (map[string]string, error) {
	params := map[string]string{}
	foundConf := false
	foundAsic := false
	selectedConf := false
	selectedAsic := false

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	scanner.Buffer(make([]byte, 64*1024), maxSystemConfOutputSize)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Configuration name:") {
			details := strings.TrimSpace(strings.TrimPrefix(line, "Configuration name:"))
			fields := strings.Fields(details)
			selectedConf = len(fields) > 0 && fields[0] == conf
			selectedAsic = false
			foundConf = foundConf || selectedConf
			continue
		}

		if matches := systemConfAsicHeader.FindStringSubmatch(line); len(matches) == 2 {
			selectedAsic = false
			if !selectedConf {
				continue
			}
			asicNumber, err := strconv.Atoi(matches[1])
			if err != nil {
				return nil, fmt.Errorf("invalid ASIC number %q: %w", matches[1], err)
			}
			selectedAsic = asicNumber == asic
			foundAsic = foundAsic || selectedAsic
			continue
		}

		if !selectedAsic || !strings.Contains(line, "=") {
			continue
		}
		for _, assignment := range strings.Fields(line) {
			name, value, ok := strings.Cut(assignment, "=")
			if !ok || name == "" || value == "" {
				return nil, fmt.Errorf("invalid parameter assignment %q", assignment)
			}
			if err := addSystemConfParam(params, name, value); err != nil {
				return nil, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan show_system_conf output: %w", err)
	}
	if !foundConf {
		return nil, fmt.Errorf("configuration %q was not found", conf)
	}
	if !foundAsic {
		return nil, fmt.Errorf("ASIC %d was not found for configuration %q", asic, conf)
	}
	if len(params) == 0 {
		return nil, fmt.Errorf("configuration %q ASIC %d contains no parameters", conf, asic)
	}
	return params, nil
}

func addSystemConfParam(params map[string]string, name, value string) error {
	matches := systemConfParamRange.FindStringSubmatch(name)
	if len(matches) == 0 {
		return addUniqueSystemConfParam(params, name, value)
	}

	start, err := strconv.Atoi(matches[2])
	if err != nil {
		return fmt.Errorf("invalid range start in parameter %q: %w", name, err)
	}
	end, err := strconv.Atoi(matches[3])
	if err != nil {
		return fmt.Errorf("invalid range end in parameter %q: %w", name, err)
	}
	if end < start {
		return fmt.Errorf("invalid descending range in parameter %q", name)
	}
	if end-start+1 > maxSystemConfRangeSize {
		return fmt.Errorf("range in parameter %q exceeds maximum size %d", name, maxSystemConfRangeSize)
	}

	for index := start; index <= end; index++ {
		concreteName := fmt.Sprintf("%s[%d]", matches[1], index)
		if err := addUniqueSystemConfParam(params, concreteName, value); err != nil {
			return err
		}
	}
	return nil
}

func addUniqueSystemConfParam(params map[string]string, name, value string) error {
	if _, exists := params[name]; exists {
		return fmt.Errorf("duplicate parameter %q", name)
	}
	params[name] = value
	return nil
}
