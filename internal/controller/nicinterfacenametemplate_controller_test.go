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

		// Create devices BEFORE the template so they exist when reconciler runs
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

		// Now create the template - reconciler will find existing devices
		template := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-interface-template",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{"key": "value"},
				PfsPerNic:        2,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%p%plane_id%",
				RailPciAddresses: [][]string{
					{"0000:1a:00.0", "0000:2a:00.0"},
					{"0000:3a:00.0", "0000:4a:00.0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())

		// Verify device1 gets nicIndex=0, railIndex=0, planeIndices=[0,1] (first NIC in rail 0)
		Eventually(getDeviceInterfaceNameSpec(ctx, device1.Name, namespaceName, k8sClient)).WithTimeout(1 * time.Minute).Should(Equal(&v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex:         0,
			RailIndex:        0,
			PlaneIndices:     []int{0, 1},
			RdmaDevicePrefix: template.Spec.RdmaDevicePrefix,
			NetDevicePrefix:  template.Spec.NetDevicePrefix,
		}))

		// Verify device2 gets nicIndex=2, railIndex=1, planeIndices=[0,1] (first NIC in rail 1)
		Eventually(getDeviceInterfaceNameSpec(ctx, device2.Name, namespaceName, k8sClient)).Should(Equal(&v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex:         2,
			RailIndex:        1,
			PlaneIndices:     []int{0, 1},
			RdmaDevicePrefix: template.Spec.RdmaDevicePrefix,
			NetDevicePrefix:  template.Spec.NetDevicePrefix,
		}))

		// Verify device3 doesn't get any spec (PCI address not in template)
		Consistently(getDeviceInterfaceNameSpec(ctx, device3.Name, namespaceName, k8sClient), time.Second).Should(BeNil())
	})

	It("should apply disjoint templates to different devices on the same node", func() {
		device1 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device1", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device1)).To(Succeed())
		device1.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device1)).To(Succeed())

		device2 := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device2", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device2)).To(Succeed())
		device2.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:2a:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device2)).To(Succeed())

		eastWest := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "east-west", Namespace: namespaceName},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				PfsPerNic:        1,
				RdmaDevicePrefix: "ew%nic_id%",
				NetDevicePrefix:  "ew%nic_id%",
				RailPciAddresses: [][]string{{"0000:1a:00.0"}},
			},
		}
		storage := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "storage", Namespace: namespaceName},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				PfsPerNic:        1,
				RdmaDevicePrefix: "ns%nic_id%",
				NetDevicePrefix:  "ns%nic_id%",
				RailPciAddresses: [][]string{{"0000:2a:00.0"}},
			},
		}
		Expect(k8sClient.Create(ctx, eastWest)).To(Succeed())
		Expect(k8sClient.Create(ctx, storage)).To(Succeed())

		Eventually(getDeviceInterfaceNameSpec(ctx, device1.Name, namespaceName, k8sClient)).WithTimeout(time.Minute).Should(Equal(&v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex:         0,
			RailIndex:        0,
			PlaneIndices:     []int{0},
			RdmaDevicePrefix: "ew%nic_id%",
			NetDevicePrefix:  "ew%nic_id%",
		}))
		Eventually(getDeviceInterfaceNameSpec(ctx, device2.Name, namespaceName, k8sClient)).WithTimeout(time.Minute).Should(Equal(&v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex:         0,
			RailIndex:        0,
			PlaneIndices:     []int{0},
			RdmaDevicePrefix: "ns%nic_id%",
			NetDevicePrefix:  "ns%nic_id%",
		}))

		Expect(k8sClient.Delete(ctx, eastWest)).To(Succeed())
		Eventually(getDeviceInterfaceNameSpec(ctx, device1.Name, namespaceName, k8sClient)).Should(BeNil())
		Consistently(getDeviceInterfaceNameSpec(ctx, device2.Name, namespaceName, k8sClient), time.Second).Should(Equal(&v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex:         0,
			RailIndex:        0,
			PlaneIndices:     []int{0},
			RdmaDevicePrefix: "ns%nic_id%",
			NetDevicePrefix:  "ns%nic_id%",
		}))
	})

	It("should apply an existing template when a matching device is discovered", func() {
		template := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "existing-template", Namespace: namespaceName},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				PfsPerNic:        1,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%",
				RailPciAddresses: [][]string{{"0000:1a:00.0"}},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())

		device := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device1", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device)).To(Succeed())
		device.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

		Eventually(getDeviceInterfaceNameSpec(ctx, device.Name, namespaceName, k8sClient)).WithTimeout(time.Minute).ShouldNot(BeNil())
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

		// Create devices BEFORE the template so they exist when reconciler runs
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

		// Now create the template - reconciler will find existing devices
		template := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-interface-template",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{"key": "value"},
				PfsPerNic:        2,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%p%plane_id%",
				RailPciAddresses: [][]string{
					{"0000:1a:00.0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())

		// Device on matching node should get the spec
		Eventually(getDeviceInterfaceNameSpec(ctx, device1.Name, namespaceName, k8sClient)).WithTimeout(1 * time.Minute).ShouldNot(BeNil())

		// Device on non-matching node should not get the spec
		// Note: since the controller only processes devices on its NodeName, device2 won't be processed
		Consistently(getDeviceInterfaceNameSpec(ctx, device2.Name, namespaceName, k8sClient), time.Second).Should(BeNil())
	})

	It("should reconcile templates when the local node labels change", func() {
		device := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device1", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device)).To(Succeed())
		device.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

		template := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "label-selected", Namespace: namespaceName},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{"fabric": "compute"},
				PfsPerNic:        1,
				RdmaDevicePrefix: "ew%nic_id%",
				NetDevicePrefix:  "ew%nic_id%",
				RailPciAddresses: [][]string{{"0000:1a:00.0"}},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())
		Consistently(getDeviceInterfaceNameSpec(ctx, device.Name, namespaceName, k8sClient), time.Second).Should(BeNil())

		node := &v1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
		node.Labels = map[string]string{"fabric": "compute"}
		Expect(k8sClient.Update(ctx, node)).To(Succeed())

		Eventually(getDeviceInterfaceNameSpec(ctx, device.Name, namespaceName, k8sClient)).WithTimeout(time.Minute).ShouldNot(BeNil())
	})

	It("should preserve the last assignment if multiple templates select the same device", func() {
		// Update the default node with required labels
		node := &v1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nodeName}, node)).To(Succeed())
		node.Labels = map[string]string{"key": "value"}
		Expect(k8sClient.Update(ctx, node)).To(Succeed())

		// Create device BEFORE templates so it exists when reconciler runs
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

		// Apply one valid template first.
		template1 := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-interface-template-1",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{"key": "value"},
				PfsPerNic:        2,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%p%plane_id%",
				RailPciAddresses: [][]string{
					{"0000:1a:00.0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template1)).To(Succeed())
		expected := &v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex:         0,
			RailIndex:        0,
			PlaneIndices:     []int{0, 1},
			RdmaDevicePrefix: template1.Spec.RdmaDevicePrefix,
			NetDevicePrefix:  template1.Spec.NetDevicePrefix,
		}
		Eventually(getDeviceInterfaceNameSpec(ctx, device.Name, namespaceName, k8sClient)).WithTimeout(time.Minute).Should(Equal(expected))

		// Add an overlapping template. The valid, already-applied assignment must remain.
		template2 := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-interface-template-2",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{"key": "value"},
				PfsPerNic:        2,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%p%plane_id%",
				RailPciAddresses: [][]string{
					{"0000:1a:00.0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template2)).To(Succeed())

		Consistently(getDeviceInterfaceNameSpec(ctx, device.Name, namespaceName, k8sClient), 2*time.Second).Should(Equal(expected))
	})

	It("should clear specs when no templates match the node", func() {
		// Create device BEFORE template so it exists when reconciler runs
		// Device has pre-set interface name template spec
		device := &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{Name: "device1", Namespace: namespaceName},
			Spec: v1alpha1.NicDeviceSpec{
				InterfaceNameTemplate: &v1alpha1.NicDeviceInterfaceNameSpec{
					NicIndex:         1,
					RailIndex:        1,
					PlaneIndices:     []int{1, 2},
					RdmaDevicePrefix: "old",
					NetDevicePrefix:  "old",
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

		// Default node has no labels, so no template will match
		// Now create template - reconciler will find device and clear its spec
		template := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-interface-template",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{"key": "value"}, // Won't match default node
				PfsPerNic:        2,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%p%plane_id%",
				RailPciAddresses: [][]string{
					{"0000:1a:00.0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())

		// The InterfaceNameTemplate spec should be cleared since node doesn't match template's selector
		Eventually(getDeviceInterfaceNameSpec(ctx, device.Name, namespaceName, k8sClient)).WithTimeout(1 * time.Minute).Should(BeNil())
	})

	It("should apply template with empty node selector to all nodes", func() {
		// Create device BEFORE template so it exists when reconciler runs
		device := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "device1", Namespace: namespaceName}}
		Expect(k8sClient.Create(ctx, device)).To(Succeed())
		device.Status = v1alpha1.NicDeviceStatus{
			Node:  nodeName,
			Type:  "ConnectX7",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}},
		}
		Expect(k8sClient.Status().Update(ctx, device)).To(Succeed())

		// Now create template - reconciler will find device and apply spec
		template := &v1alpha1.NicInterfaceNameTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-interface-template",
				Namespace: namespaceName,
			},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				NodeSelector:     map[string]string{}, // Empty selector matches all nodes
				PfsPerNic:        2,
				RdmaDevicePrefix: "rdma%nic_id%",
				NetDevicePrefix:  "net%nic_id%p%plane_id%",
				RailPciAddresses: [][]string{
					{"0000:1a:00.0"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, template)).To(Succeed())

		// Device should get the spec since empty selector matches all nodes
		Eventually(getDeviceInterfaceNameSpec(ctx, device.Name, namespaceName, k8sClient)).WithTimeout(1 * time.Minute).Should(Equal(&v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex:         0,
			RailIndex:        0,
			PlaneIndices:     []int{0, 1},
			RdmaDevicePrefix: template.Spec.RdmaDevicePrefix,
			NetDevicePrefix:  template.Spec.NetDevicePrefix,
		}))
	})
})

