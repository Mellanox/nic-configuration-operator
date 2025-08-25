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

//nolint:errcheck
package firmware

import (
	"archive/zip"
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	commonUtils "github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

type FirmwareUtils interface {
	// DownloadFile downloads the file under url and places it locally under destPath
	DownloadFile(url, destPath string) error
	// UnzipFiles extract files from the zip archive to destDir
	// Returns a list of extracted files, error if occurred
	UnzipFiles(zipPath, destDir string) ([]string, error)
	// GetBurnedFirmwareVersionFromDevice retrieves the burned FW version from the device
	GetBurnedFirmwareVersionFromDevice(pciAddress string) (string, error)
	// GetFirmwareVersionAndPSIDFromFWBinary retrieves the version and PSID from the firmware binary
	GetFirmwareVersionAndPSIDFromFWBinary(firmwareBinaryPath string) (string, string, error)
	// GetFWVersionsFromBFB retrieves the FW versions from the BFB file
	GetFWVersionsFromBFB(bfbPath string) (map[string]string, error)
	// VerifyImageBootable verifies if the image file is valid and bootable
	VerifyImageBootable(firmwareBinaryPath string) error
	// CleanupDirectory deletes any file inside a root directory except for allowedSet. Empty directories are cleaned up as well at the end
	CleanupDirectory(root string, allowedSet map[string]struct{}) error
	// BurnNicFirmware burns the requested firmware on the requested device
	// Operation can be long, require context to be able to terminate by timeout
	BurnNicFirmware(ctx context.Context, pciAddress, fwPath string) error
}

type utils struct {
	execInterface execUtils.Interface
}

// DownloadFile downloads the file under url and places it locally under destPath
func (u *utils) DownloadFile(url, destPath string) error {
	log.Log.V(2).Info("FirmwareUtils.DownloadFile()", "url", url, "destPath", destPath)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("could not download file: %w", err)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("could not create file: %w", err)
	}

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("error saving file: %w", err)
	}

	defer func() {
		err = out.Close()
		if err != nil {
			log.Log.Error(err, "failed to close file")
		}
	}()

	defer func() {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err := fmt.Errorf("bad http request status: %s", resp.Status)
			log.Log.Error(err, "failed to finish HTTP request")
		}
	}()

	return nil
}

// UnzipFiles extract files from the zip archive to destDir
// Returns a list of extracted files, error if occurred
func (u *utils) UnzipFiles(zipPath, destDir string) ([]string, error) {
	log.Log.V(2).Info("FirmwareUtils.UnzipFiles()", "zipPath", zipPath, "destDir", destDir)

	extractedFiles := []string{}

	zipReader, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("could not open zip file: %w", err)
	}
	defer zipReader.Close()

	for _, file := range zipReader.File {
		fPath := filepath.Join(destDir, file.Name)

		if !strings.HasPrefix(fPath, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("illegal file path: %s", fPath)
		}

		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(fPath, file.Mode()); err != nil {
				return nil, fmt.Errorf("error creating directory: %w", err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fPath), 0755); err != nil {
			return nil, fmt.Errorf("error creating parent directories: %w", err)
		}

		if err := extractFile(file, fPath); err != nil {
			return nil, err
		}

		extractedFiles = append(extractedFiles, fPath)
	}

	return extractedFiles, nil
}

// extractFile copies the contents of a single file from the ZIP archive
// to a local file, preserving its mode (permissions).
func extractFile(zf *zip.File, destPath string) error {
	srcFile, err := zf.Open()
	if err != nil {
		return err
	}
	defer srcFile.Close()

	outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, zf.Mode())
	if err != nil {
		return err
	}
	defer outFile.Close()

	if _, err = io.Copy(outFile, srcFile); err != nil {
		return err
	}

	return nil
}

// GetBurnedFirmwareVersionFromDevice retrieves the burned FW version from the device
func (u *utils) GetBurnedFirmwareVersionFromDevice(pciAddress string) (string, error) {
	log.Log.V(2).Info("FirmwareUtils.GetFirmwareVersionAndPSIDFromDevice()", "pciAddress", pciAddress)

	cmd := u.execInterface.Command("mlxfwmanager", "-d", pciAddress)
	output, err := cmd.Output()
	if err != nil {
		log.Log.Error(err, "GetBurnedFirmwareVersionFromDevice(): Failed to run mlxfwmanager")
		return "", err
	}

	// Parse the output for FW versions
	scanner := bufio.NewScanner(strings.NewReader(string(output)))

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Look for FW version line under Versions section. Skip FW (Running) line as it might be different from the burned version
		if strings.HasPrefix(line, "FW") && !strings.HasPrefix(line, "FW (Running)") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1], nil
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Log.Error(err, "GetFirmwareVersionAndPSIDFromDevice(): Error reading mlxfwmanager output")
		return "", err
	}

	return "", fmt.Errorf("GetBurnedFirmwareVersionFromDevice(): burned firmware version is empty")
}

