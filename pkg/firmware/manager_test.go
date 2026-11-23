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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
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
				backend:      nil,
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

})
