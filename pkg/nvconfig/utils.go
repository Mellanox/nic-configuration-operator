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
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

const (
	arrayPrefix        = "Array"
	xPathPendingSuffix = "-pending"
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
	QueryNvConfig(ctx context.Context, port v1alpha1.NicDevicePortSpec, parameters []string) (types.NvConfigQuery, error)
	// SetNvConfigParameter sets a nv config parameter for a mellanox device
	SetNvConfigParameter(port v1alpha1.NicDevicePortSpec, paramName string, paramValue string) error
	// SetNvConfigParametersBatch sets multiple NVConfig parameters in one DMS NVConfig action.
	// withDefault and force are forwarded to DMS, and the returned status reflects its requires-reset result.
	SetNvConfigParametersBatch(port v1alpha1.NicDevicePortSpec, params map[string]string, withDefault bool, force bool) (types.ApplyStatus, error)
	// ResetNvConfig resets NIC's nv config
	ResetNvConfig(port v1alpha1.NicDevicePortSpec) error
	// SetSystemConf applies a ConnectX-9 Network Bay system configuration for a single ASIC via
	// `mlxconfig -d <device> -y [--force] set_system_conf <conf>[<asic>]`. Persistent, reboot-required.
	SetSystemConf(ctx context.Context, port v1alpha1.NicDevicePortSpec, conf string, asic int, force bool) error
	// ValidateSystemConf reports whether the device's applied configuration matches the named system
	// configuration for the given ASIC via `mlxconfig -d <device> -y validate_system_conf <conf>[<asic>]`.
	// It returns the overall match bit plus the names of the mismatched params (the MISMATCH rows), so
	// callers that allow explicit rawNvConfig overrides on top of a named system conf
	// can decide whether a reported mismatch is an intentional override or real drift requiring re-apply.
	ValidateSystemConf(ctx context.Context, port v1alpha1.NicDevicePortSpec, conf string, asic int) (bool, []string, error)
}

type nvConfigUtils struct {
	execInterface execUtils.Interface
}

func resolveDevice(port v1alpha1.NicDevicePortSpec) string {
	if port.FwctlDevice != "" {
		log.Log.V(2).Info("using fwctl device for mlxconfig", "pciAddr", port.PCI, "fwctlDevice", port.FwctlDevice)
		return port.FwctlDevice
	}
	return port.PCI
}

// queryMLXConfig runs a query on mlxconfig to parse out default, current and nextboot configurations
// might run recursively to expand array parameters' values
func (h *nvConfigUtils) queryMLXConfig(ctx context.Context, query types.NvConfigQuery, targetDevice string, additionalParameter string) error {
	log.Log.Info(fmt.Sprintf("mlxconfig -d %s query %s", targetDevice, additionalParameter)) // TODO change verbosity
	valueInBracketsRegex := regexp.MustCompile(`^(.*?)\(([^)]*)\)$`)
	spaceRe := regexp.MustCompile(`\s{2,}`)

	var cmd execUtils.Cmd
	if additionalParameter == "" {
		cmd = h.execInterface.CommandContext(ctx, "mlxconfig", "-d", targetDevice, "-e", "query")
	} else {
		cmd = h.execInterface.CommandContext(ctx, "mlxconfig", "-d", targetDevice, "-e", "query", additionalParameter)
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
				err = h.queryMLXConfig(ctx, query, targetDevice, paramName+strings.TrimPrefix(defaultVal, arrayPrefix))
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
func (h *nvConfigUtils) QueryNvConfig(ctx context.Context, port v1alpha1.NicDevicePortSpec, parameters []string) (types.NvConfigQuery, error) {
	targetDevice := resolveDevice(port)
	log.Log.Info("ConfigurationUtils.QueryNvConfig()", "pciAddr", port.PCI, "targetDevice", targetDevice)

	query := types.NewNvConfigQuery()

	if len(parameters) == 0 {
		err := h.queryMLXConfig(ctx, query, targetDevice, "")
		if err != nil {
			log.Log.Error(err, "Failed to parse mlxconfig query output", "device", targetDevice)
			return query, err
		}
	} else {
		for _, param := range parameters {
			err := h.queryMLXConfig(ctx, query, targetDevice, param)
			if err != nil {
				log.Log.Error(err, "Failed to parse mlxconfig query output", "device", targetDevice, "parameter", param)
				return query, err
			}
		}
	}

	return query, nil
}

// SetNvConfigParameter sets a nv config parameter for a mellanox device
func (h *nvConfigUtils) SetNvConfigParameter(port v1alpha1.NicDevicePortSpec, paramName string, paramValue string) error {
	targetDevice := resolveDevice(port)
	log.Log.Info("ConfigurationUtils.SetNvConfigParameter()", "pciAddr", port.PCI, "targetDevice", targetDevice, "paramName", paramName, "paramValue", paramValue)

	cmd := h.execInterface.Command("mlxconfig", "-d", targetDevice, "--yes", "set", paramName+"="+paramValue)
	output, err := utils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "SetNvConfigParameter(): Failed to run mlxconfig", "output", string(output))
		return err
	}
	return nil
}

