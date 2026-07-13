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

package spectrumx

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/dms"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

// cnpDscpSysfsPathTemplate is the sysfs path template for CNP DSCP.
// Path: /sys/class/net/<iface>/ecn/roce_np/cnp_dscp
// This is a var to allow substitution in tests.
var cnpDscpSysfsPathTemplate = "/sys/class/net/%s/ecn/roce_np/cnp_dscp"

// cnpDscpExpectedValue is the expected value for CNP DSCP
const cnpDscpExpectedValue = "48"

// mlxregBinary is the path to the mlxreg binary. This is a var to allow substitution in tests.
var mlxregBinary = "/usr/bin/mlxreg"

type SpectrumXManager interface {
	// GetBreakoutMlxConfig returns the breakout mlxconfig map for the device based on its SpectrumX spec
	GetBreakoutMlxConfig(device *v1alpha1.NicDevice) (map[string]string, error)
	// GetPostBreakoutMlxConfig returns the post-breakout mlxconfig map for the device
	GetPostBreakoutMlxConfig(device *v1alpha1.NicDevice) (map[string]string, error)
	// RuntimeConfigApplied checks if the desired Spectrum-X runtime spec is applied to the device
	RuntimeConfigApplied(device *v1alpha1.NicDevice) (bool, error)
	// ApplyRuntimeConfig applies the desired Spectrum-X runtime spec to the device
	ApplyRuntimeConfig(device *v1alpha1.NicDevice) (*types.RuntimeConfigurationApplyResult, error)
	// GetDocaCCTargetVersion returns the target version of DOCA SPC-X CC for the device
	GetDocaCCTargetVersion(device *v1alpha1.NicDevice) (string, error)
	// RunDocaSpcXCC launches and keeps track of the DOCA SPC-X CC process for the given port
	RunDocaSpcXCC(port v1alpha1.NicDevicePortSpec) error
	// GetCCTerminationChannel returns a read-only channel that receives the RDMA interface name
	// when a DOCA SPC-X CC process terminates unexpectedly after startup
	GetCCTerminationChannel() <-chan string
	// SetConfig upserts the Spectrum-X profile for the given version (e.g. "RA2.0").
	// Called by the SpectrumXProfileReconciler when a profile ConfigMap is created/updated.
	SetConfig(version string, config *types.SpectrumXConfig)
	// RemoveConfig removes the Spectrum-X profile for the given version.
	// Called by the SpectrumXProfileReconciler when a profile ConfigMap is deleted.
	RemoveConfig(version string)
}

type spectrumXConfigManager struct {
	// configMutex guards spectrumXConfigs, which is mutated at runtime by the
	// SpectrumXProfileReconciler while being read from reconcile goroutines.
	configMutex      sync.RWMutex
	spectrumXConfigs map[string]*types.SpectrumXConfig
	dmsManager       dms.DMSManager
	execInterface    execUtils.Interface

	ccProcesses       map[string]*ccProcess
	ccTerminationChan chan string // buffered; carries RDMA iface name on unexpected exit
}

type ccProcess struct {
	port v1alpha1.NicDevicePortSpec
	cmd  execUtils.Cmd

	running            atomic.Bool
	startupCheckPassed atomic.Bool // set after the 3s startup window; distinguishes startup failures from runtime crashes

	// Error handling with mutex protection
	errMutex sync.RWMutex
	cmdErr   error
}

// filterParameters filters parameters by DeviceId, Breakout (numberOfPlanes), and Multiplane mode
func filterParameters(params []types.ConfigurationParameter, deviceType string, numberOfPlanes int, multiplaneMode string) []types.ConfigurationParameter {
	filtered := []types.ConfigurationParameter{}
	for _, param := range params {
		if param.DeviceId != "" && param.DeviceId != deviceType {
			continue
		}
		if param.Breakout != 0 && param.Breakout != numberOfPlanes {
			continue
		}
		if param.Multiplane != "" && param.Multiplane != multiplaneMode {
			continue
		}
		// HwplbFirstPortOnly only takes effect in hwplb mode; clear it otherwise
		if param.HwplbFirstPortOnly && multiplaneMode != consts.MultiplaneModeHwplb {
			param.HwplbFirstPortOnly = false
		}
		filtered = append(filtered, param)
	}
	return filtered
}

