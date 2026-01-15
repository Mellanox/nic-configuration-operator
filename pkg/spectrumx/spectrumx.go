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
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/dms"
	"github.com/Mellanox/nic-configuration-operator/pkg/nvconfig"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

// cnpDscpSysfsPathTemplate is the sysfs path template for CNP DSCP.
// Path: /sys/class/net/<iface>/ecn/roce_np/cnp_dscp
// This is a var to allow substitution in tests.
var cnpDscpSysfsPathTemplate = "/sys/class/net/%s/ecn/roce_np/cnp_dscp"

// cnpDscpExpectedValue is the expected value for CNP DSCP
const cnpDscpExpectedValueSwplb = "24"
const cnpDscpExpectedValueUniplane = "24"
const cnpDscpExpectedValueHwplb = "48"

type SpectrumXManager interface {
	// NvConfigApplied checks if the desired Spectrum-X NV spec is applied to the device
	NvConfigApplied(ctx context.Context, device *v1alpha1.NicDevice) (bool, error)
	// ApplyNvConfig applies the desired Spectrum-X NV spec to the device
	ApplyNvConfig(ctx context.Context, device *v1alpha1.NicDevice) (bool, error)
	// RuntimeConfigApplied checks if the desired Spectrum-X runtime spec is applied to the device
	RuntimeConfigApplied(device *v1alpha1.NicDevice) (bool, error)
	// ApplyRuntimeConfig applies the desired Spectrum-X runtime spec to the device
	ApplyRuntimeConfig(device *v1alpha1.NicDevice) error
	// GetDocaCCTargetVersion returns the target version of DOCA SPC-X CC for the device
	GetDocaCCTargetVersion(device *v1alpha1.NicDevice) (string, error)
	// RunDocaSpcXCC launches and keeps track of the DOCA SPC-X CC process for the given port
	RunDocaSpcXCC(port v1alpha1.NicDevicePortSpec) error
}

type spectrumXConfigManager struct {
	spectrumXConfigs map[string]*types.SpectrumXConfig
	dmsManager       dms.DMSManager
	execInterface    execUtils.Interface
	nvConfigUtils    nvconfig.NVConfigUtils

	ccProcesses map[string]*ccProcess
}

type ccProcess struct {
	port v1alpha1.NicDevicePortSpec
	cmd  execUtils.Cmd

	running atomic.Bool

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
		filtered = append(filtered, param)
	}
	return filtered
}

// getSpectrumXNVConfigParameters retrieves mlxconfig and dms parameters from the desired config and spec and returns them separately.
// returns: mlxconfigParams, dmsParams, error
func getSpectrumXNVConfigParameters(desiredConfig *types.SpectrumXConfig, spcXSpec *v1alpha1.SpectrumXOptimizedSpec, device *v1alpha1.NicDevice) ([]types.ConfigurationParameter, []types.ConfigurationParameter, error) {
	desiredParams := desiredConfig.NVConfig

	multiplaneMode, numberOfPlanes := spcXSpec.MultiplaneMode, spcXSpec.NumberOfPlanes
	var multiplaneConfig []types.ConfigurationParameter
	switch multiplaneMode {
	case consts.MultiplaneModeNone:
		break
	case consts.MultiplaneModeSwplb:
		multiplaneConfig = desiredConfig.MultiplaneConfig.Swplb[numberOfPlanes]
	case consts.MultiplaneModeHwplb:
		multiplaneConfig = desiredConfig.MultiplaneConfig.Hwplb[numberOfPlanes]
	case consts.MultiplaneModeUniplane:
		multiplaneConfig = desiredConfig.MultiplaneConfig.Uniplane[numberOfPlanes]
	default:
		return nil, nil, fmt.Errorf("invalid multiplane mode %s", multiplaneMode)
	}
	log.Log.V(2).Info("SpectrumXConfigManager.getSpectrumXNVConfigParameters(): multiplane config", "device", device.Name, "multiplaneConfig", multiplaneConfig)

	desiredParams = append(desiredParams, multiplaneConfig...)
	desiredParams = filterParameters(desiredParams, device.Status.Type, numberOfPlanes, multiplaneMode)

	mlxconfigParams := []types.ConfigurationParameter{}
	dmsParams := []types.ConfigurationParameter{}

	for _, param := range desiredParams {
		if param.MlxConfig != "" { // mlxconfig parameters are handled separately
			mlxconfigParams = append(mlxconfigParams, param)
		} else if param.DMSPath != "" {
			dmsParams = append(dmsParams, param)
		} else {
			err := fmt.Errorf("invalid parameter %+v", param)
			log.Log.Error(err, "NvConfigApplied(): invalid parameter", "device", device.Name)
			return nil, nil, err
		}
	}

	return mlxconfigParams, dmsParams, nil
}

