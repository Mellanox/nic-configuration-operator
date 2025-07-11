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
	const testBFBFileName = "firmware.bfb"

	var (
		fwUtilsMock *mocks.FirmwareUtils
		fwProv      FirmwareProvisioner
		tmpDir      string
		cacheName   string
		cacheDir    string
	)

	BeforeEach(func() {
		fwUtilsMock = &mocks.FirmwareUtils{}

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
  "http://example.com/fwA.zip": ["fwA.bin"],
  "http://example.com/fwB.zip": ["fwB.bin"]
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

				cachedFileName := "fwB.bin"
				cachedFilePath := filepath.Join(cacheDir, cachedFileName)

				metaFile := filepath.Join(cacheDir, "metadata.json")
				metaData := `{
  "http://example.com/fwB.zip": ["` + cachedFileName + `"]}`
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
			extractedFileName := "fwA.bin"
			extractedFile := filepath.Join(cacheDir, extractedFileName)
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
			Expect(metadata[downloadUrls[0]]).To(ConsistOf(extractedFileName))
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

		Context("when archive has no binary files", func() {
			It("should return an error", func() {
				cleanupArchives = true
				fwUtilsMock.On("DownloadFile", downloadUrls[0], mock.AnythingOfType("string")).
					Return(nil).Once()
				fwUtilsMock.On("UnzipFiles", mock.AnythingOfType("string"), mock.AnythingOfType("string")).
					Return([]string{"randomFile"}, nil).Once()

				Expect(os.MkdirAll(cacheDir, 0755)).To(Succeed())
				fileToBeDeleted := path.Join(cacheDir, "fwA.zip")
				Expect(os.WriteFile(fileToBeDeleted, []byte("dummy content"), 0644)).To(Succeed())

				err := fwProv.DownloadAndUnzipFirmwareArchives(cacheName, downloadUrls, cleanupArchives)
				Expect(err.Error()).To(ContainSubstring("requested FW zip archive http://example.com/fwA.zip doesn't contain FW binary files"))
				_, err = os.Stat(fileToBeDeleted)
				Expect(err).ToNot(HaveOccurred())
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

	Describe("BFB Functionality", func() {
		var (
			bfbCacheDir string
			bfbUrl      string
		)

		BeforeEach(func() {
			bfbCacheDir = path.Join(tmpDir, cacheName, consts.BFBFolder)
			bfbUrl = "http://example.com/firmware.bfb"
		})

		Describe("VerifyCachedBFB", func() {
			Context("when the BFB cache directory does not exist", func() {
				It("should return true (needs download)", func() {
					needsDownload, err := fwProv.VerifyCachedBFB(cacheName, bfbUrl)
					Expect(err).NotTo(HaveOccurred())
					Expect(needsDownload).To(BeTrue())
				})
			})

			Context("when the metadata file is missing", func() {
				It("should clean up the directory and return true", func() {
					// Create the directory but not metadata.json
					Expect(os.MkdirAll(bfbCacheDir, 0755)).To(Succeed())

					// Create a dummy file to verify cleanup
					dummyFile := path.Join(bfbCacheDir, "dummy.bfb")
					Expect(os.WriteFile(dummyFile, []byte("dummy"), 0644)).To(Succeed())

					needsDownload, err := fwProv.VerifyCachedBFB(cacheName, bfbUrl)
					Expect(err).NotTo(HaveOccurred())
					Expect(needsDownload).To(BeTrue())

					// Directory should be cleaned up
					_, statErr := os.Stat(bfbCacheDir)
					Expect(os.IsNotExist(statErr)).To(BeTrue())
				})
			})

			Context("when metadata file exists but is corrupted", func() {
				It("should return an error", func() {
					Expect(os.MkdirAll(bfbCacheDir, 0755)).To(Succeed())

					// Write invalid JSON
					metadataFile := path.Join(bfbCacheDir, metadataFileName)
					Expect(os.WriteFile(metadataFile, []byte("invalid json"), 0644)).To(Succeed())

					_, err := fwProv.VerifyCachedBFB(cacheName, bfbUrl)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("invalid character"))
				})
			})

			Context("when URL is not found in metadata", func() {
				It("should return true (needs download)", func() {
					Expect(os.MkdirAll(bfbCacheDir, 0755)).To(Succeed())

					// Create metadata with different URL
					metadataFile := path.Join(bfbCacheDir, metadataFileName)
					metadata := `{"http://other.com/firmware.bfb": "other.bfb"}`
					Expect(os.WriteFile(metadataFile, []byte(metadata), 0644)).To(Succeed())

					needsDownload, err := fwProv.VerifyCachedBFB(cacheName, bfbUrl)
					Expect(err).NotTo(HaveOccurred())
					Expect(needsDownload).To(BeTrue())
				})
			})

			Context("when URL is found but BFB file is missing from disk", func() {
				It("should return true (needs download)", func() {
					Expect(os.MkdirAll(bfbCacheDir, 0755)).To(Succeed())

					// Create metadata with our URL but no actual file
					metadataFile := path.Join(bfbCacheDir, metadataFileName)
					metadata := `{"` + bfbUrl + `": "firmware.bfb"}`
					Expect(os.WriteFile(metadataFile, []byte(metadata), 0644)).To(Succeed())

					needsDownload, err := fwProv.VerifyCachedBFB(cacheName, bfbUrl)
					Expect(err).NotTo(HaveOccurred())
					Expect(needsDownload).To(BeTrue())
				})
			})

			Context("when URL is found and BFB file exists", func() {
				It("should return false (skip download)", func() {
					Expect(os.MkdirAll(bfbCacheDir, 0755)).To(Succeed())

					// Create metadata and actual file
					bfbFileName := "firmware.bfb"
					metadataFile := path.Join(bfbCacheDir, metadataFileName)
					metadata := `{"` + bfbUrl + `": "` + bfbFileName + `"}`
					Expect(os.WriteFile(metadataFile, []byte(metadata), 0644)).To(Succeed())

					bfbFile := path.Join(bfbCacheDir, bfbFileName)
					Expect(os.WriteFile(bfbFile, []byte("bfb content"), 0644)).To(Succeed())

					needsDownload, err := fwProv.VerifyCachedBFB(cacheName, bfbUrl)
					Expect(err).NotTo(HaveOccurred())
					Expect(needsDownload).To(BeFalse())
				})
			})
		})

		Describe("DownloadBFB", func() {
			It("should clean up directory, download file, validate extension, and update metadata", func() {
				// Create some existing files to verify cleanup
				Expect(os.MkdirAll(bfbCacheDir, 0755)).To(Succeed())
				oldFile := path.Join(bfbCacheDir, "old.bfb")
				Expect(os.WriteFile(oldFile, []byte("old content"), 0644)).To(Succeed())

				// Mock successful download
				expectedLocalPath := path.Join(bfbCacheDir, testBFBFileName)
				fwUtilsMock.On("DownloadFile", bfbUrl, expectedLocalPath).Return(nil).Once()

				filename, err := fwProv.DownloadBFB(cacheName, bfbUrl)
				Expect(err).NotTo(HaveOccurred())
				Expect(filename).To(Equal(testBFBFileName))

				// Verify old file was cleaned up
				_, err = os.Stat(oldFile)
				Expect(os.IsNotExist(err)).To(BeTrue())

				// Verify metadata was created
				metadataFile := path.Join(bfbCacheDir, metadataFileName)
				Expect(metadataFile).To(BeARegularFile())

				// Read and verify metadata content
				data, err := os.ReadFile(metadataFile)
				Expect(err).NotTo(HaveOccurred())

				var metadata bfbMetadata
				Expect(json.Unmarshal(data, &metadata)).To(Succeed())
				Expect(metadata).To(HaveKeyWithValue(bfbUrl, testBFBFileName))
			})

			It("should replace existing BFB file when downloading new one", func() {
				// Create existing metadata and file
				Expect(os.MkdirAll(bfbCacheDir, 0755)).To(Succeed())
				metadataFile := path.Join(bfbCacheDir, metadataFileName)
				existingMetadata := `{"http://other.com/old.bfb": "old.bfb"}`
				Expect(os.WriteFile(metadataFile, []byte(existingMetadata), 0644)).To(Succeed())

				// Create the old BFB file
				oldBfbFile := path.Join(bfbCacheDir, "old.bfb")
				Expect(os.WriteFile(oldBfbFile, []byte("old bfb content"), 0644)).To(Succeed())

				// Mock successful download of new BFB
				expectedLocalPath := path.Join(bfbCacheDir, testBFBFileName)
				fwUtilsMock.On("DownloadFile", bfbUrl, expectedLocalPath).Return(nil).Once()

				filename, err := fwProv.DownloadBFB(cacheName, bfbUrl)
				Expect(err).NotTo(HaveOccurred())
				Expect(filename).To(Equal(testBFBFileName))

				// Verify old BFB file was cleaned up (directory cleanup should remove it)
				_, err = os.Stat(oldBfbFile)
				Expect(os.IsNotExist(err)).To(BeTrue())

				// Read and verify metadata contains only the new BFB file
				data, err := os.ReadFile(metadataFile)
				Expect(err).NotTo(HaveOccurred())

				var metadata bfbMetadata
				Expect(json.Unmarshal(data, &metadata)).To(Succeed())
				Expect(metadata).To(HaveLen(1))
				Expect(metadata).To(HaveKeyWithValue(bfbUrl, testBFBFileName))
				Expect(metadata).NotTo(HaveKey("http://other.com/old.bfb"))
			})

			Context("when download fails", func() {
				It("should return an error", func() {
					expectedLocalPath := path.Join(bfbCacheDir, "firmware.bfb")
					fwUtilsMock.On("DownloadFile", bfbUrl, expectedLocalPath).
						Return(errors.New("network error")).Once()

					_, err := fwProv.DownloadBFB(cacheName, bfbUrl)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("network error"))
				})
			})

			Context("when downloaded file has wrong extension", func() {
				It("should return an error", func() {
					wrongUrl := "http://example.com/firmware.bin" // Wrong extension
					expectedLocalPath := path.Join(bfbCacheDir, "firmware.bin")
					fwUtilsMock.On("DownloadFile", wrongUrl, expectedLocalPath).Return(nil).Once()

					_, err := fwProv.DownloadBFB(cacheName, wrongUrl)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("downloaded file does not have BFB extension"))
				})
			})

		})

		Describe("BFB Metadata", func() {
			var metadataFile string

			BeforeEach(func() {
				Expect(os.MkdirAll(bfbCacheDir, 0755)).To(Succeed())
				metadataFile = path.Join(bfbCacheDir, metadataFileName)
			})

			Describe("readBFBMetadataFromFile", func() {
				It("should read valid BFB metadata", func() {
					metadata := `{"http://example.com/firmware.bfb": "firmware.bfb"}`
					Expect(os.WriteFile(metadataFile, []byte(metadata), 0644)).To(Succeed())

					result, err := readBFBMetadataFromFile(metadataFile)
					Expect(err).NotTo(HaveOccurred())
					Expect(result).To(HaveLen(1))
					Expect(result).To(HaveKeyWithValue("http://example.com/firmware.bfb", "firmware.bfb"))
				})

				It("should return error when file does not exist", func() {
					_, err := readBFBMetadataFromFile("/nonexistent/file.json")
					Expect(err).To(HaveOccurred())
					Expect(os.IsNotExist(err)).To(BeTrue())
				})

				It("should return error when JSON is corrupted", func() {
					Expect(os.WriteFile(metadataFile, []byte("invalid json"), 0644)).To(Succeed())

					_, err := readBFBMetadataFromFile(metadataFile)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("invalid character"))
				})
			})

			Describe("writeMetadataFile with BFB metadata", func() {
				It("should write single BFB metadata correctly", func() {
					metadata := bfbMetadata{
						"http://example.com/firmware.bfb": "firmware.bfb",
					}

					err := writeMetadataFile(metadata, bfbCacheDir)
					Expect(err).NotTo(HaveOccurred())
					Expect(metadataFile).To(BeARegularFile())

					// Read back and verify
					data, err := os.ReadFile(metadataFile)
					Expect(err).NotTo(HaveOccurred())

					var readBack bfbMetadata
					Expect(json.Unmarshal(data, &readBack)).To(Succeed())
					Expect(readBack).To(Equal(metadata))
					Expect(readBack).To(HaveLen(1))
				})
			})

			Describe("writeMetadataFile with binary metadata (regression test)", func() {
				It("should still work with fwBinaryMetadata", func() {
					metadata := fwBinaryMetadata{
						"http://example.com/fw1.zip": []string{"fw1.bin", "fw2.bin"},
						"http://example.com/fw2.zip": []string{"fw3.bin"},
					}

					err := writeMetadataFile(metadata, bfbCacheDir)
					Expect(err).NotTo(HaveOccurred())
					Expect(metadataFile).To(BeARegularFile())

					// Read back and verify
					data, err := os.ReadFile(metadataFile)
					Expect(err).NotTo(HaveOccurred())

					var readBack fwBinaryMetadata
					Expect(json.Unmarshal(data, &readBack)).To(Succeed())
					Expect(readBack).To(Equal(metadata))
				})
			})
		})

		Describe("ValidateBFB", func() {
			var metadataFile string

			BeforeEach(func() {
				Expect(os.MkdirAll(bfbCacheDir, 0755)).To(Succeed())
				metadataFile = path.Join(bfbCacheDir, metadataFileName)
			})

			It("should return BF firmware versions when BFB is valid", func() {
				// Create metadata file
				bfbFileName := testBFBFileName
				metadata := `{"` + bfbUrl + `": "` + bfbFileName + `"}`
				Expect(os.WriteFile(metadataFile, []byte(metadata), 0644)).To(Succeed())

				// Create the BFB file
				bfbFilePath := path.Join(bfbCacheDir, bfbFileName)
				Expect(os.WriteFile(bfbFilePath, []byte("dummy bfb content"), 0644)).To(Succeed())

				// Mock the GetFWVersionsFromBFB call
				expectedVersions := map[string]string{
					"bf2": "24.35.1000",
					"bf3": "28.39.1002",
				}
				fwUtilsMock.On("GetFWVersionsFromBFB", bfbFilePath).Return(expectedVersions, nil).Once()

				versions, err := fwProv.ValidateBFB(cacheName)
				Expect(err).NotTo(HaveOccurred())
				Expect(versions).To(Equal(expectedVersions))
			})

			It("should return error when metadata file is missing", func() {
				// No metadata file created
				_, err := fwProv.ValidateBFB(cacheName)
				Expect(err).To(HaveOccurred())
				Expect(os.IsNotExist(err)).To(BeTrue())
			})

			It("should return error when metadata file is corrupted", func() {
				// Create corrupted metadata file
				Expect(os.WriteFile(metadataFile, []byte("invalid json"), 0644)).To(Succeed())

				_, err := fwProv.ValidateBFB(cacheName)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid character"))
			})

			It("should return error when GetFWVersionsFromBFB fails", func() {
				// Create valid metadata file
				bfbFileName := "firmware.bfb"
				metadata := `{"` + bfbUrl + `": "` + bfbFileName + `"}`
				Expect(os.WriteFile(metadataFile, []byte(metadata), 0644)).To(Succeed())

				// Create the BFB file
				bfbFilePath := path.Join(bfbCacheDir, bfbFileName)
				Expect(os.WriteFile(bfbFilePath, []byte("dummy bfb content"), 0644)).To(Succeed())

				// Mock GetFWVersionsFromBFB to fail
				fwUtilsMock.On("GetFWVersionsFromBFB", bfbFilePath).Return(nil, errors.New("BFB parsing failed")).Once()

				_, err := fwProv.ValidateBFB(cacheName)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("BFB parsing failed"))
			})

			It("should return error when BFB cache is empty", func() {
				// Create metadata file with no entries
				metadata := `{}`
				Expect(os.WriteFile(metadataFile, []byte(metadata), 0644)).To(Succeed())

				_, err := fwProv.ValidateBFB(cacheName)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no BFB file found in cache"))
			})

			It("should return versions for first BFB file when multiple exist", func() {
				// Create metadata file with multiple entries (although this shouldn't happen in practice)
				bfbFileName1 := "firmware1.bfb"
				bfbFileName2 := "firmware2.bfb"
				metadata := `{
					"http://example.com/firmware1.bfb": "` + bfbFileName1 + `",
					"http://example.com/firmware2.bfb": "` + bfbFileName2 + `"
				}`
				Expect(os.WriteFile(metadataFile, []byte(metadata), 0644)).To(Succeed())

				// Create both BFB files
				bfbFilePath1 := path.Join(bfbCacheDir, bfbFileName1)
				bfbFilePath2 := path.Join(bfbCacheDir, bfbFileName2)
				Expect(os.WriteFile(bfbFilePath1, []byte("dummy bfb content 1"), 0644)).To(Succeed())
				Expect(os.WriteFile(bfbFilePath2, []byte("dummy bfb content 2"), 0644)).To(Succeed())

				// Mock the GetFWVersionsFromBFB call - it should be called for one of the files
				expectedVersions := map[string]string{
					"bf2": "24.35.1000",
					"bf3": "28.39.1002",
				}
				// We can't predict which file will be processed first due to map iteration order,
				// so we'll mock both possibilities
				fwUtilsMock.On("GetFWVersionsFromBFB", mock.AnythingOfType("string")).Return(expectedVersions, nil).Once()

				versions, err := fwProv.ValidateBFB(cacheName)
				Expect(err).NotTo(HaveOccurred())
				Expect(versions).To(Equal(expectedVersions))
			})
		})

		Describe("BFB Directory Structure", func() {
			It("should create proper BFB directory structure", func() {
				// Mock successful download
				expectedLocalPath := path.Join(bfbCacheDir, "firmware.bfb")
				fwUtilsMock.On("DownloadFile", bfbUrl, expectedLocalPath).Return(nil).Once()

				_, err := fwProv.DownloadBFB(cacheName, bfbUrl)
				Expect(err).NotTo(HaveOccurred())

				// Verify directory structure
				Expect(bfbCacheDir).To(BeADirectory())

				// Check that BFB folder is separate from firmware-binaries folder
				binaryDir := path.Join(tmpDir, cacheName, consts.NicFirmwareBinariesFolder)
				Expect(bfbCacheDir).NotTo(Equal(binaryDir))

				// Verify metadata exists
				metadataFile := path.Join(bfbCacheDir, metadataFileName)
				Expect(metadataFile).To(BeARegularFile())
			})
		})
	})
})