var _ = Describe("buildInterfaceNameAssignments", func() {
	It("should treat different ports of one NicDevice as overlapping ownership", func() {
		devices := []v1alpha1.NicDevice{{
			ObjectMeta: metav1.ObjectMeta{Name: "device1", Namespace: "test"},
			Status: v1alpha1.NicDeviceStatus{
				Ports: []v1alpha1.NicDevicePortSpec{{PCI: "0000:1a:00.0"}, {PCI: "0000:1a:00.1"}},
			},
		}}
		templates := []v1alpha1.NicInterfaceNameTemplate{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "east-west", Namespace: "test"},
				Spec: v1alpha1.NicInterfaceNameTemplateSpec{
					PfsPerNic: 1, NetDevicePrefix: "ew%nic_id%", RailPciAddresses: [][]string{{"0000:1a:00.0"}},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Name: "storage", Namespace: "test"},
				Spec: v1alpha1.NicInterfaceNameTemplateSpec{
					PfsPerNic: 1, NetDevicePrefix: "ns%nic_id%", RailPciAddresses: [][]string{{"0000:1a:00.1"}},
				},
			},
		}

		_, err := buildInterfaceNameAssignments("node1", devices, templates)
		Expect(err).To(MatchError(And(
			ContainSubstring("selected by multiple NicInterfaceNameTemplates"),
			ContainSubstring("test/east-west"),
			ContainSubstring("test/storage"),
		)))
	})

	It("should reject templates with no naming prefix", func() {
		templates := []v1alpha1.NicInterfaceNameTemplate{{
			ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "test"},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				PfsPerNic: 1, RailPciAddresses: [][]string{{"0000:1a:00.0"}},
			},
		}}

		_, err := buildInterfaceNameAssignments("node1", nil, templates)
		Expect(err).To(MatchError(ContainSubstring("must set at least one device prefix")))
	})

	It("should reject non-positive pfsPerNic", func() {
		templates := []v1alpha1.NicInterfaceNameTemplate{{
			ObjectMeta: metav1.ObjectMeta{Name: "invalid-pfs", Namespace: "test"},
			Spec: v1alpha1.NicInterfaceNameTemplateSpec{
				PfsPerNic: 0, NetDevicePrefix: "net%nic_id%", RailPciAddresses: [][]string{{"0000:1a:00.0"}},
			},
		}}

		_, err := buildInterfaceNameAssignments("node1", nil, templates)
		Expect(err).To(MatchError(ContainSubstring("must set pfsPerNic greater than zero")))
	})
})

