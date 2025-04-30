/*
2024 NVIDIA CORPORATION & AFFILIATES
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"sync"
	"time"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/host/mocks"
)

var _ = Describe("DeviceDiscoveryController", func() {
	var (
		mgr            manager.Manager
		k8sClient      client.Client
		deviceRegistry *DeviceDiscoveryController
		hostManager    *mocks.HostManager
		ctx            context.Context
		cancel         context.CancelFunc
		timeout        = time.Second * 10
		namespaceName  string
		wg             sync.WaitGroup
		err            error
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.TODO())

		mgr = createManager()

		k8sClient = mgr.GetClient()

		namespaceName = createNodeAndRandomNamespace(ctx, k8sClient)

		deviceDiscoveryReconcileTime = 1 * time.Second
		hostManager = &mocks.HostManager{}

		deviceRegistry = NewDeviceDiscoveryController(k8sClient, hostManager, nodeName, namespaceName)
		Expect(mgr.Add(deviceRegistry)).To(Succeed())
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &v1alpha1.NicDevice{}, client.InNamespace(namespaceName))).To(Succeed())
		Expect(k8sClient.Delete(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &v1.Node{})).To(Succeed())
		cancel()
		wg.Wait()
	})

	Describe("getCRName", func() {
		It("should return the correct CR name", func() {
			name := deviceRegistry.getCRName("nic", "123456")
			Expect(name).To(Equal("test-node-nic-123456"))
		})

		It("should return the correct CR name for capitalized strings", func() {
			name := deviceRegistry.getCRName("NIC", "123456")
			Expect(name).To(Equal("test-node-nic-123456"))

			name = deviceRegistry.getCRName("nic", "SERIALNUMBER")
			Expect(name).To(Equal("test-node-nic-serialnumber"))
		})
	})

	Describe("setInitialsConditionsForDevice", func() {
		It("should set the initial condition for the device", func() {
			device := &v1alpha1.NicDevice{}
			setInitialsConditionsForDevice(device)

			condition := meta.FindStatusCondition(device.Status.Conditions, consts.ConfigUpdateInProgressCondition)
			Expect(condition).ToNot(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionFalse))
			Expect(condition.Reason).To(Equal(consts.DeviceConfigSpecEmptyReason))
			Expect(condition.Message).To(Equal("Device configuration spec is empty, cannot update configuration"))
		})
	})

	Describe("reconcile", func() {
		Context("when there are existing NicDevice CRs", func() {
			deviceType := "connectx6"
			serialNumber := "123456"
			deviceName := "test-node-connectx6-123456"
			BeforeEach(func() {
				existingDevice := &v1alpha1.NicDevice{
					ObjectMeta: metav1.ObjectMeta{
						Name:      deviceName,
						Namespace: namespaceName,
					},
				}
				Expect(k8sClient.Create(ctx, existingDevice)).To(Succeed())

				existingDevice.Status = v1alpha1.NicDeviceStatus{
					Node:         nodeName,
					SerialNumber: serialNumber,
					Type:         deviceType,
					Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:3b:00.0"}},
				}

				Expect(k8sClient.Status().Update(ctx, existingDevice)).To(Succeed())
			})

			It("should update CRs if status changes", func() {
				partNumber := "test-part-number"
				fwVersion := "test-fw-version"

				hostManager.On("DiscoverNicDevices").Return(map[string]v1alpha1.NicDeviceStatus{
					"123456": {
						Node:            nodeName,
						SerialNumber:    "123456",
						Type:            "connectx6",
						Ports:           []v1alpha1.NicDevicePortSpec{{PCI: "0000:3b:00.0"}},
						PartNumber:      partNumber,
						FirmwareVersion: fwVersion,
					},
				}, nil)
				hostManager.On("DiscoverOfedVersion").Return("00.00-0.0.0", nil)

				startManager(mgr, ctx, &wg)

				Eventually(func() (string, error) {
					device := &v1alpha1.NicDevice{}
					err := k8sClient.Get(ctx, client.ObjectKey{
						Name:      deviceName,
						Namespace: namespaceName,
					}, device)

					if err != nil {
						return "", nil
					}

					return device.Status.PartNumber, nil
				}, timeout).Should(Equal(partNumber))

				Eventually(func() (string, error) {
					device := &v1alpha1.NicDevice{}
					err := k8sClient.Get(ctx, client.ObjectKey{
						Name:      deviceName,
						Namespace: namespaceName,
					}, device)

					if err != nil {
						return "", nil
					}

					return device.Status.FirmwareVersion, nil
				}, timeout).Should(Equal(fwVersion))
			})

			It("should delete CRs if they do not represent observed devices", func() {
				hostManager.On("DiscoverNicDevices").Return(map[string]v1alpha1.NicDeviceStatus{}, nil)

				startManager(mgr, ctx, &wg)

				Eventually(func() (int, error) {
					list := &v1alpha1.NicDeviceList{}
					err := k8sClient.List(ctx, list, client.InNamespace(namespaceName))
					return len(list.Items), err
				}, timeout).Should(Equal(0))
			})

			It("should create new CRs for new devices", func() {
				serialNumber := "new-serial-num"

				// Add a new device that does not have a CR representation
				hostManager.On("DiscoverNicDevices").Return(map[string]v1alpha1.NicDeviceStatus{
					serialNumber: {
						SerialNumber: serialNumber,
						Type:         deviceType,
						Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:81:00.0"}},
					},
				}, nil)
				hostManager.On("DiscoverOfedVersion").Return("00.00-0.0.0", nil)

				startManager(mgr, ctx, &wg)

				Eventually(func() (string, error) {
					device := &v1alpha1.NicDevice{}
					err := k8sClient.Get(ctx, client.ObjectKey{
						Name:      deviceRegistry.getCRName(deviceType, serialNumber),
						Namespace: namespaceName,
					}, device)
					if err != nil {
						return "", nil
					}
					return device.Status.SerialNumber, err
				}, timeout).Should(Equal(serialNumber))

				device := &v1alpha1.NicDevice{}
				err = k8sClient.Get(ctx, client.ObjectKey{
					Name:      deviceRegistry.getCRName(deviceType, serialNumber),
					Namespace: namespaceName,
				}, device)
				Expect(err).NotTo(HaveOccurred())

				Expect(device.ObjectMeta.OwnerReferences).To(HaveLen(1))
				Expect(device.ObjectMeta.OwnerReferences[0].Name).To(Equal(nodeName))
			})
		})
	})
})
