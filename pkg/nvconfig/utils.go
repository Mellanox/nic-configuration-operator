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
	// parameters is an optional list of specific parameters to query, e.g. "ESWITCH_HAIRPIN_DESCRIPTORS[0..7]"
	QueryNvConfig(ctx context.Context, pciAddr string, parameters []string) (types.NvConfigQuery, error)
	// SetNvConfigParameter sets a nv config parameter for a mellanox device
	SetNvConfigParameter(pciAddr string, paramName string, paramValue string) error
	// SetNvConfigParametersBatch sets multiple nv config parameters for a mellanox device in a single mlxconfig call
	// When force is true, --force is passed to mlxconfig so it accepts a batch it would otherwise refuse
	// due to implicit parameter dependencies.
	SetNvConfigParametersBatch(pciAddr string, params map[string]string, withDefault bool, force bool) error
	// ResetNvConfig resets NIC's nv config
	ResetNvConfig(pciAddr string) error
	// SetSystemConf applies a ConnectX-9 Network Bay system configuration for a single ASIC via
	// `mlxconfig -d <pci> -y [--force] set_system_conf <conf>[<asic>]`. Persistent, reboot-required.
	SetSystemConf(ctx context.Context, pciAddr string, conf string, asic int, force bool) error
	// ValidateSystemConf reports whether the device's applied configuration matches the named system
	// configuration for the given ASIC via `mlxconfig -d <pci> -y validate_system_conf <conf>[<asic>]`.
	ValidateSystemConf(ctx context.Context, pciAddr string, conf string, asic int) (bool, error)
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
func (h *nvConfigUtils) QueryNvConfig(ctx context.Context, pciAddr string, parameters []string) (types.NvConfigQuery, error) {
	log.Log.Info("ConfigurationUtils.QueryNvConfig()", "pciAddr", pciAddr)

	query := types.NewNvConfigQuery()

	if len(parameters) == 0 {
		err := h.queryMLXConfig(ctx, query, pciAddr, "")
		if err != nil {
			log.Log.Error(err, "Failed to parse mlxconfig query output", "device", pciAddr)
			return query, err
		}
	} else {
		for _, param := range parameters {
			err := h.queryMLXConfig(ctx, query, pciAddr, param)
			if err != nil {
				log.Log.Error(err, "Failed to parse mlxconfig query output", "device", pciAddr, "parameter", param)
				return query, err
			}
		}
	}

	return query, nil
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
func (h *nvConfigUtils) SetNvConfigParametersBatch(pciAddr string, params map[string]string, withDefault bool, force bool) error {
	log.Log.Info("ConfigurationUtils.SetNvConfigParametersBatch()", "pciAddr", pciAddr, "params", params, "withDefault", withDefault, "force", force)

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
	if force {
		args = append(args, "--force")
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

// systemConfToken builds the `<conf>[<asic>]` argument for set/validate_system_conf, e.g. conf3[0].
func systemConfToken(conf string, asic int) string {
	return fmt.Sprintf("%s[%d]", conf, asic)
}

// SetSystemConf applies a ConnectX-9 Network Bay system configuration for a single ASIC.
func (h *nvConfigUtils) SetSystemConf(ctx context.Context, pciAddr string, conf string, asic int, force bool) error {
	log.Log.Info("ConfigurationUtils.SetSystemConf()", "pciAddr", pciAddr, "conf", conf, "asic", asic, "force", force)

	args := []string{"-d", pciAddr, "-y"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "set_system_conf", systemConfToken(conf, asic))

	output, err := h.execInterface.CommandContext(ctx, "mlxconfig", args...).CombinedOutput()
	log.Log.V(2).Info("command output", "command", "mlxconfig set_system_conf", "pciAddr", pciAddr, "output", string(output))
	if err != nil {
		log.Log.Error(err, "SetSystemConf(): Failed to run mlxconfig", "pciAddr", pciAddr)
		return err
	}
	return nil
}

// ValidateSystemConf reports whether the device's applied configuration matches the named system conf.
func (h *nvConfigUtils) ValidateSystemConf(ctx context.Context, pciAddr string, conf string, asic int) (bool, error) {
	log.Log.Info("ConfigurationUtils.ValidateSystemConf()", "pciAddr", pciAddr, "conf", conf, "asic", asic)

	args := []string{"-d", pciAddr, "-y", "validate_system_conf", systemConfToken(conf, asic)}
	output, err := h.execInterface.CommandContext(ctx, "mlxconfig", args...).CombinedOutput()
	log.Log.V(2).Info("command output", "command", "mlxconfig validate_system_conf", "pciAddr", pciAddr, "output", string(output))

	// mlxconfig validate_system_conf exits non-zero (e.g. 3) when the device configuration does
	// NOT match the system conf — that is a valid result, not a command failure. Treat the parsed
	// "Result:" line as authoritative regardless of exit code, and only surface the command error
	// when no result line could be parsed (i.e. mlxconfig genuinely failed to run).
	matches, parseErr := parseValidateSystemConf(output)
	if parseErr != nil {
		if err != nil {
			log.Log.Error(err, "ValidateSystemConf(): Failed to run mlxconfig", "pciAddr", pciAddr)
			return false, err
		}
		return false, parseErr
	}

	log.Log.Info("ValidateSystemConf() result", "pciAddr", pciAddr, "conf", conf, "asic", asic, "matches", matches)
	return matches, nil
}

// parseValidateSystemConf parses the trailing `Result:` line of validate_system_conf output.
// Sample output lines (see design doc §8):
//
//	Result: Device configuration MATCHES the system configuration.
//	Result: Device configuration does NOT match the system configuration.
//
// The "does NOT match" substring is checked first so it can never be mistaken for a match.
func parseValidateSystemConf(output []byte) (bool, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "Result:") {
			continue
		}
		if strings.Contains(line, "does NOT match") {
			return false, nil
		}
		if strings.Contains(line, "MATCHES") {
			return true, nil
		}
	}
	return false, fmt.Errorf("could not parse validate_system_conf output: %q", string(output))
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
