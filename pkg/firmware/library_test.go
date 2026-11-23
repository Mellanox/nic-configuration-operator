// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package firmware

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/Mellanox/dms-lib/pkg/device"
	libfirmware "github.com/Mellanox/dms-lib/pkg/firmware"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type firmwareBackendStub struct {
	versions func(context.Context, device.Device) (libfirmware.Versions, error)
	binary   func(context.Context, string) (libfirmware.BinaryInfo, error)
	bfb      func(context.Context, string) (map[string]string, error)
	install  func(context.Context, device.Device, libfirmware.InstallOptions) error
	reset    func(context.Context, device.Device, libfirmware.ResetOptions) error
}

func (b *firmwareBackendStub) QueryVersions(c context.Context, d device.Device) (libfirmware.Versions, error) {
	return b.versions(c, d)
}
func (b *firmwareBackendStub) InspectBinary(c context.Context, p string) (libfirmware.BinaryInfo, error) {
	return b.binary(c, p)
}
func (b *firmwareBackendStub) InspectBFB(c context.Context, p string) (map[string]string, error) {
	return b.bfb(c, p)
}
func (b *firmwareBackendStub) Install(c context.Context, d device.Device, o libfirmware.InstallOptions) error {
	return b.install(c, d, o)
}
func (b *firmwareBackendStub) Reset(c context.Context, d device.Device, o libfirmware.ResetOptions) error {
	return b.reset(c, d, o)
}