// getConfig returns the SpectrumXConfig for a version under a read lock.
func (m *spectrumXConfigManager) getConfig(version string) (*types.SpectrumXConfig, bool) {
	m.configMutex.RLock()
	defer m.configMutex.RUnlock()
	config, ok := m.spectrumXConfigs[version]
	return config, ok
}

// SetConfig upserts the Spectrum-X profile for the given version.
func (m *spectrumXConfigManager) SetConfig(version string, config *types.SpectrumXConfig) {
	m.configMutex.Lock()
	defer m.configMutex.Unlock()
	m.spectrumXConfigs[version] = config
}

// RemoveConfig removes the Spectrum-X profile for the given version.
func (m *spectrumXConfigManager) RemoveConfig(version string) {
	m.configMutex.Lock()
	defer m.configMutex.Unlock()
	delete(m.spectrumXConfigs, version)
}

// getDesiredConfig looks up the SpectrumXConfig for the device's version
func (m *spectrumXConfigManager) getDesiredConfig(device *v1alpha1.NicDevice) (*types.SpectrumXConfig, error) {
	version := device.Spec.Configuration.Template.SpectrumXOptimized.Version
	config, ok := m.getConfig(version)
	if !ok {
		return nil, fmt.Errorf("spectrum-x config version %s not found", version)
	}
	return config, nil
}

// GetBreakoutMlxConfig returns the breakout mlxconfig map for the device
func (m *spectrumXConfigManager) GetBreakoutMlxConfig(device *v1alpha1.NicDevice) (map[string]string, error) {
	config, err := m.getDesiredConfig(device)
	if err != nil {
		return nil, err
	}

	spcXSpec := device.Spec.Configuration.Template.SpectrumXOptimized
	multiplaneMode := spcXSpec.MultiplaneMode
	deviceType := device.Status.Type
	numberOfPlanes := spcXSpec.NumberOfPlanes

	devices, ok := config.MlxConfig[multiplaneMode]
	if !ok {
		return nil, nil
	}
	deviceConfig, ok := devices[deviceType]
	if !ok {
		return nil, nil
	}
	breakoutConfig, ok := deviceConfig.Breakout[numberOfPlanes]
	if !ok {
		return nil, nil
	}
	return breakoutConfig, nil
}

// GetPostBreakoutMlxConfig returns the post-breakout mlxconfig map for the device
func (m *spectrumXConfigManager) GetPostBreakoutMlxConfig(device *v1alpha1.NicDevice) (map[string]string, error) {
	config, err := m.getDesiredConfig(device)
	if err != nil {
		return nil, err
	}

	spcXSpec := device.Spec.Configuration.Template.SpectrumXOptimized
	multiplaneMode := spcXSpec.MultiplaneMode
	deviceType := device.Status.Type

	devices, ok := config.MlxConfig[multiplaneMode]
	if !ok {
		return nil, nil
	}
	deviceConfig, ok := devices[deviceType]
	if !ok {
		return nil, nil
	}
	return deviceConfig.PostBreakout, nil
}

// getCnpDscpPath returns the sysfs path for CNP DSCP for a given interface
func getCnpDscpPath(interfaceName string) string {
	return fmt.Sprintf(cnpDscpSysfsPathTemplate, interfaceName)
}

// checkCnpDscp checks if CNP DSCP is set to the expected value for all ports
func checkCnpDscp(device *v1alpha1.NicDevice) (bool, error) {
	log.Log.V(2).Info("SpectrumXConfigManager.checkCnpDscp()", "device", device.Name)

	for _, port := range device.Status.Ports {
		if port.NetworkInterface == "" {
			log.Log.V(2).Info("SpectrumXConfigManager.checkCnpDscp(): skipping port without network interface", "port", port.PCI)
			continue
		}

		cnpDscpPath := getCnpDscpPath(port.NetworkInterface)
		data, err := os.ReadFile(cnpDscpPath)
		if err != nil {
			log.Log.Error(err, "checkCnpDscp(): failed to read CNP DSCP file", "path", cnpDscpPath, "device", device.Name)
			return false, err
		}

		value := strings.TrimSpace(string(data))
		if value != cnpDscpExpectedValue {
			log.Log.V(2).Info("SpectrumXConfigManager.checkCnpDscp(): CNP DSCP value mismatch",
				"device", device.Name, "port", port.NetworkInterface, "expected", cnpDscpExpectedValue, "actual", value)
			return false, nil
		}
	}

	return true, nil
}

