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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"
)

var _ = Describe("utils", func() {
	var (
		testedUtils FirmwareUtils
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

	Describe("GetFirmwareVersionsFromDevice", func() {
		var pciAddress = "0000:03:00.0"

		It("should return different burned and running firmware versions when both are present", func() {
			burnedVersion := "22.45.3608"
			runningVersion := "22.43.1020"

			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("Querying Mellanox devices firmware ...\n\n" +
						"Device #1:\n" +
						"----------\n\n" +
						"  Device Type:      ConnectX6DX\n" +
						"  Part Number:      MCX623106AC-CDA_Ax\n" +
						"  Description:      ConnectX-6 Dx EN adapter card; 100GbE; Dual-port QSFP56; PCIe 4.0 x16; Crypto and Secure Boot\n" +
						"  PSID:             MT_0000000436\n" +
						"  PCI Device Name:  03:00.0\n" +
						"  Base GUID:        1c34da030073458a\n" +
						"  Base MAC:         1c34da73458a\n" +
						"  Versions:         Current        Available\n" +
						"     FW             22.45.3608     N/A\n" +
						"     FW (Running)   22.43.1020     N/A\n" +
						"     PXE            3.6.0204       N/A\n" +
						"     UEFI           14.22.0016     N/A\n"),
					nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mlxfwmanager"))
				Expect(args).To(Equal([]string{"-d", pciAddress}))
				return fakeCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			retrievedBurnedVersion, retrievedRunningVersion, err := testedUtils.GetFirmwareVersionsFromDevice(pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(retrievedBurnedVersion).To(Equal(burnedVersion))
			Expect(retrievedRunningVersion).To(Equal(runningVersion))
		})

		It("should return burned and running firmware versions even when only burned version is present", func() {
			fwVersion := "22.29.2002"

			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("Querying Mellanox devices firmware ...\n\n" +
						"Device #1:\n" +
						"----------\n\n" +
						"  Device Type:      ConnectX6DX\n" +
						"  Part Number:      MCX623106AC-CDA_Ax\n" +
						"  Description:      ConnectX-6 Dx EN adapter card; 100GbE; Dual-port QSFP56; PCIe 4.0 x16; Crypto and Secure Boot\n" +
						"  PSID:             MT_0000000436\n" +
						"  PCI Device Name:  03:00.0\n" +
						"  Base GUID:        1c34da030073458a\n" +
						"  Base MAC:         1c34da73458a\n" +
						"  Versions:         Current        Available\n" +
						"     FW             22.29.2002     N/A\n" +
						"     PXE            3.6.0204       N/A\n" +
						"     UEFI           14.22.0016     N/A\n"),
					nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mlxfwmanager"))
				Expect(args).To(Equal([]string{"-d", pciAddress}))
				return fakeCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			retrievedBurnedVersion, retrievedRunningVersion, err := testedUtils.GetFirmwareVersionsFromDevice(pciAddress)

			Expect(err).NotTo(HaveOccurred())
			Expect(retrievedBurnedVersion).To(Equal(fwVersion))
			Expect(retrievedRunningVersion).To(Equal(fwVersion))
		})

		It("should return error if firmware version is missing", func() {
			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("Querying Mellanox devices firmware ...\n\n" +
						"Device #1:\n" +
						"----------\n\n" +
						"  PSID:             MT_0000000436\n" +
						"  Versions:         Current        Available\n"),
					nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mlxfwmanager"))
				Expect(args).To(Equal([]string{"-d", pciAddress}))
				return fakeCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			retrievedBurnedVersion, retrievedRunningVersion, err := testedUtils.GetFirmwareVersionsFromDevice(pciAddress)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("burned firmware version"))
			Expect(retrievedBurnedVersion).To(Equal(""))
			Expect(retrievedRunningVersion).To(Equal(""))
		})

		It("should return error if mlxfwmanager command fails", func() {
			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return nil, []byte("device not found"), fmt.Errorf("command failed")
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("mlxfwmanager"))
				Expect(args).To(Equal([]string{"-d", pciAddress}))
				return fakeCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			retrievedBurnedVersion, retrievedRunningVersion, err := testedUtils.GetFirmwareVersionsFromDevice(pciAddress)

			Expect(err).To(HaveOccurred())
			Expect(retrievedBurnedVersion).To(Equal(""))
			Expect(retrievedRunningVersion).To(Equal(""))
		})
	})

	Describe("GetFirmwareVersionAndPSIDFromFWBinary", func() {
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
				Expect(cmd).To(Equal("flint"))
				Expect(args[1]).To(Equal(firmwareBinaryPath))
				return fakeCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			part, serial, err := testedUtils.GetFirmwareVersionAndPSIDFromFWBinary(firmwareBinaryPath)

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
				Expect(cmd).To(Equal("flint"))
				Expect(args[1]).To(Equal(firmwareBinaryPath))
				return fakeCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			part, serial, err := testedUtils.GetFirmwareVersionAndPSIDFromFWBinary(firmwareBinaryPath)

			Expect(err).To(HaveOccurred())
			Expect(part).To(Equal(""))
			Expect(serial).To(Equal(""))

			fakeCmd = &execTesting.FakeCmd{}
			fakeCmd.OutputScript = append(fakeCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("PSID: PSID"), nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("flint"))
				Expect(args[1]).To(Equal(firmwareBinaryPath))
				return fakeCmd
			})

			part, serial, err = testedUtils.GetFirmwareVersionAndPSIDFromFWBinary(firmwareBinaryPath)

			Expect(err).To(HaveOccurred())
			Expect(part).To(Equal(""))
			Expect(serial).To(Equal(""))
		})
	})

	Describe("GetFWVersionsFromBFB", func() {
		var bfbPath = "/tmp/firmware.bfb"

		It("should return BF2 and BF3 firmware versions successfully", func() {
			fakeExec := &execTesting.FakeExec{}

			// First command: mlx-mkbfb to extract info
			mlxMkbfbCmd := &execTesting.FakeCmd{}
			mlxMkbfbCmd.CombinedOutputScript = append(mlxMkbfbCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				// Create mock dump-info-v0 file
				infoContent := `{"components": [{"Name": "BF3_NIC_FW", "Version": "28.39.1002"}]}`
				err := os.WriteFile("dump-info-v0", []byte(infoContent), 0644)
				Expect(err).NotTo(HaveOccurred())
				return []byte("extraction successful"), nil, nil
			})

			// Second command: awk command for BF3 version
			bf3AwkCmd := &execTesting.FakeCmd{}
			bf3AwkCmd.OutputScript = append(bf3AwkCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("28.39.1002"), nil, nil
			})

			// Third command: awk command for BF2 version
			bf2AwkCmd := &execTesting.FakeCmd{}
			bf2AwkCmd.OutputScript = append(bf2AwkCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("24.35.1000"), nil, nil
			})

			// Set up all three commands in the CommandScript
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("/usr/sbin/mlx-mkbfb"))
				Expect(args).To(Equal([]string{"-x", "-n", "info-v0", bfbPath}))
				return mlxMkbfbCmd
			})
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("/bin/sh"))
				Expect(args[0]).To(Equal("-c"))
				Expect(args[1]).To(ContainSubstring("BF3_NIC_FW"))
				return bf3AwkCmd
			})
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("/bin/sh"))
				Expect(args[0]).To(Equal("-c"))
				Expect(args[1]).To(ContainSubstring("BF2_NIC_FW"))
				return bf2AwkCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			versions, err := testedUtils.GetFWVersionsFromBFB(bfbPath)

			Expect(err).NotTo(HaveOccurred())
			Expect(versions).To(HaveLen(2))
			Expect(versions[consts.BlueField3DeviceID]).To(Equal("28.39.1002"))
			Expect(versions[consts.BlueField2DeviceID]).To(Equal("24.35.1000"))

			// Cleanup
			_ = os.Remove("dump-info-v0")
		})

		It("should return error when mlx-mkbfb command fails", func() {
			fakeExec := &execTesting.FakeExec{}

			fakeCmd := &execTesting.FakeCmd{}
			fakeCmd.CombinedOutputScript = append(fakeCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return nil, []byte("mlx-mkbfb failed"), fmt.Errorf("command failed")
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				Expect(cmd).To(Equal("/usr/sbin/mlx-mkbfb"))
				return fakeCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			_, err := testedUtils.GetFWVersionsFromBFB(bfbPath)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("command failed"))
		})

		It("should return error when info-v0 file is missing after extraction", func() {
			fakeExec := &execTesting.FakeExec{}

			// mlx-mkbfb succeeds but doesn't create the file
			mlxMkbfbCmd := &execTesting.FakeCmd{}
			mlxMkbfbCmd.CombinedOutputScript = append(mlxMkbfbCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				return []byte("extraction successful"), nil, nil
			})

			// BF3 awk command - returns empty since file doesn't exist
			bf3AwkCmd := &execTesting.FakeCmd{}
			bf3AwkCmd.OutputScript = append(bf3AwkCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte(""), nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				return mlxMkbfbCmd
			})
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				return bf3AwkCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			_, err := testedUtils.GetFWVersionsFromBFB(bfbPath)

			Expect(err).To(HaveOccurred()) // Now fails because BF3 version is empty
			Expect(err.Error()).To(ContainSubstring("BF3 NIC FW version is empty or not found"))
		})

		It("should return error when BF3 awk command fails", func() {
			fakeExec := &execTesting.FakeExec{}

			// First command: mlx-mkbfb succeeds
			mlxMkbfbCmd := &execTesting.FakeCmd{}
			mlxMkbfbCmd.CombinedOutputScript = append(mlxMkbfbCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				err := os.WriteFile("dump-info-v0", []byte("dummy content"), 0644)
				Expect(err).NotTo(HaveOccurred())
				return []byte("extraction successful"), nil, nil
			})

			// Second command: BF3 awk fails
			bf3AwkCmd := &execTesting.FakeCmd{}
			bf3AwkCmd.OutputScript = append(bf3AwkCmd.OutputScript, func() ([]byte, []byte, error) {
				return nil, []byte("awk failed"), fmt.Errorf("awk command failed")
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				return mlxMkbfbCmd
			})
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				return bf3AwkCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			_, err := testedUtils.GetFWVersionsFromBFB(bfbPath)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("awk command failed"))

			// Cleanup
			_ = os.Remove("dump-info-v0")
		})

		It("should return error when BF2 awk command fails", func() {
			fakeExec := &execTesting.FakeExec{}

			// First command: mlx-mkbfb succeeds
			mlxMkbfbCmd := &execTesting.FakeCmd{}
			mlxMkbfbCmd.CombinedOutputScript = append(mlxMkbfbCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				err := os.WriteFile("dump-info-v0", []byte("dummy content"), 0644)
				Expect(err).NotTo(HaveOccurred())
				return []byte("extraction successful"), nil, nil
			})

			// Second command: BF3 awk succeeds
			bf3AwkCmd := &execTesting.FakeCmd{}
			bf3AwkCmd.OutputScript = append(bf3AwkCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("28.39.1002"), nil, nil
			})

			// Third command: BF2 awk fails
			bf2AwkCmd := &execTesting.FakeCmd{}
			bf2AwkCmd.OutputScript = append(bf2AwkCmd.OutputScript, func() ([]byte, []byte, error) {
				return nil, []byte("awk failed"), fmt.Errorf("BF2 awk command failed")
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				return mlxMkbfbCmd
			})
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				return bf3AwkCmd
			})
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				return bf2AwkCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			_, err := testedUtils.GetFWVersionsFromBFB(bfbPath)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("BF2 awk command failed"))

			// Cleanup
			_ = os.Remove("dump-info-v0")
		})

		It("should return error when BF3 version is empty or whitespace", func() {
			fakeExec := &execTesting.FakeExec{}

			// First command: mlx-mkbfb succeeds
			mlxMkbfbCmd := &execTesting.FakeCmd{}
			mlxMkbfbCmd.CombinedOutputScript = append(mlxMkbfbCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				err := os.WriteFile("dump-info-v0", []byte("dummy content"), 0644)
				Expect(err).NotTo(HaveOccurred())
				return []byte("extraction successful"), nil, nil
			})

			// Second command: BF3 awk returns whitespace only
			bf3AwkCmd := &execTesting.FakeCmd{}
			bf3AwkCmd.OutputScript = append(bf3AwkCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("   \n  "), nil, nil // whitespace only
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				return mlxMkbfbCmd
			})
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				return bf3AwkCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			_, err := testedUtils.GetFWVersionsFromBFB(bfbPath)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("BF3 NIC FW version is empty or not found"))

			// Cleanup
			_ = os.Remove("dump-info-v0")
		})

		It("should return error when BF2 version is empty", func() {
			fakeExec := &execTesting.FakeExec{}

			// First command: mlx-mkbfb succeeds
			mlxMkbfbCmd := &execTesting.FakeCmd{}
			mlxMkbfbCmd.CombinedOutputScript = append(mlxMkbfbCmd.CombinedOutputScript, func() ([]byte, []byte, error) {
				err := os.WriteFile("dump-info-v0", []byte("dummy content"), 0644)
				Expect(err).NotTo(HaveOccurred())
				return []byte("extraction successful"), nil, nil
			})

			// Second command: BF3 awk succeeds
			bf3AwkCmd := &execTesting.FakeCmd{}
			bf3AwkCmd.OutputScript = append(bf3AwkCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte("28.39.1002"), nil, nil
			})

			// Third command: BF2 awk returns empty
			bf2AwkCmd := &execTesting.FakeCmd{}
			bf2AwkCmd.OutputScript = append(bf2AwkCmd.OutputScript, func() ([]byte, []byte, error) {
				return []byte(""), nil, nil
			})

			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				return mlxMkbfbCmd
			})
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				return bf3AwkCmd
			})
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				return bf2AwkCmd
			})

			testedUtils := &utils{execInterface: fakeExec}

			_, err := testedUtils.GetFWVersionsFromBFB(bfbPath)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("BF2 NIC FW version is empty or not found"))

			// Cleanup
			_ = os.Remove("dump-info-v0")
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

	Describe("standalone util functions", func() {
		BeforeEach(func() {
			var err error
			tmpDir, err = os.MkdirTemp("", "fileutils_test")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			_ = os.RemoveAll(tmpDir)
		})

		Describe("copyFile", func() {
			It("should copy file content from source to destination", func() {
				src := filepath.Join(tmpDir, "src.txt")
				dst := filepath.Join(tmpDir, "dst.txt")
				content := "Hello, world!"

				err := os.WriteFile(src, []byte(content), 0644)
				Expect(err).NotTo(HaveOccurred())

				err = copyFile(src, dst)
				Expect(err).NotTo(HaveOccurred())

				data, err := os.ReadFile(dst)
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data)).To(Equal(content))
			})

			It("should return an error if the source file does not exist", func() {
				src := filepath.Join(tmpDir, "nonexistent.txt")
				dst := filepath.Join(tmpDir, "dst.txt")

				err := copyFile(src, dst)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("opening source file"))
			})

			It("should return an error if the destination file cannot be created", func() {
				src := filepath.Join(tmpDir, "src.txt")
				err := os.WriteFile(src, []byte("content"), 0644)
				Expect(err).NotTo(HaveOccurred())

				// Destination in a subdirectory that does not exist
				dst := filepath.Join(tmpDir, "nonexistent", "dst.txt")
				err = copyFile(src, dst)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("creating destination file"))
			})
		})
	})
})

func newTestFirmwareUtils() FirmwareUtils {
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
