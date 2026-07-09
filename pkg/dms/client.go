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

package dms

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

const (
	ValueNameTrustMode = "trust-mode"
	ValueNamePFC       = "pfc"
	ValueTypeString    = "string"
	ValueTypeBool      = "bool"
	ValueTypeInt       = "int"

	QoSConfigPath    = "/interfaces/interface/nvidia/qos/config"
	QoSTrustModePath = QoSConfigPath + "/" + ValueNameTrustMode
	QoSPFCPath       = QoSConfigPath + "/" + ValueNamePFC
	ToSPath          = "/interfaces/interface/nvidia/roce/config/tos"

	Interface = "interface"
	Priority  = "priority"

	dmsClientPath    = "/opt/mellanox/doca/services/dms/dmsc"
	dmsClientTimeout = "300s"
)

// DMSClient interface defines methods for interacting with a DMS server to manage NIC device configuration
type DMSClient interface {
	// GetQoSSettings returns the current QoS settings (trust mode and PFC configuration)
	GetQoSSettings(interfaceName string) (*v1alpha1.QosSpec, error)
	// SetQoSSettings sets the QoS settings for the device (trust mode and PFC configuration). Settings are applied to all ports of the device.
	SetQoSSettings(spec *v1alpha1.QosSpec) error
	// GetParameters returns the current parameters for the device
	// returns a map of DMS paths to their values
	GetParameters(params []types.ConfigurationParameter) (map[string]string, error)
	// SetParameters sets the parameters for the device
	// params is a map of DMS paths to their values
	SetParameters(params []types.ConfigurationParameter) error
	// InstallBFB installs the BFB file with the new firmware version on a BlueField device
	InstallBFB(ctx context.Context, version string, bfbPath string) error
}

// dmsClient implements the DMSClient interface
type dmsClient struct {
	deviceMutex   sync.RWMutex
	device        v1alpha1.NicDeviceStatus
	targetPCI     string
	bindAddress   string
	authParams    []string
	execInterface execUtils.Interface
}

type getPathRequest struct {
	paramPath  string
	queryPath  string
	lookupPath string
}

type getPathRequestBatch struct {
	key      string
	requests []getPathRequest
}

type dmsGetCommandResult struct {
	Source    string         `json:"source"`
	Timestamp int64          `json:"timestamp"`
	Time      string         `json:"time"`
	Updates   []dmsGetUpdate `json:"updates"`
}

type dmsGetUpdate struct {
	Path   string            `json:"Path"`
	Values map[string]string `json:"values"`
}

func (i *dmsClient) deviceSnapshot() v1alpha1.NicDeviceStatus {
	i.deviceMutex.RLock()
	defer i.deviceMutex.RUnlock()
	return *i.device.DeepCopy()
}

// injectFilterRules takes a DMS path and a map of filter rules, and injects the rules into the path
// Example:
// path: /interfaces/interface/nvidia/qos/config/trust-mode
// filters: {"interface": "name=enp3s0f0np0"}
// Returns: /interfaces/interface[name=enp3s0f0np0]/nvidia/qos/config/trust-mode
func injectFilterRules(path string, filters map[string]string) string {
	pathParts := strings.Split(path, "/")
	for i, part := range pathParts {
		if filter, exists := filters[part]; exists {
			pathParts[i] = fmt.Sprintf("%s[%s]", part, filter)
		}
	}
	result := strings.Join(pathParts, "/")
	return result
}

// interfaceNameFilter returns a filter map for DMS path filtering based on interface name
func interfaceNameFilter(interfaceName string) map[string]string {
	return map[string]string{"interface": fmt.Sprintf("name=%s", interfaceName)}
}

func priorityIdFilter(priorityId int) map[string]string {
	return map[string]string{"priority": fmt.Sprintf("id=%d", priorityId)}
}

func mergeFilterRules(filterRules ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, filterRule := range filterRules {
		for key, value := range filterRule {
			result[key] = value
		}
	}
	return result
}

