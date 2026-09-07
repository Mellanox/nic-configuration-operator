/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES
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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	maxBlueprintsArchiveBytes      = 1 << 20
	maxBlueprintsExpandedBytes     = 32 << 20
	maxBlueprintsDecompressedBytes = 40 << 20
	maxBlueprintsArchiveFiles      = 4096
)

var (
	removeBlueprintsDataDirectory = os.RemoveAll
	removeBlueprintsDataBackup    = os.RemoveAll
)

var requiredBlueprintsDataDirectories = []string{
	"features",
	"profiles",
	"templates",
}

var requiredBlueprintsDataFiles = []string{
	"execution-groups.yaml",
	"formulas.yaml",
	"hca-types.yaml",
	"phase-config.yaml",
	"platform-types.yaml",
}

// InstallBlueprintsData restores a doSPCX data archive under the DMS installation directory.
// The previous valid tree remains active if validation, extraction, or activation fails.
func (m *spectrumXConfigManager) InstallBlueprintsData(archive []byte) error {
	if m == nil {
		return fmt.Errorf("spectrum-x manager must not be nil")
	}
	if strings.TrimSpace(m.dospcxDataRoot) == "" || !filepath.IsAbs(m.dospcxDataRoot) {
		return fmt.Errorf("doSPCX data root must be a non-empty absolute path")
	}
	if len(archive) == 0 {
		return fmt.Errorf("doSPCX data archive must not be empty")
	}
	if len(archive) > maxBlueprintsArchiveBytes {
		return fmt.Errorf("doSPCX data archive exceeds the %d-byte compressed size limit", maxBlueprintsArchiveBytes)
	}

	digest := sha256Digest(archive)
	m.planMutex.Lock()
	defer m.planMutex.Unlock()
	if digest == m.dospcxDataDigest {
		if err := validateBlueprintsDataLayout(m.dospcxDataRoot); err == nil {
			return nil
		}
	}
	if err := installBlueprintsDataArchive(archive, m.dospcxDataRoot); err != nil {
		return err
	}
	m.dospcxDataDigest = digest
	return nil
}

// RemoveBlueprintsData removes data previously installed from a ConfigMap. Data supplied by the
// daemon image is left intact when this manager has not installed a bundle during its lifetime.
func (m *spectrumXConfigManager) RemoveBlueprintsData() error {
	if m == nil {
		return fmt.Errorf("spectrum-x manager must not be nil")
	}
	if strings.TrimSpace(m.dospcxDataRoot) == "" || !filepath.IsAbs(m.dospcxDataRoot) {
		return fmt.Errorf("doSPCX data root must be a non-empty absolute path")
	}

	m.planMutex.Lock()
	defer m.planMutex.Unlock()
	if m.dospcxDataDigest == "" {
		return nil
	}
	if err := removeBlueprintsDataDirectory(m.dospcxDataRoot); err != nil {
		return fmt.Errorf("remove doSPCX data directory %q: %w", m.dospcxDataRoot, err)
	}
	m.dospcxDataDigest = ""
	return nil
}

func installBlueprintsDataArchive(archive []byte, destination string) error {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create doSPCX data parent directory %q: %w", parent, err)
	}

	stagingDirectory, err := os.MkdirTemp(parent, ".nco-dospcx-data-")
	if err != nil {
		return fmt.Errorf("create doSPCX data staging directory in %q: %w", parent, err)
	}
	defer func() { _ = os.RemoveAll(stagingDirectory) }()

	if err := extractBlueprintsDataArchive(archive, stagingDirectory); err != nil {
		return fmt.Errorf("extract doSPCX data archive: %w", err)
	}
	stagedDataRoot := filepath.Join(stagingDirectory, "data")
	if err := validateBlueprintsDataLayout(stagedDataRoot); err != nil {
		return err
	}
	if err := replaceBlueprintsDataDirectory(stagedDataRoot, destination); err != nil {
		return fmt.Errorf("activate doSPCX data directory %q: %w", destination, err)
	}
	return nil
}

