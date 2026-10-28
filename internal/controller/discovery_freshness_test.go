// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"os"

	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	configurationMocks "github.com/Mellanox/nic-configuration-operator/pkg/configuration/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	firmwareMocks "github.com/Mellanox/nic-configuration-operator/pkg/firmware/mocks"
	hostMocks "github.com/Mellanox/nic-configuration-operator/pkg/host/mocks"
	maintenanceMocks "github.com/Mellanox/nic-configuration-operator/pkg/maintenance/mocks"
	udevMocks "github.com/Mellanox/nic-configuration-operator/pkg/udev/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Library discovery freshness", func() {
	var ctx context.Context
	var c client.Client
	var namespace string
	var records map[string]v1alpha1.NicDevice
	var inventory device.Inventory
	var discoveryErr error
	var statuses nicDeviceConfigurationStatuses
	var r *NicDeviceReconciler
	var registry *DeviceDiscoveryController
	var naming *udevMocks.UdevManager
	var maintenance *maintenanceMocks.MaintenanceManager
	BeforeEach(func() {
		ctx = context.Background()
		var err error
		c, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		namespace = createNodeAndRandomNamespace(ctx, c)
		previous, existed := os.LookupEnv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR)
		Expect(os.Setenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR, "true")).To(Succeed())
		DeferCleanup(func() {
			if existed {
				Expect(os.Setenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR, previous)).To(Succeed())
			} else {
				Expect(os.Unsetenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR)).To(Succeed())
			}
		})
		records = make(map[string]v1alpha1.NicDevice)
		statuses = make(nicDeviceConfigurationStatuses, 0, 2)
		discoveryErr = nil
		for i, name := range []string{"rail-a", "rail-b"} {
			d := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: v1alpha1.NicDeviceSpec{Configuration: &v1alpha1.NicDeviceConfigurationSpec{}}}
			Expect(c.Create(ctx, d)).To(Succeed())
			pci := []string{"0000:64:00.0", "0001:64:00.0"}[i]
			d.Status = v1alpha1.NicDeviceStatus{Node: nodeName, Type: "1023", PSID: "test-psid", FirmwareVersion: "40.1", SerialNumber: name, Ports: []v1alpha1.NicDevicePortSpec{{PCI: pci, NetworkInterface: "eth" + name, RdmaInterface: "rdma" + name}}}
			setDiscoveryCondition(d, nil)
			Expect(c.Status().Update(ctx, d)).To(Succeed())
			records[name] = *d.DeepCopy()
			statuses = append(statuses, &nicDeviceConfigurationStatus{device: d})
		}
		inventory = inventoryFixture(records)
		discover := func(context.Context) (device.Inventory, error) { return inventory, discoveryErr }
		naming = &udevMocks.UdevManager{}
		maintenance = &maintenanceMocks.MaintenanceManager{}
		r = &NicDeviceReconciler{Client: c, NodeName: nodeName, NamespaceName: namespace, Discover: discover, UdevManager: naming, MaintenanceManager: maintenance, ReadInterfaceNames: func(_ context.Context, pci string) (device.InterfaceNames, error) {
			for _, d := range inventory.Devices {
				for _, p := range d.Ports {
					if p.PCI == pci {
						return device.InterfaceNames{Netdev: p.Netdev, RDMA: p.RDMA}, nil
					}
				}
			}
			return device.InterfaceNames{}, errors.New("PF unavailable")
		}}
		host := &hostMocks.HostUtils{}
		host.On("DiscoverOfedVersion").Return("unknown")
		registry = NewDeviceDiscoveryController(c, c, discover, host, nodeName, namespace)
	})
	AfterEach(func() {
		Expect(c.DeleteAllOf(ctx, &v1alpha1.NicDevice{}, client.InNamespace(namespace))).To(Succeed())
		Expect(c.Delete(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
		Expect(c.DeleteAllOf(ctx, &v1.Node{})).To(Succeed())
	})
	scopedFailure := func() {
		inventory.Issues = []device.DiscoveryIssue{{PCI: "0001:64:00.0", Err: errors.New("firmware read failed")}}
	}
	It("never changes another namespace's device for the same host and ASIC", func() {
		otherNamespace := namespace + "-other"
		Expect(c.Create(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: otherNamespace}})).To(Succeed())
		DeferCleanup(func() {
			Expect(c.DeleteAllOf(ctx, &v1alpha1.NicDevice{}, client.InNamespace(otherNamespace))).To(Succeed())
			Expect(c.Delete(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: otherNamespace}})).To(Succeed())
		})
		foreign := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "foreign-device", Namespace: otherNamespace}}
		Expect(c.Create(ctx, foreign)).To(Succeed())
		foreign.Status = *statuses[0].device.Status.DeepCopy()
		Expect(c.Status().Update(ctx, foreign)).To(Succeed())
		original := foreign.DeepCopy()
		// This registry deliberately uses an unrestricted client. A namespaced
		// manager cache would hide the ownership bug instead of testing it.
		Expect(registry.reconcile(ctx)).To(Succeed())
		Expect(c.Get(ctx, client.ObjectKeyFromObject(foreign), foreign)).To(Succeed())
		Expect(foreign.ResourceVersion).To(Equal(original.ResourceVersion))
		for _, status := range statuses {
			d := &v1alpha1.NicDevice{}
			Expect(c.Get(ctx, client.ObjectKeyFromObject(status.device), d)).To(Succeed())
			Expect(d.UID).To(Equal(status.device.UID))
		}
	})
	It("retains failed CR data, marks it stale and recovers from a fresh pass", func() {
		scopedFailure()
		Expect(registry.reconcile(ctx)).To(Succeed())
		d := &v1alpha1.NicDevice{}
		Expect(c.Get(ctx, client.ObjectKeyFromObject(statuses[1].device), d)).To(Succeed())
		Expect(d.Status.Ports).To(Equal(records["rail-b"].Status.Ports))
		Expect(d.Status.SerialNumber).To(Equal("rail-b"))
		Expect(meta.IsStatusConditionFalse(d.Status.Conditions, consts.DiscoveryReadyCondition)).To(BeTrue())
		inventory.Issues = nil
		Expect(registry.reconcile(ctx)).To(Succeed())
		Expect(c.Get(ctx, client.ObjectKeyFromObject(d), d)).To(Succeed())
		Expect(meta.IsStatusConditionTrue(d.Status.Conditions, consts.DiscoveryReadyCondition)).To(BeTrue())
	})
	It("marks all retained CRs stale on a global error even with useful devices", func() {
		discoveryErr = errors.New("DMS inventory unavailable")
		Expect(registry.reconcile(ctx)).To(MatchError(ContainSubstring("DMS inventory unavailable")))
		for _, status := range statuses {
			d := &v1alpha1.NicDevice{}
			Expect(c.Get(ctx, client.ObjectKeyFromObject(status.device), d)).To(Succeed())
			Expect(meta.IsStatusConditionFalse(d.Status.Conditions, consts.DiscoveryReadyCondition)).To(BeTrue())
			Expect(d.Status.Ports).NotTo(BeEmpty())
		}
	})
	It("never deletes unobserved CRs on an incomplete scan", func() {
		inventory = device.Inventory{Complete: false}
		Expect(registry.reconcile(ctx)).To(HaveOccurred())
		list := &v1alpha1.NicDeviceList{}
		Expect(c.List(ctx, list, client.InNamespace(namespace))).To(Succeed())
		Expect(list.Items).To(HaveLen(2))
	})
	It("allows only the independently fresh ASIC and does not apply global naming", func() {
		scopedFailure()
		fresh, pending, err := r.freshDevices(ctx, statuses)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		Expect(fresh).To(HaveLen(1))
		Expect(fresh[0].device.Name).To(Equal("rail-a"))
		naming.AssertNotCalled(GinkgoT(), "ApplyUdevRules", mock.Anything, mock.Anything)
	})
	It("preserves fail-whole-scan policy when the skip switch is disabled", func() {
		Expect(os.Setenv(consts.SKIP_DEVICE_ON_DISCOVERY_ERROR, "false")).To(Succeed())
		scopedFailure()
		fresh, pending, err := r.freshDevices(ctx, statuses)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		Expect(fresh).To(BeEmpty())
	})
	It("refuses a retained PF subset and replacement metadata at the same address", func() {
		for i := range inventory.Devices {
			if inventory.Devices[i].ID == "rail-a" {
				inventory.Devices[i].Ports = append(inventory.Devices[i].Ports, device.Port{PCI: "0000:64:00.1"})
			} else {
				inventory.Devices[i].SerialNumber = "replacement"
			}
		}
		fresh, pending, err := r.freshDevices(ctx, statuses)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		Expect(fresh).To(BeEmpty())
	})
	It("keeps all node plan participants pending if one rail is stale", func() {
		for _, status := range statuses {
			status.device.Spec.Configuration.Template = &v1alpha1.ConfigurationTemplateSpec{LinkType: "Ethernet", NumVfs: 1, SpectrumXOptimized: &v1alpha1.SpectrumXOptimizedSpec{Enabled: true, Version: "RA2.1", PlatformType: "h100"}}
			Expect(c.Update(ctx, status.device)).To(Succeed())
		}
		scopedFailure()
		fresh, pending, err := r.freshDevices(ctx, statuses)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		Expect(fresh).To(BeEmpty())
	})
	It("keeps an input CR pending if it disappeared before freshness validation", func() {
		Expect(c.Delete(ctx, statuses[0].device)).To(Succeed())
		fresh, pending, err := r.freshDevices(ctx, statuses)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		Expect(fresh).To(HaveLen(1))
		Expect(fresh[0].device.Name).To(Equal("rail-b"))
		naming.AssertNotCalled(GinkgoT(), "ApplyUdevRules", mock.Anything, mock.Anything)
	})
	It("keeps a Network Bay participant pending until its peer CR exists", func() {
		for i, status := range statuses {
			status.device.Status.NetworkBay = &v1alpha1.NicDeviceNetworkBayStatus{Asic: i, PeerPCI: statuses[1-i].device.Status.Ports[0].PCI}
			Expect(c.Status().Update(ctx, status.device)).To(Succeed())
			records[status.device.Name] = *status.device.DeepCopy()
		}
		inventory = inventoryFixture(records)
		Expect(c.Delete(ctx, statuses[1].device)).To(Succeed())
		fresh, pending, err := r.freshDevices(ctx, statuses[:1])
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		Expect(fresh).To(BeEmpty())
		naming.AssertNotCalled(GinkgoT(), "ApplyUdevRules", mock.Anything, mock.Anything)
	})
	It("keeps host-wide operations pending for a scoped issue without a CR", func() {
		inventory.Issues = []device.DiscoveryIssue{{PCI: "0002:64:00.0", Err: errors.New("unregistered ASIC unreadable")}}
		fresh, pending, err := r.freshDevices(ctx, statuses)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		Expect(fresh).To(HaveLen(2))
		configuration := &configurationMocks.ConfigurationManager{}
		configuration.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, true, []string(nil), nil)
		r.ConfigurationManager = configuration
		result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: nicDeviceSyncEventName}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(shortRequeueTime))
		naming.AssertNotCalled(GinkgoT(), "ApplyUdevRules", mock.Anything, mock.Anything)
		maintenance.AssertNotCalled(GinkgoT(), "Reboot")
	})
	It("does not release maintenance when every active device is stale", func() {
		inventory.Complete = false
		result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: nicDeviceSyncEventName}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(shortRequeueTime))
		maintenance.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
		naming.AssertNotCalled(GinkgoT(), "ApplyUdevRules", mock.Anything, mock.Anything)
	})
	It("does not reboot the host from a fresh subset", func() {
		scopedFailure()
		configuration := &configurationMocks.ConfigurationManager{}
		configuration.On("ValidateDeviceNvSpec", mock.Anything, mock.Anything).Return(false, true, []string(nil), nil)
		r.ConfigurationManager = configuration
		result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: nicDeviceSyncEventName}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(shortRequeueTime))
		maintenance.AssertNotCalled(GinkgoT(), "Reboot")
		maintenance.AssertNotCalled(GinkgoT(), "ReleaseMaintenance", mock.Anything)
	})

	It("defers global naming but admits a fresh owner only when current names match", func() {
		for _, status := range statuses {
			d := status.device
			d.Spec.InterfaceNameTemplate = &v1alpha1.NicDeviceInterfaceNameSpec{PlaneIndices: []int{0}, NetDevicePrefix: d.Status.Ports[0].NetworkInterface, RdmaDevicePrefix: d.Status.Ports[0].RdmaInterface}
			Expect(c.Update(ctx, d)).To(Succeed())
		}
		scopedFailure()
		fresh, pending, err := r.freshDevices(ctx, statuses)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		Expect(fresh).To(HaveLen(1))
		naming.AssertNotCalled(GinkgoT(), "ApplyUdevRules", mock.Anything, mock.Anything)
		r.ReadInterfaceNames = func(context.Context, string) (device.InterfaceNames, error) {
			return device.InterfaceNames{Netdev: "wrong", RDMA: "wrong"}, nil
		}
		fresh, pending, err = r.freshDevices(ctx, statuses)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		Expect(fresh).To(BeEmpty())
	})
	It("waits for complete naming ownership before removing previous rules", func() {
		d := statuses[0].device
		meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{Type: consts.InterfaceNameCondition, Status: metav1.ConditionTrue, Reason: consts.InterfaceNameAppliedReason})
		Expect(c.Status().Update(ctx, d)).To(Succeed())
		scopedFailure()
		fresh, pending, err := r.freshDevices(ctx, statuses)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeTrue())
		Expect(fresh).To(BeEmpty())
	})
	It("returns after firmware activation before any configuration uses old endpoints", func() {
		d := statuses[0].device
		d.Spec.Firmware = &v1alpha1.FirmwareTemplateSpec{UpdatePolicy: consts.FirmwareUpdatePolicyUpdate, NicFirmwareSourceRef: "test-source"}
		Expect(c.Update(ctx, d)).To(Succeed())
		firmware := &firmwareMocks.FirmwareManager{}
		firmware.On("ValidateRequestedFirmwareSource", mock.Anything, mock.Anything).Return("40.2", nil)
		firmware.On("GetFirmwareVersionsFromDevice", mock.Anything).Return("40.1", "40.1", nil)
		firmware.On("InstallFirmware", mock.Anything, mock.Anything, mock.Anything).Return(false, nil)
		configuration := &configurationMocks.ConfigurationManager{}
		configuration.On("ResetNicFirmware", mock.Anything, mock.Anything).Return(nil)
		r.FirmwareManager = firmware
		r.ConfigurationManager = configuration
		naming.On("ApplyUdevRules", mock.Anything, mock.Anything).Return(nil, false, nil)
		maintenance.On("ScheduleMaintenance", mock.Anything).Return(nil)
		maintenance.On("MaintenanceAllowed", mock.Anything).Return(true, nil)
		result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKey{Name: nicDeviceSyncEventName}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(shortRequeueTime))
		configuration.AssertCalled(GinkgoT(), "ResetNicFirmware", mock.Anything, mock.Anything)
		configuration.AssertNotCalled(GinkgoT(), "ValidateDeviceNvSpec", mock.Anything, mock.Anything)
		configuration.AssertNotCalled(GinkgoT(), "ApplyRuntimeConfiguration", mock.Anything, mock.Anything)
		Expect(c.Get(ctx, client.ObjectKeyFromObject(d), d)).To(Succeed())
		Expect(d.Status.FirmwareVersion).To(Equal("40.2"))
	})
	It("retains last-known names on a failed read, including partial reader output", func() {
		r.ReadInterfaceNames = func(context.Context, string) (device.InterfaceNames, error) {
			return device.InterfaceNames{Netdev: "partial"}, errors.New("unreadable RDMA directory")
		}
		before := statuses[0].device.Status.Ports[0]
		Expect(r.refreshInterfaceNames(ctx, statuses[0].device, nil)).To(MatchError(ContainSubstring("unreadable RDMA directory")))
		Expect(statuses[0].device.Status.Ports[0]).To(Equal(before))
	})
})
