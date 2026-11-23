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

package configuration

import (
	"net"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ConfigurationUtils", func() {
	Describe("hasNoCarrier", func() {
		DescribeTable("detecting the iproute2 NO-CARRIER flag state",
			func(flags net.Flags, expected bool) {
				Expect(hasNoCarrier(flags)).To(Equal(expected))
			},
			Entry("administratively up without a running carrier", net.FlagUp, true),
			Entry("administratively up with a running carrier", net.FlagUp|net.FlagRunning, false),
			Entry("administratively down", net.Flags(0), false),
		)
	})

})

var _ = Describe("mergeParams", func() {
	It("copies src into dst with src winning on key collisions", func() {
		dst := map[string]string{"NUM_OF_PF": "1", "KEEP": "x"}
		mergeParams(dst, map[string]string{"NUM_OF_PF": "8", "MODULE_SPLIT_M0[2]": "0x1"})
		Expect(dst).To(Equal(map[string]string{
			"NUM_OF_PF": "8", "KEEP": "x", "MODULE_SPLIT_M0[2]": "0x1",
		}))
	})

	It("is a no-op for a nil or empty src", func() {
		dst := map[string]string{"A": "1"}
		mergeParams(dst, nil)
		mergeParams(dst, map[string]string{})
		Expect(dst).To(Equal(map[string]string{"A": "1"}))
	})
})
