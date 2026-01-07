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
	"sync"
	"time"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func getDeviceInterfaceNameSpec(ctx context.Context, name string, namespace string, client client.Client) func() (*v1alpha1.NicDeviceInterfaceNameSpec, error) {
	return func() (*v1alpha1.NicDeviceInterfaceNameSpec, error) {
		device := &v1alpha1.NicDevice{}
		err := client.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, device)
		if err != nil {
			return nil, err
		}
		return device.Spec.InterfaceNameTemplate, nil
	}
}

var _ = Describe("NicInterfaceNameTemplate Controller", func() {
	var (
		mgr           manager.Manager
		k8sClient     client.Client
		reconciler    *NicInterfaceNameTemplateReconciler
		ctx           context.Context
		cancel        context.CancelFunc
		namespaceName string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		mgr = createManager()

		k8sClient = mgr.GetClient()

		namespaceName = createNodeAndRandomNamespace(ctx, k8sClient)

		reconciler = &NicInterfaceNameTemplateReconciler{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			NodeName: nodeName,
		}

		Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

		testMgrCtx, cancel := context.WithCancel(ctx)
		By("start manager")
		wg := sync.WaitGroup{}
		startManager(mgr, testMgrCtx, &wg)

		DeferCleanup(func() {
			By("Shut down controller manager")
			cancel()
			wg.Wait()
		})
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &v1.Node{})).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &v1alpha1.NicDevice{}, client.InNamespace(namespaceName))).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &v1alpha1.NicInterfaceNameTemplate{}, client.InNamespace(namespaceName))).To(Succeed())
		Expect(k8sClient.Delete(ctx, &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}})).To(Succeed())
		cancel()
	})

	It("should apply template to matching devices based on PCI address", func() {
		// Update the default node with required labels
		node := &v1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
		node.Labels = map[string]string{"key": "value"}
		Expect(k8sClient.Update(ctx, node)).To(Succeed())

		template := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-interface-template",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{"key": "value"},
				PfsPerNic:        2,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%",
				RailPciAddresses: [][]string{
					{"0000:1a:00.0", "0000:2a:00.0"},
					{"0000:3a:00.0", "0000:4a:00.0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())

		// Create device that matches first PCI address in the template (rail 1, nic 1)
		device1 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device1", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device1)).To(Succeed())
		device1.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Type:  "ConnectX7",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device1)).To(Succeed())

		// Create device that matches third PCI address in the template (rail 2, nic 3)
		device2 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device2", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device2)).To(Succeed())
		device2.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Type:  "ConnectX7",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:3a:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device2)).To(Succeed())

		// Create device that doesn't match any PCI address in the template
		device3 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device3", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device3)).To(Succeed())
		device3.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Type:  "ConnectX7",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:99:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device3)).To(Succeed())

		// Verify device1 gets nicIndex=1, railIndex=1, planeIndices=[1,2] (first NIC in rail 1)
		Eventually(getDeviceInterfaceNameSpec(ctx, device1.Name, namespaceName, k8sClient)).WithTimeout(1 * time.Minute).Should(Equal(&v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex:         1,
			RailIndex:        1,
			PlaneIndices:     []int{1, 2},
			RdmaDevicePrefix: template.Spec.RdmaDevicePrefix,
			NetDevicePrefix:  template.Spec.NetDevicePrefix,
			RailPciAddresses: template.Spec.RailPciAddresses,
		}))

		// Verify device2 gets nicIndex=3, railIndex=2, planeIndices=[1,2] (first NIC in rail 2)
		Eventually(getDeviceInterfaceNameSpec(ctx, device2.Name, namespaceName, k8sClient)).Should(Equal(&v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex:         3,
			RailIndex:        2,
			PlaneIndices:     []int{1, 2},
			RdmaDevicePrefix: template.Spec.RdmaDevicePrefix,
			NetDevicePrefix:  template.Spec.NetDevicePrefix,
			RailPciAddresses: template.Spec.RailPciAddresses,
		}))

		// Verify device3 doesn't get any spec (PCI address not in template)
		Consistently(getDeviceInterfaceNameSpec(ctx, device3.Name, namespaceName, k8sClient), time.Second).Should(BeNil())
	})

	It("should not apply template to devices on non-matching nodes", func() {
		// Create a second node without matching labels
		otherNode := &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: "other-node"}}
		Expect(k8sClient.Create(ctx, otherNode)).To(Succeed())

		// Update the default node with required labels
		node := &v1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
		node.Labels = map[string]string{"key": "value"}
		Expect(k8sClient.Update(ctx, node)).To(Succeed())

		template := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-interface-template",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{"key": "value"},
				PfsPerNic:        2,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%",
				RailPciAddresses: [][]string{
					{"0000:1a:00.0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())

		// Create device on the matching node
		device1 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device1", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device1)).To(Succeed())
		device1.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Type:  "ConnectX7",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device1)).To(Succeed())

		// Create device on a non-matching node
		device2 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device2", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device2)).To(Succeed())
		device2.Status = v1alpha1.NicDeviceStatus{
			Node:  "other-node",
			Type:  "ConnectX7",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device2)).To(Succeed())

		// Device on matching node should get the spec
		Eventually(getDeviceInterfaceNameSpec(ctx, device1.Name, namespaceName, k8sClient)).WithTimeout(1 * time.Minute).ShouldNot(BeNil())

		// Device on non-matching node should not get the spec
		// Note: since the controller only processes devices on its NodeName, device2 won't be processed
		Consistently(getDeviceInterfaceNameSpec(ctx, device2.Name, namespaceName, k8sClient), time.Second).Should(BeNil())
	})

	It("should error out and clear specs if multiple templates match the node", func() {
		// Update the default node with required labels
		node := &v1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
		node.Labels = map[string]string{"key": "value"}
		Expect(k8sClient.Update(ctx, node)).To(Succeed())

		template1 := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-interface-template-1",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{"key": "value"},
				PfsPerNic:        2,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%",
				RailPciAddresses: [][]string{
					{"0000:1a:00.0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template1)).To(Succeed())

		template2 := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-interface-template-2",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{"key": "value"},
				PfsPerNic:        2,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%",
				RailPciAddresses: [][]string{
					{"0000:1a:00.0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template2)).To(Succeed())

		// Create device on the matching node
		device := &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{Name: "device1", Namespace: namespaceName},
			// Pre-set some interface name template spec
			Spec: v1alpha1.NicDeviceSpec{
				InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
					NicIndex:         1,
					RailIndex:        1,
					PlaneIndices:     []int{1, 2},
					RdmaDevicePrefix: "old",
					NetDevicePrefix:  "old",
					RailPciAddresses: [][]string{{"0000:1a:00.0"}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, device)).To(Succeed())
		device.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Type:  "ConnectX7",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

		// The InterfaceNameTemplate spec should be cleared due to multiple matching templates
		Eventually(getDeviceInterfaceNameSpec(ctx, device.Name, namespaceName, k8sClient)).WithTimeout(1 * time.Minute).Should(BeNil())
	})

	It("should clear specs when no templates match the node", func() {
		// Default node has no labels, so no template will match
		template := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-interface-template",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{"key": "value"}, // Won't match default node
				PfsPerNic:        2,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%",
				RailPciAddresses: [][]string{
					{"0000:1a:00.0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())

		// Create device with pre-set interface name template spec
		device := &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{Name: "device1", Namespace: namespaceName},
			Spec: v1alpha1.NicDeviceSpec{
				InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
					NicIndex:         1,
					RailIndex:        1,
					PlaneIndices:     []int{1, 2},
					RdmaDevicePrefix: "old",
					NetDevicePrefix:  "old",
					RailPciAddresses: [][]string{{"0000:1a:00.0"}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, device)).To(Succeed())
		device.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Type:  "ConnectX7",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

		// The InterfaceNameTemplate spec should be cleared since node doesn't match template's selector
		Eventually(getDeviceInterfaceNameSpec(ctx, device.Name, namespaceName, k8sClient)).WithTimeout(1 * time.Minute).Should(BeNil())
	})

	It("should apply template with empty node selector to all nodes", func() {
		template := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-interface-template",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{}, // Empty selector matches all nodes
				PfsPerNic:        2,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%",
				RailPciAddresses: [][]string{
					{"0000:1a:00.0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())

		// Create device on the default node (no labels needed)
		device := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device1", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device)).To(Succeed())
		device.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Type:  "ConnectX7",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

		// Device should get the spec since empty selector matches all nodes
		Eventually(getDeviceInterfaceNameSpec(ctx, device.Name, namespaceName, k8sClient)).WithTimeout(1 * time.Minute).Should(Equal(&v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex:         1,
			RailIndex:        1,
			PlaneIndices:     []int{1, 2},
			RdmaDevicePrefix: template.Spec.RdmaDevicePrefix,
			NetDevicePrefix:  template.Spec.NetDevicePrefix,
			RailPciAddresses: template.Spec.RailPciAddresses,
		}))
	})
})

var _ = Describe("calculateNicRailAndPlaneIndices", func() {
	pfsPerNic := 2

	It("should return correct indices for first PCI address (first NIC in rail 1)", func() {
		device := &v1alpha1.NicDevice{
			Status: v1alpha1.NicDeviceStatus{
				Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}},
			},
		}
		railPciAddresses := [][]string{
			{"0000:1a:00.0", "0000:2a:00.0"},
			{"0000:3a:00.0", "0000:4a:00.0"},
		}

		nicIndex, railIndex, planeIndices, found := calculateNicRailAndPlaneIndices(device, railPciAddresses, pfsPerNic)
		Expect(found).To(BeTrue())
		Expect(nicIndex).To(Equal(1))
		Expect(railIndex).To(Equal(1))
		Expect(planeIndices).To(Equal([]int{1, 2}))
	})

	It("should return correct indices for second PCI address in first rail (second NIC in rail 1)", func() {
		device := &v1alpha1.NicDevice{
			Status: v1alpha1.NicDeviceStatus{
				Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:2a:00.0"}},
			},
		}
		railPciAddresses := [][]string{
			{"0000:1a:00.0", "0000:2a:00.0"},
			{"0000:3a:00.0", "0000:4a:00.0"},
		}

		nicIndex, railIndex, planeIndices, found := calculateNicRailAndPlaneIndices(device, railPciAddresses, pfsPerNic)
		Expect(found).To(BeTrue())
		Expect(nicIndex).To(Equal(2))
		Expect(railIndex).To(Equal(1))
		Expect(planeIndices).To(Equal([]int{3, 4})) // Second NIC in rail gets planes 3,4
	})

	It("should return correct indices for first PCI address in second rail (first NIC in rail 2)", func() {
		device := &v1alpha1.NicDevice{
			Status: v1alpha1.NicDeviceStatus{
				Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:3a:00.0"}},
			},
		}
		railPciAddresses := [][]string{
			{"0000:1a:00.0", "0000:2a:00.0"},
			{"0000:3a:00.0", "0000:4a:00.0"},
		}

		nicIndex, railIndex, planeIndices, found := calculateNicRailAndPlaneIndices(device, railPciAddresses, pfsPerNic)
		Expect(found).To(BeTrue())
		Expect(nicIndex).To(Equal(3))
		Expect(railIndex).To(Equal(2))
		Expect(planeIndices).To(Equal([]int{1, 2})) // First NIC in rail 2 gets planes 1,2 (reset per rail)
	})

	It("should return correct indices for last PCI address (second NIC in rail 2)", func() {
		device := &v1alpha1.NicDevice{
			Status: v1alpha1.NicDeviceStatus{
				Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:4a:00.0"}},
			},
		}
		railPciAddresses := [][]string{
			{"0000:1a:00.0", "0000:2a:00.0"},
			{"0000:3a:00.0", "0000:4a:00.0"},
		}

		nicIndex, railIndex, planeIndices, found := calculateNicRailAndPlaneIndices(device, railPciAddresses, pfsPerNic)
		Expect(found).To(BeTrue())
		Expect(nicIndex).To(Equal(4))
		Expect(railIndex).To(Equal(2))
		Expect(planeIndices).To(Equal([]int{3, 4})) // Second NIC in rail 2 gets planes 3,4
	})

	It("should return not found for non-matching PCI address", func() {
		device := &v1alpha1.NicDevice{
			Status: v1alpha1.NicDeviceStatus{
				Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:99:00.0"}},
			},
		}
		railPciAddresses := [][]string{
			{"0000:1a:00.0", "0000:2a:00.0"},
			{"0000:3a:00.0", "0000:4a:00.0"},
		}

		_, _, _, found := calculateNicRailAndPlaneIndices(device, railPciAddresses, pfsPerNic)
		Expect(found).To(BeFalse())
	})

	It("should find match in any port", func() {
		device := &v1alpha1.NicDevice{
			Status: v1alpha1.NicDeviceStatus{
				Ports: []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:99:00.0"},
					{PCI: "0000:3a:00.0"},
				},
			},
		}
		railPciAddresses := [][]string{
			{"0000:1a:00.0", "0000:2a:00.0"},
			{"0000:3a:00.0", "0000:4a:00.0"},
		}

		nicIndex, railIndex, planeIndices, found := calculateNicRailAndPlaneIndices(device, railPciAddresses, pfsPerNic)
		Expect(found).To(BeTrue())
		Expect(nicIndex).To(Equal(3))
		Expect(railIndex).To(Equal(2))
		Expect(planeIndices).To(Equal([]int{1, 2}))
	})

	It("should return not found for empty rail addresses", func() {
		device := &v1alpha1.NicDevice{
			Status: v1alpha1.NicDeviceStatus{
				Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}},
			},
		}
		railPciAddresses := [][]string{}

		_, _, _, found := calculateNicRailAndPlaneIndices(device, railPciAddresses, pfsPerNic)
		Expect(found).To(BeFalse())
	})

	It("should handle different pfsPerNic values", func() {
		device := &v1alpha1.NicDevice{
			Status: v1alpha1.NicDeviceStatus{
				Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:2a:00.0"}},
			},
		}
		railPciAddresses := [][]string{
			{"0000:1a:00.0", "0000:2a:00.0"},
		}

		// With pfsPerNic=4, second NIC should have planes [5,6,7,8]
		nicIndex, railIndex, planeIndices, found := calculateNicRailAndPlaneIndices(device, railPciAddresses, 4)
		Expect(found).To(BeTrue())
		Expect(nicIndex).To(Equal(2))
		Expect(railIndex).To(Equal(1))
		Expect(planeIndices).To(Equal([]int{5, 6, 7, 8}))
	})
})
