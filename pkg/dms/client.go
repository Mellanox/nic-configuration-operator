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
	"strings"
	"sync"
	"sync/atomic"

	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

const (
	ValueNameTrustMode = "trust-mode"
	ValueNamePFC       = "pfc"
	ValueTypeString    = "string"
	QoSConfigPath      = "/interfaces/interface/nvidia/qos/config"
	QoSTrustModePath   = QoSConfigPath + "/" + ValueNameTrustMode
	QoSPFCPath         = QoSConfigPath + "/" + ValueNamePFC
	dmsClientPath      = "/opt/mellanox/doca/services/dms/dmsc"
)

// DMSClient interface defines methods for interacting with a DMS instance to manage NIC device configuration
type DMSClient interface {
	// IsRunning returns whether the DMS instance is running
	IsRunning() bool
	// GetQoSSettings returns the current QoS settings (trust mode and PFC configuration)
	GetQoSSettings(interfaceName string) (string, string, error)
	// SetQoSSettings sets the QoS settings for the device (trust mode and PFC configuration). Settings are applied to all ports of the device.
	SetQoSSettings(trustMode, pfc string) error
	// InstallBFB installs the BFB file with the new firmware version on a BlueField device
	InstallBFB(ctx context.Context, version string, bfbPath string) error
}

// dmsInstance implements the DMSClient interface
type dmsInstance struct {
	device        v1alpha1.NicDeviceStatus
	cmd           execUtils.Cmd
	bindAddress   string
	execInterface execUtils.Interface

	running atomic.Bool

	// Error handling with mutex protection
	errMutex sync.RWMutex
	cmdErr   error
}

