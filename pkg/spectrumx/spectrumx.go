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
	"slices"
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

// getSpectrumXParameters retrieves mlxconfig and dms parameters from the desired config and spec and returns them separately.
// returns: mlxconfigParams, dmsParams, error
func getSpectrumXParameters(desiredConfig *types.SpectrumXConfig, spcXSpec *v1alpha1.SpectrumXOptimizedSpec, device *v1alpha1.NicDevice) ([]types.ConfigurationParameter, []types.ConfigurationParameter, error) {
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
	log.Log.V(2).Info("SpectrumXConfigManager.getSpectrumXParameters(): multiplane config", "device", device.Name, "multiplaneConfig", multiplaneConfig)

	desiredParams = append(desiredParams, multiplaneConfig...)
	mlxconfigParams := []types.ConfigurationParameter{}
	dmsParams := []types.ConfigurationParameter{}

	for _, param := range desiredParams {
		// Drop device-specific parameters for non-matching devices
		if param.DeviceId != "" && param.DeviceId != device.Status.Type {
			continue
		}
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

	mlxconfigParams, dmsParams, err := getSpectrumXParameters(desiredConfig, spcXSpec, device)
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

	mlxconfigParams, dmsParams, err := getSpectrumXParameters(desiredConfig, spcXSpec, device)
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

	desiredConfig, found := m.spectrumXConfigs[device.Spec.Configuration.Template.SpectrumXOptimized.Version]
	if !found {
		return false, fmt.Errorf("spectrumx config not found for version %s", device.Spec.Configuration.Template.SpectrumXOptimized.Version)
	}

	dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to get DMS client", "device", device.Name)
		return false, err
	}

	log.Log.V(2).Info("SpectrumXConfigManager.RuntimeConfigApplied(): checking RoCE config", "device", device.Name)
	roceApplied, err := parametersApplied(device, desiredConfig.RuntimeConfig.Roce, dmsClient)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to check if RoCE config is applied", "device", device.Name)
		return false, err
	}

	if !roceApplied {
		return false, nil
	}

	log.Log.V(2).Info("SpectrumXConfigManager.RuntimeConfigApplied(): checking Adaptive Routing config", "device", device.Name)
	adaptiveRoutingApplied, err := parametersApplied(device, desiredConfig.RuntimeConfig.AdaptiveRouting, dmsClient)
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

	log.Log.V(2).Info("SpectrumXConfigManager.RuntimeConfigApplied(): checking Congestion Control config", "device", device.Name)
	congestionControlApplied, err := parametersApplied(device, desiredConfig.RuntimeConfig.CongestionControl, dmsClient)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to check if Congestion Control config is applied", "device", device.Name)
		return false, err
	}

	if !congestionControlApplied {
		return false, nil
	}

	overlay := device.Spec.Configuration.Template.SpectrumXOptimized.Overlay
	var interPacketGapParams []types.ConfigurationParameter
	switch overlay {
	case consts.OverlayL3:
		interPacketGapParams = desiredConfig.RuntimeConfig.InterPacketGap.L3EVPN
	case consts.OverlayNone:
		interPacketGapParams = desiredConfig.RuntimeConfig.InterPacketGap.PureL3
	default:
		return false, fmt.Errorf("invalid overlay %s", overlay)
	}

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
	desiredConfig, found := m.spectrumXConfigs[device.Spec.Configuration.Template.SpectrumXOptimized.Version]
	if !found {
		return fmt.Errorf("spectrumx config not found for version %s", device.Spec.Configuration.Template.SpectrumXOptimized.Version)
	}

	dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to get DMS client", "device", device.Name)
		return err
	}

	log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): setting RoCE config", "device", device.Name)
	err = dmsClient.SetParameters(desiredConfig.RuntimeConfig.Roce)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X RoCE config", "device", device.Name)
		return err
	}

	log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): setting Adaptive Routing config", "device", device.Name)
	err = dmsClient.SetParameters(desiredConfig.RuntimeConfig.AdaptiveRouting)
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

	log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): setting Congestion Control config", "device", device.Name)
	err = dmsClient.SetParameters(desiredConfig.RuntimeConfig.CongestionControl)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X Congestion Control config", "device", device.Name)
		return err
	}

	overlay := device.Spec.Configuration.Template.SpectrumXOptimized.Overlay
	var interPacketGapParams []types.ConfigurationParameter
	switch overlay {
	case consts.OverlayL3:
		interPacketGapParams = desiredConfig.RuntimeConfig.InterPacketGap.L3EVPN
	case consts.OverlayNone:
		interPacketGapParams = desiredConfig.RuntimeConfig.InterPacketGap.PureL3
	default:
		return fmt.Errorf("invalid overlay %s", overlay)
	}

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
	log.Log.Info("SpectrumXConfigManager.RunDocaSpcXCC()", "port", port.PCI)

	runningCCProcess, found := m.ccProcesses[port.PCI]
	if found && runningCCProcess.running.Load() {
		log.Log.V(2).Info("SpectrumXConfigManager.RunDocaSpcXCC(): CC process already running", "port", port.PCI)
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
			log.Log.Error(err, "SpectrumXConfigManager.RunDocaSpcXCC(): Failed to run CC process", "port", port.PCI)
		}

		log.Log.V(2).Info("SpectrumXConfigManager.RunDocaSpcXCC(): CC process output", "port", port.PCI, "output", string(output))
		process.running.Store(false)
	}()

	log.Log.V(2).Info("Waiting 3s for DOCA SPC-X CC to start", "port", port.PCI)
	time.Sleep(3 * time.Second)

	if !process.running.Load() {
		process.errMutex.RLock()
		cmdErr := process.cmdErr
		process.errMutex.RUnlock()

		if cmdErr != nil {
			log.Log.Error(cmdErr, "Failed to start DOCA SPC-X CC", "port", port.PCI)
			return fmt.Errorf("failed to start DOCA SPC-X CC for port %s: %v", port.PCI, cmdErr)
		}
		return fmt.Errorf("failed to start DOCA SPC-X CC for port %s: unknown error", port.PCI)
	}

	log.Log.V(2).Info("DOCA SPC-X CC process started", "port", port.PCI)

	m.ccProcesses[port.PCI] = process

	log.Log.Info("Started DOCA SPC-X CC process", "port", port.PCI)

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
