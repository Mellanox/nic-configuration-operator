// Copyright 2025 NVIDIA CORPORATION & AFFILIATES
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package maintenance

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	maintenanceoperator "github.com/Mellanox/maintenance-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	hostmocks "github.com/Mellanox/nic-configuration-operator/pkg/host/mocks"
)

var _ = Describe("maintenanceManager", func() {
	var (
		ctx       context.Context
		scheme    *runtime.Scheme
		namespace string
		nodeName  string
	)

	BeforeEach(func() {
		ctx = context.Background()
		scheme = runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(maintenanceoperator.AddToScheme(scheme)).To(Succeed())
		namespace = "test-ns"
		nodeName = "test-node"
	})

	It("SetNodeLabel adds, updates and deletes a label via strategic merge patch", func() {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
		m := maintenanceManager{client: cl, nodeName: nodeName}

		// add
		Expect(m.SetNodeWaitLabel(ctx, "value1")).To(Succeed())
		updated := &corev1.Node{}
		Expect(m.client.Get(ctx, types.NamespacedName{Name: nodeName}, updated)).To(Succeed())
		Expect(updated.Labels).To(HaveKeyWithValue(consts.NodeNicConfigurationWaitLabel, "value1"))

		// same value (no-op server-side)
		Expect(m.SetNodeWaitLabel(ctx, "value1")).To(Succeed())

		// update
		Expect(m.SetNodeWaitLabel(ctx, "value2")).To(Succeed())
		Expect(m.client.Get(ctx, types.NamespacedName{Name: nodeName}, updated)).To(Succeed())
		Expect(updated.Labels).To(HaveKeyWithValue(consts.NodeNicConfigurationWaitLabel, "value2"))

		// delete
		Expect(m.SetNodeWaitLabel(ctx, "")).To(Succeed())
		Expect(m.client.Get(ctx, types.NamespacedName{Name: nodeName}, updated)).To(Succeed())
		Expect(updated.Labels).ToNot(HaveKey(consts.NodeNicConfigurationWaitLabel))
	})

	It("schedules maintenance and sets the wait label; second call is idempotent", func() {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
		m := maintenanceManager{client: cl, nodeName: nodeName, namespace: namespace}

		// first schedule creates one object and sets wait label true
		Expect(m.ScheduleMaintenance(ctx)).To(Succeed())

		nmList := &maintenanceoperator.NodeMaintenanceList{}
		Expect(cl.List(ctx, nmList, clientInNamespace(namespace))).To(Succeed())
		Expect(nmList.Items).To(HaveLen(1))
		Expect(nmList.Items[0].Spec.NodeName).To(Equal(nodeName))
		Expect(nmList.Items[0].Spec.RequestorID).To(Equal(consts.MaintenanceRequestor))

		updated := &corev1.Node{}
		Expect(cl.Get(ctx, types.NamespacedName{Name: nodeName}, updated)).To(Succeed())
		Expect(updated.Labels).To(HaveKeyWithValue(consts.NodeNicConfigurationWaitLabel, consts.LabelValueTrue))

		// second schedule is a no-op and label remains true
		Expect(m.ScheduleMaintenance(ctx)).To(Succeed())
		nmList = &maintenanceoperator.NodeMaintenanceList{}
		Expect(cl.List(ctx, nmList, clientInNamespace(namespace))).To(Succeed())
		Expect(nmList.Items).To(HaveLen(1))
		Expect(cl.Get(ctx, types.NamespacedName{Name: nodeName}, updated)).To(Succeed())
		Expect(updated.Labels).To(HaveKeyWithValue(consts.NodeNicConfigurationWaitLabel, consts.LabelValueTrue))
	})

	It("reports maintenance allowed only when Ready condition is true", func() {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node).Build()
		m := maintenanceManager{client: cl, nodeName: nodeName, namespace: namespace}

		// no object
		allowed, err := m.MaintenanceAllowed(ctx)
		Expect(err).To(BeNil())
		Expect(allowed).To(BeFalse())

		// object without Ready condition
		nm := &maintenanceoperator.NodeMaintenance{
			ObjectMeta: metav1.ObjectMeta{Name: consts.MaintenanceRequestName + "-" + nodeName, Namespace: namespace},
			Spec:       maintenanceoperator.NodeMaintenanceSpec{RequestorID: consts.MaintenanceRequestor, NodeName: nodeName},
		}
		cl = fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, nm).Build()
		m.client = cl

		allowed, err = m.MaintenanceAllowed(ctx)
		Expect(err).To(BeNil())
		Expect(allowed).To(BeFalse())

		// object with Ready=false
		nm.Status.Conditions = []metav1.Condition{{Type: maintenanceoperator.ConditionTypeReady, Status: metav1.ConditionFalse}}
		cl = fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, nm).Build()
		m.client = cl
		allowed, err = m.MaintenanceAllowed(ctx)
		Expect(err).To(BeNil())
		Expect(allowed).To(BeFalse())

		// object with Ready=true
		nm.Status.Conditions = []metav1.Condition{{Type: maintenanceoperator.ConditionTypeReady, Status: metav1.ConditionTrue}}
		cl = fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, nm).Build()
		m.client = cl
		allowed, err = m.MaintenanceAllowed(ctx)
		Expect(err).To(BeNil())
		Expect(allowed).To(BeTrue())
	})

	It("releases maintenance and clears the wait label when present", func() {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: nodeName}}
		nm := &maintenanceoperator.NodeMaintenance{
			ObjectMeta: metav1.ObjectMeta{Name: consts.MaintenanceRequestName + "-" + nodeName, Namespace: namespace},
			Spec:       maintenanceoperator.NodeMaintenanceSpec{RequestorID: consts.MaintenanceRequestor, NodeName: nodeName},
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, nm).Build()
		m := maintenanceManager{client: cl, nodeName: nodeName, namespace: namespace}

		// ensure label is set true first (simulate schedule)
		Expect(m.SetNodeWaitLabel(ctx, consts.LabelValueTrue)).To(Succeed())

		// release maintenance should delete object and set label false
		Expect(m.ReleaseMaintenance(ctx)).To(Succeed())
		nmList := &maintenanceoperator.NodeMaintenanceList{}
		Expect(cl.List(ctx, nmList, clientInNamespace(namespace))).To(Succeed())
		Expect(nmList.Items).To(HaveLen(0))

		updated := &corev1.Node{}
		Expect(cl.Get(ctx, types.NamespacedName{Name: nodeName}, updated)).To(Succeed())
		Expect(updated.Labels).To(HaveKeyWithValue(consts.NodeNicConfigurationWaitLabel, consts.LabelValueFalse))
	})

	It("calls host utils to reboot and propagates errors", func() {
		mockHU := &hostmocks.HostUtils{}
		mockHU.On("ScheduleReboot").Return(nil).Once()
		m := maintenanceManager{hostUtils: mockHU}
		Expect(m.Reboot()).To(Succeed())
		mockHU.AssertExpectations(GinkgoT())

		mockHU2 := &hostmocks.HostUtils{}
		rebootErr := fmt.Errorf("reboot failed")
		mockHU2.On("ScheduleReboot").Return(rebootErr).Once()
		m = maintenanceManager{hostUtils: mockHU2}
		Expect(m.Reboot()).To(MatchError(rebootErr))
		mockHU2.AssertExpectations(GinkgoT())
	})
})

// helpers
func clientInNamespace(ns string) clientListOptionInNamespace {
	return clientListOptionInNamespace{Namespace: ns}
}

type clientListOptionInNamespace struct{ Namespace string }

func (o clientListOptionInNamespace) ApplyToList(opts *client.ListOptions) {
	opts.Namespace = o.Namespace
}
