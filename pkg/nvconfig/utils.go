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

// SystemConfValidator is an optional extension of NVConfigUtils exposing the per-param results of
// validate_system_conf. It is kept separate from NVConfigUtils so adding it does not break source
// compatibility for existing library consumers that implement NVConfigUtils themselves; callers
// type-assert for it and fall back when it is absent. NewNVConfigUtils() returns a value that satisfies it.
type SystemConfValidator interface {
	// ValidateSystemConfDetailed runs `validate_system_conf` and returns the per-param OK/MISMATCH
	// results plus the skipped params, instead of collapsing to a single match bit. Callers that allow
	// explicit overrides (e.g. rawNvConfig / Spectrum-X) on top of a named system conf use this to decide
	// whether a reported mismatch is an intentional override or real drift requiring re-apply.
	ValidateSystemConfDetailed(ctx context.Context, pciAddr string, conf string, asic int) (*SystemConfValidationResult, error)
}

// SystemConfEntry is a single per-param result line from `mlxconfig validate_system_conf`.
type SystemConfEntry struct {
	// Param is the nv config parameter name, e.g. "NUM_OF_PF" or "MODULE_SPLIT_M0[0]".
	Param string
	// Status is "OK" or "MISMATCH".
	Status string
	// Expected is the value the system conf expects. Populated for MISMATCH rows only.
	Expected string
	// Actual is the value currently on the device. Populated for both OK and MISMATCH rows.
	Actual string
}

// SystemConfValidationResult is the parsed output of `mlxconfig validate_system_conf`.
type SystemConfValidationResult struct {
	// Entries holds the OK and MISMATCH rows in source order.
	Entries []SystemConfEntry
	// Skipped lists params under "SKIPPED (failed to query):" — informational, not drift.
	Skipped []string
	// Matches mirrors the trailing `Result:` line (true == device matches the system conf).
	Matches bool
}

// Status values for SystemConfEntry.Status.
const (
	SystemConfStatusOK       = "OK"
	SystemConfStatusMismatch = "MISMATCH"
)

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
// It is a thin wrapper over ValidateSystemConfDetailed, retained for callers that only need the match bit.
func (h *nvConfigUtils) ValidateSystemConf(ctx context.Context, pciAddr string, conf string, asic int) (bool, error) {
	result, err := h.ValidateSystemConfDetailed(ctx, pciAddr, conf, asic)
	if err != nil {
		return false, err
	}
	return result.Matches, nil
}

// ValidateSystemConfDetailed runs validate_system_conf and returns the per-param results.
func (h *nvConfigUtils) ValidateSystemConfDetailed(ctx context.Context, pciAddr string, conf string, asic int) (*SystemConfValidationResult, error) {
	log.Log.Info("ConfigurationUtils.ValidateSystemConfDetailed()", "pciAddr", pciAddr, "conf", conf, "asic", asic)

	args := []string{"-d", pciAddr, "-y", "validate_system_conf", systemConfToken(conf, asic)}
	output, err := h.execInterface.CommandContext(ctx, "mlxconfig", args...).CombinedOutput()
	log.Log.V(2).Info("command output", "command", "mlxconfig validate_system_conf", "pciAddr", pciAddr, "output", string(output))

	// mlxconfig validate_system_conf exits non-zero (e.g. 3) when the device configuration does
	// NOT match the system conf — that is a valid result, not a command failure. Treat the parsed
	// output as authoritative regardless of exit code, and only surface the command error when the
	// output couldn't be parsed at all (i.e. mlxconfig genuinely failed to run).
	result, resultLineFound, parseErr := parseValidateSystemConfDetailed(output)
	if parseErr != nil {
		if err != nil {
			log.Log.Error(err, "ValidateSystemConfDetailed(): Failed to run mlxconfig", "pciAddr", pciAddr)
			return nil, err
		}
		return nil, parseErr
	}
	// No trailing "Result:" line means mlxconfig did not finish (e.g. it was killed or errored mid-output).
	// In that case the parsed rows are partial, so we must not trust a derived match bit — surface the
	// command error instead of reporting a (possibly false) match from incomplete data.
	if !resultLineFound && err != nil {
		log.Log.Error(err, "ValidateSystemConfDetailed(): incomplete validate_system_conf output", "pciAddr", pciAddr)
		return nil, err
	}

	log.Log.Info("ValidateSystemConfDetailed() result", "pciAddr", pciAddr, "conf", conf, "asic", asic, "matches", result.Matches)
	return result, nil
}

// parseValidateSystemConfDetailed parses the full per-param output of validate_system_conf.
// Sample output (mismatch case):
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
// Matches comes from the trailing Result line when present; otherwise it is derived from the
// absence of MISMATCH rows. The second return value reports whether a "Result:" line was seen, so
// callers can distinguish a complete parse from partial output. An output with neither a Result line
// nor any parsed rows is an error.
func parseValidateSystemConfDetailed(output []byte) (*SystemConfValidationResult, bool, error) {
	result := &SystemConfValidationResult{}
	foundResult := false
	inSkipped := false

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
				result.Matches = false
			} else if strings.Contains(line, "MATCHES") {
				result.Matches = true
			}
		case strings.HasPrefix(line, "SKIPPED"):
			inSkipped = true
		case inSkipped && strings.HasPrefix(line, "-"):
			param := strings.TrimSpace(strings.TrimPrefix(line, "-"))
			if param != "" {
				result.Skipped = append(result.Skipped, param)
			}
		case strings.HasPrefix(line, "OK:"):
			inSkipped = false
			rest := strings.TrimSpace(strings.TrimPrefix(line, "OK:"))
			// Expected shape is "KEY = VALUE"; keep the entry even if "=" is absent (malformed line)
			// so it is still counted and surfaced rather than silently dropped.
			param, actual := rest, ""
			if idx := strings.Index(rest, "="); idx >= 0 {
				param = strings.TrimSpace(rest[:idx])
				actual = strings.TrimSpace(rest[idx+1:])
			}
			result.Entries = append(result.Entries, SystemConfEntry{Param: param, Status: SystemConfStatusOK, Actual: actual})
		case strings.HasPrefix(line, "MISMATCH:"):
			inSkipped = false
			rest := strings.TrimSpace(strings.TrimPrefix(line, "MISMATCH:"))
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				continue
			}
			entry := SystemConfEntry{Param: fields[0], Status: SystemConfStatusMismatch}
			for i := 0; i < len(fields)-1; i++ {
				switch fields[i] {
				case "Expected:":
					entry.Expected = fields[i+1]
				case "Actual:":
					entry.Actual = fields[i+1]
				}
			}
			result.Entries = append(result.Entries, entry)
		default:
			// Any other line (e.g. the "Validating system configuration ..." header) ends a SKIPPED block.
			inSkipped = false
		}
	}

	if !foundResult {
		if len(result.Entries) == 0 && len(result.Skipped) == 0 {
			return nil, false, fmt.Errorf("could not parse validate_system_conf output: %q", string(output))
		}
		// No Result line: derive the match bit from the parsed rows (caller decides whether to trust it).
		result.Matches = true
		for _, e := range result.Entries {
			if e.Status == SystemConfStatusMismatch {
				result.Matches = false
				break
			}
		}
	}

	return result, foundResult, nil
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
