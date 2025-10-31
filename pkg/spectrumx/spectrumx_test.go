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

package spectrumx

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/dms/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	execUtils "k8s.io/utils/exec"
)

type fakeCmd struct {
	execUtils.Cmd
	output []byte
	err    error
	delay  time.Duration
}

func (c *fakeCmd) Output() ([]byte, error) {
	// Simulate some runtime for the command; tests that need immedate error set c.err
	if c.delay > 0 {
		time.Sleep(c.delay)
	} else if c.err == nil {
		// Default small delay
		time.Sleep(100 * time.Millisecond)
	}
	return c.output, c.err
}

func (f *fakeCmd) SetStderr(out io.Writer) {

}

type fakeExec struct {
	execUtils.Interface
	cmds []*fakeCmd
	pos  int
}

var (
	nextCmd *fakeCmd
)

func (f *fakeExec) next() execUtils.Cmd {
	if f.cmds != nil && f.pos < len(f.cmds) {
		c := f.cmds[f.pos]
		f.pos++
		return c
	}
	return nextCmd
}
func (f *fakeExec) Command(cmd string, args ...string) execUtils.Cmd { return f.next() }
func (f *fakeExec) CommandContext(ctx context.Context, cmd string, args ...string) execUtils.Cmd {
	return f.next()
}

