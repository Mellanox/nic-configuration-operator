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

func parseMLXConfigValue(value string, valueInBracketsRegex *regexp.Regexp) []string {
	match := valueInBracketsRegex.FindStringSubmatch(value)
	if len(match) != 3 {
		return []string{value}
	}

	for i := 1; i < len(match); i++ {
		match[i] = strings.ToLower(match[i])
	}
	return match[1:]
}

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
	SetNvConfigParametersBatch(pciAddr string, params map[string]string, withDefault bool, force bool) (types.ApplyStatus, error)
	// ResetNvConfig resets NIC's nv config
	ResetNvConfig(pciAddr string) error
	// SetSystemConf applies a ConnectX-9 Network Bay system configuration for a single ASIC via
	// `mlxconfig -d <pci> -y [--force] set_system_conf <conf>[<asic>]`. Persistent, reboot-required.
	SetSystemConf(ctx context.Context, pciAddr string, conf string, asic int, force bool) error
	// ValidateSystemConf reports whether the device's applied configuration matches the named system
	// configuration for the given ASIC via `mlxconfig -d <pci> -y validate_system_conf <conf>[<asic>]`.
	// It returns the overall match bit plus the names of the mismatched params (the MISMATCH rows), so
	// callers that allow explicit overrides (e.g. rawNvConfig / Spectrum-X) on top of a named system conf
	// can decide whether a reported mismatch is an intentional override or real drift requiring re-apply.
	ValidateSystemConf(ctx context.Context, pciAddr string, conf string, asic int) (bool, []string, error)
}

type nvConfigUtils struct {
	execInterface execUtils.Interface
}

// queryMLXConfig runs a query on mlxconfig to parse out default, current and nextboot configurations
// might run recursively to expand array parameters' values
func (h *nvConfigUtils) queryMLXConfig(ctx context.Context, query types.NvConfigQuery, pciAddr string, additionalParameter string) error {
	log.Log.Info(fmt.Sprintf("mlxconfig -d %s query %s", pciAddr, additionalParameter)) // TODO change verbosity
	valueInBracketsRegex := regexp.MustCompile(`^(.*?)\(([^)]*)\)$`)
	spaceRe := regexp.MustCompile(`\s{2,}`)

	var cmd execUtils.Cmd
	if additionalParameter == "" {
		cmd = h.execInterface.CommandContext(ctx, "mlxconfig", "-d", pciAddr, "-e", "query")
	} else {
		cmd = h.execInterface.CommandContext(ctx, "mlxconfig", "-d", pciAddr, "-e", "query", additionalParameter)
	}
	output, err := utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "queryMLXConfig(): Failed to run mlxconfig", "output", string(output))
		if trimmedOutput := strings.TrimSpace(string(output)); trimmedOutput != "" {
			return fmt.Errorf("%w: %s", err, trimmedOutput)
		}
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

			query.DefaultConfig[paramName] = parseMLXConfigValue(defaultVal, valueInBracketsRegex)
			query.CurrentConfig[paramName] = parseMLXConfigValue(currentVal, valueInBracketsRegex)
			query.NextBootConfig[paramName] = parseMLXConfigValue(nextBootVal, valueInBracketsRegex)

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

func parseSetNvConfigParametersBatchStatus(output []byte, withDefault bool) types.ApplyStatus {
	if strings.Contains(string(output), "Configurations:") {
		return types.ApplyStatusSuccess
	}
	if withDefault {
		return types.ApplyStatusNothingToDo
	}
	return types.ApplyStatusSuccess
}