// SetNvConfigParametersBatch preserves the NVConfigUtils interface. New internal callers should
// use SetNvConfigParametersBatchWithContext so cancellation reaches dms-cli.
func (h *nvConfigUtils) SetNvConfigParametersBatch(
	port v1alpha1.NicDevicePortSpec,
	params map[string]string,
	withDefault bool,
	force bool,
) (types.ApplyStatus, error) {
	ctx := logr.NewContext(context.Background(), log.Log)
	return h.SetNvConfigParametersBatchWithContext(ctx, port, params, withDefault, force)
}

// SetNvConfigParametersBatchWithContext sets multiple NVConfig parameters in one DMS NVConfig action
// and propagates cancellation to the command.
func (h *nvConfigUtils) SetNvConfigParametersBatchWithContext(
	ctx context.Context,
	port v1alpha1.NicDevicePortSpec,
	params map[string]string,
	withDefault bool,
	force bool,
) (types.ApplyStatus, error) {
	return h.setNvConfigParametersBatchWithXPaths(ctx, port, nil, params, nil, withDefault, force)
}

func sortedRawNVConfigParams(params map[string]string) []dmscli.NVConfigParam {
	paramNames := make([]string, 0, len(params))
	for name := range params {
		paramNames = append(paramNames, name)
	}
	sort.Strings(paramNames)

	raw := make([]dmscli.NVConfigParam, 0, len(params))
	for _, name := range paramNames {
		raw = append(raw, dmscli.NVConfigParam{Param: name, Value: params[name]})
	}
	return raw
}

func (h *nvConfigUtils) setNvConfigParametersBatchWithXPaths(
	ctx context.Context,
	primaryPort v1alpha1.NicDevicePortSpec,
	ports []int,
	params map[string]string,
	operations []dmscli.XPathOperation,
	withDefault bool,
	force bool,
) (types.ApplyStatus, error) {
	if len(params) == 0 && len(operations) == 0 {
		return types.ApplyStatusNothingToDo, nil
	}
	if h.execInterface == nil {
		return types.ApplyStatusFailed, fmt.Errorf("command executor must not be nil")
	}

	target := "pci/" + primaryPort.PCI
	logger := logr.FromContextOrDiscard(ctx)
	logger.Info("applying NVConfig through DMS",
		"pciAddr", primaryPort.PCI,
		"target", target,
		"ports", ports,
		"nativeParameterCount", len(params),
		"typedOperationCount", len(operations),
		"withDefault", withDefault,
		"force", force)
	logger.V(2).Info("DMS NVConfig apply payload",
		"target", target,
		"nativeParameters", params,
		"typedOperations", operations)

	result, err := dmscli.ApplyNVConfig(ctx, h.execInterface, dmscli.ApplyNVConfigRequest{
		Target:      target,
		Ports:       ports,
		Typed:       operations,
		Raw:         sortedRawNVConfigParams(params),
		WithDefault: withDefault,
		Force:       force,
	})
	if err != nil {
		logger.Error(err, "DMS NVConfig apply failed", "target", target)
		return types.ApplyStatusFailed, err
	}
	logger.V(2).Info("DMS NVConfig apply succeeded",
		"target", target,
		"primaryTarget", result.PrimaryTarget,
		"compiledCount", result.CompiledCount,
		"requiresReset", result.RequiresReset,
		"withDefault", result.WithDefault,
		"force", result.Force,
		"params", result.Params)
	if result.RequiresReset {
		return types.ApplyStatusSuccess, nil
	}
	return types.ApplyStatusNothingToDo, nil
}