var _ = Describe("calculateNicRailAndPlaneIndices", func() {
	pfsPerNic := 2

	It("should return correct indices for first PCI address (first NIC in rail 0)", func() {
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
		Expect(nicIndex).To(Equal(0))
		Expect(railIndex).To(Equal(0))
		Expect(planeIndices).To(Equal([]int{0, 1}))
	})

	It("should return correct indices for second PCI address in first rail (second NIC in rail 0)", func() {
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
		Expect(nicIndex).To(Equal(1))
		Expect(railIndex).To(Equal(0))
		Expect(planeIndices).To(Equal([]int{2, 3})) // Second NIC in rail gets planes 2,3
	})

	It("should return correct indices for first PCI address in second rail (first NIC in rail 1)", func() {
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
		Expect(nicIndex).To(Equal(2))
		Expect(railIndex).To(Equal(1))
		Expect(planeIndices).To(Equal([]int{0, 1})) // First NIC in rail 1 gets planes 0,1 (reset per rail)
	})

	It("should return correct indices for last PCI address (second NIC in rail 1)", func() {
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
		Expect(nicIndex).To(Equal(3))
		Expect(railIndex).To(Equal(1))
		Expect(planeIndices).To(Equal([]int{2, 3})) // Second NIC in rail 1 gets planes 2,3
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
		Expect(nicIndex).To(Equal(2))
		Expect(railIndex).To(Equal(1))
		Expect(planeIndices).To(Equal([]int{0, 1}))
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

		// With pfsPerNic=4, second NIC should have planes [4,5,6,7]
		nicIndex, railIndex, planeIndices, found := calculateNicRailAndPlaneIndices(device, railPciAddresses, 4)
		Expect(found).To(BeTrue())
		Expect(nicIndex).To(Equal(1))
		Expect(railIndex).To(Equal(0))
		Expect(planeIndices).To(Equal([]int{4, 5, 6, 7}))
	})
})
