package firmware

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
)

const metadataFileName = "metadata.json"

type cacheMetadata map[string][]string

type FirmwareProvisioner interface {
	// IsFWStorageAvailable checks if the cache storage exists in the pod.
	IsFWStorageAvailable() error
	// VerifyCachedBinaries checks against the metadata.json for which urls have corresponding cached fw binary files
	// Returns a list of urls that need to be processed again
	VerifyCachedBinaries(cacheName string, urls []string) ([]string, error)
	// DownloadAndUnzipFirmwareArchives downloads and unzips fw archives from a list of urls
	// Stores a metadata file, mapping download url to file names
	// Returns binaries' filenames
	DownloadAndUnzipFirmwareArchives(cacheName string, urls []string, cleanupArchives bool) error
	// AddFirmwareBinariesToCacheByMetadata finds the newly downloaded firmware binary files and organizes them in the cache according to their metadata
	AddFirmwareBinariesToCacheByMetadata(cacheName string) error
	// ValidateCache traverses the cache directory and validates that
	// 1. There are no empty directories in the cache
	// 2. Each PSID has only one matching firmware binary in the cache
	// 3. Each non-empty PSID directory contains a firmware binary file (.bin)
	// Returns mapping between firmware version to PSIDs available in the cache, error if validation failed
	ValidateCache(cacheName string) (map[string][]string, error)
}

type firmwareProvisioner struct {
	cacheRootDir string

	utils ProvisioningUtils
}

// IsFWStorageAvailable checks if the cache storage exists in the pod.
func (f firmwareProvisioner) IsFWStorageAvailable() error {
	log.Log.V(2).Info("FirmwareProvisioner.IsFWStorageAvailable()")
	_, err := os.Stat(consts.NicFirmwareStorage)
	return err
}

// VerifyCachedBinaries checks against the metadata.json for which urls have corresponding cached fw binary files
// Returns a list of urls that need to be processed again
func (f firmwareProvisioner) VerifyCachedBinaries(cacheName string, urls []string) ([]string, error) {
	cacheDir := path.Join(f.cacheRootDir, cacheName, consts.NicFirmwareBinariesFolder)
	// Nothing to verify if the dir does not exist
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		// If cache doesn't exist, there's nothing to validate, we need to process all urls
		log.Log.V(2).Info("Cache dir doesn't exist, nothing to validate", "cacheDir", cacheDir)
		return urls, nil
	} else if err != nil {
		return nil, err
	}

	log.Log.Info("Verifying if existing cache directory is up to date", "cacheDir", cacheDir)

	metadataFile := path.Join(cacheDir, metadataFileName)
	if _, err := os.Stat(metadataFile); os.IsNotExist(err) {
		log.Log.Info("Cache directory is missing metadata file, removing it", "cacheDir", cacheDir)
		// If cache metadata file doesn't exist, clean up the cache directory because we can't validate the contents
		if err = os.RemoveAll(cacheDir); err != nil {
			return nil, err
		}
		return urls, nil
	}

	metadata, err := readMetadataFromFile(metadataFile)
	if err != nil {
		log.Log.Error(err, "failed to read cache metadata file", "path", metadataFile)
		return nil, err
	}

	var urlsToProcessAgain []string
	filesAccountedFor := map[string]struct{}{}

	for _, url := range urls {
		files, found := metadata[url]
		if !found {
			urlsToProcessAgain = append(urlsToProcessAgain, url)
			log.Log.V(2).Info("Requested url not found in existing cache, processing it again", "cacheDir", cacheDir, "url", url)
			continue
		}

		// If at least one file does not exist for this url, need to download all of them again, thus delete the cached versions first
		deleteFilesForThisUrl := false

		for _, file := range files {
			if _, err := os.Stat(file); os.IsNotExist(err) {
				deleteFilesForThisUrl = true
				urlsToProcessAgain = append(urlsToProcessAgain, url)
				delete(metadata, url)

				log.Log.V(2).Info("Files for requested url are missing, processing it again", "cacheDir", cacheDir, "url", url)

				break
			} else if err != nil {
				return nil, err
			}

			filesAccountedFor[file] = struct{}{}
		}

		if deleteFilesForThisUrl {
			for _, file := range files {
				if _, err := os.Stat(file); err == nil {
					if err = os.Remove(file); err != nil {
						return nil, err
					}
				}
			}
		}
	}

	log.Log.Info("Cache directory is verified, cleaning up all unaccounted for items", "cacheDir", cacheDir)
	// After the cache files are verified, clean up everything else in the directory
	if err = f.utils.CleanupDirectory(cacheDir, filesAccountedFor); err != nil {
		return nil, err
	}

	// Write updated metadata file to disk
	if err := writeMetadataFile(metadata, cacheDir); err != nil {
		return nil, err
	}

	return urlsToProcessAgain, nil
}

