/*
2025 NVIDIA CORPORATION & AFFILIATES
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

package types

import (
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func repoRootFromThisFile() string {
	_, thisFile, _, _ := runtime.Caller(0)
	pkgDir := filepath.Dir(thisFile)
	return filepath.Dir(filepath.Dir(pkgDir))
}

var _ = Describe("LoadSpectrumXConfig", func() {
	It("parses RA2.0.yaml and populates fields", func() {
		configPath := filepath.Join(repoRootFromThisFile(), "bindata", "spectrum-x", "RA2.0.yaml")

		cfg, err := LoadSpectrumXConfig(configPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(cfg).ToNot(BeNil())

		Expect(cfg.NVConfig).ToNot(BeEmpty())

		first := cfg.NVConfig[0]
		Expect(first.Name).ToNot(BeEmpty())
		Expect(first.DMSPath).ToNot(BeEmpty())
		Expect(first.ValueType).ToNot(BeEmpty())

		Expect(cfg.UseSoftwareCCAlgorithm).To(BeTrue())
		Expect(cfg.RuntimeConfig.AdaptiveRouting).ToNot(BeEmpty())
		Expect(cfg.RuntimeConfig.CongestionControl).ToNot(BeEmpty())

		Expect(cfg.RuntimeConfig.InterPacketGap.PureL3.Name).ToNot(BeEmpty())
		Expect(cfg.RuntimeConfig.InterPacketGap.L3EVPN.Name).ToNot(BeEmpty())
	})
})
