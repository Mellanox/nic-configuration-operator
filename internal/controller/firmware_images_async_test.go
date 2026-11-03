// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	firmwareMocks "github.com/Mellanox/nic-configuration-operator/pkg/firmware/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/firmwareimage"
	maintenanceMocks "github.com/Mellanox/nic-configuration-operator/pkg/maintenance/mocks"
	spectrumxMocks "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	udevMocks "github.com/Mellanox/nic-configuration-operator/pkg/udev/mocks"
)

var _ = Describe("asynchronous firmware image delivery", func() {
	It("reports a pending image while a ready device reaches the existing update callback", func() {
		ctx, cancel := context.WithCancel(context.Background())
		mgr := createManager()
		direct, err := client.New(cfg, client.Options{Scheme: mgr.GetScheme()})
		Expect(err).NotTo(HaveOccurred())
		namespace := createNodeAndRandomNamespace(ctx, direct)
		readyRef := "registry.example/fw@sha256:" + strings.Repeat("a", 64)
		pendingRef := "registry.example/fw@sha256:" + strings.Repeat("b", 64)
		otherRef := "registry.example/fw@sha256:" + strings.Repeat("c", 64)
		pullStarted := make(chan string, 3)
		store, err := firmwareimage.NewStore(GinkgoT().TempDir(), func(ctx context.Context, ref, dir string) error {
			select {
			case pullStarted <- ref:
			default:
			}
			if ref == pendingRef {
				<-ctx.Done()
				return ctx.Err()
			}
			return os.WriteFile(filepath.Join(dir, firmwareimage.PayloadName), []byte("hardware-free fixture"), 0444)
		}, firmwareimage.DefaultStoreOptions())
		Expect(err).NotTo(HaveOccurred())
		Expect(mgr.Add(manager.RunnableFunc(store.Run))).To(Succeed())
		ccEvents := make(chan string)
		var managerWG sync.WaitGroup
		DeferCleanup(func() {
			cancel()
			managerWG.Wait()
			Expect(store.Close()).To(Succeed())
			close(ccEvents)
			Expect(direct.DeleteAllOf(context.Background(), &v1alpha1.NicDevice{}, client.InNamespace(namespace))).To(Succeed())
			Expect(direct.Delete(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
			Expect(direct.Delete(context.Background(), &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}})).To(Succeed())
		})
		updates := make(chan string, 2)
		firmware := &firmwareMocks.FirmwareManager{}
		firmware.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).Return(
			func(_ context.Context, device *v1alpha1.NicDevice) (string, error) {
				ref := device.Spec.Firmware.FirmwareImages[0]
				_, release, err := store.Acquire(ref)
				if errors.Is(err, firmwareimage.ErrPending) {
					return "", types.FirmwareSourceNotReadyError(ref, "acquisition pending")
				}
				if err != nil {
					return "", err
				}
				release()
				return "new-version", nil
			})
		firmware.On("GetFirmwareVersionsFromDevice", mock.Anything).Return("current-version", "current-version", nil)
		firmware.On("InstallFirmware", mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				select {
				case updates <- args.Get(1).(*v1alpha1.NicDevice).Name:
				default:
				}
			}).Return(false, errors.New("hardware-free update callback; never execute firmware tools"))
		maintenance := &maintenanceMocks.MaintenanceManager{}
		maintenance.On("ReleaseMaintenance", mock.Anything).Return(nil)
		maintenance.On("ScheduleMaintenance", mock.Anything).Return(nil)
		maintenance.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
		udev := &udevMocks.UdevManager{}
		udev.On("ApplyUdevRules", mock.Anything, mock.Anything).Return(nil, false, nil)
		spectrumX := &spectrumxMocks.SpectrumXManager{}
		spectrumX.On("GetCCTerminationChannel").Return((<-chan string)(ccEvents))
		reconciler := &NicDeviceReconciler{
			Client: mgr.GetClient(), Scheme: mgr.GetScheme(), NodeName: nodeName, NamespaceName: namespace,
			ConfigurationManager: nil, MaintenanceManager: maintenance, FirmwareManager: firmware, FirmwareImages: store,
			SpectrumXManager: spectrumX, HostUtils: nil, UdevManager: udev, DeviceDiscoveryUtils: nil, EventRecorder: nil,
		}
		Expect(reconciler.SetupWithManager(mgr, false)).To(Succeed())
		for name, ref := range map[string]string{"ready": readyRef, "pending": pendingRef, "other-node": otherRef} {
			device := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
				Spec: v1alpha1.NicDeviceSpec{Firmware: &v1alpha1.FirmwareTemplateSpec{
					NicFirmwareSourceRef: "", FirmwareImages: []string{ref}, UpdatePolicy: consts.FirmwareUpdatePolicyUpdate,
				}}}
			Expect(direct.Create(ctx, device)).To(Succeed())
			device.Status.Node = nodeName
			if name == "other-node" {
				device.Status.Node = "unselected-node"
			}
			device.Status.Ports = []v1alpha1.NicDevicePortSpec{}
			device.Status.FirmwareVersion = "current-version"
			Expect(direct.Status().Update(ctx, device)).To(Succeed())
		}
		startManager(mgr, ctx, &managerWG)
		Eventually(updates, 10*time.Second).Should(Receive(Equal("ready")))
		Eventually(func() string {
			device := &v1alpha1.NicDevice{}
			Expect(direct.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "pending"}, device)).To(Succeed())
			condition := meta.FindStatusCondition(device.Status.Conditions, consts.FirmwareUpdateInProgressCondition)
			if condition == nil {
				return ""
			}
			return condition.Reason
		}, 10*time.Second).Should(Equal(consts.FirmwareSourceNotReadyReason))
		first, second := <-pullStarted, <-pullStarted
		Expect([]string{first, second}).To(ConsistOf(readyRef, pendingRef))
		Consistently(pullStarted, 100*time.Millisecond).ShouldNot(Receive())
	})
})
