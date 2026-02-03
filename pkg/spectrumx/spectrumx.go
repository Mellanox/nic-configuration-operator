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
	// BreakoutConfigApplied checks if the desired Spectrum-X breakout config is applied to the device
	BreakoutConfigApplied(ctx context.Context, device *v1alpha1.NicDevice) (bool, error)
	// ApplyBreakoutConfig applies the desired Spectrum-X breakout config to the device
	ApplyBreakoutConfig(ctx context.Context, device *v1alpha1.NicDevice) error
	// NvConfigApplied checks if the desired Spectrum-X NV config is applied to the device
	NvConfigApplied(ctx context.Context, device *v1alpha1.NicDevice) (bool, error)
	// ApplyNvConfig applies the desired Spectrum-X NV config to the device
	ApplyNvConfig(ctx context.Context, device *v1alpha1.NicDevice) error
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

// getBreakoutParameters retrieves breakout config parameters from the desired config based on multiplane mode.
// Breakout parameters come from the breakout section of the config file.
// returns: mlxconfigParams, dmsParams, error
func getBreakoutParameters(desiredConfig *types.SpectrumXConfig, spcXSpec *v1alpha1.SpectrumXOptimizedSpec, device *v1alpha1.NicDevice) ([]types.ConfigurationParameter, []types.ConfigurationParameter, error) {
	multiplaneMode, numberOfPlanes := spcXSpec.MultiplaneMode, spcXSpec.NumberOfPlanes
	var breakoutConfig []types.ConfigurationParameter
	switch multiplaneMode {
	case consts.MultiplaneModeNone:
		break
	case consts.MultiplaneModeSwplb:
		breakoutConfig = desiredConfig.BreakoutConfig.Swplb[numberOfPlanes]
	case consts.MultiplaneModeHwplb:
		breakoutConfig = desiredConfig.BreakoutConfig.Hwplb[numberOfPlanes]
	case consts.MultiplaneModeUniplane:
		breakoutConfig = desiredConfig.BreakoutConfig.Uniplane[numberOfPlanes]
	default:
		return nil, nil, fmt.Errorf("invalid multiplane mode %s", multiplaneMode)
	}
	log.Log.V(2).Info("SpectrumXConfigManager.getBreakoutParameters(): breakout config", "device", device.Name, "breakoutConfig", breakoutConfig)

	breakoutConfig = filterParameters(breakoutConfig, device.Status.Type, numberOfPlanes, multiplaneMode)

	mlxconfigParams := []types.ConfigurationParameter{}
	dmsParams := []types.ConfigurationParameter{}

	for _, param := range breakoutConfig {
		if param.MlxConfig != "" {
			mlxconfigParams = append(mlxconfigParams, param)
		} else if param.DMSPath != "" {
			dmsParams = append(dmsParams, param)
		} else {
			err := fmt.Errorf("invalid parameter %+v", param)
			log.Log.Error(err, "getBreakoutParameters(): invalid parameter", "device", device.Name)
			return nil, nil, err
		}
	}

	return mlxconfigParams, dmsParams, nil
}

// getNvConfigParameters retrieves nvConfig parameters (DMS and mlxconfig) from the desired config.
// NvConfig parameters come from the nvConfig section of the config file and are filtered by multiplane mode.
// returns: mlxconfigParams, dmsParams, error
func getNvConfigParameters(desiredConfig *types.SpectrumXConfig, spcXSpec *v1alpha1.SpectrumXOptimizedSpec, device *v1alpha1.NicDevice) ([]types.ConfigurationParameter, []types.ConfigurationParameter, error) {
	multiplaneMode, numberOfPlanes := spcXSpec.MultiplaneMode, spcXSpec.NumberOfPlanes
	desiredParams := filterParameters(desiredConfig.NVConfig, device.Status.Type, numberOfPlanes, multiplaneMode)

	mlxconfigParams := []types.ConfigurationParameter{}
	dmsParams := []types.ConfigurationParameter{}

	for _, param := range desiredParams {
		if param.MlxConfig != "" {
			mlxconfigParams = append(mlxconfigParams, param)
		} else if param.DMSPath != "" {
			dmsParams = append(dmsParams, param)
		} else {
			err := fmt.Errorf("invalid parameter %+v", param)
			log.Log.Error(err, "getNvConfigParameters(): invalid parameter", "device", device.Name)
			return nil, nil, err
		}
	}

	return mlxconfigParams, dmsParams, nil
}