// DownloadAndUnzipFirmwareArchives downloads and unzips fw archives from a list of urls
// Stores a metadata file, mapping download url to file names
// Returns binaries' filenames
func (f firmwareProvisioner) DownloadAndUnzipFirmwareArchives(cacheName string, urls []string, cleanupArchives bool) error {
	firmwareBinariesDir := path.Join(f.cacheRootDir, cacheName, consts.NicFirmwareBinariesFolder)

	if _, err := os.Stat(firmwareBinariesDir); os.IsNotExist(err) {
		log.Log.V(2).Info("Cache directory doesn't exist, creating it", "cacheDir", firmwareBinariesDir)

		err := os.MkdirAll(firmwareBinariesDir, 0755)
		if err != nil {
			log.Log.Error(err, "failed to create new cache in nic fw storage", "cacheName", cacheName)
			return err
		}
	}

	log.Log.Info("Downloading firmware zip archives", "cacheDir", firmwareBinariesDir)
	log.Log.V(2).Info("URLs to process", "urls", urls)

	var urlsToFiles cacheMetadata

	metadataFile := path.Join(firmwareBinariesDir, metadataFileName)
	if _, err := os.Stat(metadataFile); os.IsNotExist(err) {
		urlsToFiles = cacheMetadata{}
	} else if err != nil {
		return err
	} else {
		urlsToFiles, err = readMetadataFromFile(metadataFile)
		if err != nil {
			log.Log.Error(err, "failed to read cache metadata file", "path", metadataFile)
			return err
		}

		log.Log.V(2).Info("Successfully read the existing metadata file", "metadata", urlsToFiles)
	}

	for _, url := range urls {
		log.Log.V(2).Info("Downloading firmware archive", "url", url)

		archiveLocalPath := filepath.Join(firmwareBinariesDir, filepath.Base(url))
		err := f.utils.DownloadFile(url, archiveLocalPath)
		if err != nil {
			log.Log.Error(err, "failed to download fw archive", "cacheName", cacheName, "url", url)
			return err
		}

		log.Log.V(2).Info("Unzipping firmware archive", "path", archiveLocalPath)

		files, err := f.utils.UnzipFiles(archiveLocalPath, firmwareBinariesDir)
		if err != nil {
			log.Log.Error(err, "failed to unzip fw archive", "cacheName", cacheName, "url", url)
			return err
		}
		urlsToFiles[url] = files

		log.Log.V(2).Info("Unzipped files", "archive", archiveLocalPath, "files", files)

		if cleanupArchives {
			log.Log.V(2).Info("Cleaning up archive", "path", archiveLocalPath)

			err = os.Remove(archiveLocalPath)
			if err != nil {
				log.Log.Error(err, "failed to remove fw archive file", "cacheName", cacheName, "url", url)
				return err
			}
		}
	}

	if err := writeMetadataFile(urlsToFiles, firmwareBinariesDir); err != nil {
		return err
	}

	return nil
}

func writeMetadataFile(metadata cacheMetadata, cacheDir string) error {
	log.Log.Info("Writing metadata file to disk", "cacheDir", cacheDir, "metadata", metadata)

	jsonData, err := json.Marshal(metadata)
	if err != nil {
		log.Log.Error(err, "failed to process cache metadata", "cacheDir", cacheDir, "metadata", metadata)
		return err
	}

	err = os.WriteFile(path.Join(cacheDir, metadataFileName), jsonData, 0644)
	if err != nil {
		log.Log.Error(err, "failed to save cache metadata", "cacheDir", cacheDir, "metadata", metadata)
	}
	return nil
}

// AddFirmwareBinariesToCacheByMetadata finds the newly downloaded firmware binary files and organizes them in the cache according to their metadata
func (f firmwareProvisioner) AddFirmwareBinariesToCacheByMetadata(cacheName string) error {
	cacheDir := path.Join(f.cacheRootDir, cacheName)
	firmwareBinariesDir := path.Join(cacheDir, "firmware-binaries")
	entries, err := os.ReadDir(firmwareBinariesDir)
	if err != nil {
		log.Log.Error(err, "failed to read firmware binaries cache", "cacheName", cacheName)
		return err
	}

	log.Log.Info("Processing downloaded firmware binaries", "cacheDir", firmwareBinariesDir)

	for _, entry := range entries {
		// We only want to process the firmware binary files
		if !strings.EqualFold(filepath.Ext(entry.Name()), consts.NicFirmwareBinaryFileExtension) {
			continue
		}

		sourcePath := filepath.Join(firmwareBinariesDir, entry.Name())

		version, psid, err := f.utils.GetFirmwareVersionAndPSID(sourcePath)
		if err != nil {
			log.Log.Error(err, "failed to get firmware binary version and PSID", "cacheName", cacheName, "file", entry.Name())
			return err
		}

		log.Log.V(2).Info("Processing firmware binary file", "path", sourcePath, "fw version", version, "psid", psid)

		targetDir := path.Join(firmwareBinariesDir, version, psid)

		if _, err := os.Stat(targetDir); os.IsNotExist(err) {
			err := os.MkdirAll(targetDir, 0755)
			if err != nil {
				log.Log.Error(err, "failed to create directory in nic fw storage", "cacheName", cacheName, "path", targetDir)
				return err
			}
		} else {
			entries, err := os.ReadDir(targetDir)
			if err != nil {
				log.Log.Error(err, "failed to read directory in nic fw storage", "cacheName", cacheName, "path", targetDir)
				return err
			}
			if len(entries) != 0 {
				err = errors.New("target directory for firmware binary file is supposed to be empty, found files")
				log.Log.Error(err, "found existing files in the fw binary file directory", "cacheName", cacheName, "path", targetDir)

				return err
			}
		}

		targetPath := path.Join(targetDir, entry.Name())
		err = os.Rename(sourcePath, targetPath)

		log.Log.V(2).Info("Firmware binary file moved to appropriate directory", "sourcePath", sourcePath, "targetPath", targetPath)

		if err != nil {
			log.Log.Error(err, "failed to place firmware binary file in cache", "cacheName", cacheName, "path", targetPath)
		}
	}

	return nil
}