// NvConfigApplied checks if the desired Spectrum-X NV spec is applied to the device
func (m *spectrumXConfigManager) NvConfigApplied(ctx context.Context, device *v1alpha1.NicDevice) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.NvConfigApplied()", "device", device.Name)

	spcXSpec := device.Spec.Configuration.Template.SpectrumXOptimized

	desiredConfig, found := m.spectrumXConfigs[spcXSpec.Version]
	if !found {
		return false, fmt.Errorf("spectrumx config not found for version %s", spcXSpec.Version)
	}

	mlxconfigParams, dmsParams, err := getSpectrumXNVConfigParameters(desiredConfig, spcXSpec, device)
	if err != nil {
		log.Log.Error(err, "NvConfigApplied(): failed to get Spectrum-X parameters", "device", device.Name)
		return false, err
	}

	for _, param := range mlxconfigParams {
		mlxConfig, err := m.nvConfigUtils.QueryNvConfig(ctx, device.Status.Ports[0].PCI, param.MlxConfig)
		if err != nil {
			log.Log.Error(err, "NvConfigApplied(): failed to get mlxconfig", "device", device.Name)
			return false, err
		}
		if !slices.Contains(mlxConfig.CurrentConfig[param.MlxConfig], param.Value) {
			log.Log.V(2).Info("SpectrumXConfigManager.NvConfigApplied(): mlxconfig parameter not applied", "device", device.Name, "param", param, "observedValues", mlxConfig.CurrentConfig[param.MlxConfig])
			return false, nil
		}
	}

	dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
	if err != nil {
		log.Log.Error(err, "NvConfigApplied(): failed to get DMS client", "device", device.Name)
		return false, err
	}

	values, err := dmsClient.GetParameters(dmsParams)
	if err != nil {
		log.Log.Error(err, "NvConfigApplied(): failed to get Spectrum-X NV config", "device", device.Name)
		return false, err
	}

	for _, param := range dmsParams {
		if values[param.DMSPath] != param.Value && values[param.DMSPath] != param.AlternativeValue {
			log.Log.V(2).Info("SpectrumXConfigManager.NvConfigApplied(): parameter not applied", "device", device.Name, "param", param)
			return false, nil
		}
	}

	return true, nil
}

// ApplyNvConfig applies the desired Spectrum-X NV spec to the device
func (m *spectrumXConfigManager) ApplyNvConfig(ctx context.Context, device *v1alpha1.NicDevice) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.ApplyNvConfig()", "device", device.Name)

	spcXSpec := device.Spec.Configuration.Template.SpectrumXOptimized

	desiredConfig, found := m.spectrumXConfigs[spcXSpec.Version]
	if !found {
		return false, fmt.Errorf("spectrumx config not found for version %s", spcXSpec.Version)
	}

	mlxconfigParams, dmsParams, err := getSpectrumXNVConfigParameters(desiredConfig, spcXSpec, device)
	if err != nil {
		log.Log.Error(err, "NvConfigApplied(): failed to get Spectrum-X parameters", "device", device.Name)
		return false, err
	}

	log.Log.V(2).Info("SpectrumXConfigManager.ApplyNvConfig(): setting Spectrum-X NV MLXconfig params", "device", device.Name, "config", mlxconfigParams)
	for _, param := range mlxconfigParams {
		err := m.nvConfigUtils.SetNvConfigParameter(device.Status.Ports[0].PCI, param.MlxConfig, param.Value)
		if err != nil {
			log.Log.Error(err, "ApplyNvConfig(): failed to set mlxconfig parameter", "device", device.Name, "param", param)
			return false, err
		}
	}

	dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
	if err != nil {
		log.Log.Error(err, "ApplyNvConfig(): failed to get DMS client", "device", device.Name)
		return false, err
	}

	log.Log.V(2).Info("SpectrumXConfigManager.ApplyNvConfig(): setting Spectrum-X NV DMS config", "device", device.Name, "config", dmsParams)
	err = dmsClient.SetParameters(dmsParams)
	if err != nil {
		log.Log.Error(err, "ApplyNvConfig(): failed to set Spectrum-X NV config", "device", device.Name)
		return false, err
	}

	return false, nil
}

