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

package nvconfig

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

const (
	arrayPrefix = "Array"
)

// NVConfigUtils is an interface that contains util functions related to querying and setting nv config
type NVConfigUtils interface {
	// QueryNvConfig queries nv config for a mellanox device and returns default, current and next boot configs
	// additionalParameter is an optional specific parameter to query, e.g. "ESWITCH_HAIRPIN_DESCRIPTORS[0..7]"
	QueryNvConfig(ctx context.Context, pciAddr string, additionalParameter string) (types.NvConfigQuery, error)
	// SetNvConfigParameter sets a nv config parameter for a mellanox device
	SetNvConfigParameter(pciAddr string, paramName string, paramValue string) error
	// SetNvConfigParametersBatch sets multiple nv config parameters for a mellanox device in a single mlxconfig call
	SetNvConfigParametersBatch(pciAddr string, params map[string]string, withDefault bool) error
	// ResetNvConfig resets NIC's nv config
	ResetNvConfig(pciAddr string) error
}

type nvConfigUtils struct {
	execInterface execUtils.Interface
}

// queryMLXConfig runs a query on mlxconfig to parse out default, current and nextboot configurations
// might run recursively to expand array parameters' values
func (h *nvConfigUtils) queryMLXConfig(ctx context.Context, query types.NvConfigQuery, pciAddr string, additionalParameter string) error {
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
func (h *nvConfigUtils) QueryNvConfig(ctx context.Context, pciAddr string, additionalParameter string) (types.NvConfigQuery, error) {
	log.Log.Info("ConfigurationUtils.QueryNvConfig()", "pciAddr", pciAddr)

	query := types.NewNvConfigQuery()

	err := h.queryMLXConfig(ctx, query, pciAddr, additionalParameter)
	if err != nil {
		log.Log.Error(err, "Failed to parse mlxconfig query output", "device", pciAddr)
	}

	return query, err
}

// SetNvConfigParameter sets a nv config parameter for a mellanox device
func (h *nvConfigUtils) SetNvConfigParameter(pciAddr string, paramName string, paramValue string) error {
	log.Log.Info("ConfigurationUtils.SetNvConfigParameter()", "pciAddr", pciAddr, "paramName", paramName, "paramValue", paramValue)

	cmd := h.execInterface.Command("mlxconfig", "-d", pciAddr, "--yes", "set", paramName+"="+paramValue)
	output, err := utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "SetNvConfigParameter(): Failed to run mlxconfig", "output", string(output))
		return err
	}
	return nil
}

// SetNvConfigParametersBatch sets multiple nv config parameters for a mellanox device in a single mlxconfig call
func (h *nvConfigUtils) SetNvConfigParametersBatch(pciAddr string, params map[string]string, withDefault bool) error {
	log.Log.Info("ConfigurationUtils.SetNvConfigParametersBatch()", "pciAddr", pciAddr, "params", params, "withDefault", withDefault)

	if len(params) == 0 {
		return nil
	}

	// Build sorted param list for deterministic command
	paramNames := make([]string, 0, len(params))
	for name := range params {
		paramNames = append(paramNames, name)
	}
	sort.Strings(paramNames)

	paramArgs := make([]string, 0, len(params))
	for _, name := range paramNames {
		paramArgs = append(paramArgs, name+"="+params[name])
	}

	args := []string{"-d", pciAddr, "--yes"}
	if withDefault {
		args = append(args, "--with_default")
	}
	args = append(args, "set")
	args = append(args, paramArgs...)

	cmd := h.execInterface.Command("mlxconfig", args...)
	output, err := utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "SetNvConfigParametersBatch(): Failed to run mlxconfig", "output", string(output))
		return err
	}
	return nil
}

// ResetNvConfig resets NIC's nv config
func (h *nvConfigUtils) ResetNvConfig(pciAddr string) error {
	log.Log.Info("ConfigurationUtils.ResetNvConfig()", "pciAddr", pciAddr)

	cmd := h.execInterface.Command("mlxconfig", "-d", pciAddr, "--yes", "reset")
	output, err := utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "ResetNvConfig(): Failed to run mlxconfig", "output", string(output))
		return err
	}
	return nil
}

func NewNVConfigUtils() NVConfigUtils {
	return &nvConfigUtils{
		execInterface: execUtils.New(),
	}
}
