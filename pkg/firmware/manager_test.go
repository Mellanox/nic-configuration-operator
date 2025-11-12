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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	dmsMocks "github.com/Mellanox/nic-configuration-operator/pkg/dms/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/firmware/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

var _ = Describe("FirmwareManager", func() {
	var (
		fwSourceName = "test-nic-fw-source"
		psid         = "MT_0000001"
		pci          = "0000:3b:00.0"
	)

	var createNicDevice = func() *v1alpha1.NicDevice {
		return &v1alpha1.NicDevice{
			Spec: v1alpha1.NicDeviceSpec{Firmware: &v1alpha1.FirmwareTemplateSpec{
				NicFirmwareSourceRef: fwSourceName,
				UpdatePolicy:         "Update",
			}},
			Status: v1alpha1.NicDeviceStatus{
				PSID:  psid,
				Ports: []v1alpha1.NicDevicePortSpec{{PCI: pci}},
			},
		}
	}

	Describe("VerifyCachedBinaries", func() {
		var createManager = func(fwSourceToServe *v1alpha1.NicFirmwareSource) FirmwareManager {
			scheme := runtime.NewScheme()
			Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if fwSourceToServe != nil {
				builder.WithObjects(fwSourceToServe)
			}
			return firmwareManager{
				client:       builder.Build(),
				utils:        nil,
				cacheRootDir: "",
				namespace:    "",
			}
		}

		var createNicFwSource = func(state, reason string, versions map[string][]string) *v1alpha1.NicFirmwareSource {
			return &v1alpha1.NicFirmwareSource{
				ObjectMeta: v1.ObjectMeta{Name: fwSourceName, Namespace: ""},
				Spec:       v1alpha1.NicFirmwareSourceSpec{},
				Status: v1alpha1.NicFirmwareSourceStatus{
					State:          state,
					Reason:         reason,
					BinaryVersions: versions,
				},
			}
		}

		var createNicFwSourceWithBFB = func(state, reason string, versions map[string][]string, bfbVersions map[string]string) *v1alpha1.NicFirmwareSource {
			return &v1alpha1.NicFirmwareSource{
				ObjectMeta: v1.ObjectMeta{Name: fwSourceName, Namespace: ""},
				Spec:       v1alpha1.NicFirmwareSourceSpec{},
				Status: v1alpha1.NicFirmwareSourceStatus{
					State:          state,
					Reason:         reason,
					BinaryVersions: versions,
					BFBVersions:    bfbVersions,
				},
			}
		}

		It("should return an error if the device's spec is empty", func() {
			manager := createManager(nil)
			version, err := manager.ValidateRequestedFirmwareSource(context.Background(), &v1alpha1.NicDevice{Spec: v1alpha1.NicDeviceSpec{Firmware: nil}})
			Expect(version).To(BeEmpty())
			Expect(err).To(MatchError("device's firmware spec is empty"))
		})

		It("should return an error if failed to get referenced firmware source", func() {
			manager := createManager(&v1alpha1.NicFirmwareSource{})
			version, err := manager.ValidateRequestedFirmwareSource(context.Background(), createNicDevice())
			Expect(version).To(BeEmpty())
			Expect(k8sErrors.IsNotFound(err)).To(BeTrue())
		})

		It("should return an error if fw source has a failed state", func() {
			states := []string{consts.FirmwareSourceDownloadFailedStatus, consts.FirmwareSourceProcessingFailedStatus, consts.FirmwareSourceCacheVerificationFailedStatus}

			for _, state := range states {
				manager := createManager(createNicFwSource(state, "reason", nil))
				version, err := manager.ValidateRequestedFirmwareSource(context.Background(), createNicDevice())
				Expect(version).To(BeEmpty())
				Expect(err).To(MatchError(fmt.Sprintf("requested firmware source %s failed: %s, %s", fwSourceName, state, "reason")))
			}
		})

		It("should return an error if fw source is not ready", func() {
			states := []string{consts.FirmwareSourceDownloadingStatus, consts.FirmwareSourceProcessingStatus}

			for _, state := range states {
				manager := createManager(createNicFwSource(state, "reason", nil))
				version, err := manager.ValidateRequestedFirmwareSource(context.Background(), createNicDevice())
				Expect(version).To(BeEmpty())
				Expect(types.IsFirmwareSourceNotReadyError(err)).To(BeTrue())
			}
		})

		It("should return an error if no matching fw image was found", func() {
			versions := map[string][]string{
				"some-version":       {"some-psid", "some-other-psid"},
				"some-other-version": {"yet-another-psid"},
			}
			manager := createManager(createNicFwSource(consts.FirmwareSourceSuccessStatus, "", versions))
			version, err := manager.ValidateRequestedFirmwareSource(context.Background(), createNicDevice())
			Expect(version).To(BeEmpty())
			Expect(err).To(MatchError(fmt.Sprintf("requested firmware source (%s) has no image for this device's PSID (%s)", fwSourceName, psid)))
		})

		It("should return version if matching fw image was found", func() {
			versions := map[string][]string{
				"some-version":       {"some-psid", "some-other-psid"},
				"some-other-version": {"yet-another-psid", psid},
			}
			manager := createManager(createNicFwSource(consts.FirmwareSourceSuccessStatus, "", versions))
			version, err := manager.ValidateRequestedFirmwareSource(context.Background(), createNicDevice())
			Expect(version).To(Equal("some-other-version"))
			Expect(err).To(Succeed())
		})

		Context("for BlueField devices", func() {
			var createBlueFieldDevice = func(deviceType string) *v1alpha1.NicDevice {
				device := createNicDevice()
				device.Status.Type = deviceType
				device.Status.SerialNumber = "test-serial-123"
				return device
			}

			It("should return BFB version for BF2 device when available", func() {
				bfbVersions := map[string]string{
					consts.BlueField2DeviceID: "24.35.1000",
					consts.BlueField3DeviceID: "28.39.1002",
				}
				manager := createManager(createNicFwSourceWithBFB(consts.FirmwareSourceSuccessStatus, "", nil, bfbVersions))
				version, err := manager.ValidateRequestedFirmwareSource(context.Background(), createBlueFieldDevice(consts.BlueField2DeviceID))
				Expect(version).To(Equal("24.35.1000"))
				Expect(err).To(Succeed())
			})

			It("should return BFB version for BF3 device when available", func() {
				bfbVersions := map[string]string{
					consts.BlueField2DeviceID: "24.35.1000",
					consts.BlueField3DeviceID: "28.39.1002",
				}
				manager := createManager(createNicFwSourceWithBFB(consts.FirmwareSourceSuccessStatus, "", nil, bfbVersions))
				version, err := manager.ValidateRequestedFirmwareSource(context.Background(), createBlueFieldDevice(consts.BlueField3DeviceID))
				Expect(version).To(Equal("28.39.1002"))
				Expect(err).To(Succeed())
			})

			It("should return error when BFB version not available for device type", func() {
				bfbVersions := map[string]string{
					consts.BlueField2DeviceID: "24.35.1000",
				}
				manager := createManager(createNicFwSourceWithBFB(consts.FirmwareSourceSuccessStatus, "", nil, bfbVersions))
				version, err := manager.ValidateRequestedFirmwareSource(context.Background(), createBlueFieldDevice(consts.BlueField3DeviceID))
				Expect(version).To(BeEmpty())
				Expect(err).To(MatchError(fmt.Sprintf("requested firmware source (%s) has no image for this BlueField device (%s)", fwSourceName, consts.BlueField3DeviceID)))
			})
		})
	})

	Describe("BurnNicFirmware", func() {
		var (
			fwUtilsMock *mocks.FirmwareUtils
			manager     FirmwareManager
			cacheDir    string
			tmpDir      string
			fwVersion   = "22.41.00"
		)

		BeforeEach(func() {
			fwUtilsMock = &mocks.FirmwareUtils{}

			var err error
			tmpDir, err = os.MkdirTemp("/tmp", "fwprovisioningtest-*")
			Expect(err).NotTo(HaveOccurred())

			cacheDir = path.Join(tmpDir, consts.NicFirmwareBinariesFolder)
			Expect(os.MkdirAll(cacheDir, 0755)).To(Succeed())

			manager = firmwareManager{
				client:       nil,
				utils:        fwUtilsMock,
				cacheRootDir: cacheDir,
				tmpDir:       tmpDir,
				namespace:    "",
			}
		})

		AfterEach(func() {
			fwUtilsMock.AssertExpectations(GinkgoT())
			_ = os.RemoveAll(tmpDir)
		})

		It("should return an error if the device's spec is empty", func() {
			err := manager.BurnNicFirmware(context.Background(), &v1alpha1.NicDevice{Spec: v1alpha1.NicDeviceSpec{Firmware: nil}}, "")
			Expect(err).To(MatchError("device's firmware spec is empty"))
		})

		It("should return an error if cache is empty", func() {
			fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
				Return("", "", errors.New("version check failed")).Once()
			err := manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)
			Expect(err).To(MatchError(ContainSubstring("no such file or directory")))
		})

		It("should return an error if there is no fw binary file", func() {
			fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
				Return("", "", errors.New("version check failed")).Once()
			fwFolder := path.Join(cacheDir, fwSourceName, consts.NicFirmwareBinariesFolder, fwVersion, psid)
			Expect(os.MkdirAll(fwFolder, 0755)).To(Succeed())

			err := manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)
			Expect(err).To(MatchError(ContainSubstring("couldn't find FW binary file")))
		})

		It("should return an error if are more than one fw binary file in Version/PSID dir", func() {
			fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
				Return("", "", errors.New("version check failed")).Once()
			fwFolder := path.Join(cacheDir, fwSourceName, consts.NicFirmwareBinariesFolder, fwVersion, psid)
			Expect(os.MkdirAll(fwFolder, 0755)).To(Succeed())
			Expect(os.WriteFile(path.Join(fwFolder, "first-file.bin"), []byte(""), 0644)).To(Succeed())
			Expect(os.WriteFile(path.Join(fwFolder, "second-file.bin"), []byte(""), 0644)).To(Succeed())

			err := manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)
			Expect(err).To(MatchError(ContainSubstring("found second FW binary file")))
		})

		Describe("fw file exists", func() {
			var (
				fwFolder         string
				fwBinary         string
				tempFwBinaryPath string
			)

			BeforeEach(func() {
				fwFolder = path.Join(cacheDir, fwSourceName, consts.NicFirmwareBinariesFolder, fwVersion, psid)
				fwBinary = path.Join(fwFolder, "fw.bin")

				Expect(os.MkdirAll(fwFolder, 0755)).To(Succeed())
				Expect(os.WriteFile(fwBinary, []byte(""), 0644)).To(Succeed())

				tempFwBinaryPath = path.Join(tmpDir, path.Base(fwBinary))
			})

			It("should proceed with burning when device version check fails", func() {
				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("", "", errors.New("version check failed")).Once()
				fwUtilsMock.On("GetFirmwareVersionAndPSIDFromFWBinary", fwBinary).
					Return(fwVersion, psid, nil).Once()
				fwUtilsMock.On("VerifyImageBootable", tempFwBinaryPath).
					Return(nil).Once()
				fwUtilsMock.On("BurnNicFirmware", mock.Anything, pci, tempFwBinaryPath).
					Return(nil).Once()

				Expect(manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)).To(Succeed())

				_, err := os.Stat(fwBinary)
				Expect(err).To(BeNil())

				_, err = os.Stat(path.Join(tmpDir, path.Base(fwBinary)))
				Expect(err).To(BeNil())
			})

			It("should proceed with burning when device version doesn't match requested", func() {
				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("22.39.00", "22.39.00", nil).Once()
				fwUtilsMock.On("GetFirmwareVersionAndPSIDFromFWBinary", fwBinary).
					Return(fwVersion, psid, nil).Once()
				fwUtilsMock.On("VerifyImageBootable", tempFwBinaryPath).
					Return(nil).Once()
				fwUtilsMock.On("BurnNicFirmware", mock.Anything, pci, tempFwBinaryPath).
					Return(nil).Once()

				Expect(manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)).To(Succeed())

				_, err := os.Stat(fwBinary)
				Expect(err).To(BeNil())

				_, err = os.Stat(path.Join(tmpDir, path.Base(fwBinary)))
				Expect(err).To(BeNil())
			})

			It("should return an error if failed to get version and PSID from fw binary file", func() {
				errText := "some fw version error"
				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("", "", errors.New("version check failed")).Once()
				fwUtilsMock.On("GetFirmwareVersionAndPSIDFromFWBinary", fwBinary).
					Return("", "", errors.New(errText)).Once()

				err := manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)
				Expect(err).To(MatchError(errText))
			})

			It("should return an error if version and PSID from fw binary file don't match the requested", func() {
				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("", "", errors.New("version check failed")).Once()
				fwUtilsMock.On("GetFirmwareVersionAndPSIDFromFWBinary", fwBinary).
					Return("some-version", "some-psid", nil).Once()

				err := manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)
				Expect(err).To(MatchError(ContainSubstring("found FW binary file doesn't match expected version or PSID")))
			})

			It("should copy fw file to tmp dir and return error if it's unbootable", func() {
				err := errors.New("image is not bootable")
				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("", "", errors.New("version check failed")).Once()
				fwUtilsMock.On("GetFirmwareVersionAndPSIDFromFWBinary", fwBinary).
					Return(fwVersion, psid, nil).Once()
				fwUtilsMock.On("VerifyImageBootable", tempFwBinaryPath).
					Return(err).Once()
				fwUtilsMock.AssertNotCalled(GinkgoT(), "BurnNicFirmware", mock.Anything, mock.Anything, mock.Anything)

				Expect(manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)).To(MatchError(err))

				_, err = os.Stat(fwBinary)
				Expect(err).To(BeNil())

				_, err = os.Stat(path.Join(tmpDir, path.Base(fwBinary)))
				Expect(err).To(BeNil())
			})

			It("should return an error if failed to burn fw", func() {
				errText := "fw internal error"
				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("", "", errors.New("version check failed")).Once()
				fwUtilsMock.On("GetFirmwareVersionAndPSIDFromFWBinary", fwBinary).
					Return(fwVersion, psid, nil).Once()
				fwUtilsMock.On("VerifyImageBootable", tempFwBinaryPath).
					Return(nil).Once()
				fwUtilsMock.On("BurnNicFirmware", mock.Anything, pci, tempFwBinaryPath).
					Return(errors.New(errText)).Once()

				err := manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)
				Expect(err).To(MatchError(ContainSubstring(errText)))
			})

			It("should copy fw file to tmp dir and return nil if successfully burned fw", func() {
				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("", "", errors.New("version check failed")).Once()
				fwUtilsMock.On("GetFirmwareVersionAndPSIDFromFWBinary", fwBinary).
					Return(fwVersion, psid, nil).Once()
				fwUtilsMock.On("VerifyImageBootable", tempFwBinaryPath).
					Return(nil).Once()
				fwUtilsMock.On("BurnNicFirmware", mock.Anything, pci, tempFwBinaryPath).
					Return(nil).Once()

				Expect(manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)).To(Succeed())

				_, err := os.Stat(fwBinary)
				Expect(err).To(BeNil())

				_, err = os.Stat(path.Join(tmpDir, path.Base(fwBinary)))
				Expect(err).To(BeNil())
			})

			It("should skip burning when burned firmware version matches requested version", func() {
				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return(fwVersion, fwVersion, nil).Once()

				Expect(manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)).To(Succeed())

				// Verify no other methods were called since we skipped burning
				fwUtilsMock.AssertNotCalled(GinkgoT(), "VerifyImageBootable", mock.Anything)
				fwUtilsMock.AssertNotCalled(GinkgoT(), "BurnNicFirmware", mock.Anything, mock.Anything, mock.Anything)
			})

			It("should proceed with burning when burned firmware version doesn't match requested", func() {
				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("22.39.00", "22.39.00", nil).Once()
				fwUtilsMock.On("GetFirmwareVersionAndPSIDFromFWBinary", fwBinary).
					Return(fwVersion, psid, nil).Once()
				fwUtilsMock.On("VerifyImageBootable", tempFwBinaryPath).
					Return(nil).Once()
				fwUtilsMock.On("BurnNicFirmware", mock.Anything, pci, tempFwBinaryPath).
					Return(nil).Once()

				Expect(manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)).To(Succeed())

				_, err := os.Stat(fwBinary)
				Expect(err).To(BeNil())

				_, err = os.Stat(path.Join(tmpDir, path.Base(fwBinary)))
				Expect(err).To(BeNil())
			})
		})

		Context("for BlueField devices with BFB installation", func() {
			var (
				dmsManagerMock *dmsMocks.DMSManager
				dmsClientMock  *dmsMocks.DMSClient
				bfbCacheDir    string
				bfbFileName    = "firmware.bfb"
				testSerial     = "test-serial-123"
				fwVersion      = "24.35.1000"
			)

			var createBlueFieldDevice = func(deviceType string) *v1alpha1.NicDevice {
				device := createNicDevice()
				device.Status.Type = deviceType
				device.Status.SerialNumber = testSerial
				return device
			}

			BeforeEach(func() {
				dmsManagerMock = &dmsMocks.DMSManager{}
				dmsClientMock = &dmsMocks.DMSClient{}

				bfbCacheDir = path.Join(cacheDir, fwSourceName, consts.BFBFolder)
				Expect(os.MkdirAll(bfbCacheDir, 0755)).To(Succeed())

				// Create a mock BFB file
				bfbFilePath := path.Join(bfbCacheDir, bfbFileName)
				Expect(os.WriteFile(bfbFilePath, []byte("mock bfb content"), 0644)).To(Succeed())

				manager = firmwareManager{
					client:       nil,
					dmsManager:   dmsManagerMock,
					utils:        fwUtilsMock,
					cacheRootDir: cacheDir,
					tmpDir:       tmpDir,
					namespace:    "",
				}
			})

			AfterEach(func() {
				dmsManagerMock.AssertExpectations(GinkgoT())
				dmsClientMock.AssertExpectations(GinkgoT())
			})

			It("should install BFB successfully on BF2 device", func() {
				device := createBlueFieldDevice(consts.BlueField2DeviceID)
				expectedBFBPath := path.Join(bfbCacheDir, bfbFileName)

				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("", "", errors.New("version check failed")).Once()
				dmsManagerMock.On("GetDMSClientBySerialNumber", testSerial).Return(dmsClientMock, nil).Once()
				dmsClientMock.On("InstallBFB", mock.Anything, fwVersion, expectedBFBPath).Return(nil).Once()

				err := manager.BurnNicFirmware(context.Background(), device, fwVersion)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should install BFB successfully on BF3 device", func() {
				device := createBlueFieldDevice(consts.BlueField3DeviceID)
				expectedBFBPath := path.Join(bfbCacheDir, bfbFileName)

				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("", "", errors.New("version check failed")).Once()
				dmsManagerMock.On("GetDMSClientBySerialNumber", testSerial).Return(dmsClientMock, nil).Once()
				dmsClientMock.On("InstallBFB", mock.Anything, fwVersion, expectedBFBPath).Return(nil).Once()

				err := manager.BurnNicFirmware(context.Background(), device, fwVersion)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should return error when BFB file not found", func() {
				device := createBlueFieldDevice(consts.BlueField2DeviceID)

				// Remove the BFB file to simulate missing file
				Expect(os.Remove(path.Join(bfbCacheDir, bfbFileName))).To(Succeed())

				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("", "", errors.New("version check failed")).Once()
				dmsManagerMock.On("GetDMSClientBySerialNumber", testSerial).Return(dmsClientMock, nil).Once()

				err := manager.BurnNicFirmware(context.Background(), device, fwVersion)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("couldn't find BFB file for version"))
			})

			It("should return error when DMS client unavailable", func() {
				device := createBlueFieldDevice(consts.BlueField2DeviceID)
				dmsError := errors.New("DMS client not available")

				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("", "", errors.New("version check failed")).Once()
				dmsManagerMock.On("GetDMSClientBySerialNumber", testSerial).Return(nil, dmsError).Once()

				err := manager.BurnNicFirmware(context.Background(), device, fwVersion)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("DMS client not available"))
			})

			It("should return error when BFB installation fails", func() {
				device := createBlueFieldDevice(consts.BlueField2DeviceID)
				expectedBFBPath := path.Join(bfbCacheDir, bfbFileName)
				installError := errors.New("BFB installation failed")

				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("", "", errors.New("version check failed")).Once()
				dmsManagerMock.On("GetDMSClientBySerialNumber", testSerial).Return(dmsClientMock, nil).Once()
				dmsClientMock.On("InstallBFB", mock.Anything, fwVersion, expectedBFBPath).Return(installError).Once()

				err := manager.BurnNicFirmware(context.Background(), device, fwVersion)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("BFB installation failed"))
			})
		})

		Context("for non-BlueField devices", func() {
			It("should continue with normal firmware binary installation", func() {
				device := createNicDevice()
				device.Status.Type = "cx6" // Non-BlueField device

				fwFolder := path.Join(cacheDir, fwSourceName, consts.NicFirmwareBinariesFolder, fwVersion, psid)
				fwBinary := path.Join(fwFolder, "fw.bin")
				tempFwBinaryPath := path.Join(tmpDir, path.Base(fwBinary))

				Expect(os.MkdirAll(fwFolder, 0755)).To(Succeed())
				Expect(os.WriteFile(fwBinary, []byte(""), 0644)).To(Succeed())

				fwUtilsMock.On("GetFirmwareVersionsFromDevice", pci).
					Return("", "", errors.New("version check failed")).Once()
				fwUtilsMock.On("GetFirmwareVersionAndPSIDFromFWBinary", fwBinary).
					Return(fwVersion, psid, nil).Once()
				fwUtilsMock.On("VerifyImageBootable", tempFwBinaryPath).
					Return(nil).Once()
				fwUtilsMock.On("BurnNicFirmware", mock.Anything, pci, tempFwBinaryPath).
					Return(nil).Once()

				err := manager.BurnNicFirmware(context.Background(), device, fwVersion)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("InstallDocaSpcXCC", func() {
		var (
			fwUtilsMock *mocks.FirmwareUtils
			manager     FirmwareManager
			tmpDir      string
			cacheDir    string
			targetVer   = "1.2.3"
		)

		createFwSource := func(version string) *v1alpha1.NicFirmwareSource {
			return &v1alpha1.NicFirmwareSource{
				ObjectMeta: v1.ObjectMeta{Name: fwSourceName, Namespace: ""},
				Status:     v1alpha1.NicFirmwareSourceStatus{DocaSpcXCCVersion: version},
			}
		}

		BeforeEach(func() {
			fwUtilsMock = &mocks.FirmwareUtils{}

			var err error
			tmpDir, err = os.MkdirTemp("/tmp", "fw-manager-doca-*")
			Expect(err).NotTo(HaveOccurred())

			cacheDir = tmpDir
		})

		AfterEach(func() {
			fwUtilsMock.AssertExpectations(GinkgoT())
			_ = os.RemoveAll(tmpDir)
		})

		It("doesn't return error when device's firmware spec is empty", func() {
			manager = firmwareManager{client: nil, utils: fwUtilsMock, cacheRootDir: cacheDir, namespace: ""}
			err := manager.InstallDocaSpcXCC(context.Background(), &v1alpha1.NicDevice{Spec: v1alpha1.NicDeviceSpec{Firmware: nil}}, targetVer)
			Expect(err).To(MatchError("device's firmware spec is empty, cannot install DOCA SPC-X CC"))
		})

		It("returns error when referenced NicFirmwareSource not found", func() {
			scheme := runtime.NewScheme()
			Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
			cli := fake.NewClientBuilder().WithScheme(scheme).Build()

			manager = firmwareManager{client: cli, utils: fwUtilsMock, cacheRootDir: cacheDir, namespace: ""}
			err := manager.InstallDocaSpcXCC(context.Background(), createNicDevice(), targetVer)
			Expect(err).To(HaveOccurred())
			Expect(k8sErrors.IsNotFound(err)).To(BeTrue())
		})

		It("returns error when provisioned version mismatches target", func() {
			scheme := runtime.NewScheme()
			Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
			cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(createFwSource("0.0.1")).Build()

			manager = firmwareManager{client: cli, utils: fwUtilsMock, cacheRootDir: cacheDir, namespace: ""}
			err := manager.InstallDocaSpcXCC(context.Background(), createNicDevice(), targetVer)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("doesn't match target version"))
		})

		It("no-ops when installed version matches target", func() {
			scheme := runtime.NewScheme()
			Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
			cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(createFwSource(targetVer)).Build()

			fwUtilsMock.On("GetInstalledDebPackageVersion", "doca-spcx-cc").Return(targetVer).Once()

			manager = firmwareManager{client: cli, utils: fwUtilsMock, cacheRootDir: cacheDir, namespace: ""}
			err := manager.InstallDocaSpcXCC(context.Background(), createNicDevice(), targetVer)
			Expect(err).NotTo(HaveOccurred())
			fwUtilsMock.AssertNotCalled(GinkgoT(), "InstallDebPackage", mock.Anything)
		})

		It("installs when not installed and .deb exists in cache", func() {
			scheme := runtime.NewScheme()
			Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
			cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(createFwSource(targetVer)).Build()

			fwUtilsMock.On("GetInstalledDebPackageVersion", "doca-spcx-cc").Return("").Once()

			// Prepare cache with a single .deb
			debDir := path.Join(cacheDir, fwSourceName, consts.DocaSpcXCCFolder)
			Expect(os.MkdirAll(debDir, 0755)).To(Succeed())
			debPath := path.Join(debDir, "doca-spcx-cc"+consts.DebPackageExtension)
			Expect(os.WriteFile(debPath, []byte("deb content"), 0644)).To(Succeed())

			fwUtilsMock.On("InstallDebPackage", debPath).Return(nil).Once()

			manager = firmwareManager{client: cli, utils: fwUtilsMock, cacheRootDir: cacheDir, namespace: ""}
			err := manager.InstallDocaSpcXCC(context.Background(), createNicDevice(), targetVer)
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns error when multiple .deb files are present", func() {
			scheme := runtime.NewScheme()
			Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
			cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(createFwSource(targetVer)).Build()

			fwUtilsMock.On("GetInstalledDebPackageVersion", "doca-spcx-cc").Return("").Once()

			debDir := path.Join(cacheDir, fwSourceName, consts.DocaSpcXCCFolder)
			Expect(os.MkdirAll(debDir, 0755)).To(Succeed())
			Expect(os.WriteFile(path.Join(debDir, "a"+consts.DebPackageExtension), []byte("a"), 0644)).To(Succeed())
			Expect(os.WriteFile(path.Join(debDir, "b"+consts.DebPackageExtension), []byte("b"), 0644)).To(Succeed())

			manager = firmwareManager{client: cli, utils: fwUtilsMock, cacheRootDir: cacheDir, namespace: ""}
			err := manager.InstallDocaSpcXCC(context.Background(), createNicDevice(), targetVer)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("found second DOCA SPC-X CC file"))
		})

		It("returns error when no .deb file found in cache", func() {
			scheme := runtime.NewScheme()
			Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
			cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(createFwSource(targetVer)).Build()

			fwUtilsMock.On("GetInstalledDebPackageVersion", "doca-spcx-cc").Return("").Once()

			debDir := path.Join(cacheDir, fwSourceName, consts.DocaSpcXCCFolder)
			Expect(os.MkdirAll(debDir, 0755)).To(Succeed())

			manager = firmwareManager{client: cli, utils: fwUtilsMock, cacheRootDir: cacheDir, namespace: ""}
			err := manager.InstallDocaSpcXCC(context.Background(), createNicDevice(), targetVer)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("couldn't find DOCA SPC-X CC package"))
		})

		It("returns error when deb installation fails", func() {
			scheme := runtime.NewScheme()
			Expect(v1alpha1.AddToScheme(scheme)).To(Succeed())
			cli := fake.NewClientBuilder().WithScheme(scheme).WithObjects(createFwSource(targetVer)).Build()

			fwUtilsMock.On("GetInstalledDebPackageVersion", "doca-spcx-cc").Return("").Once()

			debDir := path.Join(cacheDir, fwSourceName, consts.DocaSpcXCCFolder)
			Expect(os.MkdirAll(debDir, 0755)).To(Succeed())
			debPath := filepath.Join(debDir, "doca"+consts.DebPackageExtension)
			Expect(os.WriteFile(debPath, []byte("content"), 0644)).To(Succeed())

			fwUtilsMock.On("InstallDebPackage", debPath).Return(errors.New("install fail")).Once()

			manager = firmwareManager{client: cli, utils: fwUtilsMock, cacheRootDir: cacheDir, namespace: ""}
			err := manager.InstallDocaSpcXCC(context.Background(), createNicDevice(), targetVer)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("install fail"))
		})
	})
})