func extractBlueprintsDataArchive(archive []byte, destination string) error {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer func() { _ = gzipReader.Close() }()

	decompressed := &io.LimitedReader{R: gzipReader, N: maxBlueprintsDecompressedBytes + 1}
	tarReader := tar.NewReader(decompressed)
	destinationRoot, err := os.OpenRoot(destination)
	if err != nil {
		return fmt.Errorf("open extraction root %q: %w", destination, err)
	}
	defer func() { _ = destinationRoot.Close() }()

	seen := map[string]struct{}{}
	directoryModes := map[string]os.FileMode{}
	var expandedBytes int64
	entries := 0
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("read tar entry: %w", nextErr)
		}

		entries++
		if entries > maxBlueprintsArchiveFiles {
			return fmt.Errorf("archive exceeds the %d-entry limit", maxBlueprintsArchiveFiles)
		}
		archivePath, pathErr := cleanBlueprintsArchivePath(header.Name)
		if pathErr != nil {
			return pathErr
		}
		// Keep this guard adjacent to the rooted file operations. cleanBlueprintsArchivePath
		// already rejects traversal, and os.Root independently prevents escaping destination.
		if strings.Contains(archivePath, "..") {
			return fmt.Errorf("archive contains unsafe path %q", header.Name)
		}
		if _, exists := seen[archivePath]; exists {
			return fmt.Errorf("archive contains duplicate path %q", header.Name)
		}
		seen[archivePath] = struct{}{}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := destinationRoot.MkdirAll(archivePath, 0o755); err != nil {
				return fmt.Errorf("create directory %q: %w", archivePath, err)
			}
			directoryModes[archivePath] = os.FileMode(header.Mode).Perm()
		case tar.TypeReg:
			if header.Size < 0 || header.Size > maxBlueprintsExpandedBytes-expandedBytes {
				return fmt.Errorf("archive exceeds the %d-byte expanded size limit", maxBlueprintsExpandedBytes)
			}
			expandedBytes += header.Size
			if err := destinationRoot.MkdirAll(path.Dir(archivePath), 0o755); err != nil {
				return fmt.Errorf("create parent directory for %q: %w", archivePath, err)
			}
			file, openErr := destinationRoot.OpenFile(
				archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode).Perm())
			if openErr != nil {
				return fmt.Errorf("create file %q: %w", archivePath, openErr)
			}
			_, copyErr := io.CopyN(file, tarReader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("write file %q: %w", archivePath, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close file %q: %w", archivePath, closeErr)
			}
		default:
			return fmt.Errorf("archive path %q uses unsupported tar entry type %d", header.Name, header.Typeflag)
		}
	}
	if _, err := io.Copy(io.Discard, decompressed); err != nil {
		return fmt.Errorf("validate gzip stream: %w", err)
	}
	if decompressed.N <= 0 {
		return fmt.Errorf("archive exceeds the %d-byte decompressed size limit", maxBlueprintsDecompressedBytes)
	}

	directories := make([]string, 0, len(directoryModes))
	for directory := range directoryModes {
		directories = append(directories, directory)
	}
	sort.Slice(directories, func(i, j int) bool {
		return strings.Count(directories[i], "/") > strings.Count(directories[j], "/")
	})
	for _, directory := range directories {
		if err := destinationRoot.Chmod(directory, directoryModes[directory]); err != nil {
			return fmt.Errorf("set directory permissions for %q: %w", directory, err)
		}
	}
	return nil
}

func cleanBlueprintsArchivePath(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.Contains(name, `\`) {
		return "", fmt.Errorf("archive contains invalid path %q", name)
	}
	trimmed := strings.TrimSuffix(name, "/")
	cleaned := path.Clean(trimmed)
	if path.IsAbs(cleaned) || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("archive contains unsafe path %q", name)
	}
	if cleaned != trimmed {
		return "", fmt.Errorf("archive contains non-canonical path %q", name)
	}
	if cleaned != "data" && !strings.HasPrefix(cleaned, "data/") {
		return "", fmt.Errorf("archive path %q is outside the required data directory", name)
	}
	return cleaned, nil
}

func validateBlueprintsDataLayout(dataRoot string) error {
	info, err := os.Stat(dataRoot)
	if err != nil {
		return fmt.Errorf("doSPCX archive is missing its data root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("doSPCX archive data root is not a directory")
	}
	for _, requiredPath := range requiredBlueprintsDataDirectories {
		info, err := os.Stat(filepath.Join(dataRoot, requiredPath))
		if err != nil {
			return fmt.Errorf("doSPCX archive is missing required path %q: %w", requiredPath, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("doSPCX archive required path %q is not a directory", requiredPath)
		}
	}
	for _, requiredPath := range requiredBlueprintsDataFiles {
		info, err := os.Stat(filepath.Join(dataRoot, requiredPath))
		if err != nil {
			return fmt.Errorf("doSPCX archive is missing required path %q: %w", requiredPath, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("doSPCX archive required path %q is not a regular file", requiredPath)
		}
	}
	return nil
}

func replaceBlueprintsDataDirectory(stagedDataRoot, destination string) error {
	parent := filepath.Dir(destination)
	backupPath := ""
	if _, err := os.Lstat(destination); err == nil {
		backupDirectory, tempErr := os.MkdirTemp(parent, ".nco-dospcx-data-backup-")
		if tempErr != nil {
			return fmt.Errorf("reserve backup path: %w", tempErr)
		}
		if removeErr := os.Remove(backupDirectory); removeErr != nil {
			return fmt.Errorf("prepare backup path %q: %w", backupDirectory, removeErr)
		}
		backupPath = backupDirectory
		if renameErr := os.Rename(destination, backupPath); renameErr != nil {
			return fmt.Errorf("move current data directory to %q: %w", backupPath, renameErr)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect current data directory: %w", err)
	}

	if err := os.Rename(stagedDataRoot, destination); err != nil {
		if backupPath != "" {
			if rollbackErr := os.Rename(backupPath, destination); rollbackErr != nil {
				return fmt.Errorf("activate staged data: %w; restore previous data: %v", err, rollbackErr)
			}
		}
		return fmt.Errorf("activate staged data: %w", err)
	}
	if backupPath != "" {
		if err := removeBlueprintsDataBackup(backupPath); err != nil {
			// Activation already succeeded. Treat backup removal as best-effort so callers update
			// their digest to match the active tree instead of reporting a failed installation
			// while leaving the new data active.
			log.Log.Error(err, "failed to remove previous doSPCX data directory", "path", backupPath)
		}
	}
	return nil
}
