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
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dms"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type SpectrumXManager interface {
	NvConfigApplied(device *v1alpha1.NicDevice) (bool, error)
	ApplyNvConfig(device *v1alpha1.NicDevice) (bool, error)
	RuntimeConfigApplied(device *v1alpha1.NicDevice) (bool, error)
	ApplyRuntimeConfig(device *v1alpha1.NicDevice) error
	GetDocaCCTargetVersion(device *v1alpha1.NicDevice) (string, error)
	RunDocaSpcXCC(port v1alpha1.NicDevicePortSpec) error
}

type spectrumXConfigManager struct {
	spectrumXConfigs map[string]*types.SpectrumXConfig
	dmsManager       dms.DMSManager
	execInterface    execUtils.Interface

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

func (m *spectrumXConfigManager) NvConfigApplied(device *v1alpha1.NicDevice) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.NvConfigApplied()", "device", device.Name)

	desiredConfig, found := m.spectrumXConfigs[device.Spec.Configuration.Template.SpectrumXOptimized.Version]
	if !found {
		return false, fmt.Errorf("spectrumx config not found for version %s", device.Spec.Configuration.Template.SpectrumXOptimized.Version)
	}

	for i, param := range desiredConfig.NVConfig {
		// Drop device-specific parameters for non-matching devices
		if param.DeviceId != "" && param.DeviceId != device.Status.Type {
			desiredConfig.NVConfig[i] = desiredConfig.NVConfig[len(desiredConfig.NVConfig)-1]
			desiredConfig.NVConfig = desiredConfig.NVConfig[:len(desiredConfig.NVConfig)-1]
		}
	}

	dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
	if err != nil {
		log.Log.Error(err, "NvConfigApplied(): failed to get DMS client", "device", device.Name)
		return false, err
	}

	values, err := dmsClient.GetParameters(desiredConfig.NVConfig)
	if err != nil {
		log.Log.Error(err, "NvConfigApplied(): failed to get Spectrum-X NV config", "device", device.Name)
		return false, err
	}

	for _, param := range desiredConfig.NVConfig {
		if values[param.DMSPath] != param.Value && values[param.DMSPath] != param.AlternativeValue {
			return false, nil
		}
	}

	return true, nil
}

func (m *spectrumXConfigManager) ApplyNvConfig(device *v1alpha1.NicDevice) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.ApplyNvConfig()", "device", device.Name)

	desiredConfig, found := m.spectrumXConfigs[device.Spec.Configuration.Template.SpectrumXOptimized.Version]
	if !found {
		return false, fmt.Errorf("spectrumx config not found for version %s", device.Spec.Configuration.Template.SpectrumXOptimized.Version)
	}
	for i, param := range desiredConfig.NVConfig {
		// Drop device-specific parameters for non-matching devices
		if param.DeviceId != "" && param.DeviceId != device.Status.Type {
			desiredConfig.NVConfig[i] = desiredConfig.NVConfig[len(desiredConfig.NVConfig)-1]
			desiredConfig.NVConfig = desiredConfig.NVConfig[:len(desiredConfig.NVConfig)-1]
		}
	}

	dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
	if err != nil {
		log.Log.Error(err, "ApplyNvConfig(): failed to get DMS client", "device", device.Name)
		return false, err
	}

	log.Log.V(2).Info("SpectrumXConfigManager.ApplyNvConfig(): setting Spectrum-X NV config", "device", device.Name, "config", desiredConfig.NVConfig)
	err = dmsClient.SetParameters(desiredConfig.NVConfig)
	if err != nil {
		log.Log.Error(err, "ApplyNvConfig(): failed to set Spectrum-X NV config", "device", device.Name)
		return false, err
	}

	return false, nil
}

func parametersApplied(device *v1alpha1.NicDevice, params []types.ConfigurationParameter, dmsClient dms.DMSClient) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.ApplyRuntimeConfig()", "device", device.Name)

	values, err := dmsClient.GetParameters(params)
	if err != nil {
		log.Log.Error(err, "checkIfSectionApplied(): failed to get Spectrum-X config", "device", device.Name)
		return false, err
	}

	for _, param := range params {
		if values[param.DMSPath] != param.Value && values[param.DMSPath] != param.AlternativeValue {
			log.Log.V(2).Info("SpectrumXConfigManager.ApplyRuntimeConfig(): parameter not applied", "device", device.Name, "param", param)
			return false, nil
		}
	}

	return true, nil
}

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

	// For InterPacketGap check overlay first

	return true, nil

}

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
	var interPacketGapParam types.ConfigurationParameter
	switch overlay {
	case "l3":
		interPacketGapParam = desiredConfig.RuntimeConfig.InterPacketGap.L3EVPN
	case "none":
		interPacketGapParam = desiredConfig.RuntimeConfig.InterPacketGap.PureL3
	default:
		return fmt.Errorf("invalid overlay %s", overlay)
	}

	err = dmsClient.SetParameters([]types.ConfigurationParameter{interPacketGapParam})
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X InterPacketGap config", "device", device.Name)
		return err
	}

	return nil
}

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
	return &spectrumXConfigManager{dmsManager: dmsManager, spectrumXConfigs: spectrumXConfigs, execInterface: execUtils.New()}
}
