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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	zzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

var _ = Describe("NVConfig mismatch diagnostics", func() {
	DescribeTable("reports the values behind update and reboot decisions at info level",
		func(current, next []string, expectedUpdate bool) {
			core, output := observer.New(zapcore.InfoLevel)
			logger := zap.New(zap.RawZapOpts(zzap.WrapCore(func(zapcore.Core) zapcore.Core {
				return core
			}))).WithValues("device", "test-device")
			query := types.NvConfigQuery{
				DefaultConfig:  map[string][]string{"NUM_OF_PF": {"1"}},
				CurrentConfig:  map[string][]string{},
				NextBootConfig: map[string][]string{"NUM_OF_PF": next},
				AllParameters:  true,
			}
			if current != nil {
				query.CurrentConfig["NUM_OF_PF"] = current
			}

			update, reboot, unsupported := validateTemplateParamsApplied(
				logger, map[string]types.NvConfigQuery{pciAddress: query}, map[string]string{"NUM_OF_PF": "4"})

			Expect(update).To(Equal(expectedUpdate))
			Expect(reboot).To(BeTrue())
			Expect(unsupported).To(BeEmpty())
			Expect(output.All()).To(HaveLen(1))
			entry := output.All()[0]
			Expect(entry.Message).To(Equal("NVConfig parameter differs from desired value"))
			Expect(entry.ContextMap()).To(HaveKeyWithValue("device", "test-device"))
			Expect(entry.ContextMap()).To(HaveKeyWithValue("pciAddr", pciAddress))
			Expect(entry.ContextMap()).To(HaveKeyWithValue("parameter", "NUM_OF_PF"))
			Expect(entry.ContextMap()).To(HaveKeyWithValue("desiredValue", "4"))
			Expect(entry.ContextMap()).To(HaveKeyWithValue("currentValues", ConsistOf(current)))
			Expect(entry.ContextMap()).To(HaveKeyWithValue("nextBootValues", ConsistOf(next)))
			Expect(entry.ContextMap()).To(HaveKeyWithValue("nextBootMatches", !expectedUpdate))
		},
		Entry("next-boot mismatch requires update and reboot", []string{"4"}, []string{"1"}, true),
		Entry("current-only mismatch requires reboot", []string{"1"}, []string{"4"}, false),
		Entry("both columns differ", []string{"0"}, []string{"1"}, true),
		Entry("missing current value requires reboot", []string(nil), []string{"4"}, false),
	)

	It("keeps equivalent values quiet and preserves unsupported parameter reporting", func() {
		core, output := observer.New(zapcore.InfoLevel)
		logger := zap.New(zap.RawZapOpts(zzap.WrapCore(func(zapcore.Core) zapcore.Core {
			return core
		})))
		query := types.NvConfigQuery{
			DefaultConfig:  map[string][]string{"NUM_OF_PF": {"1"}},
			CurrentConfig:  map[string][]string{"NUM_OF_PF": {"0x4"}},
			NextBootConfig: map[string][]string{"NUM_OF_PF": {"04"}},
			AllParameters:  true,
		}

		update, reboot, unsupported := validateTemplateParamsApplied(
			logger, map[string]types.NvConfigQuery{pciAddress: query},
			map[string]string{"NUM_OF_PF": "4", "UNSUPPORTED": "1"})

		Expect(update).To(BeFalse())
		Expect(reboot).To(BeFalse())
		Expect(unsupported).To(Equal([]string{"UNSUPPORTED"}))
		Expect(output.All()).To(BeEmpty())
	})
})