// writeCnpDscp writes the expected CNP DSCP value for all ports
func writeCnpDscp(device *v1alpha1.NicDevice) error {
	log.Log.V(2).Info("SpectrumXConfigManager.writeCnpDscp()", "device", device.Name)

	for _, port := range device.Status.Ports {
		if port.NetworkInterface == "" {
			log.Log.V(2).Info("SpectrumXConfigManager.writeCnpDscp(): skipping port without network interface", "port", port.PCI)
			continue
		}

		cnpDscpPath := getCnpDscpPath(port.NetworkInterface)
		err := os.WriteFile(cnpDscpPath, []byte(cnpDscpExpectedValue), 0644)
		if err != nil {
			log.Log.Error(err, "writeCnpDscp(): failed to write CNP DSCP file", "path", cnpDscpPath, "device", device.Name)
			return err
		}
		log.Log.V(2).Info("SpectrumXConfigManager.writeCnpDscp(): wrote CNP DSCP value",
			"device", device.Name, "port", port.NetworkInterface, "value", cnpDscpExpectedValue)
	}

	return nil
}

func isMlxRegParam(param types.ConfigurationParameter) bool {
	return param.MlxReg != nil
}

func validateMlxRegParam(param types.ConfigurationParameter) error {
	if param.MlxReg == nil {
		return fmt.Errorf("mlxreg parameter %q is missing mlxreg config", param.Name)
	}
	if param.DMSPath != "" {
		return fmt.Errorf("mlxreg parameter %q cannot define both dmsPath and mlxreg", param.Name)
	}
	if param.Value == "" {
		return fmt.Errorf("mlxreg parameter %q is missing value", param.Name)
	}
	if param.MlxReg.Register == "" {
		return fmt.Errorf("mlxreg parameter %q is missing register", param.Name)
	}
	if param.MlxReg.Field == "" {
		return fmt.Errorf("mlxreg parameter %q is missing field", param.Name)
	}
	if len(param.MlxReg.SetFields) == 0 {
		return fmt.Errorf("mlxreg parameter %q has no setFields", param.Name)
	}
	for _, field := range param.MlxReg.SetFields {
		if field.Name == "" {
			return fmt.Errorf("mlxreg parameter %q has a setField without name", param.Name)
		}
		if field.Value == "" {
			return fmt.Errorf("mlxreg parameter %q has a setField without value", param.Name)
		}
	}
	return nil
}

func parseMlxRegFieldValue(output []byte, field string) (string, bool) {
	for _, line := range strings.Split(string(output), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) == field {
			return strings.TrimSpace(parts[1]), true
		}
	}
	return "", false
}

func commandLine(binary string, args []string) string {
	return strings.Join(append([]string{binary}, args...), " ")
}

func equalMlxRegValue(expected, actual string) bool {
	expected = strings.TrimSpace(expected)
	actual = strings.TrimSpace(actual)
	if actual == expected {
		return true
	}

	expectedValue, err := strconv.ParseUint(expected, 0, 64)
	if err != nil {
		return false
	}
	actualValue, err := strconv.ParseUint(actual, 0, 64)
	if err != nil {
		return false
	}
	return actualValue == expectedValue
}

func expectedMlxRegValue(param types.ConfigurationParameter, actual string) bool {
	return equalMlxRegValue(param.Value, actual) || (param.AlternativeValue != "" && equalMlxRegValue(param.AlternativeValue, actual))
}

func mlxRegSetValue(param types.ConfigurationParameter) string {
	fields := make([]string, 0, len(param.MlxReg.SetFields))
	for _, field := range param.MlxReg.SetFields {
		fields = append(fields, fmt.Sprintf("%s=%s", field.Name, field.Value))
	}
	return strings.Join(fields, ",")
}

