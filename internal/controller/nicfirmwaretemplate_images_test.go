// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"strings"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("firmware image template contract", func() {
	image := "registry.example/fw:cx7-mt_000001@sha256:" + strings.Repeat("a", 64)
	It("copies the complete firmware spec and clears the previous source when switching modes", func() {
		template := &nicFirmwareTemplate{template: v1alpha1.NicFirmwareTemplate{Spec: v1alpha1.NicFirmwareTemplateSpec{
			Template: &v1alpha1.FirmwareTemplateSpec{NicFirmwareSourceRef: "", FirmwareImages: []string{image}, UpdatePolicy: "Validate"},
		}}}
		device := &v1alpha1.NicDevice{Spec: v1alpha1.NicDeviceSpec{Firmware: &v1alpha1.FirmwareTemplateSpec{NicFirmwareSourceRef: "legacy", FirmwareImages: nil, UpdatePolicy: "Update"}}}
		Expect(template.applyToDevice(device)).To(BeTrue())
		Expect(device.Spec.Firmware).To(Equal(template.template.Spec.Template))
		Expect(template.applyToDevice(device)).To(BeFalse())
		device.Spec.Firmware.FirmwareImages[0] = "changed"
		Expect(template.template.Spec.Template.FirmwareImages).To(Equal([]string{image}))
		template.template.Spec.Template = &v1alpha1.FirmwareTemplateSpec{NicFirmwareSourceRef: "legacy-again", FirmwareImages: nil, UpdatePolicy: "Update"}
		Expect(template.applyToDevice(device)).To(BeTrue())
		Expect(device.Spec.Firmware.FirmwareImages).To(BeNil())
		Expect(device.Spec.Firmware.NicFirmwareSourceRef).To(Equal("legacy-again"))
	})

	It("admits exactly one valid source and rejects mutable or duplicate image references", func() {
		ctx := context.Background()
		apiClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "firmware-image-contract-"}}
		Expect(apiClient.Create(ctx, namespace)).To(Succeed())
		DeferCleanup(func() { Expect(apiClient.Delete(ctx, namespace)).To(Succeed()) })
		for _, test := range []struct {
			name   string
			source string
			images []string
			valid  bool
		}{
			{name: "legacy", source: "legacy", images: nil, valid: true},
			{name: "image", source: "", images: []string{image}, valid: true},
			{name: "neither", source: "", images: nil, valid: false},
			{name: "both", source: "legacy", images: []string{image}, valid: false},
			{name: "mutable", source: "", images: []string{"registry.example/fw:latest"}, valid: false},
			{name: "duplicates", source: "", images: []string{image, image}, valid: false},
		} {
			resource := &v1alpha1.NicFirmwareTemplate{ObjectMeta: metav1.ObjectMeta{Name: test.name, Namespace: namespace.Name}, Spec: v1alpha1.NicFirmwareTemplateSpec{
				NodeSelector: nil,
				NicSelector:  &v1alpha1.NicSelectorSpec{NicType: "1021"},
				Template:     &v1alpha1.FirmwareTemplateSpec{NicFirmwareSourceRef: test.source, FirmwareImages: test.images, UpdatePolicy: "Validate"},
			}}
			err := apiClient.Create(ctx, resource)
			if test.valid {
				Expect(err).NotTo(HaveOccurred(), test.name)
			} else {
				Expect(apierrors.IsInvalid(err)).To(BeTrue(), "%s: %v", test.name, err)
			}
		}
	})
})