// SetNvConfigParametersBatch sets multiple nv config parameters for a mellanox device in a single mlxconfig call
func (h *nvConfigUtils) SetNvConfigParametersBatch(pciAddr string, params map[string]string, withDefault bool, force bool) (types.ApplyStatus, error) {
	log.Log.Info("ConfigurationUtils.SetNvConfigParametersBatch()", "pciAddr", pciAddr, "params", params, "withDefault", withDefault, "force", force)

	if len(params) == 0 {
		return types.ApplyStatusNothingToDo, nil
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

	args := []string{"-d", pciAddr}
	if withDefault {
		args = append(args, "-e", "--with_default", "-y")
	} else {
		args = append(args, "--yes")
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
		return types.ApplyStatusFailed, err
	}
	return parseSetNvConfigParametersBatchStatus(output, withDefault), nil
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

// ValidateSystemConf runs validate_system_conf and returns the overall match bit plus the names of
// the mismatched params (the MISMATCH rows).
func (h *nvConfigUtils) ValidateSystemConf(ctx context.Context, pciAddr string, conf string, asic int) (bool, []string, error) {
	log.Log.Info("ConfigurationUtils.ValidateSystemConf()", "pciAddr", pciAddr, "conf", conf, "asic", asic)

	args := []string{"-d", pciAddr, "-y", "validate_system_conf", systemConfToken(conf, asic)}
	output, err := h.execInterface.CommandContext(ctx, "mlxconfig", args...).CombinedOutput()
	log.Log.V(2).Info("command output", "command", "mlxconfig validate_system_conf", "pciAddr", pciAddr, "output", string(output))

	// mlxconfig validate_system_conf exits non-zero (e.g. 3) when the device configuration does
	// NOT match the system conf — that is a valid result, not a command failure. Treat the parsed
	// output as authoritative regardless of exit code, and only surface the command error when the
	// output couldn't be parsed at all (i.e. mlxconfig genuinely failed to run).
	matches, mismatched, resultLineFound, parseErr := parseValidateSystemConf(output)
	if parseErr != nil {
		if err != nil {
			log.Log.Error(err, "ValidateSystemConf(): Failed to run mlxconfig", "pciAddr", pciAddr)
			return false, nil, err
		}
		return false, nil, parseErr
	}
	// No trailing "Result:" line means mlxconfig did not finish (e.g. it was killed or errored mid-output).
	// In that case the parsed rows are partial, so we must not trust a derived match bit — surface the
	// command error instead of reporting a (possibly false) match from incomplete data.
	if !resultLineFound && err != nil {
		log.Log.Error(err, "ValidateSystemConf(): incomplete validate_system_conf output", "pciAddr", pciAddr)
		return false, nil, err
	}

	log.Log.Info("ValidateSystemConf() result", "pciAddr", pciAddr, "conf", conf, "asic", asic, "matches", matches)
	return matches, mismatched, nil
}

// parseValidateSystemConf parses the per-param output of validate_system_conf into the overall match
// bit and the names of the mismatched params. Sample output (mismatch case):
//
//	Validating system configuration 'conf3[1]' on device 0001:03:00.0
//	------------------------------------------------------------------------
//	  MISMATCH: BOARD_CONFIGURATION_MODE	Expected: 0	Actual: 1
//	  OK:       MODULE_SPLIT_M0[8] = 0xFF
//	  SKIPPED (failed to query):
//	    - MODULE_SPLIT_M1[0]
//	------------------------------------------------------------------------
//	Result: Device configuration does NOT match the system configuration.
//
// matches comes from the trailing Result line when present; otherwise it is derived from the absence
// of MISMATCH rows. foundResult reports whether a "Result:" line was seen, so callers can distinguish
// a complete parse from partial output. An output with neither a Result line nor any parsed rows
// (OK / MISMATCH / SKIPPED) is an error.
func parseValidateSystemConf(output []byte) (matches bool, mismatched []string, foundResult bool, err error) {
	inSkipped := false
	sawRow := false

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "---") {
			inSkipped = false
			continue
		}

		switch {
		case strings.HasPrefix(line, "Result:"):
			inSkipped = false
			foundResult = true
			// "does NOT match" is checked first so it can never be mistaken for a match.
			if strings.Contains(line, "does NOT match") {
				matches = false
			} else if strings.Contains(line, "MATCHES") {
				matches = true
			}
		case strings.HasPrefix(line, "SKIPPED"):
			inSkipped = true
		case inSkipped && strings.HasPrefix(line, "-"):
			if param := strings.TrimSpace(strings.TrimPrefix(line, "-")); param != "" {
				sawRow = true
			}
		case strings.HasPrefix(line, "OK:"):
			inSkipped = false
			sawRow = true
		case strings.HasPrefix(line, "MISMATCH:"):
			inSkipped = false
			sawRow = true
			// Shape is "KEY\tExpected: V1\tActual: V2"; the param name is the first field.
			if fields := strings.Fields(strings.TrimPrefix(line, "MISMATCH:")); len(fields) > 0 {
				mismatched = append(mismatched, fields[0])
			}
		default:
			// Any other line (e.g. the "Validating system configuration ..." header) ends a SKIPPED block.
			inSkipped = false
		}
	}

	if !foundResult {
		if !sawRow {
			return false, nil, false, fmt.Errorf("could not parse validate_system_conf output: %q", string(output))
		}
		// No Result line: derive the match bit from the absence of MISMATCH rows (caller decides
		// whether to trust it — see foundResult).
		matches = len(mismatched) == 0
	}

	return matches, mismatched, foundResult, nil
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
