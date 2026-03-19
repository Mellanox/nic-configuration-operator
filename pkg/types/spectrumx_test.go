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
	It("parses RA3.0.yaml and populates fields", func() {
		configPath := filepath.Join(repoRootFromThisFile(), "bindata", "spectrum-x", "RA3.0.yaml")

		cfg, err := LoadSpectrumXConfig(configPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(cfg).ToNot(BeNil())

		// Check mlxConfig structure
		Expect(cfg.MlxConfig).To(HaveKey("swplb"))
		Expect(cfg.MlxConfig).To(HaveKey("hwplb"))
		Expect(cfg.MlxConfig).To(HaveKey("uniplane"))
		Expect(cfg.MlxConfig).To(HaveKey("none"))

		// Check CX8 swplb breakout
		cx8Swplb := cfg.MlxConfig["swplb"]["1023"]
		Expect(cx8Swplb.Breakout).To(HaveKey(2))
		Expect(cx8Swplb.Breakout).To(HaveKey(4))
		Expect(cx8Swplb.Breakout[2]).To(HaveKeyWithValue("NUM_OF_PF", "2"))
		Expect(cx8Swplb.Breakout[2]).To(HaveKeyWithValue("NUM_OF_PLANES_P1", "0"))

		// Check BF3 swplb breakout
		bf3Swplb := cfg.MlxConfig["swplb"]["a2dc"]
		Expect(bf3Swplb.Breakout).To(HaveKey(2))
		Expect(bf3Swplb.Breakout[2]).To(HaveKeyWithValue("NUM_OF_PF", "2"))

		// Check postBreakout
		Expect(cx8Swplb.PostBreakout).To(HaveKeyWithValue("LINK_TYPE_P1", "2"))
		Expect(cx8Swplb.PostBreakout).To(HaveKeyWithValue("SRIOV_EN", "1"))
		Expect(bf3Swplb.PostBreakout).To(HaveKeyWithValue("INTERNAL_CPU_MODEL", "1"))

		// Check hwplb CX8 only
		Expect(cfg.MlxConfig["hwplb"]).To(HaveKey("1023"))
		cx8Hwplb := cfg.MlxConfig["hwplb"]["1023"]
		Expect(cx8Hwplb.Breakout[2]).To(HaveKeyWithValue("NUM_OF_PLANES_P1", "2"))
		Expect(cx8Hwplb.PostBreakout).To(HaveKeyWithValue("FLEX_PARSER_PROFILE_ENABLE", "10"))

		// Check runtime config
		Expect(cfg.UseSoftwareCCAlgorithm).To(BeTrue())
		Expect(cfg.RuntimeConfig.AdaptiveRouting).ToNot(BeEmpty())
		Expect(cfg.RuntimeConfig.CongestionControl).ToNot(BeEmpty())
		Expect(cfg.RuntimeConfig.InterPacketGap.PureL3[0].Name).ToNot(BeEmpty())
		Expect(cfg.RuntimeConfig.InterPacketGap.L3EVPN[0].Name).ToNot(BeEmpty())
	})
})