var _ = Describe("SpectrumXConfigManager", func() {
	var (
		dmsMgr   mocks.DMSManager
		dmsCli   mocks.DMSClient
		manager  *spectrumXConfigManager
		device   *v1alpha1.NicDevice
		cfgs     map[string]*types.SpectrumXConfig
		execFake *fakeExec
	)

	beforeDevice := func() {
		device = &v1alpha1.NicDevice{
			Spec: v1alpha1.NicDeviceSpec{
				Configuration: &v1alpha1.NicDeviceConfigurationSpec{
					Template: &v1alpha1.ConfigurationTemplateSpec{
						SpectrumXOptimized: &v1alpha1.SpectrumXOptimizedSpec{Enabled: true, Version: "v1", Overlay: "none"},
						NumVfs:             1,
						LinkType:           v1alpha1.LinkTypeEnum("Ethernet"),
					},
				},
			},
			Status: v1alpha1.NicDeviceStatus{
				SerialNumber: "SN-1",
				Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:00:00.0", RdmaInterface: "mlx5_0"}},
			},
		}
	}

	BeforeEach(func() {
		dmsMgr = mocks.DMSManager{}
		dmsCli = mocks.DMSClient{}
		execFake = &fakeExec{}

		cfgs = map[string]*types.SpectrumXConfig{
			"v1": {
				NVConfig: []types.ConfigurationParameter{{Name: "a", Value: "1", DMSPath: "/a"}},
				RuntimeConfig: types.SpectrumXRuntimeConfig{
					Roce:              []types.ConfigurationParameter{{Name: "r", Value: "x", DMSPath: "/r"}},
					AdaptiveRouting:   []types.ConfigurationParameter{{Name: "ar", Value: "y", DMSPath: "/ar"}},
					CongestionControl: []types.ConfigurationParameter{{Name: "cc", Value: "z", DMSPath: "/cc"}},
					InterPacketGap: types.InterPacketGapConfig{
						PureL3: types.ConfigurationParameter{Name: "ipg_pure", DMSPath: "/ipg", Value: "25"},
						L3EVPN: types.ConfigurationParameter{Name: "ipg_l3evpn", DMSPath: "/ipg", Value: "33"},
					},
				},
				UseSoftwareCCAlgorithm: true,
			},
		}

		manager = &spectrumXConfigManager{dmsManager: &dmsMgr, spectrumXConfigs: cfgs, execInterface: execFake, ccProcesses: map[string]*ccProcess{}}

		beforeDevice()
		dmsMgr.On("GetDMSClientBySerialNumber", device.Status.SerialNumber).Return(&dmsCli, nil).Maybe()
	})

	Describe("NvConfigApplied", func() {
		It("returns true when values match", func() {
			dmsCli.On("GetParameters", cfgs["v1"].NVConfig).Return(map[string]string{"/a": "1"}, nil)
			applied, err := manager.NvConfigApplied(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeTrue())
		})

		It("returns false when any value mismatches", func() {
			dmsCli.On("GetParameters", cfgs["v1"].NVConfig).Return(map[string]string{"/a": "2"}, nil)
			applied, err := manager.NvConfigApplied(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeFalse())
		})

		It("returns error if DMS get fails", func() {
			dmsCli.On("GetParameters", cfgs["v1"].NVConfig).Return(nil, errors.New("get error"))
			_, err := manager.NvConfigApplied(device)
			Expect(err).To(HaveOccurred())
		})

		It("filters out non-matching DeviceId parameters", func() {
			cfgs["v1"].NVConfig = []types.ConfigurationParameter{
				{Name: "match", Value: "ok", DMSPath: "/m", DeviceId: "1023"},
				{Name: "skip", Value: "bad", DMSPath: "/s", DeviceId: "a2dc"},
			}
			device.Status.Type = "1023"
			expected := []types.ConfigurationParameter{{Name: "match", Value: "ok", DMSPath: "/m", DeviceId: "1023"}}
			dmsCli.On("GetParameters", expected).Return(map[string]string{"/m": "ok"}, nil)
			applied, err := manager.NvConfigApplied(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeTrue())
		})
	})

	Describe("ApplyNvConfig", func() {
		It("sets parameters via DMS", func() {
			dmsCli.On("SetParameters", cfgs["v1"].NVConfig).Return(nil)
			_, err := manager.ApplyNvConfig(device)
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns error if DMS set fails", func() {
			dmsCli.On("SetParameters", cfgs["v1"].NVConfig).Return(errors.New("set error"))
			_, err := manager.ApplyNvConfig(device)
			Expect(err).To(HaveOccurred())
		})

		It("filters out non-matching DeviceId parameters before setting", func() {
			cfgs["v1"].NVConfig = []types.ConfigurationParameter{
				{Name: "match", Value: "ok", DMSPath: "/m", DeviceId: "1023"},
				{Name: "skip", Value: "bad", DMSPath: "/s", DeviceId: "a2dc"},
			}
			device.Status.Type = "1023"
			expected := []types.ConfigurationParameter{{Name: "match", Value: "ok", DMSPath: "/m", DeviceId: "1023"}}
			dmsCli.On("SetParameters", expected).Return(nil)
			_, err := manager.ApplyNvConfig(device)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("RuntimeConfigApplied", func() {
		It("returns true when all runtime sections applied and CC runs", func() {
			dmsCli.On("GetParameters", []types.ConfigurationParameter{cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3}).Return(map[string]string{"/ipg": "25"}, nil)
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(map[string]string{"/r": "x"}, nil)
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(map[string]string{"/ar": "y"}, nil)
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(map[string]string{"/cc": "z"}, nil)

			nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
			applied, err := manager.RuntimeConfigApplied(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeTrue())
		})

		It("returns error if CC fails to start", func() {
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(map[string]string{"/r": "x"}, nil)
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(map[string]string{"/ar": "y"}, nil)

			nextCmd = &fakeCmd{output: []byte(""), err: errors.New("exec fail")}
			_, err := manager.RuntimeConfigApplied(device)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("exec fail"))
		})
	})

	Describe("ApplyRuntimeConfig", func() {
		It("sets sections, inter-packet gap for overlay=none, and runs CC", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.Overlay = consts.OverlayNone
			cfgs["v1"].RuntimeConfig.InterPacketGap = types.InterPacketGapConfig{
				PureL3: types.ConfigurationParameter{Name: "ipg_pure", DMSPath: "/ipg/pure", Value: "10"},
				L3EVPN: types.ConfigurationParameter{Name: "ipg_l3evpn", DMSPath: "/ipg/evpn", Value: "20"},
			}

			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(nil)
			dmsCli.On("SetParameters", []types.ConfigurationParameter{cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3}).Return(nil)

			nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
			err := manager.ApplyRuntimeConfig(device)
			Expect(err).NotTo(HaveOccurred())
		})

		It("sets inter-packet gap for overlay=l3", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.Overlay = "l3"
			cfgs["v1"].RuntimeConfig.InterPacketGap = types.InterPacketGapConfig{
				PureL3: types.ConfigurationParameter{Name: "ipg_pure", DMSPath: "/ipg/pure", Value: "10"},
				L3EVPN: types.ConfigurationParameter{Name: "ipg_l3evpn", DMSPath: "/ipg/evpn", Value: "20"},
			}

			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(nil)
			dmsCli.On("SetParameters", []types.ConfigurationParameter{cfgs["v1"].RuntimeConfig.InterPacketGap.L3EVPN}).Return(nil)

			nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
			err := manager.ApplyRuntimeConfig(device)
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns error for invalid overlay", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.Overlay = "invalid"
			cfgs["v1"].RuntimeConfig.InterPacketGap = types.InterPacketGapConfig{
				PureL3: types.ConfigurationParameter{Name: "ipg_pure", DMSPath: "/ipg/pure", Value: "10"},
				L3EVPN: types.ConfigurationParameter{Name: "ipg_l3evpn", DMSPath: "/ipg/evpn", Value: "20"},
			}

			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(nil)

			nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
			err := manager.ApplyRuntimeConfig(device)
			Expect(err).To(HaveOccurred())
			Expect(strings.ToLower(err.Error())).To(ContainSubstring("invalid overlay"))
		})

		It("bubbles up DMS errors", func() {
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(errors.New("roce set error"))
			err := manager.ApplyRuntimeConfig(device)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("roce set error"))
		})
	})

	Describe("GetDocaCCTargetVersion", func() {
		It("returns empty when SpectrumXOptimized is nil", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized = nil
			v, err := manager.GetDocaCCTargetVersion(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal(""))
		})

		It("returns version when UseSoftwareCCAlgorithm is true", func() {
			cfgs["v1"].UseSoftwareCCAlgorithm = true
			cfgs["v1"].DocaCCVersion = "1.2.3"
			device.Spec.Configuration.Template.SpectrumXOptimized = &v1alpha1.SpectrumXOptimizedSpec{Enabled: true, Version: "v1"}
			v, err := manager.GetDocaCCTargetVersion(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(v).To(Equal("1.2.3"))
		})

		It("returns error when version not found", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized = &v1alpha1.SpectrumXOptimizedSpec{Enabled: true, Version: "v2-missing"}
			_, err := manager.GetDocaCCTargetVersion(device)
			Expect(err).To(HaveOccurred())
			Expect(strings.ToLower(err.Error())).To(ContainSubstring("spectrumx config not found"))
		})
	})

	Describe("RunDocaSpcXCC", func() {
		It("returns nil if process already running", func() {
			port := device.Status.Ports[0]
			manager.ccProcesses[port.PCI] = &ccProcess{port: port}
			manager.ccProcesses[port.PCI].running.Store(true)
			err := manager.RunDocaSpcXCC(port)
			Expect(err).NotTo(HaveOccurred())
		})

		It("starts process and keeps running", func() {
			nextCmd = &fakeCmd{output: []byte("running"), err: nil, delay: 5 * time.Second}
			port := device.Status.Ports[0]
			err := manager.RunDocaSpcXCC(port)
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.ccProcesses).To(HaveKey(port.PCI))
		})

		It("returns error if process fails to start within wait window", func() {
			nextCmd = &fakeCmd{output: []byte(""), err: errors.New("failed")}
			port := device.Status.Ports[0]
			err := manager.RunDocaSpcXCC(port)
			Expect(err).To(HaveOccurred())
			Expect(strings.ToLower(err.Error())).To(ContainSubstring("failed to start"))
		})
	})
})
