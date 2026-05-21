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
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const spectrumXTestYAML = `
useSoftwareCCAlgorithm: true
docaCCVersion: "1.0"
mlxConfig:
  swplb:
    "1023":
      breakout:
        2:
          NUM_OF_PF: "2"
          NUM_OF_PLANES_P1: "0"
        4:
          NUM_OF_PF: "4"
      postBreakout:
        LINK_TYPE_P1: "2"
        SRIOV_EN: "1"
runtimeConfig:
  roce: []
  adaptiveRouting:
    - name: ar_enabled
      mlxconfig: ROCE_ADAPTIVE_ROUTING_EN
      value: "1"
  congestionControl:
    - name: cc_enabled
      mlxconfig: ROCE_CC_PRIO_MASK_P1
      value: "0xff"
  interPacketGap:
    pureL3:
      - name: ipg_pureL3
        mlxconfig: IPG_PURE_L3
        value: "1"
    l3EVPN:
      - name: ipg_l3evpn
        mlxconfig: IPG_L3_EVPN
        value: "1"
`

var _ = Describe("LoadSpectrumXConfig", func() {
	It("parses an inline YAML payload and populates fields", func() {
		dir := GinkgoT().TempDir()
		configPath := filepath.Join(dir, "test.yaml")
		Expect(os.WriteFile(configPath, []byte(spectrumXTestYAML), 0o600)).To(Succeed())

		cfg, err := LoadSpectrumXConfig(configPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(cfg).ToNot(BeNil())

		Expect(cfg.MlxConfig).To(HaveKey("swplb"))
		swplb := cfg.MlxConfig["swplb"]["1023"]
		Expect(swplb.Breakout).To(HaveKey(2))
		Expect(swplb.Breakout).To(HaveKey(4))
		Expect(swplb.Breakout[2]).To(HaveKeyWithValue("NUM_OF_PF", "2"))
		Expect(swplb.Breakout[2]).To(HaveKeyWithValue("NUM_OF_PLANES_P1", "0"))
		Expect(swplb.PostBreakout).To(HaveKeyWithValue("LINK_TYPE_P1", "2"))
		Expect(swplb.PostBreakout).To(HaveKeyWithValue("SRIOV_EN", "1"))

		Expect(cfg.UseSoftwareCCAlgorithm).To(BeTrue())
		Expect(cfg.DocaCCVersion).To(Equal("1.0"))
		Expect(cfg.RuntimeConfig.AdaptiveRouting).ToNot(BeEmpty())
		Expect(cfg.RuntimeConfig.AdaptiveRouting[0].Name).To(Equal("ar_enabled"))
		Expect(cfg.RuntimeConfig.CongestionControl).ToNot(BeEmpty())
		Expect(cfg.RuntimeConfig.InterPacketGap.PureL3[0].Name).To(Equal("ipg_pureL3"))
		Expect(cfg.RuntimeConfig.InterPacketGap.L3EVPN[0].Name).To(Equal("ipg_l3evpn"))
	})

	It("returns an error when the file does not exist", func() {
		_, err := LoadSpectrumXConfig(filepath.Join(GinkgoT().TempDir(), "missing.yaml"))
		Expect(err).To(HaveOccurred())
	})
})
