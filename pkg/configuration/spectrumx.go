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

package configuration

import (
	"fmt"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dms"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type SpectrumXConfigManager interface {
	NvConfigApplied(device *v1alpha1.NicDevice) (bool, error)
	ApplyNvConfig(device *v1alpha1.NicDevice) (bool, error)
	RuntimeConfigApplied(device *v1alpha1.NicDevice) (bool, error)
	ApplyRuntimeConfig(device *v1alpha1.NicDevice) error
}

type spectrumXConfigManager struct {
	spectrumXConfigs map[string]*types.SpectrumXConfig
	dmsManager       dms.DMSManager
}

func (m *spectrumXConfigManager) NvConfigApplied(device *v1alpha1.NicDevice) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.NvConfigApplied()", "device", device.Name)

	desiredConfig, found := m.spectrumXConfigs[device.Spec.Configuration.Template.SpectrumXOptimized.Version]
	if !found {
		return false, fmt.Errorf("spectrumx config not found for version %s", device.Spec.Configuration.Template.SpectrumXOptimized.Version)
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
		if values[param.DMSPath] != param.Value {
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

	dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
	if err != nil {
		log.Log.Error(err, "ApplyNvConfig(): failed to get DMS client", "device", device.Name)
		return false, err
	}

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
		if values[param.DMSPath] != param.Value {
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

	roceApplied, err := parametersApplied(device, desiredConfig.RuntimeConfig.Roce, dmsClient)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to check if RoCE config is applied", "device", device.Name)
		return false, err
	}

	if !roceApplied {
		return false, nil
	}

	adaptiveRoutingApplied, err := parametersApplied(device, desiredConfig.RuntimeConfig.AdaptiveRouting, dmsClient)
	if err != nil {
		log.Log.Error(err, "RuntimeConfigApplied(): failed to check if Adaptive Routing config is applied", "device", device.Name)
		return false, err
	}

	if !adaptiveRoutingApplied {
		return false, nil
	}

	// TODO check if congestion control algorithm is enabled

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

	err = dmsClient.SetParameters(desiredConfig.RuntimeConfig.Roce)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X RoCE config", "device", device.Name)
		return err
	}

	err = dmsClient.SetParameters(desiredConfig.RuntimeConfig.AdaptiveRouting)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X Adaptive Routing config", "device", device.Name)
		return err
	}

	// TODO enable congestion control algorithm first

	err = dmsClient.SetParameters(desiredConfig.RuntimeConfig.CongestionControl)
	if err != nil {
		log.Log.Error(err, "ApplyRuntimeConfig(): failed to set Spectrum-X Congestion Control config", "device", device.Name)
		return err
	}

	// TODO for InterPacketGap check overlay first

	return nil
}

func NewSpectrumXConfigManager(dmsManager dms.DMSManager, spectrumXConfigs map[string]*types.SpectrumXConfig) SpectrumXConfigManager {
	return &spectrumXConfigManager{dmsManager: dmsManager, spectrumXConfigs: spectrumXConfigs}
}