// ValidateNvConfigXPaths checks current and pending typed NVConfig values on
// every PCI function. Split functions expose their NVConfig as port 1.
func (h *nvConfigUtils) ValidateNvConfigXPaths(
	ctx context.Context,
	ports []v1alpha1.NicDevicePortSpec,
	operations []dmscli.XPathOperation,
) (updateNeeded, rebootNeeded bool, err error) {
	if len(operations) == 0 {
		return false, false, nil
	}
	logger := logr.FromContextOrDiscard(ctx)
	queries := xpathQueries(operations)
	queryBatches := xpathQueryBatches(queries)
	logger.V(2).Info("validating doSPCX NVConfig on all device ports",
		"ports", len(ports),
		"operations", len(operations),
		"queryPaths", len(queries),
		"batchesPerPort", len(queryBatches))
	for _, port := range ports {
		target := "pci/" + port.PCI + "?port=1"
		logger.V(2).Info("querying doSPCX NVConfig state for device port",
			"pciAddr", port.PCI,
			"target", target,
			"batches", len(queryBatches))
		result := &dmscli.QueryXPathsResult{Status: "ok", Values: map[string]map[string]any{}}
		for batchIndex, queryBatch := range queryBatches {
			logger.V(2).Info("querying doSPCX NVConfig batch",
				"target", target,
				"batch", batchIndex+1,
				"totalBatches", len(queryBatches),
				"paths", len(queryBatch))
			batchResult, queryErr := dmscli.QueryXPaths(ctx, h.execInterface, target, queryBatch)
			if queryErr != nil {
				return false, false, fmt.Errorf("query NVConfig XPaths on target %q: %w", target, queryErr)
			}
			for path, values := range batchResult.Values {
				result.Values[path] = values
			}
		}
		portUpdateNeeded, portRebootNeeded, matchErr := matchXPathValues(result, operations)
		if matchErr != nil {
			return false, false, fmt.Errorf("validate NVConfig XPaths on target %q: %w", target, matchErr)
		}
		updateNeeded = updateNeeded || portUpdateNeeded
		rebootNeeded = rebootNeeded || portRebootNeeded
		logger.V(2).Info("doSPCX NVConfig validation complete for device port",
			"pciAddr", port.PCI,
			"target", target,
			"configUpdateNeeded", portUpdateNeeded,
			"rebootNeeded", portRebootNeeded)
	}
	logger.V(2).Info("doSPCX NVConfig validation complete for all device ports",
		"ports", len(ports),
		"configUpdateNeeded", updateNeeded,
		"rebootNeeded", rebootNeeded)
	return updateNeeded, rebootNeeded, nil
}

func xpathQueries(operations []dmscli.XPathOperation) []dmscli.XPathQuery {
	queries := make([]dmscli.XPathQuery, 0, len(operations))
	indices := map[string]int{}
	leaves := map[string]map[string]struct{}{}
	for _, operation := range operations {
		if _, found := leaves[operation.Path]; !found {
			indices[operation.Path] = len(queries)
			leaves[operation.Path] = map[string]struct{}{}
			queries = append(queries, dmscli.XPathQuery{Path: operation.Path})
		}
		for leaf := range operation.Values {
			leaves[operation.Path][leaf] = struct{}{}
			leaves[operation.Path][leaf+xPathPendingSuffix] = struct{}{}
		}
	}
	for path, pathLeaves := range leaves {
		query := &queries[indices[path]]
		for leaf := range pathLeaves {
			query.Leaves = append(query.Leaves, leaf)
		}
		sort.Strings(query.Leaves)
	}
	return queries
}