// getCnpDscpPath returns the sysfs path for CNP DSCP for a given interface
func getCnpDscpPath(interfaceName string) string {
	return fmt.Sprintf(cnpDscpSysfsPathTemplate, interfaceName)
}

func getCnpDscpExpectedValue(multiplaneMode string) string {
	switch multiplaneMode {
	case consts.MultiplaneModeSwplb:
		return cnpDscpExpectedValueSwplb
	case consts.MultiplaneModeHwplb:
		return cnpDscpExpectedValueHwplb
	case consts.MultiplaneModeUniplane:
		return cnpDscpExpectedValueUniplane
	}
	return ""
}

// checkCnpDscp checks if CNP DSCP is set to the expected value for all ports
func checkCnpDscp(device *v1alpha1.NicDevice, multiplaneMode string) (bool, error) {
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

		cnpDscpExpectedValue := getCnpDscpExpectedValue(multiplaneMode)

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
func writeCnpDscp(device *v1alpha1.NicDevice, multiplaneMode string) error {
	log.Log.V(2).Info("SpectrumXConfigManager.writeCnpDscp()", "device", device.Name)

	for _, port := range device.Status.Ports {
		if port.NetworkInterface == "" {
			log.Log.V(2).Info("SpectrumXConfigManager.writeCnpDscp(): skipping port without network interface", "port", port.PCI)
			continue
		}

		cnpDscpExpectedValue := getCnpDscpExpectedValue(multiplaneMode)
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

// parametersApplied checks if the given parameters are applied to the device
func parametersApplied(device *v1alpha1.NicDevice, params []types.ConfigurationParameter, dmsClient dms.DMSClient) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.parametersApplied()", "device", device.Name)

	values, err := dmsClient.GetParameters(params)
	if err != nil {
		log.Log.Error(err, "checkIfSectionApplied(): failed to get Spectrum-X config", "device", device.Name)
		return false, err
	}
	log.Log.V(2).Info("SpectrumXConfigManager.parametersApplied(): got the following values", "device", device.Name, "values", values)

	for _, param := range params {
		if values[param.DMSPath] != param.Value && values[param.DMSPath] != param.AlternativeValue {
			log.Log.V(2).Info("SpectrumXConfigManager.parametersApplied(): parameter not applied", "device", device.Name, "param", param)
			return false, nil
		}
	}

	return true, nil
}

// RuntimeConfigApplied checks if the desired Spectrum-X runtime spec is applied to the device
func (m *spectrumXConfigManager) RuntimeConfigApplied(device *v1alpha1.NicDevice) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.RuntimeConfigApplied()", "device", device.Name)

	spcXSpec := device.Spec.Configuration.Template.SpectrumXOptimized
	desiredConfig, found := m.spectrumXConfigs[spcXSpec.Version]
	if !found {
		return false, fmt.Errorf("spectrumx config not found for version %s", spcXSpec.Version)
	}

	dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
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
	roceApplied, err := parametersApplied(device, roceParams, dmsClient)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to check if RoCE config is applied", "device", device.Name)
		return false, err
	}

	if !roceApplied {
		return false, nil
	}

	// Check CNP DSCP after RoCE config
	log.Log.V(2).Info("SpectrumXConfigManager.RuntimeConfigApplied(): checking CNP DSCP config", "device", device.Name)
	cnpDscpApplied, err := checkCnpDscp(device, multiplaneMode)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to check if CNP DSCP is applied", "device", device.Name)
		return false, err
	}

	if !cnpDscpApplied {
		return false, nil
	}

	adaptiveRoutingParams := filterParameters(desiredConfig.RuntimeConfig.AdaptiveRouting, deviceType, numberOfPlanes, multiplaneMode)
	log.Log.V(2).Info("SpectrumXConfigManager.RuntimeConfigApplied(): checking Adaptive Routing config", "device", device.Name)
	adaptiveRoutingApplied, err := parametersApplied(device, adaptiveRoutingParams, dmsClient)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to check if Adaptive Routing config is applied", "device", device.Name)
		return false, err
	}

	if !adaptiveRoutingApplied {
		return false, nil
	}

	if desiredConfig.UseSoftwareCCAlgorithm {
		log.Log.V(2).Info("SpectrumXConfigManager.RuntimeConfigApplied(): running DOCA SPC-X CC algorithm", "device", device.Name)
		for _, port := range device.Status.Ports {
			err = m.RunDocaSpcXCC(port)
			if err != nil {
				log.Log.Error(err, "ApplyRuntimeConfig(): failed to run DOCA SPC-X CC", "device", device.Name)
				return false, err
			}
		}
	} else {
		log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): not running DOCA SPC-X CC algorithm as specified in config", "device", device.Name)
	}

	congestionControlParams := filterParameters(desiredConfig.RuntimeConfig.CongestionControl, deviceType, numberOfPlanes, multiplaneMode)
	log.Log.V(2).Info("SpectrumXConfigManager.RuntimeConfigApplied(): checking Congestion Control config", "device", device.Name)
	congestionControlApplied, err := parametersApplied(device, congestionControlParams, dmsClient)
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

	overlayParamsApplied, err := parametersApplied(device, interPacketGapParams, dmsClient)
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
func (m *spectrumXConfigManager) ApplyRuntimeConfig(device *v1alpha1.NicDevice) error {
	spcXSpec := device.Spec.Configuration.Template.SpectrumXOptimized
	desiredConfig, found := m.spectrumXConfigs[spcXSpec.Version]
	if !found {
		return fmt.Errorf("spectrumx config not found for version %s", spcXSpec.Version)
	}

	dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to get DMS client", "device", device.Name)
		return err
	}

	// Filter parameters by device type, breakout (number of planes), and multiplane mode
	deviceType := device.Status.Type
	numberOfPlanes := spcXSpec.NumberOfPlanes
	multiplaneMode := spcXSpec.MultiplaneMode

	roceParams := filterParameters(desiredConfig.RuntimeConfig.Roce, deviceType, numberOfPlanes, multiplaneMode)
	log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): setting RoCE config", "device", device.Name)
	err = dmsClient.SetParameters(roceParams)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X RoCE config", "device", device.Name)
		return err
	}

	// Write CNP DSCP after RoCE config
	log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): setting CNP DSCP config", "device", device.Name)
	err = writeCnpDscp(device, multiplaneMode)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set CNP DSCP config", "device", device.Name)
		return err
	}

	adaptiveRoutingParams := filterParameters(desiredConfig.RuntimeConfig.AdaptiveRouting, deviceType, numberOfPlanes, multiplaneMode)
	log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): setting Adaptive Routing config", "device", device.Name)
	err = dmsClient.SetParameters(adaptiveRoutingParams)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X Adaptive Routing config", "device", device.Name)
		return err
	}

	if desiredConfig.UseSoftwareCCAlgorithm {
		log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): running DOCA SPC-X CC algorithm", "device", device.Name)
		for _, port := range device.Status.Ports {
			err = m.RunDocaSpcXCC(port)
			if err != nil {
				log.Log.Error(err, "ApplyRuntimeConfig(): failed to run DOCA SPC-X CC", "device", device.Name)
				return err
			}
		}
	} else {
		log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): not running DOCA SPC-X CC algorithm as specified in config", "device", device.Name)
	}

	congestionControlParams := filterParameters(desiredConfig.RuntimeConfig.CongestionControl, deviceType, numberOfPlanes, multiplaneMode)
	log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): setting Congestion Control config", "device", device.Name)
	err = dmsClient.SetParameters(congestionControlParams)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X Congestion Control config", "device", device.Name)
		return err
	}

	overlay := spcXSpec.Overlay
	var interPacketGapParams []types.ConfigurationParameter
	switch overlay {
	case consts.OverlayL3:
		interPacketGapParams = desiredConfig.RuntimeConfig.InterPacketGap.L3EVPN
	case consts.OverlayNone:
		interPacketGapParams = desiredConfig.RuntimeConfig.InterPacketGap.PureL3
	default:
		return fmt.Errorf("invalid overlay %s", overlay)
	}
	interPacketGapParams = filterParameters(interPacketGapParams, deviceType, numberOfPlanes, multiplaneMode)

	err = dmsClient.SetParameters(interPacketGapParams)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X InterPacketGap config", "device", device.Name)
		return err
	}

	return nil
}

