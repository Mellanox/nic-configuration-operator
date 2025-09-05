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
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/dms"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"k8s.io/apimachinery/pkg/api/equality"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	TX_SCHED_LOCALITY_ACCUMULATIVE = "TX_SCHED_LOCALITY_ACCUMULATIVE"
	MULTIPATH_DSCP_DEFAULT         = "MULTIPATH_DSCP_DEFAULT"
	RTT_RESP_DSCP_DEFAULT_MODE     = "RTT_RESP_DSCP_DEFAULT"
	RTT_RESP_DSCP_DEFAULT_VALUE    = 0
	ENABLED                        = "ENABLED"
)

type SpectrumXConfigManager interface {
	NvConfigApplied(device *v1alpha1.NicDevice) (bool, error)
	ApplyNvConfig(device *v1alpha1.NicDevice) (bool, error)
	ApplyRuntimeConfig(device *v1alpha1.NicDevice) error
}

type spectrumXConfigManager struct {
	dmsManager dms.DMSManager
}

func (m *spectrumXConfigManager) NvConfigApplied(device *v1alpha1.NicDevice) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.NvConfigApplied()", "device", device.Name)

	dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
	if err != nil {
		log.Log.Error(err, "NvConfigApplied(): failed to get DMS client", "device", device.Name)
		return false, err
	}

	config, err := dmsClient.GetSpectrumXNVConfig()
	if err != nil {
		log.Log.Error(err, "NvConfigApplied(): failed to get Spectrum-X NV config", "device", device.Name)
		return false, err
	}

	return equality.Semantic.DeepEqual(config, getEnabledSpectrumXConfig()), nil
}

func (m *spectrumXConfigManager) ApplyNvConfig(device *v1alpha1.NicDevice) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.ApplyNvConfig()", "device", device.Name)

	dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
	if err != nil {
		log.Log.Error(err, "ApplyNvConfig(): failed to get DMS client", "device", device.Name)
		return false, err
	}

	alreadyApplied, err := m.NvConfigApplied(device)
	if err != nil {
		log.Log.Error(err, "ApplyNvConfig(): failed to check if Spectrum-X NV config is applied", "device", device.Name)
		return false, err
	}

	if alreadyApplied {
		log.Log.Info("SpectrumXConfigManager.ApplyNvConfig()", "device", device.Name, "already applied")
		return false, nil
	}

	err = dmsClient.SetSpectrumXNVConfig(getEnabledSpectrumXConfig())
	if err != nil {
		log.Log.Error(err, "ApplyNvConfig(): failed to set Spectrum-X NV config", "device", device.Name)
		return false, err
	}

	return false, nil
}

func (m *spectrumXConfigManager) ApplyRuntimeConfig(device *v1alpha1.NicDevice) error {
	return nil
}

func NewSpectrumXConfigManager(dmsManager dms.DMSManager) SpectrumXConfigManager {
	return &spectrumXConfigManager{dmsManager: dmsManager}
}

func getEnabledSpectrumXConfig() *types.SpectrumXNVConfig {
	return &types.SpectrumXNVConfig{
		AdaptiveRouting:      true,
		UserProgrammable:     true,
		TxSchedLocalityMode:  TX_SCHED_LOCALITY_ACCUMULATIVE,
		MultipathDSCP:        MULTIPATH_DSCP_DEFAULT,
		RTTRespDSCP:          RTT_RESP_DSCP_DEFAULT_VALUE,
		RTTRespDSCPMode:      RTT_RESP_DSCP_DEFAULT_MODE,
		CCSteeringExtEnabled: true,
	}
}