// ValidateCache traverses the cache directory and validates that
// 1. There are no empty directories in the cache
// 2. Each PSID has only one matching firmware binary in the cache
// 3. Each non-empty PSID directory contains a firmware binary file (.bin)
// Returns mapping between firmware version to PSIDs available in the cache, error if validation failed
func (f firmwareProvisioner) ValidateCache(cacheName string) (map[string][]string, error) {
	cacheDir := path.Join(f.cacheRootDir, cacheName)
	firmwareBinariesDir := path.Join(cacheDir, "firmware-binaries")
	cachedVersions := make(map[string][]string)
	foundPSIDs := make(map[string]struct{})

	log.Log.Info("Validating cache directory after processing", "cacheDir", firmwareBinariesDir)

	firmwareVersions, err := os.ReadDir(firmwareBinariesDir)
	if err != nil {
		log.Log.Error(err, "failed to read directory in nic fw storage", "cacheName", cacheName, "path", firmwareBinariesDir)
		return nil, err
	}

	log.Log.V(2).Info("Available firmware versions", "cacheDir", firmwareBinariesDir, "versions", firmwareVersions)

	for _, firmwareVersion := range firmwareVersions {
		if !firmwareVersion.IsDir() {
			continue
		}

		fwVersion := firmwareVersion.Name()
		fwVersionPath := filepath.Join(firmwareBinariesDir, fwVersion)

		psids, err := os.ReadDir(fwVersionPath)
		if err != nil {
			log.Log.Error(err, "failed to read directory in nic fw storage", "cacheName", cacheName, "path", fwVersionPath)
			return nil, err
		}

		log.Log.V(2).Info("Available PSIDs", "cacheDir", firmwareBinariesDir, "version", firmwareVersion, "psids", psids)

		for _, psid := range psids {
			if !psid.IsDir() {
				continue
			}
			psid := psid.Name()
			psidFolderPath := path.Join(fwVersionPath, psid)
			entries, err := os.ReadDir(psidFolderPath)
			if err != nil {
				log.Log.Error(err, "failed to read directory in nic fw storage", "cacheName", cacheName, "path", psidFolderPath)
				return nil, err
			}

			if len(entries) == 0 {
				err = fmt.Errorf("cache directory is empty. Expected firmware binary file. Cache name: %s, PSID: %s, Firmware version: %s", cacheName, psid, fwVersion)
				log.Log.Error(err, "")
				return nil, err
			}

			binFileFound := false

			for _, entry := range entries {
				if !strings.EqualFold(filepath.Ext(entry.Name()), consts.NicFirmwareBinaryFileExtension) {
					continue
				} else if binFileFound {
					err = fmt.Errorf("multiple firmware binary files in the same directory. Cache name: %s, PSID: %s, Firmware version: %s", cacheName, psid, fwVersion)
					log.Log.Error(err, "")
					return nil, err
				}

				binFileFound = true
				log.Log.V(2).Info("Found .bin file for FW Version / PSID combination", "cacheDir", firmwareBinariesDir, "version", firmwareVersion, "psid", psid, "name", entry.Name())
			}

			if !binFileFound {
				err = fmt.Errorf("no firmware binary files in the PSID directory. Cache name: %s, PSID: %s, Firmware version: %s", cacheName, psid, fwVersion)
				log.Log.Error(err, "")
				return nil, err
			}

			if _, found := foundPSIDs[psid]; found {
				err = fmt.Errorf("multiple firmware binary files for the same PSID. Cache name: %s, PSID: %s, Firmware version: %s", cacheName, psid, fwVersion)
				log.Log.Error(err, "")
				return nil, err
			} else {
				foundPSIDs[psid] = struct{}{}
			}

			cachedVersions[fwVersion] = append(cachedVersions[fwVersion], psid)
		}
	}

	return cachedVersions, nil
}

func readMetadataFromFile(path string) (cacheMetadata, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	metadata := cacheMetadata{}
	err = json.Unmarshal(file, &metadata)
	if err != nil {
		return nil, err
	}

	return metadata, nil
}

func NewFirmwareProvisioner() FirmwareProvisioner {
	return firmwareProvisioner{cacheRootDir: consts.NicFirmwareStorage, utils: newFirmwareUtils()}
}