// TODO(dospcx-nvconfig): Remove the individual breakout-lane queries once
// dms-cli preserves indexed XPath keys in batched JSON GET responses. Today
// paths ending in port/[1] and port/[255] both return as .../module/port and
// overwrite each other in the response object.
func xpathQueryBatches(queries []dmscli.XPathQuery) [][]dmscli.XPathQuery {
	batched := make([]dmscli.XPathQuery, 0, len(queries))
	individual := make([][]dmscli.XPathQuery, 0)
	for _, query := range queries {
		if strings.HasPrefix(query.Path, "/nvidia/link/breakout/") {
			individual = append(individual, []dmscli.XPathQuery{query})
			continue
		}
		batched = append(batched, query)
	}
	if len(batched) == 0 {
		return individual
	}
	return append([][]dmscli.XPathQuery{batched}, individual...)
}

func matchXPathValues(result *dmscli.QueryXPathsResult, operations []dmscli.XPathOperation) (updateNeeded, rebootNeeded bool, err error) {
	if result == nil {
		return false, false, fmt.Errorf("DMS returned a nil XPath query result")
	}
	for _, operation := range operations {
		values, found := result.Values[operation.Path]
		if !found {
			return false, false, fmt.Errorf("DMS response does not contain XPath %q", operation.Path)
		}
		for leaf, desired := range operation.Values {
			current, found := values[leaf]
			if !found {
				return false, false, fmt.Errorf("DMS response does not contain XPath leaf %q/%s", operation.Path, leaf)
			}
			pendingLeaf := leaf + xPathPendingSuffix
			pending, found := values[pendingLeaf]
			if !found {
				return false, false, fmt.Errorf("DMS response does not contain XPath leaf %q/%s", operation.Path, pendingLeaf)
			}
			currentMatches := xpathValuesEqual(current, desired)
			pendingMatches := xpathValuesEqual(pending, desired)
			updateNeeded = updateNeeded || !pendingMatches
			rebootNeeded = rebootNeeded || !currentMatches || !pendingMatches
		}
	}
	return updateNeeded, rebootNeeded, nil
}

func xpathValuesEqual(actual, desired any) bool {
	normalizedActual := normalizeXPathValue(actual)
	normalizedDesired := normalizeXPathValue(desired)
	if _, desiredIsList := normalizedDesired.([]any); desiredIsList {
		if actualString, actualIsString := normalizedActual.(string); actualIsString {
			parts := strings.Split(actualString, ",")
			actualList := make([]any, len(parts))
			for index, part := range parts {
				actualList[index] = normalizeXPathValue(part)
			}
			normalizedActual = actualList
		}
	}
	return reflect.DeepEqual(normalizedActual, normalizedDesired)
}

