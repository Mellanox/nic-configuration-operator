/*
2025 NVIDIA CORPORATION & AFFILIATES
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	k8sTypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/dms-lib/pkg/device"
	libfirmware "github.com/Mellanox/dms-lib/pkg/firmware"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	utilsPkg "github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

// FirmwareManager contains logic for managing NIC devices FW on the host
type FirmwareManager interface {
	// ValidateRequestedFirmwareSource will validate the NicFirmwareSource object, requested for NicDevice
	// returns string - requested firmware version
	// returns error - firmware source is not ready or there are errors
	// Possible errors:
	// Referenced NicFirmwareSource obj doesn't exist
	// Source exists but not ready / failed
	// Source exists and ready but doesn't contain an image for this device's PSID
	ValidateRequestedFirmwareSource(ctx context.Context, device *v1alpha1.NicDevice) (string, error)

	// InstallFirmware will update the device's FW to the requested version
	// returns bool - reboot required when an attempted firmware reset fails.
	// SkipReset leaves activation to the caller and retains the historical false result.
	// returns error - there were errors while updating firmware
	InstallFirmware(ctx context.Context, device *v1alpha1.NicDevice, options *types.FirmwareInstallOptions) (bool, error)

	// GetFirmwareVersionsFromDevice retrieves the burned and running FW versions from the device
	// returns string - burned FW version
	// returns string - running FW version
	// returns error - there were errors while retrieving the firmware versions
	GetFirmwareVersionsFromDevice(device *v1alpha1.NicDevice) (string, string, error)
}

// firmwareDevice copies only observed identity and PF endpoints; the library owns
// firmware targeting and BlueField multi-PF preparation.
func firmwareDevice(nic *v1alpha1.NicDevice) (device.Device, error) {
	if nic == nil || len(nic.Status.Ports) == 0 {
		return device.Device{}, errors.New("device has no ports")
	}
	observed := device.Device{ID: utilsPkg.PCIDeviceAddress(nic.Status.Ports[0].PCI), Type: nic.Status.Type, PSID: nic.Status.PSID}
	for _, port := range nic.Status.Ports {
		observed.Ports = append(observed.Ports, device.Port{PCI: port.PCI})
	}
	return observed, nil
}

type firmwareManager struct {
	client client.Client

	backend libfirmware.Manager

	cacheRootDir string
	namespace    string
	tmpDir       string
}

// ValidateRequestedFirmwareSource will validate the NicFirmwareSource object, requested for NicDevice
// returns string - requested firmware version
// returns error - firmware source is not ready or there are errors
// Possible errors:
// Referenced NicFirmwareSource obj doesn't exist
// Source exists but not ready / failed
// Source exists and ready but doesn't contain an image for this device's PSID
func (f firmwareManager) ValidateRequestedFirmwareSource(ctx context.Context, device *v1alpha1.NicDevice) (string, error) {
	log.Log.Info("FirmwareManager.ValidateRequestedFirmwareSource()", "device", device.Name)

	if device.Spec.Firmware == nil {
		return "", errors.New("device's firmware spec is empty")
	}

	fwSourceName := device.Spec.Firmware.NicFirmwareSourceRef
	fwSourceObj := v1alpha1.NicFirmwareSource{}
	err := f.client.Get(ctx, k8sTypes.NamespacedName{Name: fwSourceName, Namespace: f.namespace}, &fwSourceObj)
	if err != nil {
		log.Log.Error(err, "failed to get NicFirmwareSource obj", "name", fwSourceName)
		return "", err
	}

	status := fwSourceObj.Status.State

	switch status {
	case consts.FirmwareSourceDownloadFailedStatus, consts.FirmwareSourceProcessingFailedStatus, consts.FirmwareSourceCacheVerificationFailedStatus:
		return "", fmt.Errorf("requested firmware source %s failed: %s, %s", fwSourceName, status, fwSourceObj.Status.Reason)
	case consts.FirmwareSourceSuccessStatus:
		deviceID := device.Status.Type
		if utilsPkg.IsBlueFieldDevice(deviceID) {
			version, found := fwSourceObj.Status.BFBVersions[deviceID]
			if !found {
				return "", fmt.Errorf("requested firmware source (%s) has no image for this BlueField device (%s)", fwSourceName, deviceID)
			}

			return version, nil
		}

		devicePSID := device.Status.PSID

		for version, PSIDs := range fwSourceObj.Status.BinaryVersions {
			if slices.Contains(PSIDs, devicePSID) {
				return version, nil
			}
		}

		return "", fmt.Errorf("requested firmware source (%s) has no image for this device's PSID (%s)", fwSourceName, devicePSID)
	default:
		return "", types.FirmwareSourceNotReadyError(fwSourceName, status)
	}
}

// InstallFirmware will update the device's FW to the requested version
// returns bool - reboot required when an attempted firmware reset fails.
// SkipReset leaves activation to the caller and retains the historical false result.
// returns error - there were errors while updating firmware
func (f firmwareManager) InstallFirmware(ctx context.Context, device *v1alpha1.NicDevice, options *types.FirmwareInstallOptions) (bool, error) {
	observed, err := firmwareDevice(device)
	if err != nil {
		return false, err
	}
	log.Log.Info("FirmwareManager.InstallFirmware()", "device", device.Name, "options", options)

	if options == nil {
		return false, errors.New("options must not be nil")
	}

	version := options.Version

	// Resolve an omitted version before comparing against already burned firmware.
	if version == "" && options.FwFilePath != "" {
		version, err = f.getVersionFromFirmwareFile(ctx, device, options.FwFilePath)
		if err != nil {
			return false, fmt.Errorf("failed to determine firmware version from file: %w", err)
		}
	}

	// Check current firmware versions before proceeding with burn (idempotency)
	if version != "" {
		versions, err := f.backend.QueryVersions(ctx, observed)
		burnedVersion := versions.Burned
		if err != nil {
			log.Log.V(2).Info("Could not retrieve current firmware version, proceeding with burn", "device", device.Name, "error", err.Error())
		} else if burnedVersion == version {
			log.Log.Info("Burned firmware version already matches requested version, skipping burn", "device", device.Name, "version", version)
			return false, nil
		}
		log.Log.Info("Burned firmware version does not match requested version, proceeding with burn", "device", device.Name, "burned", burnedVersion, "requested", version)
	}

	// Resolve firmware file path from cache if not provided directly
	fwFilePath := options.FwFilePath
	if fwFilePath == "" {
		fwFilePath, err = f.resolveFirmwareFilePath(device, version)
		if err != nil {
			return false, err
		}
	}

	if err := f.backend.Install(ctx, observed, libfirmware.InstallOptions{Path: fwFilePath, Version: version}); err != nil {
		return false, err
	}

	if !options.SkipReset {
		err = f.backend.Reset(ctx, observed, libfirmware.ResetOptions{SkipVMCheck: os.Getenv(consts.SKIP_VM_CHECK) == consts.LabelValueTrue})
		if err != nil {
			log.Log.Error(err, "failed to reset NIC firmware after install, reboot required", "device", device.Name)
			return true, nil
		}
	}

	return false, nil
}

// resolveFirmwareFilePath resolves the firmware file path from the K8s cache.
// For BlueField devices: finds .bfb file in {cacheDir}/bfb/
// For regular NICs: finds .bin file in {cacheDir}/binaries/{version}/{PSID}/, copies to tmpDir
func (f firmwareManager) resolveFirmwareFilePath(device *v1alpha1.NicDevice, version string) (string, error) {
	if device.Spec.Firmware == nil {
		return "", errors.New("device's firmware spec is empty")
	}

	fwSourceName := device.Spec.Firmware.NicFirmwareSourceRef
	cacheDir := path.Join(f.cacheRootDir, fwSourceName)

	if utilsPkg.IsBlueFieldDevice(device.Status.Type) {
		return f.resolveBFBFilePathFromCache(cacheDir, version)
	}

	return f.resolveFirmwareBinaryFilePathFromCache(cacheDir, version, device.Status.PSID)
}

// resolveBFBFilePathFromCache finds a .bfb file in the cache directory
func (f firmwareManager) resolveBFBFilePathFromCache(cacheDir string, version string) (string, error) {
	bfbFolderPath := filepath.Join(cacheDir, consts.BFBFolder)
	log.Log.V(2).Info("Searching for BFB file in the cache", "bfbFolderPath", bfbFolderPath)

	var bfbPath string
	err := filepath.WalkDir(bfbFolderPath, func(path string, d os.DirEntry, err error) error {
		if err == nil && strings.EqualFold(filepath.Ext(d.Name()), ".bfb") {
			log.Log.V(2).Info("Found BFB file", "path", path)
			bfbPath = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		log.Log.Error(err, "failed to search for BFB file", "bfbFolderPath", bfbFolderPath)
		return "", err
	}

	if bfbPath == "" {
		return "", fmt.Errorf("couldn't find BFB file for version %s", version)
	}

	return bfbPath, nil
}

// resolveFirmwareBinaryFilePathFromCache finds a .bin file in the cache directory and copies it to tmpDir
func (f firmwareManager) resolveFirmwareBinaryFilePathFromCache(cacheDir string, version string, devicePSID string) (string, error) {
	log.Log.V(2).Info("Searching for FW binary in the cache", "cacheDir", cacheDir, "PSID", devicePSID)

	fwFolderPath := filepath.Join(cacheDir, consts.NicFirmwareBinariesFolder, version, devicePSID)

	fwPath := ""
	err := filepath.WalkDir(fwFolderPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if strings.EqualFold(filepath.Ext(d.Name()), consts.NicFirmwareBinaryFileExtension) {
			log.Log.V(2).Info("Found FW binary file", "path", path)
			if fwPath != "" {
				return fmt.Errorf("found second FW binary file for the %s version and %s PSID: %s", version, devicePSID, path)
			}

			fwPath = path
		}

		return nil
	})

	if err != nil {
		log.Log.Error(err, "error occurred while searching for FW binary file", "cacheDir", cacheDir, "version", version, "PSID", devicePSID)
		return "", err
	}

	if fwPath == "" {
		return "", fmt.Errorf("couldn't find FW binary file for the %s version and %s PSID", version, devicePSID)
	}

	// Copy to tmpDir — cache may be a shared/read-only volume mount
	if err := os.MkdirAll(f.tmpDir, 0755); err != nil {
		log.Log.Error(err, "failed to create tmp directory")
		return "", err
	}

	log.Log.V(2).Info("copying fw binary file to a local folder", "path", fwPath)
	copiedFilePath := path.Join(f.tmpDir, filepath.Base(fwPath))
	err = copyFile(fwPath, copiedFilePath)
	if err != nil {
		log.Log.Error(err, "failed to copy fw binary file to a local folder", "path", fwPath)
		return "", err
	}

	return copiedFilePath, nil
}

// getVersionFromFirmwareFile selects the requested device version from image metadata.
func (f firmwareManager) getVersionFromFirmwareFile(ctx context.Context, device *v1alpha1.NicDevice, fwFilePath string) (string, error) {
	if utilsPkg.IsBlueFieldDevice(device.Status.Type) {
		versions, err := f.backend.InspectBFB(ctx, fwFilePath)
		if err != nil {
			return "", err
		}
		version, found := versions[device.Status.Type]
		if !found {
			return "", fmt.Errorf("BFB file does not contain firmware for device type %s", device.Status.Type)
		}
		return version, nil
	}

	info, err := f.backend.InspectBinary(ctx, fwFilePath)
	if err != nil {
		return "", err
	}
	return info.Version, nil
}

// GetFirmwareVersionsFromDevice retrieves the burned and running FW versions from the device
// returns string - burned FW version
// returns string - running FW version
// returns error - there were errors while retrieving the firmware versions
func (f firmwareManager) GetFirmwareVersionsFromDevice(device *v1alpha1.NicDevice) (string, string, error) {
	observed, err := firmwareDevice(device)
	if err != nil {
		return "", "", err
	}
	versions, err := f.backend.QueryVersions(context.Background(), observed)
	return versions.Burned, versions.Running, err
}

func NewFirmwareManager(client client.Client, backend libfirmware.Manager, namespace string) FirmwareManager {
	return &firmwareManager{client: client, backend: backend, cacheRootDir: consts.NicFirmwareStorage, namespace: namespace, tmpDir: consts.TempDir}
}