func (m *spectrumXConfigManager) checkMlxRegParamApplied(device *v1alpha1.NicDevice, param types.ConfigurationParameter) (bool, error) {
	if err := validateMlxRegParam(param); err != nil {
		return false, err
	}

	log.Log.V(2).Info("SpectrumXConfigManager.checkMlxRegParamApplied()", "device", device.Name, "param", param.Name)
	for _, port := range device.Status.Ports {
		args := []string{"-d", port.PCI, "--reg_name", param.MlxReg.Register, "--get"}
		command := commandLine(mlxregBinary, args)
		log.Log.V(2).Info("checkMlxRegParamApplied(): running mlxreg get",
			"device", device.Name, "pci", port.PCI, "param", param.Name, "field", param.MlxReg.Field,
			"expected", param.Value, "alternativeExpected", param.AlternativeValue, "command", command)

		cmd := m.execInterface.Command(mlxregBinary, args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			log.Log.Error(err, "checkMlxRegParamApplied(): failed to run mlxreg get",
				"device", device.Name, "pci", port.PCI, "param", param.Name, "command", command, "output", string(output))
			return false, fmt.Errorf("failed to check mlxreg parameter %q for PF %s: %w", param.Name, port.PCI, err)
		}

		value, found := parseMlxRegFieldValue(output, param.MlxReg.Field)
		matches := found && expectedMlxRegValue(param, value)
		log.Log.V(2).Info("checkMlxRegParamApplied(): got mlxreg value",
			"device", device.Name, "pci", port.PCI, "param", param.Name, "register", param.MlxReg.Register,
			"field", param.MlxReg.Field, "expected", param.Value, "alternativeExpected", param.AlternativeValue,
			"actual", value, "found", found, "matches", matches, "command", command, "output", string(output))
		if !found {
			log.Log.V(2).Info("checkMlxRegParamApplied(): field not found in mlxreg output",
				"device", device.Name, "pci", port.PCI, "param", param.Name, "field", param.MlxReg.Field,
				"command", command, "output", string(output))
			return false, nil
		}
		if !matches {
			log.Log.V(2).Info("checkMlxRegParamApplied(): mlxreg parameter not applied",
				"device", device.Name, "pci", port.PCI, "param", param.Name, "expected", param.Value,
				"alternativeExpected", param.AlternativeValue, "actual", value, "command", command)
			return false, nil
		}
	}
	return true, nil
}

func (m *spectrumXConfigManager) setMlxRegParam(device *v1alpha1.NicDevice, param types.ConfigurationParameter) error {
	if err := validateMlxRegParam(param); err != nil {
		return err
	}
	setValue := mlxRegSetValue(param)

	log.Log.V(2).Info("SpectrumXConfigManager.setMlxRegParam()", "device", device.Name, "param", param.Name)
	for _, port := range device.Status.Ports {
		args := []string{"-d", port.PCI, "--reg_name", param.MlxReg.Register, "--set", setValue, "--yes"}
		command := commandLine(mlxregBinary, args)
		log.Log.V(2).Info("setMlxRegParam(): running mlxreg set",
			"device", device.Name, "pci", port.PCI, "param", param.Name, "register", param.MlxReg.Register,
			"setValue", setValue, "command", command)

		cmd := m.execInterface.Command(mlxregBinary, args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			log.Log.Error(err, "setMlxRegParam(): failed to run mlxreg set",
				"device", device.Name, "pci", port.PCI, "param", param.Name, "command", command, "output", string(output))
			return fmt.Errorf("failed to set mlxreg parameter %q for PF %s: %w", param.Name, port.PCI, err)
		}
		log.Log.V(2).Info("setMlxRegParam(): successfully set mlxreg parameter on PF",
			"device", device.Name, "pci", port.PCI, "param", param.Name, "command", command, "output", string(output))
	}
	return nil
}

// checkDmsParamsApplied checks if the given DMS parameters are applied to the device
func checkDmsParamsApplied(device *v1alpha1.NicDevice, params []types.ConfigurationParameter, dmsClient dms.DMSClient) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.checkDmsParamsApplied()", "device", device.Name)
	if len(params) == 0 {
		return true, nil
	}

	values, err := dmsClient.GetParameters(params)
	if err != nil {
		if types.IsValuesDoNotMatchError(err) {
			log.Log.V(2).Info("checkDmsParamsApplied(): values do not match across ports/priorities", "device", device.Name, "error", err.Error())
			return false, nil
		}
		log.Log.Error(err, "checkDmsParamsApplied(): failed to get DMS config", "device", device.Name)
		return false, err
	}
	log.Log.V(2).Info("SpectrumXConfigManager.checkDmsParamsApplied(): got the following values", "device", device.Name, "values", values)

	for _, param := range params {
		actual, found := values[param.DMSPath]
		matches := found && (actual == param.Value || (param.AlternativeValue != "" && actual == param.AlternativeValue))
		log.Log.V(2).Info("SpectrumXConfigManager.checkDmsParamsApplied(): compared parameter",
			"device", device.Name, "param", param.Name, "path", param.DMSPath, "expected", param.Value,
			"alternativeExpected", param.AlternativeValue, "actual", actual, "found", found, "matches", matches)
		if !matches {
			log.Log.V(2).Info("SpectrumXConfigManager.checkDmsParamsApplied(): parameter not applied", "device", device.Name, "param", param)
			return false, nil
		}
	}

	return true, nil
}

