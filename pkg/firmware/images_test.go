// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package firmware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/firmware/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/firmwareimage"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

type testImageResolver struct {
	root string
	mu   sync.Mutex
	pins map[string]int
}

func (r *testImageResolver) Acquire(ref string) (string, func(), error) {
	key, err := firmwareimage.ImageKey(ref)
	if err != nil {
		return "", nil, err
	}
	name := filepath.Join(r.root, key, firmwareimage.PayloadName)
	info, err := os.Lstat(name)
	if os.IsNotExist(err) {
		return "", nil, firmwareimage.ErrPending
	}
	if err != nil {
		return "", nil, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", nil, fmt.Errorf("payload must be a regular file")
	}
	r.mu.Lock()
	r.pins[ref]++
	r.mu.Unlock()
	var once sync.Once
	return name, func() { once.Do(func() { r.mu.Lock(); defer r.mu.Unlock(); r.pins[ref]-- }) }, nil
}

var _ = Describe("container firmware resolution", func() {
	const psid = "mt_0000001"
	const version = "28.44.1036"
	const pci = "0000:3b:00.0"
	var (
		manager                 firmwareManager
		utils                   *mocks.FirmwareUtils
		device                  *v1alpha1.NicDevice
		firstImage, secondImage string
		resolver                *testImageResolver
	)
	BeforeEach(func() {
		firstImage = "registry.example/fw:cx7-mt_0000001@sha256:" + strings.Repeat("a", 64)
		secondImage = "registry.example/fw:cx8-mt_0000002@sha256:" + strings.Repeat("b", 64)
		utils = &mocks.FirmwareUtils{}
		resolver = &testImageResolver{root: GinkgoT().TempDir(), pins: map[string]int{}}
		manager = firmwareManager{client: nil, dmsManager: nil, utils: utils, cacheRootDir: "", namespace: "", tmpDir: "", images: resolver}
		device = &v1alpha1.NicDevice{
			Spec:   v1alpha1.NicDeviceSpec{Firmware: &v1alpha1.FirmwareTemplateSpec{NicFirmwareSourceRef: "", FirmwareImages: []string{firstImage}, UpdatePolicy: consts.FirmwareUpdatePolicyValidate}},
			Status: v1alpha1.NicDeviceStatus{PSID: psid, Type: "1021", Ports: []v1alpha1.NicDevicePortSpec{{PCI: pci}}},
		}
	})
	AfterEach(func() {
		utils.AssertExpectations(GinkgoT())
		for _, pins := range resolver.pins {
			Expect(pins).To(BeZero(), "image acquisition must be released on every path")
		}
	})
	publish := func(image string) string {
		key, err := firmwareimage.ImageKey(image)
		Expect(err).NotTo(HaveOccurred())
		name := filepath.Join(resolver.root, key, firmwareimage.PayloadName)
		Expect(os.MkdirAll(filepath.Dir(name), 0755)).To(Succeed())
		Expect(os.WriteFile(name, []byte("test firmware"), 0444)).To(Succeed())
		return name
	}
	It("uses actual file metadata and performs validation without firmware writes", func() {
		name := publish(firstImage)
		utils.On("GetFirmwareVersionAndPSIDFromFWBinary", name).Return(version, psid, nil).Once()
		utils.On("VerifyImageBootable", name).Return(nil).Once()
		actual, err := manager.ValidateRequestedFirmwareSource(context.Background(), device)
		Expect(err).NotTo(HaveOccurred())
		Expect(actual).To(Equal(version))
		utils.AssertNotCalled(GinkgoT(), "BurnNicFirmware")
		utils.AssertNotCalled(GinkgoT(), "ResetNicFirmware")
	})
	It("selects one image by PSID without using its tag", func() {
		device.Spec.Firmware.FirmwareImages = []string{firstImage, secondImage}
		first := publish(firstImage)
		second := publish(secondImage)
		utils.On("GetFirmwareVersionAndPSIDFromFWBinary", first).Return("other-version", "other-psid", nil).Once()
		utils.On("GetFirmwareVersionAndPSIDFromFWBinary", second).Return(version, psid, nil).Once()
		utils.On("VerifyImageBootable", second).Return(nil).Once()
		actual, name, release, err := manager.resolveFirmwareImage(device)
		Expect(err).NotTo(HaveOccurred())
		Expect(actual).To(Equal(version))
		Expect(name).To(Equal(second))
		Expect(resolver.pins[secondImage]).To(Equal(1))
		release()
	})
	It("accepts PSID case variants through validation and the existing install checks", func() {
		device.Status.PSID = strings.ToUpper(psid)
		name := publish(firstImage)
		utils.On("GetFirmwareVersionAndPSIDFromFWBinary", name).Return(version, psid, nil).Times(3)
		utils.On("VerifyImageBootable", name).Return(nil).Times(3)
		actual, err := manager.ValidateRequestedFirmwareSource(context.Background(), device)
		Expect(err).NotTo(HaveOccurred())
		Expect(actual).To(Equal(version))
		utils.On("GetFirmwareVersionsFromDevice", pci).Return("older", "older", nil).Once()
		utils.On("BurnNicFirmware", context.Background(), pci, name).Return(nil).Once()
		_, err = manager.InstallFirmware(context.Background(), device, &types.FirmwareInstallOptions{
			Version: version, FwFilePath: "", SkipReset: true,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(device.Status.PSID).To(Equal(strings.ToUpper(psid)))
	})
	It("waits for an unpublished image without inspecting hardware", func() {
		_, err := manager.ValidateRequestedFirmwareSource(context.Background(), device)
		Expect(types.IsFirmwareSourceNotReadyError(err)).To(BeTrue())
		Expect(utils.Calls).To(BeEmpty())
	})
	It("rejects duplicate PSIDs even when both images declare the same version", func() {
		device.Spec.Firmware.FirmwareImages = []string{firstImage, secondImage}
		utils.On("GetFirmwareVersionAndPSIDFromFWBinary", publish(firstImage)).Return(version, psid, nil).Once()
		utils.On("GetFirmwareVersionAndPSIDFromFWBinary", publish(secondImage)).Return(version, strings.ToUpper(psid), nil).Once()
		_, err := manager.ValidateRequestedFirmwareSource(context.Background(), device)
		Expect(err).To(MatchError(ContainSubstring("same PSID")))
	})
	It("rejects a payload with no matching PSID", func() {
		utils.On("GetFirmwareVersionAndPSIDFromFWBinary", publish(firstImage)).Return(version, "wrong-psid", nil).Once()
		_, err := manager.ValidateRequestedFirmwareSource(context.Background(), device)
		Expect(err).To(MatchError(ContainSubstring("no binary for device PSID")))
	})
	It("rejects malformed metadata and unbootable binaries", func() {
		name := publish(firstImage)
		utils.On("GetFirmwareVersionAndPSIDFromFWBinary", name).Return(version, psid, nil).Once()
		utils.On("VerifyImageBootable", name).Return(errors.New("invalid image")).Once()
		_, err := manager.ValidateRequestedFirmwareSource(context.Background(), device)
		Expect(err).To(MatchError(ContainSubstring("invalid image")))
	})
	It("rejects a symlink payload", func() {
		name := publish(firstImage)
		Expect(os.Remove(name)).To(Succeed())
		Expect(os.Symlink("/etc/passwd", name)).To(Succeed())
		_, err := manager.ValidateRequestedFirmwareSource(context.Background(), device)
		Expect(err).To(MatchError(ContainSubstring("regular file")))
		Expect(utils.Calls).To(BeEmpty())
	})
	It("rejects mutable image references", func() {
		device.Spec.Firmware.FirmwareImages = []string{"registry.example/fw:latest"}
		_, err := manager.ValidateRequestedFirmwareSource(context.Background(), device)
		Expect(err).To(HaveOccurred())
		Expect(utils.Calls).To(BeEmpty())
	})
	It("rejects mutually exclusive sources and BlueField container updates", func() {
		device.Spec.Firmware.NicFirmwareSourceRef = "legacy"
		_, err := manager.ValidateRequestedFirmwareSource(context.Background(), device)
		Expect(err).To(MatchError(ContainSubstring("mutually exclusive")))
		device.Spec.Firmware.NicFirmwareSourceRef = ""
		device.Status.Type = consts.BlueField3DeviceID
		_, err = manager.ValidateRequestedFirmwareSource(context.Background(), device)
		Expect(err).To(MatchError(ContainSubstring("BlueField")))
		Expect(utils.Calls).To(BeEmpty())
	})
	It("rejects a changed requested version before reading or writing the device", func() {
		name := publish(firstImage)
		utils.On("GetFirmwareVersionAndPSIDFromFWBinary", name).Return(version, psid, nil).Once()
		utils.On("VerifyImageBootable", name).Return(nil).Once()
		_, err := manager.InstallFirmware(context.Background(), device, &types.FirmwareInstallOptions{Version: "wrong-version", FwFilePath: "", SkipReset: true})
		Expect(err).To(MatchError(ContainSubstring("does not match requested version")))
		utils.AssertNotCalled(GinkgoT(), "GetFirmwareVersionsFromDevice")
		utils.AssertNotCalled(GinkgoT(), "BurnNicFirmware")
	})
	It("installs the selected immutable payload through the existing burn flow", func() {
		name := publish(firstImage)
		utils.On("GetFirmwareVersionAndPSIDFromFWBinary", name).Return(version, psid, nil).Twice()
		utils.On("VerifyImageBootable", name).Return(nil).Twice()
		utils.On("GetFirmwareVersionsFromDevice", pci).Return("older", "older", nil).Once()
		utils.On("BurnNicFirmware", context.Background(), pci, name).Run(func(_ mock.Arguments) {
			Expect(resolver.pins[firstImage]).To(Equal(1), "keep image alive while firmware tools reopen its path")
		}).Return(nil).Once()
		_, err := manager.InstallFirmware(context.Background(), device, &types.FirmwareInstallOptions{Version: version, FwFilePath: "", SkipReset: true})
		Expect(err).NotTo(HaveOccurred())
	})
	DescribeTable("enforces PSID identity independently of firmware version at installation",
		func(known bool, filePSID string, allowed bool) {
			device.Status.PSID = strings.ToUpper(psid)
			name := publish(firstImage)
			knownPSID := ""
			if known {
				knownPSID = filePSID
			} else {
				utils.On("GetFirmwareVersionAndPSIDFromFWBinary", name).Return(version, filePSID, nil).Once()
			}
			if allowed {
				utils.On("VerifyImageBootable", name).Return(nil).Once()
				utils.On("BurnNicFirmware", context.Background(), pci, name).Return(nil).Once()
			}
			err := manager.installConnectXFirmware(context.Background(), device, version, pci, name, knownPSID)
			if allowed {
				Expect(err).NotTo(HaveOccurred())
			} else {
				Expect(err).To(HaveOccurred())
				utils.AssertNotCalled(GinkgoT(), "VerifyImageBootable")
				utils.AssertNotCalled(GinkgoT(), "BurnNicFirmware")
			}
		},
		Entry("known metadata permits a case-only difference", true, psid, true),
		Entry("known metadata rejects a different PSID", true, "mt_0000002", false),
		Entry("reread metadata permits a case-only difference", false, psid, true),
		Entry("reread metadata rejects a different PSID at the same version", false, "mt_0000002", false),
	)

})
