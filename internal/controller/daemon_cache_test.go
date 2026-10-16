/*
2026 NVIDIA CORPORATION & AFFILIATES
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
	"errors"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	deviceDiscoveryMocks "github.com/Mellanox/nic-configuration-operator/pkg/devicediscovery/mocks"
	hostMocks "github.com/Mellanox/nic-configuration-operator/pkg/host/mocks"
)

const otherNodeName = "other-node"

func createNodeScopedManager() manager.Manager {
	byObject := map[client.Object]cache.ByObject{}
	AddNodeScopedCacheObjects(byObject, nodeName, true)

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:     scheme.Scheme,
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
		Cache:      cache.Options{ByObject: byObject},
	})
	Expect(err).NotTo(HaveOccurred())

	err = mgr.GetCache().IndexField(context.Background(), &v1alpha1.NicDevice{}, "status.node", func(o client.Object) []string {
		return []string{o.(*v1alpha1.NicDevice).Status.Node}
	})
	Expect(err).NotTo(HaveOccurred())

	return mgr
}

func createNicDeviceOnNode(ctx context.Context, c client.Client, namespace, name, node string) {
	device := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	Expect(c.Create(ctx, device)).To(Succeed())
	device.Status = v1alpha1.NicDeviceStatus{
		Node:  node,
		Type:  "connectx7",
		Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:3b:00.0"}},
	}
	Expect(c.Status().Update(ctx, device)).To(Succeed())
}

var _ = Describe("NicDeviceNodeSelectorSupported", func() {
	listErrorClient := func(listErr error) client.Client {
		return fake.NewClientBuilder().WithScheme(scheme.Scheme).WithInterceptorFuncs(interceptor.Funcs{
			List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
				return listErr
			},
		}).Build()
	}

	It("should report unsupported when the API server rejects the field selector", func() {
		c := listErrorClient(apierrors.NewBadRequest("field label not supported: status.node"))
		supported, err := NicDeviceNodeSelectorSupported(context.Background(), c, "ns", nodeName)
		Expect(err).NotTo(HaveOccurred())
		Expect(supported).To(BeFalse())
	})

	It("should return other list errors", func() {
		listErr := errors.New("connection refused")
		c := listErrorClient(listErr)
		_, err := NicDeviceNodeSelectorSupported(context.Background(), c, "ns", nodeName)
		Expect(err).To(MatchError(listErr))
	})
})

var _ = Describe("Node-scoped daemon cache", func() {
	var (
		mgr           manager.Manager
		k8sClient     client.Client
		ctx           context.Context
		cancel        context.CancelFunc
		namespaceName string
		wg            sync.WaitGroup
		timeout       = time.Second * 10
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.TODO())
		mgr = createNodeScopedManager()
		k8sClient = mgr.GetClient()
		namespaceName = createNodeAndRandomNamespace(ctx, k8sClient)
		Expect(k8sClient.Create(ctx, &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: otherNodeName}})).To(Succeed())
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &v1alpha1.NicDevice{}, client.InNamespace(namespaceName))).To(Succeed())
		Expect(k8sClient.Delete(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &v1.Node{})).To(Succeed())
		cancel()
		wg.Wait()
	})

	It("should be supported by the API server with the generated NicDevice CRD", func() {
		supported, err := NicDeviceNodeSelectorSupported(ctx, mgr.GetAPIReader(), namespaceName, nodeName)
		Expect(err).NotTo(HaveOccurred())
		Expect(supported).To(BeTrue())
	})

	It("should cache only this node's NicDevices and Node object", func() {
		createNicDeviceOnNode(ctx, k8sClient, namespaceName, "local-device", nodeName)
		createNicDeviceOnNode(ctx, k8sClient, namespaceName, "remote-device", otherNodeName)

		startManager(mgr, ctx, &wg)

		Eventually(func(g Gomega) {
			devices := &v1alpha1.NicDeviceList{}
			g.Expect(k8sClient.List(ctx, devices, client.InNamespace(namespaceName))).To(Succeed())
			g.Expect(devices.Items).To(HaveLen(1))
			g.Expect(devices.Items[0].Name).To(Equal("local-device"))
		}).WithTimeout(timeout).Should(Succeed())

		nodes := &v1.NodeList{}
		Expect(k8sClient.List(ctx, nodes)).To(Succeed())
		Expect(nodes.Items).To(HaveLen(1))
		Expect(nodes.Items[0].Name).To(Equal(nodeName))

		allDevices := &v1alpha1.NicDeviceList{}
		Expect(mgr.GetAPIReader().List(ctx, allDevices, client.InNamespace(namespaceName))).To(Succeed())
		Expect(allDevices.Items).To(HaveLen(2))
	})

	It("should let device discovery adopt a NicDevice whose status.node was never set", func() {
		deviceDiscovery := &deviceDiscoveryMocks.DeviceDiscovery{}
		hostUtils := &hostMocks.HostUtils{}
		discoveryController := NewDeviceDiscoveryController(
			k8sClient, mgr.GetAPIReader(), deviceDiscovery, hostUtils, nodeName, namespaceName)
		Expect(mgr.Add(discoveryController)).To(Succeed())

		pciDevKey := "0000:3b:00"
		deviceType := "connectx7"
		deviceName := discoveryController.getCRName(deviceType, pciDevKey)
		Expect(k8sClient.Create(ctx, &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespaceName},
		})).To(Succeed())

		deviceDiscovery.On("DiscoverNicDevices").Return(map[string]v1alpha1.NicDevice{
			pciDevKey: {
				Status: v1alpha1.NicDeviceStatus{
					SerialNumber: "orphan-serial",
					Type:         deviceType,
					Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:3b:00.0"}},
				},
			},
		}, nil)
		hostUtils.On("DiscoverOfedVersion").Return("00.00-0.0.0", nil)

		startManager(mgr, ctx, &wg)

		Eventually(func(g Gomega) {
			device := &v1alpha1.NicDevice{}
			g.Expect(mgr.GetAPIReader().Get(ctx, client.ObjectKey{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
			g.Expect(device.Status.Node).To(Equal(nodeName))
			g.Expect(device.Status.SerialNumber).To(Equal("orphan-serial"))
		}).WithTimeout(timeout).Should(Succeed())
	})
})