func (m *spectrumXConfigManager) checkRuntimeParamsApplied(device *v1alpha1.NicDevice, params []types.ConfigurationParameter, dmsClient dms.DMSClient) (bool, error) {
	dmsBatch := []types.ConfigurationParameter{}
	flushDMSBatch := func() (bool, error) {
		applied, err := checkDmsParamsApplied(device, dmsBatch, dmsClient)
		dmsBatch = nil
		return applied, err
	}

	for _, param := range params {
		if isMlxRegParam(param) {
			if err := validateMlxRegParam(param); err != nil {
				return false, err
			}
			applied, err := flushDMSBatch()
			if err != nil || !applied {
				return applied, err
			}
			applied, err = m.checkMlxRegParamApplied(device, param)
			if err != nil || !applied {
				return applied, err
			}
			continue
		}
		dmsBatch = append(dmsBatch, param)
	}

	return flushDMSBatch()
}

func (m *spectrumXConfigManager) applyRuntimeParams(device *v1alpha1.NicDevice, params []types.ConfigurationParameter, dmsClient dms.DMSClient) error {
	dmsBatch := []types.ConfigurationParameter{}
	flushDMSBatch := func() error {
		if len(dmsBatch) == 0 {
			return nil
		}
		err := dmsClient.SetParameters(dmsBatch)
		dmsBatch = nil
		return err
	}

	for _, param := range params {
		if isMlxRegParam(param) {
			if err := validateMlxRegParam(param); err != nil {
				return err
			}
			if err := flushDMSBatch(); err != nil {
				return err
			}
			if err := m.setMlxRegParam(device, param); err != nil {
				return err
			}
			continue
		}
		dmsBatch = append(dmsBatch, param)
	}

	return flushDMSBatch()
}

