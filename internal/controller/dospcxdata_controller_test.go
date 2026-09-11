/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES
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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	spectrumxmocks "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/mocks"
)

var _ = Describe("DospcxDataReconciler", func() {
	var (
		ctx           context.Context
		k8sClient     client.Client
		namespaceName string
		manager       *spectrumxmocks.SpectrumXManager
		reconciler    *DospcxDataReconciler
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		namespaceName = "dospcx-data-" + rand.String(6)
		Expect(k8sClient.Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespaceName},
		})).To(Succeed())

		manager = spectrumxmocks.NewSpectrumXManager(GinkgoT())
		reconciler = &DospcxDataReconciler{
			Client:           k8sClient,
			Scheme:           scheme.Scheme,
			SpectrumXManager: manager,
		}
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &corev1.ConfigMap{}, client.InNamespace(namespaceName))).To(Succeed())
		Eventually(func() int {
			configMaps := &corev1.ConfigMapList{}
			Expect(k8sClient.List(ctx, configMaps, client.InNamespace(namespaceName))).To(Succeed())
			return len(configMaps.Items)
		}).Should(BeZero())
		Expect(k8sClient.Delete(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespaceName},
		})).To(Succeed())
	})

	newBundle := func(name string, archive []byte) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: namespaceName,
				Labels: map[string]string{consts.DospcxDataLabel: ""},
			},
			Data: map[string]string{
				consts.DospcxDataConfigMapFormatKey: consts.DospcxDataConfigMapFormat,
			},
			BinaryData: map[string][]byte{
				consts.DospcxDataConfigMapArchiveKey: archive,
			},
		}
	}

	It("installs the selected data bundle", func() {
		archive := []byte("archive")
		Expect(k8sClient.Create(ctx, newBundle("dospcx-data-main", archive))).To(Succeed())
		manager.On("InstallBlueprintsData", archive).Return(nil).Once()

		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).NotTo(HaveOccurred())
	})

	It("removes manager-installed data when no bundle exists", func() {
		manager.On("RemoveBlueprintsData").Return(nil).Once()

		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).NotTo(HaveOccurred())
	})

	It("deactivates data and rejects multiple selected bundles", func() {
		Expect(k8sClient.Create(ctx, newBundle("first", []byte("first")))).To(Succeed())
		Expect(k8sClient.Create(ctx, newBundle("second", []byte("second")))).To(Succeed())
		manager.On("RemoveBlueprintsData").Return(nil).Once()

		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).To(MatchError(ContainSubstring("multiple doSPCX data ConfigMaps")))
		manager.AssertNotCalled(GinkgoT(), "InstallBlueprintsData")
	})

	It("reports a failure to deactivate conflicting data", func() {
		Expect(k8sClient.Create(ctx, newBundle("first", []byte("first")))).To(Succeed())
		Expect(k8sClient.Create(ctx, newBundle("second", []byte("second")))).To(Succeed())
		manager.On("RemoveBlueprintsData").Return(errors.New("remove failed")).Once()

		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).To(MatchError(ContainSubstring("could not be deactivated")))
	})

	It("rejects an unsupported bundle format", func() {
		bundle := newBundle("dospcx-data-main", []byte("archive"))
		bundle.Data[consts.DospcxDataConfigMapFormatKey] = "unsupported"
		Expect(k8sClient.Create(ctx, bundle)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).To(MatchError(ContainSubstring("unsupported")))
		manager.AssertNotCalled(GinkgoT(), "InstallBlueprintsData")
	})

	It("rejects an empty archive", func() {
		Expect(k8sClient.Create(ctx, newBundle("dospcx-data-main", nil))).To(Succeed())

		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).To(MatchError(ContainSubstring("missing or has an empty")))
		manager.AssertNotCalled(GinkgoT(), "InstallBlueprintsData")
	})

	It("returns bundle installation errors", func() {
		archive := []byte("archive")
		Expect(k8sClient.Create(ctx, newBundle("dospcx-data-main", archive))).To(Succeed())
		manager.On("InstallBlueprintsData", archive).Return(errors.New("install failed")).Once()

		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).To(HaveOccurred())
		Expect(strings.Contains(err.Error(), "failed to install doSPCX data")).To(BeTrue())
	})
})
