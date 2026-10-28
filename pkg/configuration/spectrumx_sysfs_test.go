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

package configuration

import (
	"os"
	"path/filepath"

	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CC global-status sysfs readback", func() {
	var root string
	var query dmscli.XPathQuery
	write := func(subsystem, name, value string) {
		path := filepath.Join(root, "bus/pci/devices/0001:03:00.0", subsystem, name, "ecn/roce_np/enable/2")
		Expect(os.MkdirAll(filepath.Dir(path), 0755)).To(Succeed())
		Expect(os.WriteFile(path, []byte(value), 0600)).To(Succeed())
	}
	BeforeEach(func() {
		root = GinkgoT().TempDir()
		query = dmscli.XPathQuery{Path: "/nvidia/cc/global-status/[np,2]", Leaves: []string{"enabled"}}
	})
	DescribeTable("reads fresh boolean values", func(subsystem, value string, expected bool) {
		write(subsystem, "device0", value)
		actual, ok := readCCGlobalStatus(root, "pci/0001:03:00.0", query)
		Expect(ok).To(BeTrue())
		Expect(actual).To(Equal(expected))
	},
		Entry("netdev enabled", "net", "1\n", true),
		Entry("netdev disabled", "net", "0\n", false),
		Entry("RDMA fallback", "infiniband", "1\n", true))
	It("reads RP priorities and falls back on read errors", func() {
		write("net", "eth0", "1")
		base := filepath.Join(root, "bus/pci/devices/0001:03:00.0/net/eth0/ecn")
		Expect(os.Rename(filepath.Join(base, "roce_np"), filepath.Join(base, "roce_rp"))).To(Succeed())
		query.Path = "/nvidia/cc/global-status/[rp,2]"
		actual, ok := readCCGlobalStatus(root, "pci/0001:03:00.0", query)
		Expect(ok).To(BeTrue())
		Expect(actual).To(BeTrue())
		path := filepath.Join(base, "roce_rp/enable/2")
		Expect(os.Remove(path)).To(Succeed())
		Expect(os.Mkdir(path, 0755)).To(Succeed())
		_, ok = readCCGlobalStatus(root, "pci/0001:03:00.0", query)
		Expect(ok).To(BeFalse())
	})
	It("prefers netdev state and does not cache it", func() {
		write("net", "eth0", "0")
		write("infiniband", "roce0", "1")
		actual, ok := readCCGlobalStatus(root, "pci/0001:03:00.0", query)
		Expect(ok).To(BeTrue())
		Expect(actual).To(BeFalse())
		write("net", "eth0", "1")
		actual, ok = readCCGlobalStatus(root, "pci/0001:03:00.0", query)
		Expect(ok).To(BeTrue())
		Expect(actual).To(BeTrue())
	})
	It("defers missing, malformed, ambiguous and wrong-target reads to DMS", func() {
		_, ok := readCCGlobalStatus(root, "pci/0001:03:00.0", query)
		Expect(ok).To(BeFalse())
		write("net", "eth0", "invalid")
		_, ok = readCCGlobalStatus(root, "pci/0001:03:00.0", query)
		Expect(ok).To(BeFalse())
		write("net", "eth0", "1")
		_, ok = readCCGlobalStatus(root, "pci/0001:03:00.1", query)
		Expect(ok).To(BeFalse())
		write("net", "eth1", "0")
		_, ok = readCCGlobalStatus(root, "pci/0001:03:00.0", query)
		Expect(ok).To(BeFalse())
	})
	DescribeTable("leaves unsupported queries to DMS", func(target, path string, leaves []string) {
		write("net", "eth0", "1")
		_, ok := readCCGlobalStatus(root, target, dmscli.XPathQuery{Path: path, Leaves: leaves})
		Expect(ok).To(BeFalse())
	},
		Entry("port selector", "pci/0001:03:00.0?port=1", "/nvidia/cc/global-status/[np,2]", []string{"enabled"}),
		Entry("other leaf", "pci/0001:03:00.0", "/nvidia/cc/global-status/[np,2]", []string{"priority"}),
		Entry("invalid priority", "pci/0001:03:00.0", "/nvidia/cc/global-status/[np,8]", []string{"enabled"}))
})