// checkParamsApplied checks if the given mlxconfig and DMS parameters are applied to the device.
func (m *spectrumXConfigManager) checkParamsApplied(ctx context.Context, mlxconfigParams, dmsParams []types.ConfigurationParameter, device *v1alpha1.NicDevice) (bool, error) {
	// Check mlxconfig params
	for _, param := range mlxconfigParams {
		mlxConfig, err := m.nvConfigUtils.QueryNvConfig(ctx, device.Status.Ports[0].PCI, param.MlxConfig)
		if err != nil {
			log.Log.Error(err, "checkParamsApplied(): failed to get mlxconfig", "device", device.Name)
			return false, err
		}
		if !slices.Contains(mlxConfig.CurrentConfig[param.MlxConfig], param.Value) {
			log.Log.V(2).Info("checkParamsApplied(): mlxconfig parameter not applied", "device", device.Name, "param", param, "observedValues", mlxConfig.CurrentConfig[param.MlxConfig])
			return false, nil
		}
	}

	// Check DMS params
	if len(dmsParams) > 0 {
		dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
		if err != nil {
			log.Log.Error(err, "checkParamsApplied(): failed to get DMS client", "device", device.Name)
			return false, err
		}

		applied, err := checkDmsParamsApplied(device, dmsParams, dmsClient)
		if err != nil {
			log.Log.Error(err, "checkParamsApplied(): failed to check DMS params", "device", device.Name)
			return false, err
		}

		if !applied {
			return false, nil
		}
	}

	return true, nil
}

// applyParams applies the given mlxconfig and DMS parameters to the device.
func (m *spectrumXConfigManager) applyParams(mlxconfigParams, dmsParams []types.ConfigurationParameter, device *v1alpha1.NicDevice) error {
	// Apply mlxconfig params
	for _, param := range mlxconfigParams {
		log.Log.V(2).Info("applyParams(): setting mlxconfig param", "device", device.Name, "param", param.MlxConfig, "value", param.Value)
		err := m.nvConfigUtils.SetNvConfigParameter(device.Status.Ports[0].PCI, param.MlxConfig, param.Value)
		if err != nil {
			log.Log.Error(err, "applyParams(): failed to set mlxconfig parameter", "device", device.Name, "param", param)
			return err
		}
	}

	// Apply DMS params
	if len(dmsParams) > 0 {
		dmsClient, err := m.dmsManager.GetDMSClientBySerialNumber(device.Status.SerialNumber)
		if err != nil {
			log.Log.Error(err, "applyParams(): failed to get DMS client", "device", device.Name)
			return err
		}

		log.Log.V(2).Info("applyParams(): setting DMS params", "device", device.Name, "config", dmsParams)
		err = dmsClient.SetParameters(dmsParams)
		if err != nil {
			log.Log.Error(err, "applyParams(): failed to set DMS config", "device", device.Name)
			return err
		}
	}

	return nil
}

// BreakoutConfigApplied checks if the desired Spectrum-X breakout config is applied to the device.
func (m *spectrumXConfigManager) BreakoutConfigApplied(ctx context.Context, device *v1alpha1.NicDevice) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.BreakoutConfigApplied()", "device", device.Name)

	spcXSpec := device.Spec.Configuration.Template.SpectrumXOptimized
	desiredConfig, found := m.spectrumXConfigs[spcXSpec.Version]
	if !found {
		return false, fmt.Errorf("spectrumx config not found for version %s", spcXSpec.Version)
	}

	mlxconfigParams, dmsParams, err := getBreakoutParameters(desiredConfig, spcXSpec, device)
	if err != nil {
		log.Log.Error(err, "BreakoutConfigApplied(): failed to get breakout parameters", "device", device.Name)
		return false, err
	}

	return m.checkParamsApplied(ctx, mlxconfigParams, dmsParams, device)
}

