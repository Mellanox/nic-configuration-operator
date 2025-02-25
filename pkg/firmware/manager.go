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

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
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

	// BurnNicFirmware will update the device's FW to the requested version
	// returns error - there were errors while updating firmware
	BurnNicFirmware(ctx context.Context, device *v1alpha1.NicDevice, version string) error
}

type firmwareManager struct {
	client client.Client

	utils FirmwareUtils

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
		devicePSID := device.Status.PSID

		for version, PSIDs := range fwSourceObj.Status.Versions {
			if slices.Contains(PSIDs, devicePSID) {
				return version, nil
			}
		}

		return "", fmt.Errorf("requested firmware source (%s) has no image for this device's PSID (%s)", fwSourceName, devicePSID)
	default:
		return "", types.FirmwareSourceNotReadyError(fwSourceName, status)
	}
}

// BurnNicFirmware will update the device's FW to the requested version
// returns error - there were errors while updating firmware
func (f firmwareManager) BurnNicFirmware(ctx context.Context, device *v1alpha1.NicDevice, version string) error {
	log.Log.Info("FirmwareManager.BurnNicFirmware()", "device", device.Name, "version", version)

	if device.Spec.Firmware == nil {
		return errors.New("device's firmware spec is empty")
	}

	fwSourceName := device.Spec.Firmware.NicFirmwareSourceRef

	cacheDir := path.Join(f.cacheRootDir, fwSourceName)
	devicePSID := device.Status.PSID

	log.Log.V(2).Info("Searching for FW binary in the cache", "cacheDir", cacheDir, "PSID", devicePSID)

	fwFolderPath := filepath.Join(cacheDir, "firmware-binaries", version, devicePSID)

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
		return err
	}

	if fwPath == "" {
		return fmt.Errorf("couldn't find FW binary file for the %s version and %s PSID", version, devicePSID)
	}

	versionInFile, psidInFile, err := f.utils.GetFirmwareVersionAndPSID(fwPath)
	if err != nil {
		log.Log.Error(err, "couldn't get FW version and PSID from FW binary file", "path", fwPath)
		return err
	}
	if versionInFile != version || psidInFile != devicePSID {
		return fmt.Errorf("found FW binary file doesn't match expected version or PSID. Requested %s, %s, found %s, %s", version, devicePSID, versionInFile, psidInFile)
	}

	if err := createDirIfNotExists(f.tmpDir); err != nil {
		log.Log.Error(err, "failed to create tmp directory")
		return err
	}

	log.Log.V(2).Info("copying fw binary file to a local folder", "path", fwPath)
	targetPath := path.Join(f.tmpDir, filepath.Base(fwPath))
	err = copyFile(fwPath, targetPath)
	if err != nil {
		log.Log.Error(err, "failed to copy fw binary file to a local folder", "path", fwPath)
		return err
	}

	pci := device.Status.Ports[0].PCI
	log.Log.Info("Starting firmware image burning. The process might take a long time", "path", fwPath, "pci", pci)

	err = f.utils.BurnNicFirmware(ctx, pci, fwPath)
	if err != nil {
		log.Log.Error(err, "failed to burn FW on device", "path", fwPath, "pci", pci)
		return err
	}

	return nil
}

func NewFirmwareManager(client client.Client, namespace string) FirmwareManager {
	return &firmwareManager{client: client, cacheRootDir: consts.NicFirmwareStorage, namespace: namespace, tmpDir: consts.TempDir, utils: newFirmwareUtils()}
}
