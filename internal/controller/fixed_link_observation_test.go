// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Fixed link observations", func() {
	It("leaves unbound PF evidence absent for the translator to interpret", func() {
		observed := device.Device{ID: "asic", Ports: []device.Port{{PCI: "0000:03:00.0", Netdev: ""}}}
		template := &v1alpha1.ConfigurationTemplateSpec{LinkType: consts.Ethernet}
		observations, err := observeFixedLinkTypes(context.Background(), template, observed, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(observations).To(BeEmpty())
	})
	It("does not hide named PF observation failures after an unbound PF", func() {
		observed := device.Device{ID: "asic", Ports: []device.Port{{PCI: "0000:03:00.0", Netdev: ""}, {PCI: "0000:03:00.1", Netdev: "nco-no-such-link"}}}
		template := &v1alpha1.ConfigurationTemplateSpec{LinkType: consts.Ethernet}
		_, err := observeFixedLinkTypes(context.Background(), template, observed, nil)
		Expect(err).To(MatchError(ContainSubstring("read link type for nco-no-such-link")))
	})
})
