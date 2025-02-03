/*
Copyright 2024.

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
	"slices"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
)

func getDeviceSpecTemplate(ctx context.Context, name string, namespace string, client client.Client) func() (*v1alpha1.ConfigurationTemplateSpec, error) {
	return func() (*v1alpha1.ConfigurationTemplateSpec, error) {
		device := &v1alpha1.NicDevice{}
		err := client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, device)
		if err != nil {
			return nil, err
		}
		if device.Spec.Configuration == nil {
			return nil, nil
		}

		return device.Spec.Configuration.Template, nil
	}
}

func getMatchedDevicesFromStatus(ctx context.Context, name string, namespace string, client client.Client) func() []string {
	return func() []string {
		templateObj := &v1alpha1.NicConfigurationTemplate{}
		Expect(client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, templateObj)).To(Succeed())
		devices := templateObj.Status.NicDevices
		slices.Sort(devices)
		return devices
	}
}

var _ = Describe("NicConfigurationTemplate Controller", func() {
	var (
		mgr          manager.Manager
		k8sClient    client.Client
		reconciler   *NicConfigurationTemplateReconciler
		nodeName     = "test-node"
		templateName = "test-template"
		deviceName   = "test-device"
		ctx          context.Context
		cancel       context.CancelFunc
		//timeout       = time.Second * 10
		namespaceName string

		err error
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		mgr, err = ctrl.NewManager(cfg, ctrl.Options{
			Scheme:     scheme.Scheme,
			Metrics:    metricsserver.Options{BindAddress: "0"},
			Controller: config.Controller{SkipNameValidation: ptr.To(true)},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(mgr).NotTo(BeNil())

		k8sClient = mgr.GetClient()

		namespaceName = "nic-configuration-operator-" + rand.String(6)
		ns := &v1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: namespaceName,
		}}
		Expect(k8sClient.Create(context.Background(), ns)).To(Succeed())

		reconciler = &NicConfigurationTemplateReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}

		Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

		testMgrCtx, cancel := context.WithCancel(ctx)
		By("start manager")
		wg := sync.WaitGroup{}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer GinkgoRecover()
			By("Start controller manager")
			err := mgr.Start(testMgrCtx)
			Expect(err).ToNot(HaveOccurred())
		}()

		DeferCleanup(func() {
			By("Shut down controller manager")
			cancel()
			wg.Wait()
		})
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &v1.Node{})).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &v1alpha1.NicDevice{}, client.InNamespace(namespaceName))).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &v1alpha1.NicConfigurationTemplate{}, client.InNamespace(namespaceName))).To(Succeed())
		Expect(k8sClient.Delete(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})).To(Succeed())
		cancel()
	})

	It("should correctly match devices and apply template", func() {
		nodeLabels := map[string]string{"key": "value"}

		validNode := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: nodeLabels}}
		Expect(k8sClient.Create(ctx, validNode)).To(Succeed())
		invalidNode := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node2"}}
		Expect(k8sClient.Create(ctx, invalidNode)).To(Succeed())

		template := &v1alpha1.NicConfigurationTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      templateName,
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicConfigurationTemplateSpec{
				NodeSelector: nodeLabels,
				NicSelector: &v1alpha1.NicSelectorSpec{
					NicType:       "ConnectX6",
					PciAddresses:  []string{"0000:3b:00.0", "0000:d8:00.0"},
					SerialNumbers: []string{"serialNumber1", "serialNumber2"},
				},
				Template: &v1alpha1.ConfigurationTemplateSpec{
					NumVfs:   4,
					LinkType: "Ethernet",
				},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())

		device1 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device1", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device1)).To(Succeed())
		device1.Status = v1alpha1.NicDeviceStatus{
			Node:         validNode.Name,
			Type:         "ConnectX6",
			Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:3b:00.0"}},
			SerialNumber: "serialNumber1",
		}
		Expect(k8sClient.Status().Update(ctx, device1)).To(Succeed())

		device2 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device2", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device2)).To(Succeed())
		device2.Status = v1alpha1.NicDeviceStatus{
			Node:         validNode.Name,
			Type:         "ConnectX6",
			Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:d8:00.0"}},
			SerialNumber: "serialNumber2",
		}
		Expect(k8sClient.Status().Update(ctx, device2)).To(Succeed())

		// device3 doesn't match node selector
		device3 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device3", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device3)).To(Succeed())
		device3.Status = v1alpha1.NicDeviceStatus{
			Node:         invalidNode.Name,
			Type:         "ConnectX6",
			Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:3b:00.0"}},
			SerialNumber: "serialNumber1",
		}
		Expect(k8sClient.Status().Update(ctx, device3)).To(Succeed())

		// device4 doesn't match nic type selector
		device4 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device4", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device4)).To(Succeed())
		device4.Status = v1alpha1.NicDeviceStatus{
			Node:         validNode.Name,
			Type:         "ConnectX4",
			Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:d8:00.0"}},
			SerialNumber: "serialNumber1",
		}
		Expect(k8sClient.Status().Update(ctx, device4)).To(Succeed())

		// device5 doesn't match PCI addresses selector
		device5 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device5", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device5)).To(Succeed())
		device5.Status = v1alpha1.NicDeviceStatus{
			Node:         validNode.Name,
			Type:         "ConnectX6",
			Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:81:00.0"}},
			SerialNumber: "serialNumber1",
		}
		Expect(k8sClient.Status().Update(ctx, device5)).To(Succeed())

		// device6 doesn't match Serial numbers selector
		device6 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device6", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device6)).To(Succeed())
		device6.Status = v1alpha1.NicDeviceStatus{
			Node:         validNode.Name,
			Type:         "ConnectX6",
			Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:3b:00.0"}},
			SerialNumber: "serialNumber3",
		}
		Expect(k8sClient.Status().Update(ctx, device6)).To(Succeed())

		Eventually(getDeviceSpecTemplate(ctx, device1.Name, namespaceName, k8sClient)).WithTimeout(1 * time.Minute).Should(Equal(template.Spec.Template))
		Eventually(getDeviceSpecTemplate(ctx, device2.Name, namespaceName, k8sClient)).Should(Equal(template.Spec.Template))
		Eventually(getMatchedDevicesFromStatus(ctx, template.Name, template.Namespace, k8sClient)).Should(Equal([]string{device1.Name, device2.Name}))
		Consistently(getDeviceSpecTemplate(ctx, device3.Name, namespaceName, k8sClient)).Should(BeNil())
		Consistently(getDeviceSpecTemplate(ctx, device4.Name, namespaceName, k8sClient)).Should(BeNil())
		Consistently(getDeviceSpecTemplate(ctx, device5.Name, namespaceName, k8sClient)).Should(BeNil())
		Consistently(getDeviceSpecTemplate(ctx, device6.Name, namespaceName, k8sClient)).Should(BeNil())
	})

	It("should update spec if resetToDefault differs", func() {
		node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())

		template := &v1alpha1.NicConfigurationTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      templateName,
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicConfigurationTemplateSpec{
				NicSelector: &v1alpha1.NicSelectorSpec{
					NicType: "ConnectX6",
				},
				ResetToDefault: false,
				Template: &v1alpha1.ConfigurationTemplateSpec{
					NumVfs:   2,
					LinkType: consts.Ethernet,
				},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())

		device := &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespaceName},
			Spec: v1alpha1.NicDeviceSpec{Configuration: &v1alpha1.NicDeviceConfigurationSpec{
				ResetToDefault: true,
			}},
		}
		Expect(k8sClient.Create(ctx, device)).To(Succeed())
		device.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Type:  "ConnectX6",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:3b:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

		Eventually(func() (bool, error) {
			device := &v1alpha1.NicDevice{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)
			if err != nil {
				return true, err
			}
			return device.Spec.Configuration.ResetToDefault, nil
		}).Should(BeFalse())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deviceName, Namespace: namespaceName}, device)).To(Succeed())
		Expect(device.Spec.Configuration.ResetToDefault).To(Equal(false))

		Eventually(getMatchedDevicesFromStatus(ctx, template.Name, template.Namespace, k8sClient)).Should(Equal([]string{device.Name}))
	})

	It("should update spec if template differs", func() {
		node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())

		template := &v1alpha1.NicConfigurationTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      templateName,
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicConfigurationTemplateSpec{
				NicSelector: &v1alpha1.NicSelectorSpec{
					NicType: "ConnectX6",
				},
				Template: &v1alpha1.ConfigurationTemplateSpec{
					NumVfs:   4,
					LinkType: consts.Ethernet,
					PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{
						Enabled:        true,
						MaxAccOutRead:  4,
						MaxReadRequest: 1024,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())

		device := &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespaceName},
			Spec: v1alpha1.NicDeviceSpec{Configuration: &v1alpha1.NicDeviceConfigurationSpec{
				Template: &v1alpha1.ConfigurationTemplateSpec{
					NumVfs:   8,
					LinkType: consts.Infiniband,
					RoceOptimized: &v1alpha1.RoceOptimizedSpec{
						Enabled: true,
						Qos: &v1alpha1.QosSpec{
							Trust: "dscp",
							PFC:   "0,0,0,1,0,0,0,0",
						},
					},
				},
			}},
		}
		Expect(k8sClient.Create(ctx, device)).To(Succeed())
		device.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Type:  "ConnectX6",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:3b:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

		Eventually(getDeviceSpecTemplate(ctx, deviceName, namespaceName, k8sClient)).Should(Equal(template.Spec.Template))
		Eventually(getMatchedDevicesFromStatus(ctx, template.Name, template.Namespace, k8sClient)).Should(Equal([]string{device.Name}))
	})

	It("should not apply spec if NicDevice matches more than one template", func() {
		node := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		Expect(k8sClient.Create(ctx, node)).To(Succeed())

		template1 := &v1alpha1.NicConfigurationTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "template1",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicConfigurationTemplateSpec{
				NicSelector: &v1alpha1.NicSelectorSpec{
					NicType: "ConnectX6",
				},
				Template: &v1alpha1.ConfigurationTemplateSpec{
					NumVfs:   8,
					LinkType: consts.Ethernet,
				},
			},
		}
		Expect(k8sClient.Create(ctx, template1)).To(Succeed())

		template2 := &v1alpha1.NicConfigurationTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "template2",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicConfigurationTemplateSpec{
				NicSelector: &v1alpha1.NicSelectorSpec{
					NicType: "ConnectX6",
				},
				Template: &v1alpha1.ConfigurationTemplateSpec{
					NumVfs:   12,
					LinkType: consts.Ethernet,
				},
			},
		}
		Expect(k8sClient.Create(ctx, template2)).To(Succeed())

		device := &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{Name: deviceName, Namespace: namespaceName},
			Spec: v1alpha1.NicDeviceSpec{Configuration: &v1alpha1.NicDeviceConfigurationSpec{
				Template: &v1alpha1.ConfigurationTemplateSpec{NumVfs: 4, LinkType: consts.Ethernet}}},
		}
		Expect(k8sClient.Create(ctx, device)).To(Succeed())
		device.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Type:  "ConnectX6",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:3b:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

		Eventually(getDeviceSpecTemplate(ctx, deviceName, namespaceName, k8sClient)).Should(BeNil())
		Eventually(getMatchedDevicesFromStatus(ctx, template1.Name, template1.Namespace, k8sClient)).Should(BeEmpty())
		Eventually(getMatchedDevicesFromStatus(ctx, template2.Name, template2.Namespace, k8sClient)).Should(BeEmpty())
	})
})