// RuntimeConfigApplied checks if the desired Spectrum-X runtime spec is applied to the device
func (m *spectrumXConfigManager) RuntimeConfigApplied(device *v1alpha1.NicDevice) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.RuntimeConfigApplied()", "device", device.Name)

	spcXSpec := device.Spec.Configuration.Template.SpectrumXOptimized
	desiredConfig, found := m.getConfig(spcXSpec.Version)
	if !found {
		return false, fmt.Errorf("spectrumx config not found for version %s", spcXSpec.Version)
	}

	dmsClient, err := dms.GetDMSClientForDevice(m.dmsManager, device)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to get DMS client", "device", device.Name)
		return false, err
	}

	// Filter parameters by device type, breakout (number of planes), and multiplane mode
	deviceType := device.Status.Type
	numberOfPlanes := spcXSpec.NumberOfPlanes
	multiplaneMode := spcXSpec.MultiplaneMode

	roceParams := filterParameters(desiredConfig.RuntimeConfig.Roce, deviceType, numberOfPlanes, multiplaneMode)
	log.Log.V(2).Info("SpectrumXConfigManager.RuntimeConfigApplied(): checking RoCE config", "device", device.Name)
	roceApplied, err := m.checkRuntimeParamsApplied(device, roceParams, dmsClient)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to check if RoCE config is applied", "device", device.Name)
		return false, err
	}

	if !roceApplied {
		return false, nil
	}

	// Check CNP DSCP after RoCE config
	log.Log.V(2).Info("SpectrumXConfigManager.RuntimeConfigApplied(): checking CNP DSCP config", "device", device.Name)
	cnpDscpApplied, err := checkCnpDscp(device)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to check if CNP DSCP is applied", "device", device.Name)
		return false, err
	}

	if !cnpDscpApplied {
		return false, nil
	}

	adaptiveRoutingParams := filterParameters(desiredConfig.RuntimeConfig.AdaptiveRouting, deviceType, numberOfPlanes, multiplaneMode)
	log.Log.V(2).Info("SpectrumXConfigManager.RuntimeConfigApplied(): checking Adaptive Routing config", "device", device.Name)
	adaptiveRoutingApplied, err := m.checkRuntimeParamsApplied(device, adaptiveRoutingParams, dmsClient)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to check if Adaptive Routing config is applied", "device", device.Name)
		return false, err
	}
	if !adaptiveRoutingApplied {
		return false, nil
	}

	if desiredConfig.UseSoftwareCCAlgorithm {
		log.Log.V(2).Info("SpectrumXConfigManager.RuntimeConfigApplied(): check if DOCA SPC-X CC algorithm is running", "device", device.Name)
		if multiplaneMode == consts.MultiplaneModeHwplb {
			if len(device.Status.Ports) == 0 || !m.IsDocaSpcXCCRunning(device.Status.Ports[0].RdmaInterface) {
				log.Log.Info("RuntimeConfigApplied(): DOCA SPC-X CC algorithm is not running", "device", device.Name)
				return false, nil
			}
		} else {
			for _, port := range device.Status.Ports {
				if !m.IsDocaSpcXCCRunning(port.RdmaInterface) {
					log.Log.Info("RuntimeConfigApplied(): DOCA SPC-X CC algorithm is not running", "device", device.Name)
					return false, nil
				}
			}
		}
	} else {
		log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): not running DOCA SPC-X CC algorithm as specified in config", "device", device.Name)
	}

	congestionControlParams := filterParameters(desiredConfig.RuntimeConfig.CongestionControl, deviceType, numberOfPlanes, multiplaneMode)
	log.Log.V(2).Info("SpectrumXConfigManager.RuntimeConfigApplied(): checking Congestion Control config", "device", device.Name)
	congestionControlApplied, err := m.checkRuntimeParamsApplied(device, congestionControlParams, dmsClient)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to check if Congestion Control config is applied", "device", device.Name)
		return false, err
	}

	if !congestionControlApplied {
		return false, nil
	}

	overlay := spcXSpec.Overlay
	var interPacketGapParams []types.ConfigurationParameter
	switch overlay {
	case consts.OverlayL3:
		interPacketGapParams = desiredConfig.RuntimeConfig.InterPacketGap.L3EVPN
	case consts.OverlayNone:
		interPacketGapParams = desiredConfig.RuntimeConfig.InterPacketGap.PureL3
	default:
		return false, fmt.Errorf("invalid overlay %s", overlay)
	}
	interPacketGapParams = filterParameters(interPacketGapParams, deviceType, numberOfPlanes, multiplaneMode)

	overlayParamsApplied, err := m.checkRuntimeParamsApplied(device, interPacketGapParams, dmsClient)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X InterPacketGap config", "device", device.Name)
		return false, err
	}

	if !overlayParamsApplied {
		return false, nil
	}

	return true, nil

}