// ApplyBreakoutConfig applies the desired Spectrum-X breakout config to the device.
func (m *spectrumXConfigManager) ApplyBreakoutConfig(ctx context.Context, device *v1alpha1.NicDevice) error {
	log.Log.Info("SpectrumXConfigManager.ApplyBreakoutConfig()", "device", device.Name)

	spcXSpec := device.Spec.Configuration.Template.SpectrumXOptimized
	desiredConfig, found := m.spectrumXConfigs[spcXSpec.Version]
	if !found {
		return fmt.Errorf("spectrumx config not found for version %s", spcXSpec.Version)
	}

	mlxconfigParams, dmsParams, err := getBreakoutParameters(desiredConfig, spcXSpec, device)
	if err != nil {
		log.Log.Error(err, "ApplyBreakoutConfig(): failed to get breakout parameters", "device", device.Name)
		return err
	}

	return m.applyParams(mlxconfigParams, dmsParams, device)
}

// NvConfigApplied checks if the desired Spectrum-X NV config is applied to the device.
func (m *spectrumXConfigManager) NvConfigApplied(ctx context.Context, device *v1alpha1.NicDevice) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.NvConfigApplied()", "device", device.Name)

	spcXSpec := device.Spec.Configuration.Template.SpectrumXOptimized
	desiredConfig, found := m.spectrumXConfigs[spcXSpec.Version]
	if !found {
		return false, fmt.Errorf("spectrumx config not found for version %s", spcXSpec.Version)
	}

	mlxconfigParams, dmsParams, err := getNvConfigParameters(desiredConfig, spcXSpec, device)
	if err != nil {
		log.Log.Error(err, "NvConfigApplied(): failed to get NV config parameters", "device", device.Name)
		return false, err
	}

	return m.checkParamsApplied(ctx, mlxconfigParams, dmsParams, device)
}

// ApplyNvConfig applies the desired Spectrum-X NV config to the device.
func (m *spectrumXConfigManager) ApplyNvConfig(ctx context.Context, device *v1alpha1.NicDevice) error {
	log.Log.Info("SpectrumXConfigManager.ApplyNvConfig()", "device", device.Name)

	spcXSpec := device.Spec.Configuration.Template.SpectrumXOptimized
	desiredConfig, found := m.spectrumXConfigs[spcXSpec.Version]
	if !found {
		return fmt.Errorf("spectrumx config not found for version %s", spcXSpec.Version)
	}

	mlxconfigParams, dmsParams, err := getNvConfigParameters(desiredConfig, spcXSpec, device)
	if err != nil {
		log.Log.Error(err, "ApplyNvConfig(): failed to get NV config parameters", "device", device.Name)
		return err
	}

	return m.applyParams(mlxconfigParams, dmsParams, device)
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

// checkDmsParamsApplied checks if the given DMS parameters are applied to the device
func checkDmsParamsApplied(device *v1alpha1.NicDevice, params []types.ConfigurationParameter, dmsClient dms.DMSClient) (bool, error) {
	log.Log.Info("SpectrumXConfigManager.checkDmsParamsApplied()", "device", device.Name)

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
		if values[param.DMSPath] != param.Value && values[param.DMSPath] != param.AlternativeValue {
			log.Log.V(2).Info("SpectrumXConfigManager.checkDmsParamsApplied(): parameter not applied", "device", device.Name, "param", param)
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
	roceApplied, err := checkDmsParamsApplied(device, roceParams, dmsClient)
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
	adaptiveRoutingApplied, err := checkDmsParamsApplied(device, adaptiveRoutingParams, dmsClient)
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
	congestionControlApplied, err := checkDmsParamsApplied(device, congestionControlParams, dmsClient)
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

	overlayParamsApplied, err := checkDmsParamsApplied(device, interPacketGapParams, dmsClient)
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
		if multiplaneMode == consts.MultiplaneModeHwplb {
			if len(device.Status.Ports) == 0 {
				return fmt.Errorf("no ports available for device %s", device.Name)
			}
			err = m.RunDocaSpcXCC(device.Status.Ports[0])
			if err != nil {
				log.Log.Error(err, "ApplyRuntimeConfig(): failed to run DOCA SPC-X CC", "device", device.Name)
				return err
			}
		} else {
			for _, port := range device.Status.Ports {
				err = m.RunDocaSpcXCC(port)
				if err != nil {
					log.Log.Error(err, "ApplyRuntimeConfig(): failed to run DOCA SPC-X CC", "device", device.Name)
					return err
				}
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
		return err
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
