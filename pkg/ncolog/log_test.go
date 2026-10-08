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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	zzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
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