// ApplyRuntimeConfig applies the desired Spectrum-X runtime spec to the device
func (m *spectrumXConfigManager) ApplyRuntimeConfig(device *v1alpha1.NicDevice) (*types.RuntimeConfigurationApplyResult, error) {
	spcXSpec := device.Spec.Configuration.Template.SpectrumXOptimized
	desiredConfig, found := m.getConfig(spcXSpec.Version)
	if !found {
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, fmt.Errorf("spectrumx config not found for version %s", spcXSpec.Version)
	}

	dmsClient, err := dms.GetDMSClientForDevice(m.dmsManager, device)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to get DMS client", "device", device.Name)
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	// Filter parameters by device type, breakout (number of planes), and multiplane mode
	deviceType := device.Status.Type
	numberOfPlanes := spcXSpec.NumberOfPlanes
	multiplaneMode := spcXSpec.MultiplaneMode

	roceParams := filterParameters(desiredConfig.RuntimeConfig.Roce, deviceType, numberOfPlanes, multiplaneMode)
	log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): setting RoCE config", "device", device.Name)
	err = m.applyRuntimeParams(device, roceParams, dmsClient)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X RoCE config", "device", device.Name)
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	// Write CNP DSCP after RoCE config
	log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): setting CNP DSCP config", "device", device.Name)
	err = writeCnpDscp(device)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set CNP DSCP config", "device", device.Name)
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	adaptiveRoutingParams := filterParameters(desiredConfig.RuntimeConfig.AdaptiveRouting, deviceType, numberOfPlanes, multiplaneMode)
	log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): setting Adaptive Routing config", "device", device.Name)
	err = m.applyRuntimeParams(device, adaptiveRoutingParams, dmsClient)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X Adaptive Routing config", "device", device.Name)
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	if desiredConfig.UseSoftwareCCAlgorithm {
		log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): running DOCA SPC-X CC algorithm", "device", device.Name)
		if multiplaneMode == consts.MultiplaneModeHwplb {
			if len(device.Status.Ports) == 0 {
				return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, fmt.Errorf("no ports available for device %s", device.Name)
			}
			err = m.RunDocaSpcXCC(device.Status.Ports[0])
			if err != nil {
				log.Log.Error(err, "ApplyRuntimeConfig(): failed to run DOCA SPC-X CC", "device", device.Name)
				return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
			}
		} else {
			for _, port := range device.Status.Ports {
				err = m.RunDocaSpcXCC(port)
				if err != nil {
					log.Log.Error(err, "ApplyRuntimeConfig(): failed to run DOCA SPC-X CC", "device", device.Name)
					return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
				}
			}
		}
	} else {
		log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): not running DOCA SPC-X CC algorithm as specified in config", "device", device.Name)
	}

	congestionControlParams := filterParameters(desiredConfig.RuntimeConfig.CongestionControl, deviceType, numberOfPlanes, multiplaneMode)
	log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): setting Congestion Control config", "device", device.Name)
	err = m.applyRuntimeParams(device, congestionControlParams, dmsClient)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X Congestion Control config", "device", device.Name)
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	overlay := spcXSpec.Overlay
	var interPacketGapParams []types.ConfigurationParameter
	switch overlay {
	case consts.OverlayL3:
		interPacketGapParams = desiredConfig.RuntimeConfig.InterPacketGap.L3EVPN
	case consts.OverlayNone:
		interPacketGapParams = desiredConfig.RuntimeConfig.InterPacketGap.PureL3
	default:
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, fmt.Errorf("invalid overlay %s", overlay)
	}
	interPacketGapParams = filterParameters(interPacketGapParams, deviceType, numberOfPlanes, multiplaneMode)

	err = m.applyRuntimeParams(device, interPacketGapParams, dmsClient)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X InterPacketGap config", "device", device.Name)
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	// Wait for 1 second to apply the IPG settings
	time.Sleep(1 * time.Second)
	err = dmsClient.SetParameters([]types.ConfigurationParameter{
		{
			Name:      "Shut down interface",
			Value:     "false",
			DMSPath:   "/interfaces/interface/config/enabled",
			ValueType: "bool",
		},
	})
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to shut down interface", "device", device.Name)
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}

	err = dmsClient.SetParameters([]types.ConfigurationParameter{
		{
			Name:      "Bring up interface to apply IPG settings",
			Value:     "true",
			DMSPath:   "/interfaces/interface/config/enabled",
			ValueType: "bool",
		},
	})
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to bring up interface", "device", device.Name)
		return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, err
	}
	return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusSuccess}, nil
}

// GetDocaCCTargetVersion returns the target version of DOCA SPC-X CC for the device
func (m *spectrumXConfigManager) GetDocaCCTargetVersion(device *v1alpha1.NicDevice) (string, error) {
	if device.Spec.Configuration == nil || device.Spec.Configuration.Template == nil || device.Spec.Configuration.Template.SpectrumXOptimized == nil {
		log.Log.V(2).Info("SpectrumXConfigManager.GetDocaCCTargetVersion(): device SPC-X spec is empty, no DOCA SPC-X CC required", "device", device.Name)
		return "", nil
	}

	spcXVersion := device.Spec.Configuration.Template.SpectrumXOptimized.Version
	config, found := m.getConfig(spcXVersion)
	if !found {
		return "", fmt.Errorf("spectrumx config not found for version %s", spcXVersion)
	}

	if config.UseSoftwareCCAlgorithm {
		log.Log.V(2).Info("SpectrumXConfigManager.GetDocaCCTargetVersion(): using software CC algorithm", "device", device.Name, "version", config.DocaCCVersion)
		return config.DocaCCVersion, nil
	}

	return "", nil
}

