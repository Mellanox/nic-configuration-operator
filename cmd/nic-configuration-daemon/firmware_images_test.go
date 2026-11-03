// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"math"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFirmwareImageConfiguration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Firmware image configuration")
}

var _ = Describe("firmware image cache sizing", func() {
	DescribeTable("uses Kubernetes quantities without wrapping oversized budgets",
		func(value string, bytes int64, valid bool) {
			options, err := firmwareImageStoreOptions(value)
			if !valid {
				Expect(err).To(HaveOccurred())
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(options.MaxCacheBytes).To(Equal(bytes))
		},
		Entry("default", "", int64(4<<30), true),
		Entry("minimum", "2Gi", int64(2<<30), true),
		Entry("larger inventory", "20Gi", int64(20<<30), true),
		Entry("fractional binary unit", "2.5Gi", int64(5<<29), true),
		Entry("zero", "0", int64(0), false),
		Entry("negative", "-4Gi", int64(0), false),
		Entry("insufficient staging space", "1Gi", int64(0), false),
		Entry("malformed", "four", int64(0), false),
		Entry("binary quantity saturates as Kubernetes does", "10Ei", int64(math.MaxInt64), true),
		Entry("decimal int64 overflow", "1e30", int64(0), false),
	)
})
