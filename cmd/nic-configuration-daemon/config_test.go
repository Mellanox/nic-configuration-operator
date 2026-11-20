// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
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

package main

import (
	"os"
	"testing"

	"github.com/Mellanox/nic-configuration-operator/pkg/nvconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestDaemonConfig(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Daemon Configuration Suite")
}

var _ = Describe("native mlxconfig environment configuration", func() {
	DescribeTable("selects the configured PCI preference or reports invalid input",
		func(present bool, value string, expected bool, invalid bool) {
			GinkgoT().Setenv(mlxconfigUsePCIAddressEnv, value)
			if !present {
				Expect(os.Unsetenv(mlxconfigUsePCIAddressEnv)).To(Succeed())
			}
			options, err := nvConfigOptionsFromEnv()
			if invalid {
				Expect(err).To(MatchError(ContainSubstring(mlxconfigUsePCIAddressEnv + " must be a boolean")))
				Expect(options).To(Equal(nvconfig.Options{}))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(options).To(Equal(nvconfig.Options{UsePCIAddress: expected}))
		},
		Entry("unset retains fwctl preference", false, "", false, false),
		Entry("true selects PCI", true, "true", true, false),
		Entry("false retains fwctl preference", true, "false", false, false),
		Entry("numeric true", true, "1", true, false),
		Entry("numeric false", true, "0", false, false),
		Entry("empty explicit value is invalid", true, "", false, true),
		Entry("invalid value is rejected", true, "pci", false, true),
	)
})