// GetFirmwareVersionAndPSIDFromFWBinary retrieves the version and PSID from the firmware binary
func (u *utils) GetFirmwareVersionAndPSIDFromFWBinary(firmwareBinaryPath string) (string, string, error) {
	log.Log.V(2).Info("FirmwareUtils.GetFirmwareVersionAndPSIDFromFWBinary()", "firmwareBinaryPath", firmwareBinaryPath)
	cmd := u.execInterface.Command("mstflint", "-i", firmwareBinaryPath, "q")
	output, err := cmd.Output()
	if err != nil {
		log.Log.Error(err, "GetFirmwareVersionAndPSIDFromFWBinary(): Failed to run mstflint")
		return "", "", err
	}

	// Parse the output for FW version and PSID
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	var firmwareVersion, PSID string

	for scanner.Scan() {
		line := strings.ToLower(scanner.Text())

		if strings.HasPrefix(line, consts.FirmwareVersionPrefix) {
			firmwareVersion = strings.TrimSpace(strings.TrimPrefix(line, consts.FirmwareVersionPrefix))
		}
		if strings.HasPrefix(line, consts.PSIDPrefix) {
			PSID = strings.TrimSpace(strings.TrimPrefix(line, consts.PSIDPrefix))
		}
	}

	if err := scanner.Err(); err != nil {
		log.Log.Error(err, "GetFirmwareVersionAndPSIDFromFWBinary(): Error reading mstflint output")
		return "", "", err
	}

	if firmwareVersion == "" || PSID == "" {
		return "", "", fmt.Errorf("GetFirmwareVersionAndPSIDFromFWBinary(): firmware version (%v) or PSID (%v) is empty", firmwareVersion, PSID)
	}

	log.Log.V(2).Info("Firmware version and PSID found in .bin file", "version", firmwareVersion, "psid", PSID, "path", firmwareBinaryPath)

	return firmwareVersion, PSID, nil
}

// GetFWVersionsFromBFB retrieves the FW versions from the BFB file
func (u *utils) GetFWVersionsFromBFB(bfbPath string) (map[string]string, error) {
	log.Log.V(2).Info("FirmwareUtils.GetFWVersionsFromBFB()", "bfbPath", bfbPath)
	dir := filepath.Dir(bfbPath)

	// Extract JSON file containing component details and versions
	log.Log.V(2).Info("Extracting info-v0 file from BFB", "bfbPath", bfbPath)
	cmd := u.execInterface.Command("/usr/sbin/mlx-mkbfb", "-x", "-n", "info-v0", bfbPath)
	cmd.SetDir(dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Log.Error(err, "GetFWVersionsFromBFB(): Failed to run mlx-mkbfb", "output", string(output))
		return nil, err
	}

	infoFile := filepath.Join(dir, "dump-info-v0")
	if _, err := os.Stat(infoFile); os.IsNotExist(err) {
		log.Log.Error(err, "GetFWVersionsFromBFB(): failed to extract info-v0 file from BFB", "bfbPath", bfbPath)
	}

	versions := make(map[string]string)

	log.Log.V(2).Info("Extracting versions from info-v0 file", "bfbPath", bfbPath)
	cmd = u.execInterface.Command("/bin/sh", "-c", `awk '/"Name": "BF3_NIC_FW"/ {getline; print $2}' `+infoFile+` | tr -d '",'`)
	bf3NicFwVersion, err := cmd.Output()
	if err != nil {
		log.Log.Error(err, "GetFWVersionsFromBFB(): Failed to extract BF3 NIC FW version")
		return nil, err
	}

	bf3Version := strings.TrimSpace(string(bf3NicFwVersion))
	if bf3Version == "" {
		return nil, fmt.Errorf("GetFWVersionsFromBFB(): BF3 NIC FW version is empty or not found in BFB file")
	}
	versions[consts.BlueField3DeviceID] = bf3Version

	cmd = u.execInterface.Command("/bin/sh", "-c", `awk '/"Name": "BF2_NIC_FW"/ {getline; print $2}' `+infoFile+` | tr -d '",'`)
	bf2NicFwVersion, err := cmd.Output()
	if err != nil {
		log.Log.Error(err, "GetFWVersionsFromBFB(): Failed to extract BF2 NIC FW version")
		return nil, err
	}

	bf2Version := strings.TrimSpace(string(bf2NicFwVersion))
	if bf2Version == "" {
		return nil, fmt.Errorf("GetFWVersionsFromBFB(): BF2 NIC FW version is empty or not found in BFB file")
	}
	versions[consts.BlueField2DeviceID] = bf2Version

	return versions, nil
}

