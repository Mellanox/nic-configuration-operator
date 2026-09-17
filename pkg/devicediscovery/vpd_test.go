/*
2026 NVIDIA CORPORATION & AFFILIATES
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

package devicediscovery

import (
	"github.com/jaypipes/ghw/pkg/pci"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("PCI VPD", func() {
	Describe("mapPCIVPD", func() {
		It("maps and normalizes the identifier and required read-only keywords", func() {
			modelName := "NVIDIA ConnectX-9 C9180 HHHL SuperNIC"
			parsed := &pci.VPD{
				Identifier: "  " + modelName + "\x00",
				ReadOnly: map[string]string{
					"PN": " " + partNumber + " ",
					"SN": serialNumber + "\n",
				},
			}

			vpd, err := mapPCIVPD(parsed)

			Expect(err).NotTo(HaveOccurred())
			Expect(vpd.ModelName).To(Equal(modelName))
			Expect(vpd.PartNumber).To(Equal(partNumber))
			Expect(vpd.SerialNumber).To(Equal(serialNumber))
		})

		It("preserves the optional-model behavior when the identifier string is absent", func() {
			parsed := &pci.VPD{ReadOnly: map[string]string{
				"PN": partNumber,
				"SN": serialNumber,
			}}

			vpd, err := mapPCIVPD(parsed)

			Expect(err).NotTo(HaveOccurred())
			Expect(vpd.ModelName).To(BeEmpty())
		})

		DescribeTable("rejects missing required fields",
			func(pn, sn, missing string) {
				parsed := &pci.VPD{ReadOnly: map[string]string{"PN": pn, "SN": sn}}

				vpd, err := mapPCIVPD(parsed)

				Expect(err).To(MatchError(ContainSubstring("missing required keyword(s): " + missing)))
				Expect(vpd).To(BeNil())
			},
			Entry("missing PN", "", serialNumber, "PN"),
			Entry("missing SN", partNumber, "", "SN"),
			Entry("missing PN and SN", "", "", "PN, SN"),
		)

		It("rejects invalid text in a required field", func() {
			parsed := &pci.VPD{ReadOnly: map[string]string{
				"PN": string([]byte{0xff}),
				"SN": serialNumber,
			}}

			vpd, err := mapPCIVPD(parsed)

			Expect(err).To(MatchError("VPD field PN contains invalid UTF-8"))
			Expect(vpd).To(BeNil())
		})

		It("rejects a nil parsed VPD", func() {
			vpd, err := mapPCIVPD(nil)

			Expect(err).To(MatchError("parsed VPD is nil"))
			Expect(vpd).To(BeNil())
		})
	})
})
