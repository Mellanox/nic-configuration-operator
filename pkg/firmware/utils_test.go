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
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"
)

var _ = Describe("utils", func() {
	var (
		testedUtils ProvisioningUtils
		tmpDir      string
	)

	BeforeEach(func() {
		var err error
		tmpDir, err = os.MkdirTemp("", "utils-test-*")
		Expect(err).NotTo(HaveOccurred())

		testedUtils = newTestFirmwareUtils()
	})

	AfterEach(func() {
		os.RemoveAll(tmpDir)
	})

	Describe("DownloadFile", func() {
		It("should download a file from an HTTP server to the specified path", func() {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("Hello from test server"))
			}))
			defer server.Close()

			destPath := filepath.Join(tmpDir, "downloaded.txt")

			err := testedUtils.DownloadFile(server.URL, destPath)
			Expect(err).NotTo(HaveOccurred())

			data, err := os.ReadFile(destPath)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal("Hello from test server"))
		})

		It("should fail if the URL is invalid", func() {
			destPath := filepath.Join(tmpDir, "invalid.txt")
			err := testedUtils.DownloadFile("http://invalid domain", destPath)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("UnzipFiles", func() {
		var zipPath string

		BeforeEach(func() {
			zipPath = filepath.Join(tmpDir, "test.zip")
			createTestZip(zipPath, map[string]string{
				"fileA.txt":         "Content A",
				"folderB/fileB.txt": "Content B",
			})
		})

		It("should extract a zip archive to the destination directory", func() {
			destDir := filepath.Join(tmpDir, "extracted")

			extracted, err := testedUtils.UnzipFiles(zipPath, destDir)
			Expect(err).NotTo(HaveOccurred())

			Expect(extracted).To(HaveLen(2))

			// Sort for consistent comparison
			sort.Strings(extracted)

			Expect(extracted[0]).To(ContainSubstring("fileA.txt"))
			Expect(extracted[1]).To(ContainSubstring("fileB.txt"))

			dataA, err := os.ReadFile(filepath.Join(destDir, "fileA.txt"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(dataA)).To(Equal("Content A"))

			dataB, err := os.ReadFile(filepath.Join(destDir, "folderB", "fileB.txt"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(dataB)).To(Equal("Content B"))
		})

		It("should fail if the zip file does not exist", func() {
			err := os.Remove(zipPath)
			Expect(err).NotTo(HaveOccurred())

			_, err = testedUtils.UnzipFiles(zipPath, tmpDir)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("could not open zip file"))
		})

		It("should fail if the zip contains files escaping the destDir", func() {
			// Add a malicious file path: "../evil.txt"
			// Recreate the zip with an illegal path
			_ = os.Remove(zipPath)
			zipPath = filepath.Join(tmpDir, "illegal.zip")
			createTestZip(zipPath, map[string]string{
				"../evil.txt": "Evil data",
			})

			_, err := testedUtils.UnzipFiles(zipPath, filepath.Join(tmpDir, "extracted"))
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("illegal file path"))
		})
	})

	Describe("GetFirmwareVersionAndPSID", func() {
		var firmwareBinaryPath = "/tmp/somepath"

		It("should return lowercased firmware version and psid", func() {
			fwVersion := "VeRsIoN"
			PSID := "PSID"

			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("irrelevant line\n" +
						"FW Version: VeRsIoN\n" +
						"PSID: PSID\n" +
						"another irrelevant line"),
					nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mstflint"))
				Expect(args[1]).To(Equal(firmwareBinaryPath))
				return fakeCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			part, serial, err := testedUtils.GetFirmwareVersionAndPSID(firmwareBinaryPath)

			Expect(err).NotTo(HaveOccurred())
			Expect(part).To(Equal(strings.ToLower(fwVersion)))
			Expect(serial).To(Equal(strings.ToLower(PSID)))
		})
		It("should return empty string for both numbers if one is empty", func() {
			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("FW Version: VeRsIoN"), nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mstflint"))
				Expect(args[1]).To(Equal(firmwareBinaryPath))
				return fakeCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			part, serial, err := testedUtils.GetFirmwareVersionAndPSID(firmwareBinaryPath)

			Expect(err).To(HaveOccurred())
			Expect(part).To(Equal(""))
			Expect(serial).To(Equal(""))

			fakeCmd = &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("PSID: PSID"), nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mstflint"))
				Expect(args[1]).To(Equal(firmwareBinaryPath))
				return fakeCmd
			})

			part, serial, err = testedUtils.GetFirmwareVersionAndPSID(firmwareBinaryPath)

			Expect(err).To(HaveOccurred())
			Expect(part).To(Equal(""))
			Expect(serial).To(Equal(""))
		})
	})

	Describe("CleanupDirectory", func() {
		var rootDir string

		BeforeEach(func() {
			// Directory structure
			//   rootDir/
			//      keepA.bin
			//      removeMe.txt
			//      subdir/
			//         keepB.bin
			//         removeMe2.txt
			//         emptySub/
			//
			rootDir = filepath.Join(tmpDir, "cache-dir")
			Expect(os.MkdirAll(filepath.Join(rootDir, "subdir", "emptySub"), 0755)).To(Succeed())

			Expect(os.WriteFile(filepath.Join(rootDir, "keepA.bin"), []byte("A"), 0644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(rootDir, "removeMe.txt"), []byte("remove1"), 0644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(rootDir, "subdir", "keepB.bin"), []byte("B"), 0644)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(rootDir, "subdir", "removeMe2.txt"), []byte("remove2"), 0644)).To(Succeed())
		})

		It("should remove everything not in the allowed set and clean up empty dirs", func() {
			keepA := mustAbs(filepath.Join(rootDir, "keepA.bin"))
			keepB := mustAbs(filepath.Join(rootDir, "subdir", "keepB.bin"))
			allowed := map[string]struct{}{
				keepA: {},
				keepB: {},
			}

			err := testedUtils.CleanupDirectory(rootDir, allowed)
			Expect(err).NotTo(HaveOccurred())

			_, err = os.Stat(filepath.Join(rootDir, "removeMe.txt"))
			Expect(os.IsNotExist(err)).To(BeTrue())

			_, err = os.Stat(filepath.Join(rootDir, "subdir", "removeMe2.txt"))
			Expect(os.IsNotExist(err)).To(BeTrue())

			_, err = os.Stat(filepath.Join(rootDir, "subdir", "emptySub"))
			Expect(os.IsNotExist(err)).To(BeTrue())

			Expect(filepath.Join(rootDir, "keepA.bin")).To(BeARegularFile())
			Expect(filepath.Join(rootDir, "subdir", "keepB.bin")).To(BeARegularFile())
		})
	})
})

func newTestFirmwareUtils() ProvisioningUtils {
	return &utils{}
}

func createTestZip(zipPath string, files map[string]string) {
	f, err := os.Create(zipPath)
	Expect(err).NotTo(HaveOccurred())
	defer f.Close()

	w := zip.NewWriter(f)
	defer w.Close()

	for name, content := range files {
		fw, err := w.Create(name)
		Expect(err).NotTo(HaveOccurred())
		_, err = io.Copy(fw, bytes.NewReader([]byte(content)))
		Expect(err).NotTo(HaveOccurred())
	}
	Expect(w.Close()).To(Succeed())
}

// mustAbs returns the absolute version of a path, or panics on error.
func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	Expect(err).NotTo(HaveOccurred())
	return abs
}
