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
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/firmware/mocks"
)

var _ = Describe("FirmwareProvisioner", func() {
	var (
		fwUtilsMock *mocks.ProvisioningUtils
		fwProv      FirmwareProvisioner
		tmpDir      string
		cacheName   string
		cacheDir    string
	)

	BeforeEach(func() {
		fwUtilsMock = &mocks.ProvisioningUtils{}

		var err error
		tmpDir, err = os.MkdirTemp("/tmp", "fwprovisioningtest-*")
		Expect(err).NotTo(HaveOccurred())

		cacheName = "test-cache"
		cacheDir = path.Join(tmpDir, cacheName, consts.NicFirmwareBinariesFolder)

		fwProv = firmwareProvisioner{cacheRootDir: tmpDir, utils: fwUtilsMock}
	})

	AfterEach(func() {
		fwUtilsMock.AssertExpectations(GinkgoT())

		_ = os.RemoveAll(tmpDir)
	})

	Describe("VerifyCachedBinaries", func() {
		var (
			urls []string
		)

		BeforeEach(func() {
			urls = []string{"http://example.com/fwA.zip", "http://example.com/fwB.zip"}
		})

		Context("when the cache directory does not exist", func() {
			It("should return all provided URLs for re-processing", func() {
				// We won't create the directory, so it doesn't exist
				reprocess, err := fwProv.VerifyCachedBinaries(cacheName, urls)
				Expect(err).NotTo(HaveOccurred())
				Expect(reprocess).To(Equal(urls))
			})
		})

		Context("when the metadata file is missing", func() {
			It("should remove the entire cache directory and return all URLs", func() {
				// Create the directory but not metadata.json
				Expect(os.MkdirAll(cacheDir, 0755)).To(Succeed())

				reprocess, err := fwProv.VerifyCachedBinaries(cacheName, urls)
				Expect(err).NotTo(HaveOccurred())
				Expect(reprocess).To(Equal(urls))

				_, statErr := os.Stat(cacheDir)
				Expect(os.IsNotExist(statErr)).To(BeTrue())
			})
		})

		Context("with an existing metadata file", func() {
			It("should return missing URLs if some cached files are absent", func() {
				Expect(os.MkdirAll(cacheDir, 0755)).To(Succeed())

				metaFile := filepath.Join(cacheDir, "metadata.json")
				metaData := `{
  "http://example.com/fwA.zip": ["` + filepath.Join(cacheDir, "fwA.bin") + `"],
  "http://example.com/fwB.zip": ["` + filepath.Join(cacheDir, "fwB.bin") + `"]
}`
				Expect(os.WriteFile(metaFile, []byte(metaData), 0644)).To(Succeed())

				// Create only fwA.bin
				Expect(os.WriteFile(filepath.Join(cacheDir, "fwA.bin"), []byte("dummy content"), 0644)).To(Succeed())

				fwUtilsMock.On("CleanupDirectory", mock.AnythingOfType("string"), mock.AnythingOfType("map[string]struct {}")).
					Return(nil).Once()

				reprocess, err := fwProv.VerifyCachedBinaries(cacheName, urls)
				Expect(err).NotTo(HaveOccurred())

				// fwB.bin is missing, so fwB.zip is reprocessed
				Expect(reprocess).To(ConsistOf("http://example.com/fwB.zip"))
			})

			It("should call clean up for unaccounted files", func() {
				Expect(os.MkdirAll(cacheDir, 0755)).To(Succeed())

				cachedFilePath := filepath.Join(cacheDir, "fwB.bin")

				metaFile := filepath.Join(cacheDir, "metadata.json")
				metaData := `{
  "http://example.com/fwB.zip": ["` + cachedFilePath + `"]}`
				Expect(os.WriteFile(metaFile, []byte(metaData), 0644)).To(Succeed())
				Expect(os.WriteFile(cachedFilePath, []byte("dummy content"), 0644)).To(Succeed())

				fwUtilsMock.On("CleanupDirectory", mock.AnythingOfType("string"), map[string]struct{}{cachedFilePath: {}}).
					Return(nil).Once()

				reprocess, err := fwProv.VerifyCachedBinaries(cacheName, urls)
				Expect(err).NotTo(HaveOccurred())

				Expect(reprocess).To(ConsistOf("http://example.com/fwA.zip"))
			})
		})
	})

	Describe("DownloadAndUnzipFirmwareArchives", func() {
		var (
			downloadUrls    []string
			cleanupArchives bool
			metadataPath    string
		)

		BeforeEach(func() {
			downloadUrls = []string{"http://example.com/fwA.zip"}
			cleanupArchives = false
			metadataPath = filepath.Join(cacheDir, "metadata.json")
		})

		readMetadata := func() map[string][]string {
			data, err := os.ReadFile(metadataPath)
			Expect(err).NotTo(HaveOccurred())

			meta := map[string][]string{}
			Expect(json.Unmarshal(data, &meta)).To(Succeed())
			return meta
		}

		It("should create the cache directory if missing, then download/unzip each archive", func() {
			fwUtilsMock.On("DownloadFile", downloadUrls[0], mock.AnythingOfType("string")).
				Return(nil).Once()
			extractedFile := filepath.Join(cacheDir, "fwA.bin")
			fwUtilsMock.
				On("UnzipFiles", mock.AnythingOfType("string"), cacheDir).
				Return([]string{extractedFile}, nil).Once()

			Expect(
				fwProv.DownloadAndUnzipFirmwareArchives(cacheName, downloadUrls, cleanupArchives),
			).To(Succeed())

			info, statErr := os.Stat(cacheDir)
			Expect(statErr).NotTo(HaveOccurred())
			Expect(info.IsDir()).To(BeTrue())

			Expect(metadataPath).To(BeARegularFile())

			metadata := readMetadata()

			Expect(metadata).To(HaveKey(downloadUrls[0]))
			Expect(metadata[downloadUrls[0]]).To(ConsistOf(extractedFile))
		})

		Context("when a download fails", func() {
			It("should return an error immediately", func() {
				fwUtilsMock.On("DownloadFile", downloadUrls[0], mock.AnythingOfType("string")).
					Return(errors.New("download error")).Once()

				err := fwProv.DownloadAndUnzipFirmwareArchives(cacheName, downloadUrls, cleanupArchives)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("download error"))
			})
		})

		Context("when unzip fails", func() {
			It("should return an error immediately", func() {
				fwUtilsMock.On("DownloadFile", downloadUrls[0], mock.AnythingOfType("string")).
					Return(nil).Once()

				fwUtilsMock.On("UnzipFiles", mock.AnythingOfType("string"), mock.AnythingOfType("string")).
					Return(nil, errors.New("unzip error")).Once()

				err := fwProv.DownloadAndUnzipFirmwareArchives(cacheName, downloadUrls, cleanupArchives)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("unzip error"))
			})
		})

		Context("when cleanupArchives is true", func() {
			It("should remove the downloaded archive after unzipping", func() {
				cleanupArchives = true
				fwUtilsMock.On("DownloadFile", downloadUrls[0], mock.AnythingOfType("string")).
					Return(nil).Once()
				fwUtilsMock.On("UnzipFiles", mock.AnythingOfType("string"), mock.AnythingOfType("string")).
					Return([]string{"fwA.bin"}, nil).Once()

				Expect(os.MkdirAll(cacheDir, 0755)).To(Succeed())
				fileToBeDeleted := path.Join(cacheDir, "fwA.zip")
				Expect(os.WriteFile(fileToBeDeleted, []byte("dummy content"), 0644)).To(Succeed())

				err := fwProv.DownloadAndUnzipFirmwareArchives(cacheName, downloadUrls, cleanupArchives)
				Expect(err).NotTo(HaveOccurred())
				_, err = os.Stat(fileToBeDeleted)
				Expect(err).To(MatchError(os.ErrNotExist))
			})
		})
	})

	Describe("AddFirmwareBinariesToCacheByMetadata", func() {
		BeforeEach(func() {
			Expect(os.MkdirAll(cacheDir, 0755)).To(Succeed())
		})

		It("should move each .bin file to the correct version/PSID subdirectory", func() {
			binFileA := path.Join(cacheDir, "fwA.bin")
			Expect(os.WriteFile(binFileA, []byte("dummy content"), 0644)).To(Succeed())

			Expect(os.MkdirAll(cacheDir, 0755)).To(Succeed())
			binFileB := path.Join(cacheDir, "fwB.bin")
			Expect(os.WriteFile(binFileB, []byte("dummy content"), 0644)).To(Succeed())

			fwUtilsMock.On("GetFirmwareVersionAndPSID", binFileA).
				Return("1.2.3", "PSID123", nil).Once()
			fwUtilsMock.On("GetFirmwareVersionAndPSID", binFileB).
				Return("3.2.1", "PSID321", nil).Once()

			err := fwProv.AddFirmwareBinariesToCacheByMetadata(cacheName)
			Expect(err).NotTo(HaveOccurred())

			_, err = os.Stat(binFileA)
			Expect(err).To(MatchError(os.ErrNotExist))

			_, err = os.Stat(binFileB)
			Expect(err).To(MatchError(os.ErrNotExist))

			_, err = os.Stat(path.Join(cacheDir, "1.2.3", "PSID123", "fwA.bin"))
			Expect(err).NotTo(HaveOccurred())
			_, err = os.Stat(path.Join(cacheDir, "3.2.1", "PSID321", "fwB.bin"))
			Expect(err).NotTo(HaveOccurred())
		})

		Context("when GetFirmwareVersionAndPSID returns an error", func() {
			It("should fail immediately", func() {
				binFileA := path.Join(cacheDir, "fwA.bin")
				Expect(os.WriteFile(binFileA, []byte("dummy content"), 0644)).To(Succeed())

				fwUtilsMock.On("GetFirmwareVersionAndPSID", mock.AnythingOfType("string")).
					Return("", "", errors.New("parse error")).Once()

				err := fwProv.AddFirmwareBinariesToCacheByMetadata(cacheName)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("parse error"))
			})
		})
	})

	Describe("ValidateCache", func() {
		BeforeEach(func() {
			Expect(os.MkdirAll(cacheDir, 0755)).To(Succeed())
		})

		It("should return a map of firmware versions to PSIDs if everything is valid", func() {
			Expect(os.MkdirAll(path.Join(cacheDir, "1.2.3", "PSID123"), 0755)).To(Succeed())
			Expect(os.MkdirAll(path.Join(cacheDir, "1.2.3", "PSID321"), 0755)).To(Succeed())
			binFileA := path.Join(cacheDir, "1.2.3", "PSID123", "fwA.bin")
			Expect(os.WriteFile(binFileA, []byte("dummy content"), 0644)).To(Succeed())
			binFileB := path.Join(cacheDir, "1.2.3", "PSID321", "fwB.bin")
			Expect(os.WriteFile(binFileB, []byte("dummy content"), 0644)).To(Succeed())

			versions, err := fwProv.ValidateCache(cacheName)
			Expect(err).NotTo(HaveOccurred())
			Expect(versions).To(Equal(map[string][]string{"1.2.3": {"PSID123", "PSID321"}}))
		})

		Context("when a PSID directory is empty", func() {
			It("should return an error", func() {
				Expect(os.MkdirAll(path.Join(cacheDir, "1.2.3", "PSID123"), 0755)).To(Succeed())

				_, err := fwProv.ValidateCache(cacheName)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("cache directory is empty"))
			})
		})

		Context("when multiple versions share the same PSID", func() {
			It("should return an error about multiple binaries for the same PSID", func() {
				psidDir1 := path.Join(cacheDir, "1.2.3", "PSID123")
				psidDir2 := path.Join(cacheDir, "3.2.1", "PSID123")
				Expect(os.MkdirAll(psidDir1, 0755)).To(Succeed())
				Expect(os.MkdirAll(psidDir2, 0755)).To(Succeed())
				Expect(os.WriteFile(path.Join(psidDir1, "fileA.bin"), []byte("dummy content"), 0644)).To(Succeed())
				Expect(os.WriteFile(path.Join(psidDir2, "fileB.bin"), []byte("dummy content"), 0644)).To(Succeed())

				_, err := fwProv.ValidateCache(cacheName)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("multiple firmware binary files for the same PSID"))
			})
		})

		Context("when multiple bin files are in the same PSID dir", func() {
			It("should return an error about multiple binaries in the directory", func() {
				psidDir := path.Join(cacheDir, "1.2.3", "PSID123")
				Expect(os.MkdirAll(psidDir, 0755)).To(Succeed())
				Expect(os.WriteFile(path.Join(psidDir, "fileA.bin"), []byte("dummy content"), 0644)).To(Succeed())
				Expect(os.WriteFile(path.Join(psidDir, "fileB.bin"), []byte("dummy content"), 0644)).To(Succeed())

				_, err := fwProv.ValidateCache(cacheName)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("multiple firmware binary files in the same directory"))
			})
		})

		Context("when no files are in the PSID dir", func() {
			It("should return an error about no files in the directory", func() {
				psidDir := path.Join(cacheDir, "1.2.3", "PSID123")
				Expect(os.MkdirAll(psidDir, 0755)).To(Succeed())

				_, err := fwProv.ValidateCache(cacheName)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("cache directory is empty. Expected firmware binary file."))
			})
		})

		Context("when no binary files are in the PSID dir", func() {
			It("should return an error about no binaries in the directory", func() {
				psidDir := path.Join(cacheDir, "1.2.3", "PSID123")
				Expect(os.MkdirAll(psidDir, 0755)).To(Succeed())
				Expect(os.WriteFile(path.Join(psidDir, "fileA.nobin"), []byte("dummy content"), 0644)).To(Succeed())

				_, err := fwProv.ValidateCache(cacheName)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no firmware binary files in the PSID directory"))
			})
		})
	})
})
