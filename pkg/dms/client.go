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

	dmsClientPath = "/opt/mellanox/doca/services/dms/dmsc"
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
	device        v1alpha1.NicDeviceStatus
	targetPCI     string
	bindAddress   string
	execInterface execUtils.Interface
}

// injectFilterRules takes a DMS path and a map of filter rules, and injects the rules into the path
// Example:
// path: /interfaces/interface/nvidia/qos/config/trust-mode
// filters: {"interface": "name=enp3s0f0np0"}
// Returns: /interfaces/interface[name=enp3s0f0np0]/nvidia/qos/config/trust-mode
func injectFilterRules(path string, filters map[string]string) string {
	log.Log.V(2).Info("injectFilterRules", "path", path, "filters", filters)
	pathParts := strings.Split(path, "/")
	for i, part := range pathParts {
		if filter, exists := filters[part]; exists {
			pathParts[i] = fmt.Sprintf("%s[%s]", part, filter)
		}
	}
	result := strings.Join(pathParts, "/")
	log.Log.V(2).Info("injectFilterRules result", "original", path, "filtered", result)
	return result
}

// interfaceNameFilter returns a filter map for DMS path filtering based on interface name
func interfaceNameFilter(interfaceName string) map[string]string {
	log.Log.V(2).Info("interfaceNameFilter", "interfaceName", interfaceName)
	return map[string]string{"interface": fmt.Sprintf("name=%s", interfaceName)}
}