var _ = Describe("Firmware library adapter", func() {
	var backend *firmwareBackendStub
	var manager firmwareManager
	var nic *v1alpha1.NicDevice
	var options *types.FirmwareInstallOptions
	BeforeEach(func() {
		backend = &firmwareBackendStub{}
		manager = firmwareManager{backend: backend, cacheRootDir: GinkgoT().TempDir(), tmpDir: GinkgoT().TempDir()}
		nic = &v1alpha1.NicDevice{Spec: v1alpha1.NicDeviceSpec{Firmware: &v1alpha1.FirmwareTemplateSpec{NicFirmwareSourceRef: "source"}}, Status: v1alpha1.NicDeviceStatus{Type: "1023", PSID: "psid", Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:03:00.0"}, {PCI: "0000:03:00.1"}}}}
		options = &types.FirmwareInstallOptions{Version: "2", FwFilePath: "/provided.bin", SkipReset: true}
		backend.versions = func(context.Context, device.Device) (libfirmware.Versions, error) {
			return libfirmware.Versions{Burned: "1", Running: "1"}, nil
		}
	})
	It("rejects missing input before backend work", func() {
		_, err := manager.InstallFirmware(context.Background(), nil, options)
		Expect(err).To(HaveOccurred())
		_, err = manager.InstallFirmware(context.Background(), nic, nil)
		Expect(err).To(HaveOccurred())
		nic.Status.Ports = nil
		_, _, err = manager.GetFirmwareVersionsFromDevice(nic)
		Expect(err).To(HaveOccurred())
	})
	It("skips an already burned version before resolving a missing cache or resetting", func() {
		options.FwFilePath = ""
		options.SkipReset = false
		backend.versions = func(context.Context, device.Device) (libfirmware.Versions, error) {
			return libfirmware.Versions{Burned: "2", Running: "1"}, nil
		}
		reboot, err := manager.InstallFirmware(context.Background(), nic, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(reboot).To(BeFalse())
	})
	It("continues installation after version-query failure and preserves observed ASIC endpoints", func() {
		backend.versions = func(context.Context, device.Device) (libfirmware.Versions, error) {
			return libfirmware.Versions{}, errors.New("query failed")
		}
		backend.install = func(_ context.Context, d device.Device, o libfirmware.InstallOptions) error {
			Expect(d.ID).To(Equal("0000:03:00"))
			Expect(d.Type).To(Equal("1023"))
			Expect(d.PSID).To(Equal("psid"))
			Expect(d.Ports).To(HaveLen(2))
			Expect(d.Ports[1].PCI).To(Equal("0000:03:00.1"))
			Expect(o).To(Equal(libfirmware.InstallOptions{Path: "/provided.bin", Version: "2"}))
			return nil
		}
		reboot, err := manager.InstallFirmware(context.Background(), nic, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(reboot).To(BeFalse())
	})
	It("infers a BIN version before idempotency and delegates verification to Install", func() {
		options.Version = ""
		backend.binary = func(_ context.Context, p string) (libfirmware.BinaryInfo, error) {
			Expect(p).To(Equal(options.FwFilePath))
			return libfirmware.BinaryInfo{Version: "2", PSID: "psid"}, nil
		}
		backend.install = func(_ context.Context, _ device.Device, o libfirmware.InstallOptions) error {
			Expect(o.Version).To(Equal("2"))
			return errors.New("artifact rejected")
		}
		_, err := manager.InstallFirmware(context.Background(), nic, options)
		Expect(err).To(MatchError("artifact rejected"))
	})
	It("selects the matching BFB device component and delegates all PFs", func() {
		options.Version = ""
		options.FwFilePath = "/provided.bfb"
		nic.Status.Type = consts.BlueField3DeviceID
		backend.bfb = func(context.Context, string) (map[string]string, error) {
			return map[string]string{consts.BlueField2DeviceID: "1", consts.BlueField3DeviceID: "2"}, nil
		}
		backend.install = func(_ context.Context, d device.Device, o libfirmware.InstallOptions) error {
			Expect(d.Ports).To(HaveLen(2))
			Expect(o.Version).To(Equal("2"))
			return nil
		}
		reboot, err := manager.InstallFirmware(context.Background(), nic, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(reboot).To(BeFalse())
	})
	It("rejects a BFB without the selected device component before install", func() {
		options.Version = ""
		nic.Status.Type = consts.BlueField3DeviceID
		backend.bfb = func(context.Context, string) (map[string]string, error) {
			return map[string]string{consts.BlueField2DeviceID: "2"}, nil
		}
		_, err := manager.InstallFirmware(context.Background(), nic, options)
		Expect(err).To(MatchError(ContainSubstring("does not contain firmware")))
	})
	DescribeTable("preserves reset fallback and the firmware-specific VM check policy", func(resetErr error, skipVM bool) {
		GinkgoT().Setenv(consts.SKIP_VM_CHECK, "false")
		if skipVM {
			GinkgoT().Setenv(consts.SKIP_VM_CHECK, "true")
		}
		options.SkipReset = false
		backend.install = func(context.Context, device.Device, libfirmware.InstallOptions) error { return nil }
		backend.reset = func(_ context.Context, _ device.Device, o libfirmware.ResetOptions) error {
			Expect(o.SkipVMCheck).To(Equal(skipVM))
			return resetErr
		}
		reboot, err := manager.InstallFirmware(context.Background(), nic, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(reboot).To(Equal(resetErr != nil))
	}, Entry("successful reset", nil, false), Entry("successful VM-skip reset", nil, true), Entry("failed reset requires reboot", errors.New("reset failed"), false))
	It("reports absent cache and absent source before any install", func() {
		options.FwFilePath = ""
		_, err := manager.InstallFirmware(context.Background(), nic, options)
		Expect(err).To(HaveOccurred())
		nic.Spec.Firmware = nil
		_, err = manager.InstallFirmware(context.Background(), nic, options)
		Expect(err).To(MatchError("device's firmware spec is empty"))
	})
	It("copies a unique cached BIN locally before delegation", func() {
		options.FwFilePath = ""
		dir := filepath.Join(manager.cacheRootDir, "source", consts.NicFirmwareBinariesFolder, "2", "psid")
		Expect(os.MkdirAll(dir, 0755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "fw.bin"), []byte("image"), 0600)).To(Succeed())
		backend.install = func(_ context.Context, _ device.Device, o libfirmware.InstallOptions) error {
			Expect(o.Path).To(Equal(filepath.Join(manager.tmpDir, "fw.bin")))
			data, err := os.ReadFile(o.Path)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal("image"))
			return nil
		}
		_, err := manager.InstallFirmware(context.Background(), nic, options)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(dir, "second.bin"), []byte("image"), 0600)).To(Succeed())
		_, err = manager.InstallFirmware(context.Background(), nic, options)
		Expect(err).To(MatchError(ContainSubstring("found second FW binary")))
	})
	It("resolves a cached BFB and propagates install failure without reset", func() {
		options.FwFilePath = ""
		options.SkipReset = false
		nic.Status.Type = consts.BlueField3DeviceID
		dir := filepath.Join(manager.cacheRootDir, "source", consts.BFBFolder)
		Expect(os.MkdirAll(dir, 0755)).To(Succeed())
		file := filepath.Join(dir, "image.bfb")
		Expect(os.WriteFile(file, []byte("image"), 0600)).To(Succeed())
		backend.install = func(_ context.Context, _ device.Device, o libfirmware.InstallOptions) error {
			Expect(o.Path).To(Equal(file))
			return errors.New("install failed")
		}
		reboot, err := manager.InstallFirmware(context.Background(), nic, options)
		Expect(err).To(MatchError("install failed"))
		Expect(reboot).To(BeFalse())
	})
	It("delegates provisioning metadata inspection without host-tool duplication", func() {
		backend.binary = func(_ context.Context, p string) (libfirmware.BinaryInfo, error) {
			Expect(p).To(Equal("fw.bin"))
			return libfirmware.BinaryInfo{Version: "2", PSID: "psid"}, nil
		}
		backend.bfb = func(_ context.Context, p string) (map[string]string, error) {
			Expect(p).To(Equal("fw.bfb"))
			return nil, errors.New("invalid BFB")
		}
		utility := &utils{inspector: backend}
		version, psid, err := utility.GetFirmwareVersionAndPSIDFromFWBinary("fw.bin")
		Expect(err).NotTo(HaveOccurred())
		Expect(version).To(Equal("2"))
		Expect(psid).To(Equal("psid"))
		_, err = utility.GetFWVersionsFromBFB("fw.bfb")
		Expect(err).To(MatchError("invalid BFB"))
	})
})