// GetDocaCCTargetVersion returns the target version of DOCA SPC-X CC for the device
func (m *spectrumXConfigManager) GetDocaCCTargetVersion(device *v1alpha1.NicDevice) (string, error) {
	if device.Spec.Configuration == nil || device.Spec.Configuration.Template == nil || device.Spec.Configuration.Template.SpectrumXOptimized == nil {
		log.Log.V(2).Info("SpectrumXConfigManager.GetDocaCCTargetVersion(): device SPC-X spec is empty, no DOCA SPC-X CC required", "device", device.Name)
		return "", nil
	}

	spcXVersion := device.Spec.Configuration.Template.SpectrumXOptimized.Version
	config, found := m.spectrumXConfigs[spcXVersion]
	if !found {
		return "", fmt.Errorf("spectrumx config not found for version %s", spcXVersion)
	}

	if config.UseSoftwareCCAlgorithm {
		log.Log.V(2).Info("SpectrumXConfigManager.GetDocaCCTargetVersion(): using software CC algorithm", "device", device.Name, "version", config.DocaCCVersion)
		return config.DocaCCVersion, nil
	}

	return "", nil
}

// RunDocaSpcXCC launches and keeps track of the DOCA SPC-X CC process for the given port
func (m *spectrumXConfigManager) RunDocaSpcXCC(port v1alpha1.NicDevicePortSpec) error {
	log.Log.Info("SpectrumXConfigManager.RunDocaSpcXCC()", "rdma", port.RdmaInterface)

	// use rdma interface name as key for ccProcesses map
	// because with HW PLB different ports of the same NIC share the same RDMA device
	runningCCProcess, found := m.ccProcesses[port.RdmaInterface]
	if found && runningCCProcess.running.Load() {
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
		output, err := process.cmd.Output()
		if err != nil {
			process.errMutex.Lock()
			process.cmdErr = err
			process.errMutex.Unlock()
			log.Log.Error(err, "SpectrumXConfigManager.RunDocaSpcXCC(): Failed to run CC process", "rdma", port.RdmaInterface)
		}

		log.Log.V(2).Info("SpectrumXConfigManager.RunDocaSpcXCC(): CC process output", "rdma", port.RdmaInterface, "output", string(output))
		process.running.Store(false)
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

	m.ccProcesses[port.RdmaInterface] = process

	log.Log.Info("Started DOCA SPC-X CC process", "rdma", port.RdmaInterface)

	return nil
}

func NewSpectrumXConfigManager(dmsManager dms.DMSManager, spectrumXConfigs map[string]*types.SpectrumXConfig) SpectrumXManager {
	return &spectrumXConfigManager{
		dmsManager:       dmsManager,
		spectrumXConfigs: spectrumXConfigs,
		execInterface:    execUtils.New(),
		nvConfigUtils:    nvconfig.NewNVConfigUtils(),
		ccProcesses:      make(map[string]*ccProcess),
	}
}