func priorityIdFilter(priorityId int) map[string]string {
	log.Log.V(2).Info("priorityIdFilter", "priorityId", priorityId)
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

func (i *dmsClient) RunGetPathCommand(path string, filterRules map[string]string) (string, error) {
	log.Log.V(2).Info("dmsClient.RunGetPathCommand()", "path", path, "filterRules", filterRules, "device", i.device.SerialNumber)

	queryPath := injectFilterRules(path, filterRules)

	args := []string{dmsClientPath, "-a", i.bindAddress, "--insecure", "--target", i.targetPCI, "get", "--path", queryPath}
	log.Log.V(2).Info("dmsClient.RunGetPathCommand()", "args", strings.Join(args, " "))

	command := i.execInterface.Command(args[0], args[1:]...)

	log.Log.V(2).Info("Executing command", "device", i.device.SerialNumber, "command", strings.Join(args, " "))
	output, err := command.Output()
	if err != nil {
		log.Log.V(2).Error(err, "Command execution failed", "device", i.device.SerialNumber, "path", path)
		return "", fmt.Errorf("failed to run get path command: %v", err)
	}
	log.Log.V(2).Info("Command execution successful", "device", i.device.SerialNumber, "outputSize", len(output))

	var result []struct {
		Source    string `json:"source"`
		Timestamp int64  `json:"timestamp"`
		Time      string `json:"time"`
		Updates   []struct {
			Path   string            `json:"Path"`
			Values map[string]string `json:"values"`
		} `json:"updates"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		log.Log.V(2).Error(err, "Failed to unmarshal command output", "device", i.device.SerialNumber)
		return "", fmt.Errorf("failed to unmarshal command output: %v", err)
	}

	log.Log.V(2).Info("dmsClient.RunGetPathCommand()", "json result", result)

	if len(result) == 0 || len(result[0].Updates) == 0 {
		log.Log.V(2).Info("No updates found in command output", "device", i.device.SerialNumber)
		return "", fmt.Errorf("no updates found in command output")
	}

	// we have to remove the leading "/" from the path, because DMS returns the path without it
	// Remove all "[... ]" blocks from the path for lookup to match the values map keys
	lookupPath := stripSquareBracketClauses(strings.TrimPrefix(path, "/"))
	value, ok := result[0].Updates[0].Values[lookupPath]
	if !ok {
		log.Log.V(2).Info("Value not found for path", "device", i.device.SerialNumber, "path", path)
		return "", fmt.Errorf("value not found for path %s", path)
	}

	log.Log.V(2).Info("RunGetPathCommand successful", "device", i.device.SerialNumber, "path", path, "value", value)
	return value, nil
}

func (i *dmsClient) RunSetPathCommand(path, value, valueType string, filterRules map[string]string) error {
	log.Log.V(2).Info("dmsClient.RunSetPathCommand()", "path", path, "value", value, "valueType", valueType, "device", i.device.SerialNumber)

	queryPath := injectFilterRules(path, filterRules)

	args := []string{dmsClientPath, "-a", i.bindAddress, "--insecure", "--target", i.targetPCI, "set", "--update", fmt.Sprintf("%s:::%s:::%s", queryPath, valueType, value)}
	log.Log.V(2).Info("dmsClient.RunSetPathCommand()", "args", strings.Join(args, " "))

	command := i.execInterface.Command(args[0], args[1:]...)

	log.Log.V(2).Info("Executing command", "device", i.device.SerialNumber, "command", strings.Join(args, " "))
	output, err := command.Output()
	if err != nil {
		log.Log.V(2).Error(err, "Command execution failed", "device", i.device.SerialNumber, "path", path, "output", string(output))
		return fmt.Errorf("failed to set path %s: %v, output: %s", path, err, string(output))
	}
	log.Log.V(2).Info("RunSetPathCommand successful", "device", i.device.SerialNumber, "path", path)

	return nil
}

// GetQoSSettings returns the current QoS settings (trust mode and PFC configuration)
func (i *dmsClient) GetQoSSettings(interfaceName string) (*v1alpha1.QosSpec, error) {
	log.Log.V(2).Info("dmsClient.GetQoSSettings()", "interfaceName", interfaceName, "device", i.device.SerialNumber)

	log.Log.V(2).Info("Getting trust mode", "device", i.device.SerialNumber, "interface", interfaceName)
	trust, err := i.RunGetPathCommand(QoSTrustModePath, interfaceNameFilter(interfaceName))
	if err != nil {
		log.Log.V(2).Error(err, "Failed to get trust mode", "device", i.device.SerialNumber, "interface", interfaceName)
		return nil, fmt.Errorf("failed to get trust mode: %v", err)
	}
	log.Log.V(2).Info("Trust mode retrieved", "device", i.device.SerialNumber, "trust", trust)

	log.Log.V(2).Info("Getting PFC configuration", "device", i.device.SerialNumber, "interface", interfaceName)
	pfc, err := i.RunGetPathCommand(QoSPFCPath, interfaceNameFilter(interfaceName))
	if err != nil {
		log.Log.V(2).Error(err, "Failed to get PFC configuration", "device", i.device.SerialNumber, "interface", interfaceName)
		return nil, fmt.Errorf("failed to get PFC configuration: %v", err)
	}
	log.Log.V(2).Info("PFC configuration retrieved", "device", i.device.SerialNumber, "pfc", pfc)

	log.Log.V(2).Info("QoS settings for interface", "interfaceName", interfaceName, "trust", trust, "pfc", pfc)

	// PFC settings are comma-separated values, e.g. "0,0,0,1,0,0,0,0". DMS returns a digit-only string, e.g. "00001000".
	// So, we need to convert the string to a comma-separated value.
	pfcFormatted := strings.Join(strings.Split(pfc, ""), ",")
	log.Log.V(2).Info("Formatted PFC configuration", "device", i.device.SerialNumber, "original", pfc, "formatted", pfcFormatted)

	log.Log.V(2).Info("Getting ToS configuration", "device", i.device.SerialNumber, "interface", interfaceName)
	tos, err := i.RunGetPathCommand(ToSPath, interfaceNameFilter(interfaceName))
	if err != nil {
		log.Log.V(2).Error(err, "Failed to get ToS configuration", "device", i.device.SerialNumber, "interface", interfaceName)
		return nil, fmt.Errorf("failed to get ToS configuration: %v", err)
	}
	tosFormatted, err := strconv.Atoi(tos)
	if err != nil {
		log.Log.V(2).Error(err, "Failed to convert ToS to int", "device", i.device.SerialNumber, "interface", interfaceName)
		return nil, fmt.Errorf("failed to convert ToS to int: %v", err)
	}
	log.Log.V(2).Info("ToS configuration retrieved", "device", i.device.SerialNumber, "tos", tosFormatted)

	return &v1alpha1.QosSpec{
		Trust: trust,
		PFC:   pfcFormatted,
		ToS:   tosFormatted,
	}, nil
}

// SetQoSSettings sets the QoS settings for the device (trust and PFC configuration). Settings are applied to all ports of the device.
func (i *dmsClient) SetQoSSettings(spec *v1alpha1.QosSpec) error {
	log.Log.V(2).Info("dmsClient.SetQoSSettings()", "spec", spec, "device", i.device.SerialNumber)

	trust := ""
	switch spec.Trust {
	case consts.TrustModeDscp:
		trust = "dscp"
	case consts.TrustModePfc:
		trust = "pfc"
	default:
		log.Log.V(2).Info("Invalid trust mode", "device", i.device.SerialNumber, "trust", trust)
		return fmt.Errorf("invalid trust mode: %s", trust)
	}
	log.Log.V(2).Info("Normalized trust mode", "device", i.device.SerialNumber, "trust", trust)

	// PFC settings are comma-separated values, e.g. "0,0,0,1,0,0,0,0". DMS requires a digit-only string, e.g. "00001000"
	pfcFormatted := strings.ReplaceAll(spec.PFC, ",", "")
	log.Log.V(2).Info("Formatted PFC configuration", "device", i.device.SerialNumber, "original", spec.PFC, "formatted", pfcFormatted)

	portCount := len(i.device.Ports)
	log.Log.V(2).Info("Setting QoS settings on all ports", "device", i.device.SerialNumber, "portCount", portCount)

	for idx, port := range i.device.Ports {
		log.Log.V(2).Info("Setting trust mode", "device", i.device.SerialNumber, "port", idx+1, "interface", port.NetworkInterface)
		err := i.RunSetPathCommand(QoSTrustModePath, trust, ValueTypeString, interfaceNameFilter(port.NetworkInterface))
		if err != nil {
			log.Log.V(2).Error(err, "Failed to set trust mode", "device", i.device.SerialNumber, "interface", port.NetworkInterface)
			return fmt.Errorf("failed to set trust mode: %v", err)
		}
		log.Log.V(2).Info("Trust mode set successfully", "device", i.device.SerialNumber, "interface", port.NetworkInterface)

		log.Log.V(2).Info("Setting PFC configuration", "device", i.device.SerialNumber, "port", idx+1, "interface", port.NetworkInterface)
		err = i.RunSetPathCommand(QoSPFCPath, pfcFormatted, ValueTypeString, interfaceNameFilter(port.NetworkInterface))
		if err != nil {
			log.Log.V(2).Error(err, "Failed to set PFC configuration", "device", i.device.SerialNumber, "interface", port.NetworkInterface)
			return fmt.Errorf("failed to set PFC configuration: %v", err)
		}
		log.Log.V(2).Info("PFC configuration set successfully", "device", i.device.SerialNumber, "interface", port.NetworkInterface)

		if spec.ToS != 0 {
			log.Log.V(2).Info("Setting ToS configuration", "device", i.device.SerialNumber, "port", idx+1, "interface", port.NetworkInterface)
			err = i.RunSetPathCommand(ToSPath, fmt.Sprintf("%d", spec.ToS), ValueTypeInt, interfaceNameFilter(port.NetworkInterface))
			if err != nil {
				log.Log.V(2).Error(err, "Failed to set ToS configuration", "device", i.device.SerialNumber, "interface", port.NetworkInterface)
				return fmt.Errorf("failed to set ToS configuration: %v", err)
			}
			log.Log.V(2).Info("ToS configuration set successfully", "device", i.device.SerialNumber, "interface", port.NetworkInterface)
		}
	}

	log.Log.V(2).Info("QoS settings applied to all ports", "device", i.device.SerialNumber, "portCount", portCount)
	return nil
}

// InstallBFB installs the BFB file with the new firmware version on a BlueField device
func (i *dmsClient) InstallBFB(ctx context.Context, version string, bfbPath string) error {
	log.Log.V(2).Info("dmsClient.InstallBFB()", "version", version, "bfbPath", bfbPath, "device", i.device.SerialNumber)

	if !utils.IsBlueFieldDevice(i.device.Type) {
		err := fmt.Errorf("cannot install BFB file on non-BlueField device")
		log.Log.Error(err, "failed to install BFB", "device", i.device.SerialNumber, "deviceType", i.device.Type)
		return err
	}

	args := []string{dmsClientPath, "-a", i.bindAddress, "--insecure", "--target", i.targetPCI, "os", "install", "--version", version, "--pkg", bfbPath}
	log.Log.V(2).Info("dmsClient.InstallBFB() install command", "args", strings.Join(args, " "))

	command := i.execInterface.CommandContext(ctx, args[0], args[1:]...)
	output, err := utils.RunCommand(command)
	if err != nil {
		log.Log.Error(err, "failed to install BFB", "device", i.device.SerialNumber, "deviceType", i.device.Type)
		return err
	}

	log.Log.V(2).Info("BFB installed successfully", "device", i.device.SerialNumber, "version", version, "output", string(output))

	args = []string{dmsClientPath, "-a", i.bindAddress, "--insecure", "--target", i.targetPCI, "os", "activate", "--version", version}
	log.Log.V(2).Info("dmsClient.InstallBFB() activate command", "args", strings.Join(args, " "))

	command = i.execInterface.CommandContext(ctx, args[0], args[1:]...)
	output, err = utils.RunCommand(command)
	if err != nil {
		log.Log.Error(err, "failed to activate BFB", "device", i.device.SerialNumber, "deviceType", i.device.Type)
		return err
	}

	log.Log.V(2).Info("BFB activated successfully", "device", i.device.SerialNumber, "version", version, "output", string(output))

	return nil
}

func (i *dmsClient) getCompareReplaceValue(param *types.ConfigurationParameter, filterRules map[string]string, value *string) error {
	result, err := i.RunGetPathCommand(param.DMSPath, filterRules)
	if err != nil {
		log.Log.V(2).Error(err, "Failed to get parameter", "device", i.device.SerialNumber, "param", param)
		return fmt.Errorf("failed to get parameter: %v", err)
	}

	if *value != "" && result != *value {
		err = types.ValuesDoNotMatchError(*param, result)
		log.Log.V(2).Error(err, "Failed to get parameter", "device", i.device.SerialNumber, "param", param)
		return err
	}

	*value = result
	return nil

}

func (i *dmsClient) GetParameters(params []types.ConfigurationParameter) (map[string]string, error) {
	log.Log.V(2).Info("dmsClient.GetParameters()", "params", params, "device", i.device.SerialNumber)

	values := make(map[string]string)

	for _, param := range params {
		paramPathParts := strings.Split(param.DMSPath, "/")
		value := ""

		if slices.Contains(paramPathParts, Interface) {
			for _, port := range i.device.Ports {
				filterRules := interfaceNameFilter(port.NetworkInterface)

				if slices.Contains(paramPathParts, Priority) {
					for id := 0; id < 8; id++ {
						filterRules := mergeFilterRules(filterRules, priorityIdFilter(id))

						err := i.getCompareReplaceValue(&param, filterRules, &value)
						if err != nil {
							return nil, err
						}
					}
				} else {
					err := i.getCompareReplaceValue(&param, filterRules, &value)
					if err != nil {
						return nil, err
					}
				}
			}

		} else {
			var err error
			value, err = i.RunGetPathCommand(param.DMSPath, nil)
			if err != nil {
				log.Log.V(2).Error(err, "Failed to get parameter", "device", i.device.SerialNumber, "param", param)
				return nil, fmt.Errorf("failed to get parameter: %v", err)
			}
		}

		values[param.DMSPath] = value
	}
	return values, nil
}

func (i *dmsClient) SetParameters(params []types.ConfigurationParameter) error {
	log.Log.V(2).Info("dmsClient.SetParameters()", "params", params, "device", i.device.SerialNumber)

	for _, param := range params {
		paramPathParts := strings.Split(param.DMSPath, "/")

		if slices.Contains(paramPathParts, Interface) {
			for _, port := range i.device.Ports {
				filterRules := interfaceNameFilter(port.NetworkInterface)

				if slices.Contains(paramPathParts, Priority) {
					for id := 0; id < 8; id++ {
						filterRules := mergeFilterRules(filterRules, priorityIdFilter(id))

						err := i.RunSetPathCommand(param.DMSPath, param.Value, param.ValueType, filterRules)

						if err != nil {
							if param.IgnoreError {
								log.Log.V(2).Info("IgnoreError flag explicitly set for param, ignoring error", "device", i.device.SerialNumber, "param", param)
								continue
							}
							return err
						}
					}

				} else {
					err := i.RunSetPathCommand(param.DMSPath, param.Value, param.ValueType, filterRules)

					if err != nil {
						if param.IgnoreError {
							log.Log.V(2).Info("IgnoreError flag explicitly set for param, ignoring error", "device", i.device.SerialNumber, "param", param)
							continue
						}
						return err
					}
				}
			}
		} else {
			err := i.RunSetPathCommand(param.DMSPath, param.Value, param.ValueType, nil)
			if err != nil {
				if param.IgnoreError {
					log.Log.V(2).Info("IgnoreError flag explicitly set for param, ignoring error", "device", i.device.SerialNumber, "param", param)
					continue
				}
				return err
			}
		}
	}

	return nil
}
