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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
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
					State:    state,
					Reason:   reason,
					Versions: versions,
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
	})

	Describe("BurnNicFirmware", func() {
		var (
			fwUtilsMock *mocks.FirmwareUtils
			manager     FirmwareManager
			cacheDir    string
			cacheName   string
			tmpDir      string
			fwVersion   = "22.41.00"
		)

		BeforeEach(func() {
			fwUtilsMock = &mocks.FirmwareUtils{}

			var err error
			tmpDir, err = os.MkdirTemp("/tmp", "fwprovisioningtest-*")
			Expect(err).NotTo(HaveOccurred())

			cacheName = "test-cache"
			cacheDir = path.Join(tmpDir, cacheName, consts.NicFirmwareBinariesFolder)
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
			err := manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)
			Expect(err).To(MatchError(ContainSubstring("no such file or directory")))
		})

		It("should return an error if there is not fw binary file", func() {
			fwFolder := path.Join(cacheDir, fwSourceName, fwVersion, psid)
			Expect(os.MkdirAll(fwFolder, 0755)).To(Succeed())

			err := manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)
			Expect(err).To(MatchError(ContainSubstring("couldn't find FW binary file")))
		})

		It("should return an error if are more than one fw binary file in Version/PSID dir", func() {
			fwFolder := path.Join(cacheDir, fwSourceName, fwVersion, psid)
			Expect(os.MkdirAll(fwFolder, 0755)).To(Succeed())
			Expect(os.WriteFile(path.Join(fwFolder, "first-file.bin"), []byte(""), 0644)).To(Succeed())
			Expect(os.WriteFile(path.Join(fwFolder, "second-file.bin"), []byte(""), 0644)).To(Succeed())

			err := manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)
			Expect(err).To(MatchError(ContainSubstring("found second FW binary file")))
		})

		Describe("fw file exists", func() {
			var (
				fwFolder string
				fwBinary string
			)

			BeforeEach(func() {
				fwFolder = path.Join(cacheDir, fwSourceName, fwVersion, psid)
				fwBinary = path.Join(fwFolder, "fw.bin")

				Expect(os.MkdirAll(fwFolder, 0755)).To(Succeed())
				Expect(os.WriteFile(fwBinary, []byte(""), 0644)).To(Succeed())
			})

			It("should return an error if failed to get version and PSID from fw binary file", func() {
				errText := "some fw version error"
				fwUtilsMock.On("GetFirmwareVersionAndPSID", fwBinary).
					Return("", "", errors.New(errText)).Once()

				err := manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)
				Expect(err).To(MatchError(errText))
			})

			It("should return an error if version and PSID from fw binary file don't match the requested", func() {
				fwUtilsMock.On("GetFirmwareVersionAndPSID", fwBinary).
					Return("some-version", "some-psid", nil).Once()

				err := manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)
				Expect(err).To(MatchError(ContainSubstring("found FW binary file doesn't match expected version or PSID")))
			})

			It("should return an error if failed to burn fw", func() {
				errText := "fw internal error"
				fwUtilsMock.On("GetFirmwareVersionAndPSID", fwBinary).
					Return(fwVersion, psid, nil).Once()
				fwUtilsMock.On("BurnNicFirmware", mock.Anything, pci, fwBinary).
					Return(errors.New(errText)).Once()

				err := manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)
				Expect(err).To(MatchError(ContainSubstring(errText)))
			})

			It("should copy fw file to tmp dir and return nil if successfully burned fw", func() {
				fwUtilsMock.On("GetFirmwareVersionAndPSID", fwBinary).
					Return(fwVersion, psid, nil).Once()
				fwUtilsMock.On("BurnNicFirmware", mock.Anything, pci, fwBinary).
					Return(nil).Once()

				Expect(manager.BurnNicFirmware(context.Background(), createNicDevice(), fwVersion)).To(Succeed())

				_, err := os.Stat(fwBinary)
				Expect(err).To(BeNil())

				_, err = os.Stat(path.Join(tmpDir, path.Base(fwBinary)))
				Expect(err).To(BeNil())
			})
		})
	})
})