// stripSquareBracketClauses removes any substrings enclosed in square brackets, including the brackets themselves.
// For example: "interface[name=enp3s0]/nvidia" -> "interface/nvidia".
// If brackets are unbalanced, all characters from the first unmatched '[' to the end are discarded.
func stripSquareBracketClauses(s string) string {
	if s == "" {
		return s
	}
	var builder strings.Builder
	builder.Grow(len(s))
	depth := 0
	for _, r := range s {
		if r == '[' {
			depth++
			continue
		}
		if r == ']' {
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func normalizeDMSPath(path string) string {
	return strings.TrimPrefix(path, "/")
}

func newGetPathRequest(path string, filterRules map[string]string) getPathRequest {
	return getPathRequest{
		paramPath:  path,
		queryPath:  injectFilterRules(path, filterRules),
		lookupPath: stripSquareBracketClauses(normalizeDMSPath(path)),
	}
}

func getPathRequestBatchKey(filterRules map[string]string) string {
	if interfaceFilter, ok := filterRules[Interface]; ok {
		return Interface + "[" + interfaceFilter + "]"
	}
	return "global"
}

func (i *dmsClient) RunGetPathCommand(path string, filterRules map[string]string) (string, error) {
	device := i.deviceSnapshot()
	log.Log.V(2).Info("dmsClient.RunGetPathCommand()", "path", path, "filterRules", filterRules, "device", device.SerialNumber)

	request := newGetPathRequest(path, filterRules)
	values, err := i.RunGetPathCommands(getPathRequestBatch{key: getPathRequestBatchKey(filterRules), requests: []getPathRequest{request}})
	if err != nil {
		return "", err
	}

	value, ok := values[request.paramPath]
	if !ok {
		return "", fmt.Errorf("value not found for path %s", request.paramPath)
	}
	log.Log.V(2).Info("RunGetPathCommand successful", "device", device.SerialNumber, "path", path, "value", value)
	return value, nil
}

func (i *dmsClient) RunGetPathCommands(batch getPathRequestBatch) (map[string]string, error) {
	device := i.deviceSnapshot()
	requests := batch.requests
	if len(requests) == 0 {
		return map[string]string{}, nil
	}

	batchKey := batch.key
	log.Log.V(2).Info("dmsClient.RunGetPathCommands()", "requestCount", len(requests), "batchKey", batchKey, "device", device.SerialNumber)

	args := append([]string{dmsClientPath, "-a", i.bindAddress}, i.authParams...)
	args = append(args, "--target", i.targetPCI, "--timeout", dmsClientTimeout, "get")
	for _, request := range requests {
		args = append(args, "--path", request.queryPath)
	}
	log.Log.V(2).Info("dmsClient.RunGetPathCommands()", "batchKey", batchKey, "args", strings.Join(args, " "))

	command := i.execInterface.Command(args[0], args[1:]...)

	log.Log.V(2).Info("Executing command", "device", device.SerialNumber, "command", strings.Join(args, " "))
	output, err := command.CombinedOutput()
	log.Log.V(2).Info("dmsClient.RunGetPathCommands() raw DMS output", "device", device.SerialNumber, "batchKey", batchKey, "output", string(output))
	if err != nil {
		log.Log.V(2).Error(err, "Command execution failed", "device", device.SerialNumber)
		return nil, fmt.Errorf("failed to run get path command: %v, output: %s", err, string(output))
	}
	log.Log.V(2).Info("Command execution successful", "device", device.SerialNumber, "outputSize", len(output))

	values, err := parseGetPathCommandOutput(output, requests)
	if err != nil {
		log.Log.V(2).Error(err, "Failed to parse get path command output", "device", device.SerialNumber)
		return nil, err
	}

	return values, nil
}

func parseGetPathCommandOutput(output []byte, requests []getPathRequest) (map[string]string, error) {
	var result []dmsGetCommandResult
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal command output: %v", err)
	}

	log.Log.V(2).Info("parseGetPathCommandOutput()", "json result", result)

	var updates []dmsGetUpdate
	for _, item := range result {
		updates = append(updates, item.Updates...)
	}
	if len(updates) == 0 {
		return nil, fmt.Errorf("no updates found in command output")
	}

	updatesByPath := make(map[string]dmsGetUpdate, len(updates))
	for _, update := range updates {
		updatePath := normalizeDMSPath(update.Path)
		existing, found := updatesByPath[updatePath]
		if !found {
			updatesByPath[updatePath] = update
			continue
		}

		values := make(map[string]string, len(existing.Values)+len(update.Values))
		for key, value := range existing.Values {
			values[key] = value
		}
		for key, value := range update.Values {
			if existingValue, exists := values[key]; exists && existingValue != value {
				return nil, fmt.Errorf("conflicting value found for path %s", update.Path)
			}
			values[key] = value
		}
		existing.Values = values
		updatesByPath[updatePath] = existing
	}

	values := make(map[string]string, len(requests))
	for _, request := range requests {
		update, ok := updatesByPath[normalizeDMSPath(request.queryPath)]
		if !ok {
			return nil, fmt.Errorf("no update found for path %s", request.queryPath)
		}
		value, ok := update.Values[request.lookupPath]
		if !ok {
			updateLookupPath := stripSquareBracketClauses(normalizeDMSPath(update.Path))
			value, ok = update.Values[updateLookupPath]
		}
		if !ok {
			return nil, fmt.Errorf("value not found for path %s", request.paramPath)
		}

		path := request.paramPath
		currentValue, found := values[path]
		if found && currentValue != value {
			return nil, types.ValuesDoNotMatchError(types.ConfigurationParameter{Name: request.paramPath, DMSPath: path}, value)
		}
		values[path] = value
	}

	return values, nil
}

// formatSetUpdate builds a "path:::type:::value" update entry, applying filter rules to the path.
func formatSetUpdate(path, value, valueType string, filterRules map[string]string) string {
	queryPath := injectFilterRules(path, filterRules)
	return fmt.Sprintf("%s:::%s:::%s", queryPath, valueType, value)
}

func (i *dmsClient) RunSetPathCommand(path, value, valueType string, filterRules map[string]string) error {
	device := i.deviceSnapshot()
	log.Log.V(2).Info("dmsClient.RunSetPathCommand()", "path", path, "value", value, "valueType", valueType, "device", device.SerialNumber)

	args := append([]string{dmsClientPath, "-a", i.bindAddress}, i.authParams...)
	args = append(args, "--target", i.targetPCI, "--timeout", dmsClientTimeout, "set", "--update", formatSetUpdate(path, value, valueType, filterRules))
	log.Log.V(2).Info("dmsClient.RunSetPathCommand()", "args", strings.Join(args, " "))

	command := i.execInterface.Command(args[0], args[1:]...)

	log.Log.V(2).Info("Executing command", "device", device.SerialNumber, "command", strings.Join(args, " "))
	output, err := command.Output()
	if err != nil {
		log.Log.V(2).Error(err, "Command execution failed", "device", device.SerialNumber, "path", path, "output", string(output))
		return fmt.Errorf("failed to set path %s: %v, output: %s", path, err, string(output))
	}
	log.Log.V(2).Info("RunSetPathCommand successful", "device", device.SerialNumber, "path", path)

	return nil
}

// GetQoSSettings returns the current QoS settings (trust mode and PFC configuration)
func (i *dmsClient) GetQoSSettings(interfaceName string) (*v1alpha1.QosSpec, error) {
	device := i.deviceSnapshot()
	log.Log.V(2).Info("dmsClient.GetQoSSettings()", "interfaceName", interfaceName, "device", device.SerialNumber)

	log.Log.V(2).Info("Getting trust mode", "device", device.SerialNumber, "interface", interfaceName)
	trust, err := i.RunGetPathCommand(QoSTrustModePath, interfaceNameFilter(interfaceName))
	if err != nil {
		log.Log.V(2).Error(err, "Failed to get trust mode", "device", device.SerialNumber, "interface", interfaceName)
		return nil, fmt.Errorf("failed to get trust mode: %v", err)
	}
	log.Log.V(2).Info("Trust mode retrieved", "device", device.SerialNumber, "trust", trust)

	log.Log.V(2).Info("Getting PFC configuration", "device", device.SerialNumber, "interface", interfaceName)
	pfc, err := i.RunGetPathCommand(QoSPFCPath, interfaceNameFilter(interfaceName))
	if err != nil {
		log.Log.V(2).Error(err, "Failed to get PFC configuration", "device", device.SerialNumber, "interface", interfaceName)
		return nil, fmt.Errorf("failed to get PFC configuration: %v", err)
	}
	log.Log.V(2).Info("PFC configuration retrieved", "device", device.SerialNumber, "pfc", pfc)

	log.Log.V(2).Info("QoS settings for interface", "interfaceName", interfaceName, "trust", trust, "pfc", pfc)

	// PFC settings are comma-separated values, e.g. "0,0,0,1,0,0,0,0". DMS returns a digit-only string, e.g. "00001000".
	// So, we need to convert the string to a comma-separated value.
	pfcFormatted := strings.Join(strings.Split(pfc, ""), ",")
	log.Log.V(2).Info("Formatted PFC configuration", "device", device.SerialNumber, "original", pfc, "formatted", pfcFormatted)

	log.Log.V(2).Info("Getting ToS configuration", "device", device.SerialNumber, "interface", interfaceName)
	tos, err := i.RunGetPathCommand(ToSPath, interfaceNameFilter(interfaceName))
	if err != nil {
		log.Log.V(2).Error(err, "Failed to get ToS configuration", "device", device.SerialNumber, "interface", interfaceName)
		return nil, fmt.Errorf("failed to get ToS configuration: %v", err)
	}
	tosFormatted, err := strconv.Atoi(tos)
	if err != nil {
		log.Log.V(2).Error(err, "Failed to convert ToS to int", "device", device.SerialNumber, "interface", interfaceName)
		return nil, fmt.Errorf("failed to convert ToS to int: %v", err)
	}
	log.Log.V(2).Info("ToS configuration retrieved", "device", device.SerialNumber, "tos", tosFormatted)

	return &v1alpha1.QosSpec{
		Trust: trust,
		PFC:   pfcFormatted,
		ToS:   tosFormatted,
	}, nil
}

// SetQoSSettings sets the QoS settings for the device (trust and PFC configuration). Settings are applied to all ports of the device.
func (i *dmsClient) SetQoSSettings(spec *v1alpha1.QosSpec) error {
	device := i.deviceSnapshot()
	log.Log.V(2).Info("dmsClient.SetQoSSettings()", "spec", spec, "device", device.SerialNumber)

	trust := ""
	switch spec.Trust {
	case consts.TrustModeDscp:
		trust = "dscp"
	case consts.TrustModePfc:
		trust = "pfc"
	default:
		log.Log.V(2).Info("Invalid trust mode", "device", device.SerialNumber, "trust", trust)
		return fmt.Errorf("invalid trust mode: %s", trust)
	}
	log.Log.V(2).Info("Normalized trust mode", "device", device.SerialNumber, "trust", trust)

	// PFC settings are comma-separated values, e.g. "0,0,0,1,0,0,0,0". DMS requires a digit-only string, e.g. "00001000"
	pfcFormatted := strings.ReplaceAll(spec.PFC, ",", "")
	log.Log.V(2).Info("Formatted PFC configuration", "device", device.SerialNumber, "original", spec.PFC, "formatted", pfcFormatted)

	portCount := len(device.Ports)
	log.Log.V(2).Info("Setting QoS settings on all ports", "device", device.SerialNumber, "portCount", portCount)

	for idx, port := range device.Ports {
		log.Log.V(2).Info("Setting trust mode", "device", device.SerialNumber, "port", idx+1, "interface", port.NetworkInterface)
		err := i.RunSetPathCommand(QoSTrustModePath, trust, ValueTypeString, interfaceNameFilter(port.NetworkInterface))
		if err != nil {
			log.Log.V(2).Error(err, "Failed to set trust mode", "device", device.SerialNumber, "interface", port.NetworkInterface)
			return fmt.Errorf("failed to set trust mode: %v", err)
		}
		log.Log.V(2).Info("Trust mode set successfully", "device", device.SerialNumber, "interface", port.NetworkInterface)

		log.Log.V(2).Info("Setting PFC configuration", "device", device.SerialNumber, "port", idx+1, "interface", port.NetworkInterface)
		err = i.RunSetPathCommand(QoSPFCPath, pfcFormatted, ValueTypeString, interfaceNameFilter(port.NetworkInterface))
		if err != nil {
			log.Log.V(2).Error(err, "Failed to set PFC configuration", "device", device.SerialNumber, "interface", port.NetworkInterface)
			return fmt.Errorf("failed to set PFC configuration: %v", err)
		}
		log.Log.V(2).Info("PFC configuration set successfully", "device", device.SerialNumber, "interface", port.NetworkInterface)

		if spec.ToS != 0 {
			log.Log.V(2).Info("Setting ToS configuration", "device", device.SerialNumber, "port", idx+1, "interface", port.NetworkInterface)
			err = i.RunSetPathCommand(ToSPath, fmt.Sprintf("%d", spec.ToS), ValueTypeInt, interfaceNameFilter(port.NetworkInterface))
			if err != nil {
				log.Log.V(2).Error(err, "Failed to set ToS configuration", "device", device.SerialNumber, "interface", port.NetworkInterface)
				return fmt.Errorf("failed to set ToS configuration: %v", err)
			}
			log.Log.V(2).Info("ToS configuration set successfully", "device", device.SerialNumber, "interface", port.NetworkInterface)
		}
	}

	log.Log.V(2).Info("QoS settings applied to all ports", "device", device.SerialNumber, "portCount", portCount)
	return nil
}

// InstallBFB installs the BFB file with the new firmware version on a BlueField device
func (i *dmsClient) InstallBFB(ctx context.Context, version string, bfbPath string) error {
	device := i.deviceSnapshot()
	log.Log.V(2).Info("dmsClient.InstallBFB()", "version", version, "bfbPath", bfbPath, "device", device.SerialNumber)

	if !utils.IsBlueFieldDevice(device.Type) {
		err := fmt.Errorf("cannot install BFB file on non-BlueField device")
		log.Log.Error(err, "failed to install BFB", "device", device.SerialNumber, "deviceType", device.Type)
		return err
	}

	args := append([]string{dmsClientPath, "-a", i.bindAddress}, i.authParams...)
	args = append(args, "--target", i.targetPCI, "os", "install", "--version", version, "--pkg", bfbPath)
	log.Log.V(2).Info("dmsClient.InstallBFB() install command", "args", strings.Join(args, " "))

	command := i.execInterface.CommandContext(ctx, args[0], args[1:]...)
	output, err := utils.RunCommand(command)
	if err != nil {
		log.Log.Error(err, "failed to install BFB", "device", device.SerialNumber, "deviceType", device.Type)
		return err
	}

	log.Log.V(2).Info("BFB installed successfully", "device", device.SerialNumber, "version", version, "output", string(output))

	args = append([]string{dmsClientPath, "-a", i.bindAddress}, i.authParams...)
	args = append(args, "--target", i.targetPCI, "os", "activate", "--version", version)
	log.Log.V(2).Info("dmsClient.InstallBFB() activate command", "args", strings.Join(args, " "))

	command = i.execInterface.CommandContext(ctx, args[0], args[1:]...)
	output, err = utils.RunCommand(command)
	if err != nil {
		log.Log.Error(err, "failed to activate BFB", "device", device.SerialNumber, "deviceType", device.Type)
		return err
	}

	log.Log.V(2).Info("BFB activated successfully", "device", device.SerialNumber, "version", version, "output", string(output))

	return nil
}

func (i *dmsClient) GetParameters(params []types.ConfigurationParameter) (map[string]string, error) {
	device := i.deviceSnapshot()
	log.Log.V(2).Info("dmsClient.GetParameters()", "params", params, "device", device.SerialNumber)

	values := make(map[string]string)
	paramByPath := make(map[string]types.ConfigurationParameter, len(params))
	for _, param := range params {
		paramByPath[param.DMSPath] = param
	}

	batches := collectGetPathRequestBatches(device, params)
	for _, batch := range batches {
		requestValues, err := i.RunGetPathCommands(batch)
		if err != nil {
			log.Log.V(2).Error(err, "Failed to get parameters", "device", device.SerialNumber, "batchKey", batch.key)
			if types.IsValuesDoNotMatchError(err) {
				return nil, err
			}
			return nil, fmt.Errorf("failed to get parameter: %v", err)
		}

		for path, result := range requestValues {
			param, ok := paramByPath[path]
			if !ok {
				return nil, fmt.Errorf("unexpected value found for path %s", path)
			}
			value, found := values[path]
			if found && result != value {
				err := types.ValuesDoNotMatchError(param, result)
				log.Log.V(2).Error(err, "Failed to get parameter", "device", device.SerialNumber, "param", param)
				return nil, err
			}
			values[path] = result
		}
	}

	return values, nil
}

func collectGetPathRequestBatches(device v1alpha1.NicDeviceStatus, params []types.ConfigurationParameter) []getPathRequestBatch {
	batches := []getPathRequestBatch{}
	batchIndexes := make(map[string]int)
	appendRequest := func(batchKey string, request getPathRequest) {
		batchIndex, found := batchIndexes[batchKey]
		if !found {
			batchIndex = len(batches)
			batchIndexes[batchKey] = batchIndex
			batches = append(batches, getPathRequestBatch{key: batchKey})
		}
		batches[batchIndex].requests = append(batches[batchIndex].requests, request)
	}

	for _, param := range params {
		paramPathParts := strings.Split(param.DMSPath, "/")

		if slices.Contains(paramPathParts, Interface) {
			ports := device.Ports
			if param.HwplbFirstPortOnly && len(ports) > 0 {
				ports = ports[:1]
			}
			for _, port := range ports {
				filterRules := interfaceNameFilter(port.NetworkInterface)
				batchKey := getPathRequestBatchKey(filterRules)

				if slices.Contains(paramPathParts, Priority) {
					for id := 0; id < 8; id++ {
						fr := mergeFilterRules(filterRules, priorityIdFilter(id))
						appendRequest(batchKey, newGetPathRequest(param.DMSPath, fr))
					}
				} else {
					appendRequest(batchKey, newGetPathRequest(param.DMSPath, filterRules))
				}
			}
		} else {
			appendRequest(getPathRequestBatchKey(nil), newGetPathRequest(param.DMSPath, nil))
		}
	}
	return batches
}

// collectSetUpdates expands all parameters into individual "path:::type:::value" update entries,
// applying interface and priority filter rules as needed.
func (i *dmsClient) collectSetUpdates(device v1alpha1.NicDeviceStatus, params []types.ConfigurationParameter) []string {
	var updates []string

	for _, param := range params {
		paramPathParts := strings.Split(param.DMSPath, "/")

		if slices.Contains(paramPathParts, Interface) {
			ports := device.Ports
			if param.HwplbFirstPortOnly && len(ports) > 0 {
				ports = ports[:1]
			}
			for _, port := range ports {
				filterRules := interfaceNameFilter(port.NetworkInterface)

				if slices.Contains(paramPathParts, Priority) {
					for id := 0; id < 8; id++ {
						fr := mergeFilterRules(filterRules, priorityIdFilter(id))
						updates = append(updates, formatSetUpdate(param.DMSPath, param.Value, param.ValueType, fr))
					}
				} else {
					updates = append(updates, formatSetUpdate(param.DMSPath, param.Value, param.ValueType, filterRules))
				}
			}
		} else {
			updates = append(updates, formatSetUpdate(param.DMSPath, param.Value, param.ValueType, nil))
		}
	}

	return updates
}

func (i *dmsClient) SetParameters(params []types.ConfigurationParameter) error {
	device := i.deviceSnapshot()
	log.Log.V(2).Info("dmsClient.SetParameters()", "params", params, "device", device.SerialNumber)

	updates := i.collectSetUpdates(device, params)
	if len(updates) == 0 {
		log.Log.V(2).Info("No updates to set", "device", device.SerialNumber)
		return nil
	}

	log.Log.V(2).Info("Collected set updates", "device", device.SerialNumber, "updateCount", len(updates))

	args := append([]string{dmsClientPath, "-a", i.bindAddress}, i.authParams...)
	args = append(args, "--target", i.targetPCI, "--timeout", dmsClientTimeout, "set")
	for _, update := range updates {
		args = append(args, "--update", update)
	}
	log.Log.V(2).Info("dmsClient.SetParameters() batch command", "args", strings.Join(args, " "))

	command := i.execInterface.Command(args[0], args[1:]...)
	output, err := command.CombinedOutput()
	log.Log.V(2).Info("dmsClient.SetParameters() batch output", "device", device.SerialNumber, "output", string(output))
	if err != nil {
		allIgnore := true
		for _, param := range params {
			if !param.IgnoreError {
				allIgnore = false
				break
			}
		}
		if allIgnore {
			log.Log.V(2).Info("All parameters have IgnoreError set, ignoring batch error", "device", device.SerialNumber, "err", err)
			return nil
		}
		log.Log.V(2).Error(err, "Batch set command failed", "device", device.SerialNumber, "output", string(output))
		return fmt.Errorf("failed to set parameters: %v, output: %s", err, string(output))
	}

	log.Log.V(2).Info("SetParameters batch command successful", "device", device.SerialNumber)
	return nil
}