// IsRunning returns whether the DMS instance is running
func (i *dmsInstance) IsRunning() bool {
	log.Log.V(2).Info("dmsInstance.IsRunning()", "device", i.device.SerialNumber, "running", i.running.Load())
	return i.running.Load()
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

func (i *dmsInstance) RunGetPathCommand(path string, filterRules map[string]string) (string, error) {
	log.Log.V(2).Info("dmsInstance.RunGetPathCommand()", "path", path, "filterRules", filterRules, "device", i.device.SerialNumber)

	queryPath := injectFilterRules(path, filterRules)

	args := []string{dmsClientPath, "-a", i.bindAddress, "--insecure", "get", "--path", queryPath}
	log.Log.V(2).Info("dmsInstance.RunGetPathCommand()", "args", strings.Join(args, " "))

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

	log.Log.V(2).Info("dmsInstance.RunGetPathCommand()", "json result", result)

	if len(result) == 0 || len(result[0].Updates) == 0 {
		log.Log.V(2).Info("No updates found in command output", "device", i.device.SerialNumber)
		return "", fmt.Errorf("no updates found in command output")
	}

	// we have to remove the leading "/" from the path, because DMS returns the path without it
	value, ok := result[0].Updates[0].Values[path[1:]]
	if !ok {
		log.Log.V(2).Info("Value not found for path", "device", i.device.SerialNumber, "path", path)
		return "", fmt.Errorf("value not found for path %s", path)
	}

	log.Log.V(2).Info("RunGetPathCommand successful", "device", i.device.SerialNumber, "path", path, "value", value)
	return value, nil
}

func (i *dmsInstance) RunSetPathCommand(path, value, valueType string, filterRules map[string]string) error {
	log.Log.V(2).Info("dmsInstance.RunSetPathCommand()", "path", path, "value", value, "valueType", valueType, "device", i.device.SerialNumber)

	queryPath := injectFilterRules(path, filterRules)

	args := []string{dmsClientPath, "-a", i.bindAddress, "--insecure", "set", "--update", fmt.Sprintf("%s:::%s:::%s", queryPath, valueType, value)}
	log.Log.V(2).Info("dmsInstance.RunSetPathCommand()", "args", strings.Join(args, " "))

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
func (i *dmsInstance) GetQoSSettings(interfaceName string) (string, string, error) {
	log.Log.V(2).Info("dmsInstance.GetQoSSettings()", "interfaceName", interfaceName, "device", i.device.SerialNumber)

	if !i.running.Load() {
		log.Log.V(2).Info("DMS instance not running", "device", i.device.SerialNumber)
		return "", "", fmt.Errorf("DMS instance is not running")
	}

	log.Log.V(2).Info("Getting trust mode", "device", i.device.SerialNumber, "interface", interfaceName)
	trust, err := i.RunGetPathCommand(QoSTrustModePath, interfaceNameFilter(interfaceName))
	if err != nil {
		log.Log.V(2).Error(err, "Failed to get trust mode", "device", i.device.SerialNumber, "interface", interfaceName)
		return "", "", fmt.Errorf("failed to get trust mode: %v", err)
	}
	log.Log.V(2).Info("Trust mode retrieved", "device", i.device.SerialNumber, "trust", trust)

	log.Log.V(2).Info("Getting PFC configuration", "device", i.device.SerialNumber, "interface", interfaceName)
	pfc, err := i.RunGetPathCommand(QoSPFCPath, interfaceNameFilter(interfaceName))
	if err != nil {
		log.Log.V(2).Error(err, "Failed to get PFC configuration", "device", i.device.SerialNumber, "interface", interfaceName)
		return "", "", fmt.Errorf("failed to get PFC configuration: %v", err)
	}
	log.Log.V(2).Info("PFC configuration retrieved", "device", i.device.SerialNumber, "pfc", pfc)

	log.Log.V(2).Info("QoS settings for interface", "interfaceName", interfaceName, "trust", trust, "pfc", pfc)

	// PFC settings are comma-separated values, e.g. "0,0,0,1,0,0,0,0". DMS returns a digit-only string, e.g. "00001000".
	// So, we need to convert the string to a comma-separated value.
	pfcFormatted := strings.Join(strings.Split(pfc, ""), ",")
	log.Log.V(2).Info("Formatted PFC configuration", "device", i.device.SerialNumber, "original", pfc, "formatted", pfcFormatted)

	return trust, pfcFormatted, nil
}

// SetQoSSettings sets the QoS settings for the device (trust and PFC configuration). Settings are applied to all ports of the device.
func (i *dmsInstance) SetQoSSettings(trust, pfc string) error {
	log.Log.V(2).Info("dmsInstance.SetQoSSettings()", "trust", trust, "pfc", pfc, "device", i.device.SerialNumber)

	if !i.running.Load() {
		log.Log.V(2).Info("DMS instance not running", "device", i.device.SerialNumber)
		return fmt.Errorf("DMS instance is not running")
	}

	switch trust {
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
	pfcFormatted := strings.ReplaceAll(pfc, ",", "")
	log.Log.V(2).Info("Formatted PFC configuration", "device", i.device.SerialNumber, "original", pfc, "formatted", pfcFormatted)

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
	}

	log.Log.V(2).Info("QoS settings applied to all ports", "device", i.device.SerialNumber, "portCount", portCount)
	return nil
}

// InstallBFB installs the BFB file with the new firmware version on a BlueField device
func (i *dmsInstance) InstallBFB(ctx context.Context, version string, bfbPath string) error {
	log.Log.V(2).Info("dmsInstance.InstallBFB()", "version", version, "bfbPath", bfbPath, "device", i.device.SerialNumber)

	if !i.running.Load() {
		log.Log.V(2).Info("DMS instance not running", "device", i.device.SerialNumber)
		return fmt.Errorf("DMS instance is not running")
	}

	if !utils.IsBlueFieldDevice(i.device.Type) {
		err := fmt.Errorf("cannot install BFB file on non-BlueField device")
		log.Log.Error(err, "failed to install BFB", "device", i.device.SerialNumber, "deviceType", i.device.Type)
		return err
	}

	args := []string{dmsClientPath, "-a", i.bindAddress, "--insecure", "os", "install", "--version", version, "--pkg", bfbPath}
	log.Log.V(2).Info("dmsInstance.InstallBFB() install command", "args", strings.Join(args, " "))

	command := i.execInterface.CommandContext(ctx, args[0], args[1:]...)
	output, err := utils.RunCommand(command)
	if err != nil {
		log.Log.Error(err, "failed to install BFB", "device", i.device.SerialNumber, "deviceType", i.device.Type)
		return err
	}

	log.Log.V(2).Info("BFB installed successfully", "device", i.device.SerialNumber, "version", version, "output", string(output))

	args = []string{dmsClientPath, "-a", i.bindAddress, "--insecure", "os", "activate", "--version", version}
	log.Log.V(2).Info("dmsInstance.InstallBFB() activate command", "args", strings.Join(args, " "))

	command = i.execInterface.CommandContext(ctx, args[0], args[1:]...)
	output, err = utils.RunCommand(command)
	if err != nil {
		log.Log.Error(err, "failed to activate BFB", "device", i.device.SerialNumber, "deviceType", i.device.Type)
		return err
	}

	log.Log.V(2).Info("BFB activated successfully", "device", i.device.SerialNumber, "version", version, "output", string(output))

	return nil
}
