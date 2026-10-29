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

package ncolog

import (
	"flag"

	zzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// logr uses positive verbosity for debug, whereas Zap uses negative levels.
// Keep this adaptation at the logging boundary; thresholds use native Zap levels.
const (
	DebugLevel = 1
	InfoLevel  = 0
)

// Options stores controller-runtime (zap) log config
var Options = &zap.Options{
	Development: true,
	// we dont log with panic level, so this essentially
	// disables stacktrace, for now, it avoids un-needed clutter in logs
	StacktraceLevel: zapcore.DPanicLevel,
	TimeEncoder:     zapcore.RFC3339NanoTimeEncoder,
	Level:           zzap.NewAtomicLevelAt(zapcore.InfoLevel),
	// log caller (file and line number) in "caller" key
	EncoderConfigOptions: []zap.EncoderConfigOption{func(ec *zapcore.EncoderConfig) { ec.CallerKey = "caller" }},
	ZapOpts:              []zzap.Option{zzap.AddCaller(), zzap.WrapCore(withoutControllerContext)},
}

// controllerContextCore removes framework fields bound to a logger while keeping
// fields supplied by individual operations, including interface names and errors.
type controllerContextCore struct {
	zapcore.Core
}

func withoutControllerContext(core zapcore.Core) zapcore.Core {
	return controllerContextCore{Core: core}
}

func (core controllerContextCore) With(fields []zapcore.Field) zapcore.Core {
	filtered := make([]zapcore.Field, 0, len(fields))
	for _, field := range fields {
		switch field.Key {
		case "controller", "controllerGroup", "controllerKind", "NicDevice", "namespace", "name", "reconcileID":
			continue
		}
		filtered = append(filtered, field)
	}
	return controllerContextCore{Core: core.Core.With(filtered)}
}

// BindFlags binds controller-runtime logging flags to provided flag Set
func BindFlags(fs *flag.FlagSet) {
	Options.BindFlags(fs)
}

// InitLog initializes controller-runtime log (zap log)
// this should be called once Options have been initialized
// either by parsing flags or directly modifying Options.
func InitLog() {
	log.SetLogger(zap.New(zap.UseFlagOptions(Options)))
}

// SetLogLevel sets the native Zap threshold. Empty means info, as in zapcore.ParseLevel.
func SetLogLevel(level string) error {
	newLevel, err := zapcore.ParseLevel(level)
	if err != nil {
		return err
	}
	atomicLevel := Options.Level.(zzap.AtomicLevel)
	currLevel := atomicLevel.Level()
	if newLevel != currLevel {
		atomicLevel.SetLevel(newLevel)
		log.Log.Info("Set log level", "new-level", newLevel.String(), "current-level", currLevel.String())
	}
	return nil
}