func normalizeXPathValue(value any) any {
	if value == nil {
		return nil
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.Array || reflected.Kind() == reflect.Slice {
		result := make([]any, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			result[index] = normalizeXPathValue(reflected.Index(index).Interface())
		}
		return result
	}

	formatted := strings.ToLower(strings.TrimSpace(fmt.Sprint(value)))
	formatted = strings.TrimSuffix(formatted, "_value")
	return strings.TrimPrefix(formatted, "device_")
}

// SetNvConfigParametersBatchWithXPaths applies native parameters and typed
// XPath operations through one primary-target dms-cli invocation.
//
// TODO(dospcx-nvconfig): HIGH PRIORITY -- pass the discovered BDF set once
// /nvidia/nvconfig/apply supports multi-target execution. The current DMS API
// uses ports only to expand {port} in native parameter names and runs one
// mlxconfig command on the primary BDF, so it cannot fan out to split PCI
// functions represented as separate NicDevice ports.
func (h *nvConfigUtils) SetNvConfigParametersBatchWithXPaths(
	ctx context.Context,
	primaryPort v1alpha1.NicDevicePortSpec,
	portCount int,
	params map[string]string,
	operations []dmscli.XPathOperation,
	withDefault bool,
	force bool,
) (types.ApplyStatus, error) {
	ports := make([]int, portCount)
	for index := range ports {
		ports[index] = index + 1
	}
	return h.setNvConfigParametersBatchWithXPaths(
		ctx, primaryPort, ports, params, operations, withDefault, force)
}

// systemConfToken builds the `<conf>[<asic>]` argument for set/validate_system_conf, e.g. conf3[0].
func systemConfToken(conf string, asic int) string {
	return fmt.Sprintf("%s[%d]", conf, asic)
}

// SetSystemConf applies a ConnectX-9 Network Bay system configuration for a single ASIC.
func (h *nvConfigUtils) SetSystemConf(ctx context.Context, port v1alpha1.NicDevicePortSpec, conf string, asic int, force bool) error {
	targetDevice := resolveDevice(port)
	log.Log.Info("ConfigurationUtils.SetSystemConf()", "pciAddr", port.PCI, "targetDevice", targetDevice, "conf", conf, "asic", asic, "force", force)

	args := []string{"-d", targetDevice, "-y"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "set_system_conf", systemConfToken(conf, asic))

	output, err := h.execInterface.CommandContext(ctx, "mlxconfig", args...).CombinedOutput()
	log.Log.V(2).Info("command output", "command", "mlxconfig set_system_conf", "pciAddr", port.PCI, "targetDevice", targetDevice, "output", string(output))
	if err != nil {
		log.Log.Error(err, "SetSystemConf(): Failed to run mlxconfig", "pciAddr", port.PCI, "targetDevice", targetDevice)
		return err
	}
	return nil
}

// ValidateSystemConf runs validate_system_conf and returns the overall match bit plus the names of
// the mismatched params (the MISMATCH rows).
func (h *nvConfigUtils) ValidateSystemConf(ctx context.Context, port v1alpha1.NicDevicePortSpec, conf string, asic int) (bool, []string, error) {
	targetDevice := resolveDevice(port)
	log.Log.Info("ConfigurationUtils.ValidateSystemConf()", "pciAddr", port.PCI, "targetDevice", targetDevice, "conf", conf, "asic", asic)

	args := []string{"-d", targetDevice, "-y", "validate_system_conf", systemConfToken(conf, asic)}
	output, err := h.execInterface.CommandContext(ctx, "mlxconfig", args...).CombinedOutput()
	log.Log.V(2).Info("command output", "command", "mlxconfig validate_system_conf", "pciAddr", port.PCI, "targetDevice", targetDevice, "output", string(output))

	// mlxconfig validate_system_conf exits non-zero (e.g. 3) when the device configuration does
	// NOT match the system conf — that is a valid result, not a command failure. Treat the parsed
	// output as authoritative regardless of exit code, and only surface the command error when the
	// output couldn't be parsed at all (i.e. mlxconfig genuinely failed to run).
	matches, mismatched, resultLineFound, parseErr := parseValidateSystemConf(output)
	if parseErr != nil {
		if err != nil {
			log.Log.Error(err, "ValidateSystemConf(): Failed to run mlxconfig", "pciAddr", port.PCI, "targetDevice", targetDevice)
			return false, nil, err
		}
		return false, nil, parseErr
	}
	// No trailing "Result:" line means mlxconfig did not finish (e.g. it was killed or errored mid-output).
	// In that case the parsed rows are partial, so we must not trust a derived match bit — surface the
	// command error instead of reporting a (possibly false) match from incomplete data.
	if !resultLineFound && err != nil {
		log.Log.Error(err, "ValidateSystemConf(): incomplete validate_system_conf output", "pciAddr", port.PCI, "targetDevice", targetDevice)
		return false, nil, err
	}

	log.Log.Info("ValidateSystemConf() result", "pciAddr", port.PCI, "targetDevice", targetDevice, "conf", conf, "asic", asic, "matches", matches)
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
func (h *nvConfigUtils) ResetNvConfig(port v1alpha1.NicDevicePortSpec) error {
	targetDevice := resolveDevice(port)
	log.Log.Info("ConfigurationUtils.ResetNvConfig()", "pciAddr", port.PCI, "targetDevice", targetDevice)

	cmd := h.execInterface.Command("mlxconfig", "-d", targetDevice, "--yes", "reset")
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
