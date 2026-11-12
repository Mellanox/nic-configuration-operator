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

package ncolog

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	zzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestLogging(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Logging Suite")
}

var _ = Describe("controller logging context", func() {
	It("applies the filter through the configured logger options", func() {
		var output bytes.Buffer
		logger := zap.New(zap.UseFlagOptions(Options), zap.WriteTo(&output),
			zap.Encoder(zapcore.NewJSONEncoder(zzap.NewProductionEncoderConfig())))
		logger.WithValues("controller", "nicDeviceReconciler", "reconcileID", "123", "device", "nic-0").
			Info("carrier check", "name", "ethgpu0")
		var entry map[string]any
		Expect(json.Unmarshal(output.Bytes(), &entry)).To(Succeed())
		Expect(entry).NotTo(HaveKey("controller"))
		Expect(entry).NotTo(HaveKey("reconcileID"))
		Expect(entry).To(HaveKeyWithValue("device", "nic-0"))
		Expect(entry).To(HaveKeyWithValue("name", "ethgpu0"))
	})

	It("omits inherited framework fields and preserves operation context", func() {
		core, output := observer.New(zapcore.DebugLevel)
		logger := zap.New(zap.UseFlagOptions(Options), zap.RawZapOpts(zzap.WrapCore(func(zapcore.Core) zapcore.Core {
			return withoutControllerContext(core)
		})))
		logger = logger.WithValues("controller", "nicDeviceReconciler", "controllerGroup", "configuration.net.nvidia.com",
			"controllerKind", "NicDevice", "NicDevice", "nic-device-sync-event-name", "namespace", "", "name", "sync")
		logger = logger.WithValues("reconcileID", "123", "device", "nic-0", "target", "pci/0000:17:00.0")
		logger.Info("command output", "command", "dms-cli", "stdout", "ok")
		Expect(output.All()).To(HaveLen(1))
		Expect(output.All()[0].ContextMap()).To(Equal(map[string]any{
			"device": "nic-0", "target": "pci/0000:17:00.0", "command": "dms-cli", "stdout": "ok",
		}))
	})

	It("retains fields supplied directly by an operation and error details", func() {
		core, output := observer.New(zapcore.DebugLevel)
		logger := zap.New(zap.UseFlagOptions(Options), zap.RawZapOpts(zzap.WrapCore(func(zapcore.Core) zapcore.Core {
			return withoutControllerContext(core)
		}))).WithValues("name", "sync", "reconcileID", "123")
		logger.Error(errors.New("register not supported"), "configuration failed", "name", "ethgpu0", "namespace", "test")
		Expect(output.All()).To(HaveLen(1))
		Expect(output.All()[0].ContextMap()).To(Equal(map[string]any{
			"error": "register not supported", "name": "ethgpu0", "namespace": "test",
		}))
	})
})

var _ = Describe("native Zap log levels", func() {
	BeforeEach(func() {
		original := Options.Level
		Options.Level = zzap.NewAtomicLevelAt(zapcore.InfoLevel)
		DeferCleanup(func() { Options.Level = original })
	})

	DescribeTable("parses native level names",
		func(name string, expected zapcore.Level) {
			Expect(SetLogLevel(name)).To(Succeed())
			Expect(Options.Level.(zzap.AtomicLevel).Level()).To(Equal(expected))
		},
		Entry("empty defaults to info", "", zapcore.InfoLevel),
		Entry("debug", "debug", zapcore.DebugLevel),
		Entry("uppercase debug", "DEBUG", zapcore.DebugLevel),
		Entry("mixed case info", "Info", zapcore.InfoLevel),
		Entry("warn", "warn", zapcore.WarnLevel),
		Entry("error", "error", zapcore.ErrorLevel),
		Entry("dpanic", "dpanic", zapcore.DPanicLevel),
		Entry("panic", "panic", zapcore.PanicLevel),
		Entry("fatal", "fatal", zapcore.FatalLevel),
	)

	DescribeTable("rejects invalid levels without changing the threshold", func(name string) {
		Expect(SetLogLevel("error")).To(Succeed())
		Expect(SetLogLevel(name)).NotTo(Succeed())
		Expect(Options.Level.(zzap.AtomicLevel).Level()).To(Equal(zapcore.ErrorLevel))
	}, Entry("unknown", "trace"), Entry("numeric", "2"))

	It("changes filtering for an existing logger, including verbosity-qualified errors", func() {
		core, output := observer.New(Options.Level)
		logger := zap.New(zap.UseFlagOptions(Options), zap.RawZapOpts(zzap.WrapCore(func(zapcore.Core) zapcore.Core {
			return core
		})))
		emit := func() {
			logger.V(DebugLevel).Info("debug")
			logger.Info("info")
			logger.V(DebugLevel).Error(errors.New("failed"), "error")
		}
		for _, tc := range []struct {
			level    string
			messages []string
		}{
			{level: "info", messages: []string{"info", "error"}},
			{level: "debug", messages: []string{"debug", "info", "error"}},
			{level: "warn", messages: []string{"error"}},
			{level: "error", messages: []string{"error"}},
			{level: "info", messages: []string{"info", "error"}},
		} {
			Expect(SetLogLevel(tc.level)).To(Succeed())
			emit()
			entries := output.TakeAll()
			messages := make([]string, 0, len(entries))
			for _, entry := range entries {
				messages = append(messages, entry.Message)
				if entry.Message == "debug" {
					Expect(entry.Level).To(Equal(zapcore.DebugLevel))
				}
			}
			Expect(messages).To(Equal(tc.messages))
		}
	})

	It("filters the threshold-change announcement using the new level", func() {
		core, output := observer.New(Options.Level)
		originalLogger := log.Log
		log.Log = zap.New(zap.UseFlagOptions(Options), zap.RawZapOpts(zzap.WrapCore(func(zapcore.Core) zapcore.Core {
			return core
		})))
		DeferCleanup(func() { log.Log = originalLogger })

		Expect(SetLogLevel("error")).To(Succeed())
		Expect(output.All()).To(BeEmpty())
		Expect(SetLogLevel("debug")).To(Succeed())
		Expect(output.All()).To(HaveLen(1))
		Expect(output.All()[0].ContextMap()).To(Equal(map[string]any{
			"new-level": "debug", "current-level": "error",
		}))
	})

	It("enables named debug call sites with the Zap flag and allows an environment override", func() {
		flags := flag.NewFlagSet("logging-test", flag.ContinueOnError)
		BindFlags(flags)
		Expect(flags.Parse([]string{"--zap-log-level=debug"})).To(Succeed())
		logger := zap.New(zap.UseFlagOptions(Options))
		Expect(logger.V(DebugLevel).Enabled()).To(BeTrue())
		Expect(SetLogLevel("error")).To(Succeed())
		Expect(logger.Enabled()).To(BeFalse())
		Expect(logger.V(DebugLevel).Enabled()).To(BeFalse())
	})
})