// VerifyImageBootable verifies if the image file is valid and bootable
func (u utils) VerifyImageBootable(firmwareBinaryPath string) error {
	log.Log.V(2).Info("FirmwareUtils.VerifyImageBootable()", "firmwareBinaryPath", firmwareBinaryPath)
	cmd := u.execInterface.Command("mstflint", "-i", firmwareBinaryPath, "v")
	_, err := commonUtils.RunCommand(cmd)
	if err != nil {
		log.Log.Error(err, "VerifyImageBootable(): mstflint check failed")
		return err
	}

	return nil
}

// CleanupDirectory deletes any file inside a root directory except for allowedSet. Empty directories are cleaned up as well at the end
func (u *utils) CleanupDirectory(root string, allowedSet map[string]struct{}) error {
	log.Log.V(2).Info("FirmwareUtils.CleanupDirectory()", "root", root, "allowedSet", allowedSet)
	log.Log.Info("Cleaning up cache directory", "cacheDir", root)

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			return nil
		}

		abs, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("failed to get absolute path of %q: %w", path, err)
		}

		if _, ok := allowedSet[abs]; !ok {
			if err := os.Remove(abs); err != nil {
				return fmt.Errorf("failed to remove %q: %w", abs, err)
			}
			log.Log.V(2).Info("deleted unaccounted file from cache dir", "path", abs, "cacheDir", root)
		}
		return nil
	})

	if err != nil {
		err := fmt.Errorf("failed to walk directory for file removal: %w", err)
		log.Log.Error(err, "failed to cleanup the cache directory", "cacheDir", root)
	}

	// After the unaccounted for files were deleted, clean up empty directories
	if err := u.removeEmptyDirs(root); err != nil {
		err := fmt.Errorf("failed to remove empty directories: %w", err)
		log.Log.Error(err, "failed to cleanup the cache directory", "cacheDir", root)
	}

	return nil
}

// removeEmptyDirs recursively removes directories that are empty.
// It does a post-order traversal: children first, then the parent.
func (u *utils) removeEmptyDirs(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	// Recurse into subdirectories first
	for _, entry := range entries {
		if entry.IsDir() {
			subDir := filepath.Join(dir, entry.Name())
			if err := u.removeEmptyDirs(subDir); err != nil {
				return err
			}
		}
	}

	// After processing children, check if 'dir' is now empty
	// (Re-read directory to see if it has become empty)
	entries, err = os.ReadDir(dir)
	if err != nil {
		return nil // If we can't re-read, just skip
	}
	if len(entries) == 0 && dir != "/" {
		// Avoid removing root if you didn't intend to
		if err := os.Remove(dir); err != nil {
			return fmt.Errorf("failed to remove empty directory %q: %w", dir, err)
		}
		log.Log.V(2).Info("deleted empty directory", "dir", dir)
	}

	return nil
}

// BurnNicFirmware burns the requested firmware on the requested device
// Operation can be long, require context to be able to terminate by timeout
func (u *utils) BurnNicFirmware(ctx context.Context, pciAddress, fwPath string) error {
	log.Log.V(2).Info("FirmwareUtils.BurnNicFirmware()", "pciAddress", pciAddress, "fwPath", fwPath)

	cmd := u.execInterface.CommandContext(ctx, "mstflint", "--device", pciAddress, "--image", fwPath, "--yes", "burn")
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Log.Error(err, "BurnNicFirmware(): Failed to run mstflint", "output", output)
		return err
	}
	return nil
}

func newFirmwareUtils() FirmwareUtils {
	return &utils{execInterface: execUtils.New()}
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer func() {
		cerr := in.Close()
		if cerr != nil {
			err = cerr
		}
	}()

	// Create the destination file for writing.
	out, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	// Ensure the file is closed and capture any error.
	defer func() {
		cerr := out.Close()
		if cerr != nil {
			err = cerr
		}
	}()

	// Copy the file content from in to out.
	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("copying file: %w", err)
	}

	// Optionally sync to flush write buffers.
	if err = out.Sync(); err != nil {
		return fmt.Errorf("syncing file: %w", err)
	}

	return err
}