func (m *spectrumXConfigManager) IsDocaSpcXCCRunning(rdmaInterface string) bool {
	runningCCProcess, found := m.ccProcesses[rdmaInterface]
	if found && runningCCProcess.running.Load() {
		return true
	}
	return false
}

// RunDocaSpcXCC launches and keeps track of the DOCA SPC-X CC process for the given port
func (m *spectrumXConfigManager) RunDocaSpcXCC(port v1alpha1.NicDevicePortSpec) error {
	log.Log.Info("SpectrumXConfigManager.RunDocaSpcXCC()", "rdma", port.RdmaInterface)

	// use rdma interface name as key for ccProcesses map
	// because with HW PLB different ports of the same NIC share the same RDMA device
	if m.IsDocaSpcXCCRunning(port.RdmaInterface) {
		log.Log.V(2).Info("SpectrumXConfigManager.RunDocaSpcXCC(): CC process already running", "rdma", port.RdmaInterface)
		return nil
	}

	cmd := m.execInterface.Command("/opt/mellanox/doca/tools/doca_spcx_cc", "--device", port.RdmaInterface)

	process := &ccProcess{
		port: port,
		cmd:  cmd,
	}

	process.running.Store(true)

	go func() {
		output, err := process.cmd.CombinedOutput()
		if err != nil {
			process.errMutex.Lock()
			process.cmdErr = err
			process.errMutex.Unlock()
			log.Log.Error(err, "SpectrumXConfigManager.RunDocaSpcXCC(): Failed to run CC process", "rdma", port.RdmaInterface)
		}

		log.Log.V(2).Info("SpectrumXConfigManager.RunDocaSpcXCC(): CC process output", "rdma", port.RdmaInterface, "output", string(output))
		process.running.Store(false)

		// Notify controller only for runtime crashes (after startup check passed)
		if process.startupCheckPassed.Load() {
			log.Log.Info("SpectrumXConfigManager.RunDocaSpcXCC(): CC process terminated unexpectedly, sending notification", "rdma", port.RdmaInterface)
			select {
			case m.ccTerminationChan <- port.RdmaInterface:
			default:
				log.Log.V(2).Info("SpectrumXConfigManager.RunDocaSpcXCC(): termination channel full, notification dropped", "rdma", port.RdmaInterface)
			}
		}
	}()

	log.Log.V(2).Info("Waiting 3s for DOCA SPC-X CC to start", "rdma", port.RdmaInterface)
	time.Sleep(3 * time.Second)

	if !process.running.Load() {
		process.errMutex.RLock()
		cmdErr := process.cmdErr
		process.errMutex.RUnlock()

		if cmdErr != nil {
			log.Log.Error(cmdErr, "Failed to start DOCA SPC-X CC", "rdma", port.RdmaInterface)
			return fmt.Errorf("failed to start DOCA SPC-X CC for port %s: %v", port.PCI, cmdErr)
		}
		return fmt.Errorf("failed to start DOCA SPC-X CC for port %s: unknown error", port.RdmaInterface)
	}

	log.Log.V(2).Info("DOCA SPC-X CC process started", "rdma", port.RdmaInterface)

	process.startupCheckPassed.Store(true)
	m.ccProcesses[port.RdmaInterface] = process

	log.Log.Info("Started DOCA SPC-X CC process", "rdma", port.RdmaInterface)

	return nil
}

// GetCCTerminationChannel returns a read-only channel for CC process termination notifications.
// The channel carries the RDMA interface name of the terminated CC process.
func (m *spectrumXConfigManager) GetCCTerminationChannel() <-chan string {
	return m.ccTerminationChan
}

func NewSpectrumXConfigManager(dmsManager dms.DMSManager, spectrumXConfigs map[string]*types.SpectrumXConfig) SpectrumXManager {
	// Ensure the map is always usable so SetConfig is safe even when started with no configs
	// (the operator daemon loads profiles at runtime from ConfigMaps via SetConfig).
	if spectrumXConfigs == nil {
		spectrumXConfigs = map[string]*types.SpectrumXConfig{}
	}
	return &spectrumXConfigManager{
		dmsManager:        dmsManager,
		spectrumXConfigs:  spectrumXConfigs,
		execInterface:     execUtils.New(),
		ccProcesses:       make(map[string]*ccProcess),
		ccTerminationChan: make(chan string, 10),
	}
}
