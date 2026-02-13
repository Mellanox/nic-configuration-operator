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
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	dmsmocks "github.com/Mellanox/nic-configuration-operator/pkg/dms/mocks"
	nvconfigmocks "github.com/Mellanox/nic-configuration-operator/pkg/nvconfig/mocks"
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

func (c *fakeCmd) CombinedOutput() ([]byte, error) {
	return c.Output()
}

type fakeExec struct {
	execUtils.Interface
	cmds []*fakeCmd
	pos  int
}

const (
	shutdownInterfaceParamName = "Shut down interface"
	bringUpInterfaceParamName  = "Bring up interface to apply IPG settings"
)

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
		dmsMgr      dmsmocks.DMSManager
		dmsCli      dmsmocks.DMSClient
		nvConfigMgr nvconfigmocks.NVConfigUtils
		manager     *spectrumXConfigManager
		device      *v1alpha1.NicDevice
		cfgs        map[string]*types.SpectrumXConfig
		execFake    *fakeExec
		ctx         context.Context
	)

	beforeDevice := func() {
		device = &v1alpha1.NicDevice{
			Spec: v1alpha1.NicDeviceSpec{
				Configuration: &v1alpha1.NicDeviceConfigurationSpec{
					Template: &v1alpha1.ConfigurationTemplateSpec{
						SpectrumXOptimized: &v1alpha1.SpectrumXOptimizedSpec{Enabled: true, Version: "v1", Overlay: "none", MultiplaneMode: "none", NumberOfPlanes: 1},
						NumVfs:             1,
						LinkType:           v1alpha1.LinkTypeEnum("Ethernet"),
					},
				},
			},
			Status: v1alpha1.NicDeviceStatus{
				SerialNumber: "SN-1",
				Type:         "1023",
				Ports:        []v1alpha1.NicDevicePortSpec{{PCI: "0000:00:00.0", RdmaInterface: "mlx5_0"}},
			},
		}
	}

	BeforeEach(func() {
		dmsMgr = dmsmocks.DMSManager{}
		dmsCli = dmsmocks.DMSClient{}
		nvConfigMgr = nvconfigmocks.NVConfigUtils{}
		execFake = &fakeExec{}
		ctx = context.Background()

		cfgs = map[string]*types.SpectrumXConfig{
			"v1": {
				NVConfig: []types.ConfigurationParameter{{Name: "a", Value: "1", DMSPath: "/a"}},
				BreakoutConfig: types.SpectrumXBreakoutConfig{
					Swplb: map[int][]types.ConfigurationParameter{
						2: {
							{Name: "swplb_2_param1", Value: "sw2val1", DMSPath: "/swplb/2/p1"},
							{Name: "swplb_2_param2", Value: "sw2val2", DMSPath: "/swplb/2/p2"},
						},
						4: {
							{Name: "swplb_4_param1", Value: "sw4val1", DMSPath: "/swplb/4/p1"},
							{Name: "swplb_4_param2", Value: "sw4val2", DMSPath: "/swplb/4/p2"},
						},
					},
					Hwplb: map[int][]types.ConfigurationParameter{
						2: {
							{Name: "hwplb_2_param1", Value: "hw2val1", DMSPath: "/hwplb/2/p1"},
							{Name: "hwplb_2_param2", Value: "hw2val2", DMSPath: "/hwplb/2/p2"},
						},
						4: {
							{Name: "hwplb_4_param1", Value: "hw4val1", DMSPath: "/hwplb/4/p1"},
							{Name: "hwplb_4_param2", Value: "hw4val2", DMSPath: "/hwplb/4/p2"},
						},
					},
					Uniplane: map[int][]types.ConfigurationParameter{
						2: {
							{Name: "uniplane_2_param1", Value: "uni2val1", DMSPath: "/uniplane/2/p1"},
							{Name: "uniplane_2_param2", Value: "uni2val2", DMSPath: "/uniplane/2/p2"},
						},
						4: {
							{Name: "uniplane_4_param1", Value: "uni4val1", DMSPath: "/uniplane/4/p1"},
							{Name: "uniplane_4_param2", Value: "uni4val2", DMSPath: "/uniplane/4/p2"},
						},
					},
					None: map[int][]types.ConfigurationParameter{
						1: {
							{Name: "none_num_pfs", Value: "1", DMSPath: "/none/num-pf"},
							{Name: "none_num_planes", Value: "0", MlxConfig: "NUM_OF_PLANES_P1"},
						},
					},
				},
				RuntimeConfig: types.SpectrumXRuntimeConfig{
					Roce:              []types.ConfigurationParameter{{Name: "r", Value: "x", DMSPath: "/r"}},
					AdaptiveRouting:   []types.ConfigurationParameter{{Name: "ar", Value: "y", DMSPath: "/ar"}},
					CongestionControl: []types.ConfigurationParameter{{Name: "cc", Value: "z", DMSPath: "/cc"}},
					InterPacketGap: types.InterPacketGapConfig{
						PureL3: []types.ConfigurationParameter{{Name: "ipg_pure", DMSPath: "/ipg", Value: "25"}},
						L3EVPN: []types.ConfigurationParameter{{Name: "ipg_l3evpn", DMSPath: "/ipg", Value: "33"}},
					},
				},
				UseSoftwareCCAlgorithm: true,
			},
		}

		manager = &spectrumXConfigManager{
			dmsManager:        &dmsMgr,
			spectrumXConfigs:  cfgs,
			execInterface:     execFake,
			nvConfigUtils:     &nvConfigMgr,
			ccProcesses:       map[string]*ccProcess{},
			ccTerminationChan: make(chan string, 10),
		}

		beforeDevice()
		dmsMgr.On("GetDMSClientBySerialNumber", device.Status.SerialNumber).Return(&dmsCli, nil).Maybe()
	})

	Describe("NvConfigApplied", func() {
		It("returns true when values match", func() {
			dmsCli.On("GetParameters", cfgs["v1"].NVConfig).Return(map[string]string{"/a": "1"}, nil)
			applied, err := manager.NvConfigApplied(ctx, device)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeTrue())
		})

		It("returns false when any value mismatches", func() {
			dmsCli.On("GetParameters", cfgs["v1"].NVConfig).Return(map[string]string{"/a": "2"}, nil)
			applied, err := manager.NvConfigApplied(ctx, device)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeFalse())
		})

		It("returns error if DMS get fails", func() {
			dmsCli.On("GetParameters", cfgs["v1"].NVConfig).Return(nil, errors.New("get error"))
			_, err := manager.NvConfigApplied(ctx, device)
			Expect(err).To(HaveOccurred())
		})

		It("filters out non-matching DeviceId parameters", func() {
			cfgs["v1"].NVConfig = []types.ConfigurationParameter{
				{Name: "match", Value: "ok", DMSPath: "/m", DeviceId: "1023"},
				{Name: "skip", Value: "bad", DMSPath: "/s", DeviceId: consts.BlueField3DeviceID},
			}
			device.Status.Type = "1023"
			expected := []types.ConfigurationParameter{{Name: "match", Value: "ok", DMSPath: "/m", DeviceId: "1023"}}
			dmsCli.On("GetParameters", expected).Return(map[string]string{"/m": "ok"}, nil)
			applied, err := manager.NvConfigApplied(ctx, device)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeTrue())
		})

		It("returns false when NvConfig has ValuesDoNotMatchError", func() {
			valuesDoNotMatchErr := types.ValuesDoNotMatchError(types.ConfigurationParameter{Name: "nv_param"}, "mismatch_value")
			dmsCli.On("GetParameters", cfgs["v1"].NVConfig).Return(nil, valuesDoNotMatchErr)

			applied, err := manager.NvConfigApplied(ctx, device)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeFalse())
		})
	})

	Describe("ApplyNvConfig", func() {
		It("sets parameters via DMS", func() {
			dmsCli.On("SetParameters", cfgs["v1"].NVConfig).Return(nil)
			err := manager.ApplyNvConfig(ctx, device)
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns error if DMS set fails", func() {
			dmsCli.On("SetParameters", cfgs["v1"].NVConfig).Return(errors.New("set error"))
			err := manager.ApplyNvConfig(ctx, device)
			Expect(err).To(HaveOccurred())
		})

		It("filters out non-matching DeviceId parameters before setting", func() {
			cfgs["v1"].NVConfig = []types.ConfigurationParameter{
				{Name: "match", Value: "ok", DMSPath: "/m", DeviceId: "1023"},
				{Name: "skip", Value: "bad", DMSPath: "/s", DeviceId: consts.BlueField3DeviceID},
			}
			device.Status.Type = "1023"
			expected := []types.ConfigurationParameter{{Name: "match", Value: "ok", DMSPath: "/m", DeviceId: "1023"}}
			dmsCli.On("SetParameters", expected).Return(nil)
			err := manager.ApplyNvConfig(ctx, device)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("RuntimeConfigApplied", func() {
		It("returns true when all runtime sections applied and CC runs", func() {
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(map[string]string{"/ipg": "25"}, nil)
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(map[string]string{"/r": "x"}, nil)
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(map[string]string{"/ar": "y"}, nil)
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(map[string]string{"/cc": "z"}, nil)
			manager.ccProcesses[device.Status.Ports[0].RdmaInterface] = &ccProcess{port: device.Status.Ports[0]}
			manager.ccProcesses[device.Status.Ports[0].RdmaInterface].running.Store(true)
			nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
			applied, err := manager.RuntimeConfigApplied(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeTrue())
		})

		It("returns false if DOCA SPC-X CC algorithm is not running", func() {
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(map[string]string{"/r": "x"}, nil)
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(map[string]string{"/ar": "y"}, nil)

			applied, err := manager.RuntimeConfigApplied(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeFalse())
		})

		It("returns false when RoCE config has ValuesDoNotMatchError", func() {
			valuesDoNotMatchErr := types.ValuesDoNotMatchError(types.ConfigurationParameter{Name: "test_param"}, "mismatch_value")
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(nil, valuesDoNotMatchErr)

			applied, err := manager.RuntimeConfigApplied(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeFalse())
		})
	})

	Describe("ApplyRuntimeConfig", func() {
		It("sets sections, inter-packet gap for overlay=none, and runs CC", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.Overlay = consts.OverlayNone
			cfgs["v1"].RuntimeConfig.InterPacketGap = types.InterPacketGapConfig{
				PureL3: []types.ConfigurationParameter{{Name: "ipg_pure", DMSPath: "/ipg/pure", Value: "10"}},
				L3EVPN: []types.ConfigurationParameter{{Name: "ipg_l3evpn", DMSPath: "/ipg/evpn", Value: "20"}},
			}

			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(nil)
			dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
				return len(params) == 1 && params[0].Name == shutdownInterfaceParamName
			})).Return(nil)
			dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
				return len(params) == 1 && params[0].Name == bringUpInterfaceParamName
			})).Return(nil)

			nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
			err := manager.ApplyRuntimeConfig(device)
			Expect(err).NotTo(HaveOccurred())
		})

		It("sets inter-packet gap for overlay=l3", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.Overlay = "l3"
			cfgs["v1"].RuntimeConfig.InterPacketGap = types.InterPacketGapConfig{
				PureL3: []types.ConfigurationParameter{{Name: "ipg_pure", DMSPath: "/ipg/pure", Value: "10"}},
				L3EVPN: []types.ConfigurationParameter{{Name: "ipg_l3evpn", DMSPath: "/ipg/evpn", Value: "20"}},
			}

			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.L3EVPN).Return(nil)
			dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
				return len(params) == 1 && params[0].Name == shutdownInterfaceParamName
			})).Return(nil)
			dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
				return len(params) == 1 && params[0].Name == bringUpInterfaceParamName
			})).Return(nil)

			nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
			err := manager.ApplyRuntimeConfig(device)
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns error for invalid overlay", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.Overlay = "invalid"
			cfgs["v1"].RuntimeConfig.InterPacketGap = types.InterPacketGapConfig{
				PureL3: []types.ConfigurationParameter{{Name: "ipg_pure", DMSPath: "/ipg/pure", Value: "10"}},
				L3EVPN: []types.ConfigurationParameter{{Name: "ipg_l3evpn", DMSPath: "/ipg/evpn", Value: "20"}},
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
			manager.ccProcesses[port.RdmaInterface] = &ccProcess{port: port}
			manager.ccProcesses[port.RdmaInterface].running.Store(true)
			err := manager.RunDocaSpcXCC(port)
			Expect(err).NotTo(HaveOccurred())
		})

		It("starts process and keeps running", func() {
			nextCmd = &fakeCmd{output: []byte("running"), err: nil, delay: 5 * time.Second}
			port := device.Status.Ports[0]
			err := manager.RunDocaSpcXCC(port)
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.ccProcesses).To(HaveKey(port.RdmaInterface))
		})

		It("returns error if process fails to start within wait window", func() {
			nextCmd = &fakeCmd{output: []byte(""), err: errors.New("failed")}
			port := device.Status.Ports[0]
			err := manager.RunDocaSpcXCC(port)
			Expect(err).To(HaveOccurred())
			Expect(strings.ToLower(err.Error())).To(ContainSubstring("failed to start"))
		})

		It("sends notification on channel when process dies after startup", func() {
			// fakeCmd with 4s delay survives the 3s startup check, then fails
			nextCmd = &fakeCmd{output: []byte(""), err: errors.New("runtime crash"), delay: 4 * time.Second}
			port := device.Status.Ports[0]
			err := manager.RunDocaSpcXCC(port)
			Expect(err).NotTo(HaveOccurred())
			Expect(manager.ccProcesses).To(HaveKey(port.RdmaInterface))

			// Wait for process to die and notification to fire
			Eventually(manager.GetCCTerminationChannel(), 5*time.Second).Should(Receive(Equal(port.RdmaInterface)))
		})

		It("does NOT send notification when process fails during startup", func() {
			nextCmd = &fakeCmd{output: []byte(""), err: errors.New("startup failure")}
			port := device.Status.Ports[0]
			err := manager.RunDocaSpcXCC(port)
			Expect(err).To(HaveOccurred())

			Consistently(manager.GetCCTerminationChannel(), 1*time.Second).ShouldNot(Receive())
		})
	})

	Describe("GetCCTerminationChannel", func() {
		It("returns the termination channel", func() {
			ch := manager.GetCCTerminationChannel()
			Expect(ch).NotTo(BeNil())
		})
	})

	Describe("BreakoutConfigApplied", func() {
		Context("MultiplaneMode none", func() {
			It("returns true when None[1] is specified and all params match", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeNone
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 1

				expectedDmsParams := []types.ConfigurationParameter{
					{Name: "none_num_pfs", Value: "1", DMSPath: "/none/num-pf"},
				}
				dmsCli.On("GetParameters", expectedDmsParams).Return(map[string]string{
					"/none/num-pf": "1",
				}, nil)

				nvConfigQuery := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"NUM_OF_PLANES_P1": {"0"}},
				}
				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "NUM_OF_PLANES_P1").Return(nvConfigQuery, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns false when None[1] is specified but params do not match", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeNone
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 1

				nvConfigQuery := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"NUM_OF_PLANES_P1": {"4"}},
				}
				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "NUM_OF_PLANES_P1").Return(nvConfigQuery, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})

			It("returns true as noop when breakout None is not specified", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeNone
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 1

				cfgs["v1"].BreakoutConfig.None = nil

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})
		})

		Context("MultiplaneMode swplb", func() {
			It("returns true when MultiplaneMode is swplb with 2 planes and all params match", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				expectedParams := cfgs["v1"].BreakoutConfig.Swplb[2]
				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/swplb/2/p1": "sw2val1",
					"/swplb/2/p2": "sw2val2",
				}, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns true when MultiplaneMode is swplb with 4 planes and all params match", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4

				expectedParams := cfgs["v1"].BreakoutConfig.Swplb[4]
				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/swplb/4/p1": "sw4val1",
					"/swplb/4/p2": "sw4val2",
				}, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns false when MultiplaneMode is swplb but breakout param mismatches", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				expectedParams := cfgs["v1"].BreakoutConfig.Swplb[2]
				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/swplb/2/p1": "sw2val1",
					"/swplb/2/p2": "wrong_value",
				}, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("MultiplaneMode hwplb", func() {
			It("returns true when MultiplaneMode is hwplb with 2 planes and all params match", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				expectedParams := cfgs["v1"].BreakoutConfig.Hwplb[2]
				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/hwplb/2/p1": "hw2val1",
					"/hwplb/2/p2": "hw2val2",
				}, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns true when MultiplaneMode is hwplb with 4 planes and all params match", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4

				expectedParams := cfgs["v1"].BreakoutConfig.Hwplb[4]
				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/hwplb/4/p1": "hw4val1",
					"/hwplb/4/p2": "hw4val2",
				}, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns false when MultiplaneMode is hwplb but breakout param mismatches", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4

				expectedParams := cfgs["v1"].BreakoutConfig.Hwplb[4]
				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/hwplb/4/p1": "wrong_value",
					"/hwplb/4/p2": "hw4val2",
				}, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("MultiplaneMode uniplane", func() {
			It("returns true when MultiplaneMode is uniplane with 2 planes and all params match", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeUniplane
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				expectedParams := cfgs["v1"].BreakoutConfig.Uniplane[2]
				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/uniplane/2/p1": "uni2val1",
					"/uniplane/2/p2": "uni2val2",
				}, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns true when MultiplaneMode is uniplane with 4 planes and all params match", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeUniplane
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4

				expectedParams := cfgs["v1"].BreakoutConfig.Uniplane[4]
				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/uniplane/4/p1": "uni4val1",
					"/uniplane/4/p2": "uni4val2",
				}, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns false when MultiplaneMode is uniplane but breakout param mismatches", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeUniplane
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				expectedParams := cfgs["v1"].BreakoutConfig.Uniplane[2]
				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/uniplane/2/p1": "uni2val1",
					"/uniplane/2/p2": "wrong_value",
				}, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("Error cases", func() {
			It("returns error for invalid multiplane mode", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = "invalid-mode"
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				_, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(strings.ToLower(err.Error())).To(ContainSubstring("invalid multiplane mode"))
			})

			It("returns error when DMS GetParameters fails with breakout config", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				expectedParams := cfgs["v1"].BreakoutConfig.Swplb[2]
				dmsCli.On("GetParameters", expectedParams).Return(nil, errors.New("dms error"))

				_, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("dms error"))
			})
		})

		Context("Device-specific filtering", func() {
			It("filters out non-matching DeviceId in breakout params", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Status.Type = "1023"

				cfgs["v1"].BreakoutConfig.Swplb[2] = []types.ConfigurationParameter{
					{Name: "match1", Value: "ok1", DMSPath: "/m1", DeviceId: "1023"},
					{Name: "skip1", Value: "bad1", DMSPath: "/s1", DeviceId: consts.BlueField3DeviceID},
					{Name: "match2", Value: "ok2", DMSPath: "/m2", DeviceId: "1023"},
				}

				expectedParams := []types.ConfigurationParameter{
					{Name: "match1", Value: "ok1", DMSPath: "/m1", DeviceId: "1023"},
					{Name: "match2", Value: "ok2", DMSPath: "/m2", DeviceId: "1023"},
				}

				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/m1": "ok1",
					"/m2": "ok2",
				}, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})
		})

		Context("AlternativeValue support", func() {
			It("accepts alternative value for breakout parameters", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				cfgs["v1"].BreakoutConfig.Swplb[2] = []types.ConfigurationParameter{
					{Name: "param_with_alt", Value: "primary", AlternativeValue: "alternative", DMSPath: "/alt"},
				}

				expectedParams := cfgs["v1"].BreakoutConfig.Swplb[2]
				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/alt": "alternative",
				}, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})
		})

		Context("ValuesDoNotMatchError handling", func() {
			It("returns false when breakout config has ValuesDoNotMatchError", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				valuesDoNotMatchErr := types.ValuesDoNotMatchError(types.ConfigurationParameter{Name: "breakout_param"}, "mismatch_value")
				expectedParams := cfgs["v1"].BreakoutConfig.Swplb[2]
				dmsCli.On("GetParameters", expectedParams).Return(nil, valuesDoNotMatchErr)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})
	})

	Describe("ApplyBreakoutConfig", func() {
		Context("MultiplaneMode none", func() {
			It("applies None[1] params when specified", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeNone
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 1

				expectedDmsParams := []types.ConfigurationParameter{
					{Name: "none_num_pfs", Value: "1", DMSPath: "/none/num-pf"},
				}
				dmsCli.On("SetParameters", expectedDmsParams).Return(nil)
				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "NUM_OF_PLANES_P1", "0").Return(nil)

				err := manager.ApplyBreakoutConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})

			It("succeeds as noop when breakout None is not specified", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeNone
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 1

				cfgs["v1"].BreakoutConfig.None = nil

				err := manager.ApplyBreakoutConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("MultiplaneMode swplb", func() {
			It("sets swplb config for 2 planes", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				expectedParams := cfgs["v1"].BreakoutConfig.Swplb[2]
				dmsCli.On("SetParameters", expectedParams).Return(nil)

				err := manager.ApplyBreakoutConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})

			It("sets swplb config for 4 planes", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4

				expectedParams := cfgs["v1"].BreakoutConfig.Swplb[4]
				dmsCli.On("SetParameters", expectedParams).Return(nil)

				err := manager.ApplyBreakoutConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("MultiplaneMode hwplb", func() {
			It("sets hwplb config for 2 planes", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				expectedParams := cfgs["v1"].BreakoutConfig.Hwplb[2]
				dmsCli.On("SetParameters", expectedParams).Return(nil)

				err := manager.ApplyBreakoutConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})

			It("sets hwplb config for 4 planes", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4

				expectedParams := cfgs["v1"].BreakoutConfig.Hwplb[4]
				dmsCli.On("SetParameters", expectedParams).Return(nil)

				err := manager.ApplyBreakoutConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("MultiplaneMode uniplane", func() {
			It("sets uniplane config for 2 planes", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeUniplane
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				expectedParams := cfgs["v1"].BreakoutConfig.Uniplane[2]
				dmsCli.On("SetParameters", expectedParams).Return(nil)

				err := manager.ApplyBreakoutConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})

			It("sets uniplane config for 4 planes", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeUniplane
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4

				expectedParams := cfgs["v1"].BreakoutConfig.Uniplane[4]
				dmsCli.On("SetParameters", expectedParams).Return(nil)

				err := manager.ApplyBreakoutConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("Error cases", func() {
			It("returns error for invalid multiplane mode during apply", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = "invalid-mode"
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				err := manager.ApplyBreakoutConfig(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(strings.ToLower(err.Error())).To(ContainSubstring("invalid multiplane mode"))
			})

			It("returns error when DMS SetParameters fails with breakout config", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				expectedParams := cfgs["v1"].BreakoutConfig.Swplb[2]
				dmsCli.On("SetParameters", expectedParams).Return(errors.New("dms set error"))

				err := manager.ApplyBreakoutConfig(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("dms set error"))
			})
		})

		Context("Device-specific filtering", func() {
			It("filters out non-matching DeviceId in breakout params before setting", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4
				device.Status.Type = "1023"

				cfgs["v1"].BreakoutConfig.Hwplb[4] = []types.ConfigurationParameter{
					{Name: "match1", Value: "ok1", DMSPath: "/m1", DeviceId: "1023"},
					{Name: "skip1", Value: "bad1", DMSPath: "/s1", DeviceId: consts.BlueField3DeviceID},
					{Name: "match2", Value: "ok2", DMSPath: "/m2", DeviceId: "1023"},
				}

				expectedParams := []types.ConfigurationParameter{
					{Name: "match1", Value: "ok1", DMSPath: "/m1", DeviceId: "1023"},
					{Name: "match2", Value: "ok2", DMSPath: "/m2", DeviceId: "1023"},
				}

				dmsCli.On("SetParameters", expectedParams).Return(nil)

				err := manager.ApplyBreakoutConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("Edge cases with Breakout", func() {
		It("handles empty breakout config for a specific plane count", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
			device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

			cfgs["v1"].BreakoutConfig.Swplb[2] = []types.ConfigurationParameter{}

			// No DMS call should be made when breakout config is empty
			err := manager.ApplyBreakoutConfig(ctx, device)
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns error when spectrumx config version not found for breakout", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.Version = "non-existent"
			device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb

			_, err := manager.BreakoutConfigApplied(ctx, device)
			Expect(err).To(HaveOccurred())
			Expect(strings.ToLower(err.Error())).To(ContainSubstring("spectrumx config not found"))
		})

		It("returns error when spectrumx config version not found for nvconfig", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.Version = "non-existent"
			device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb

			_, err := manager.NvConfigApplied(ctx, device)
			Expect(err).To(HaveOccurred())
			Expect(strings.ToLower(err.Error())).To(ContainSubstring("spectrumx config not found"))
		})
	})

	Describe("NvConfigApplied with MLXConfig", func() {
		Context("Basic MLXConfig handling", func() {
			It("returns true when mlxconfig params match", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param1", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
					{Name: "mlx_param2", MlxConfig: "ADVANCED_PCI_SETTINGS", Value: "1"},
				}

				nvConfigQuery1 := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"NUM_OF_PLANES_P1": {"2"}},
				}
				nvConfigQuery2 := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"ADVANCED_PCI_SETTINGS": {"1"}},
				}

				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "NUM_OF_PLANES_P1").Return(nvConfigQuery1, nil)
				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "ADVANCED_PCI_SETTINGS").Return(nvConfigQuery2, nil)
				dmsCli.On("GetParameters", []types.ConfigurationParameter{}).Return(map[string]string{}, nil)

				applied, err := manager.NvConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns false when mlxconfig param value mismatches", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param1", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
					{Name: "mlx_param2", MlxConfig: "ADVANCED_PCI_SETTINGS", Value: "1"},
				}

				nvConfigQuery1 := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"NUM_OF_PLANES_P1": {"2"}},
				}
				nvConfigQuery2 := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"ADVANCED_PCI_SETTINGS": {"0"}}, // mismatch
				}

				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "NUM_OF_PLANES_P1").Return(nvConfigQuery1, nil)
				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "ADVANCED_PCI_SETTINGS").Return(nvConfigQuery2, nil)

				applied, err := manager.NvConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})

			It("returns true when mixed DMS and mlxconfig params all match", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "dms_param", DMSPath: "/dms/path", Value: "val1"},
					{Name: "mlx_param", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
				}

				nvConfigQuery := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"NUM_OF_PLANES_P1": {"2"}},
				}

				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "NUM_OF_PLANES_P1").Return(nvConfigQuery, nil)
				dmsCli.On("GetParameters", []types.ConfigurationParameter{{Name: "dms_param", DMSPath: "/dms/path", Value: "val1"}}).
					Return(map[string]string{"/dms/path": "val1"}, nil)

				applied, err := manager.NvConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})
		})

		Context("MLXConfig with multiplane", func() {
			It("returns true when mlxconfig in base config and multiplane swplb params match", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
				}
				cfgs["v1"].BreakoutConfig.Swplb[2] = []types.ConfigurationParameter{
					{Name: "dms_param", DMSPath: "/swplb/param", Value: "val1"},
				}

				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				nvConfigQuery := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"NUM_OF_PLANES_P1": {"2"}},
				}

				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "NUM_OF_PLANES_P1").Return(nvConfigQuery, nil)
				dmsCli.On("GetParameters", []types.ConfigurationParameter{{Name: "dms_param", DMSPath: "/swplb/param", Value: "val1"}}).
					Return(map[string]string{"/swplb/param": "val1"}, nil)

				applied, err := manager.NvConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns true when mlxconfig in multiplane config matches", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "dms_param", DMSPath: "/base/param", Value: "val1"},
				}
				cfgs["v1"].BreakoutConfig.Swplb[2] = []types.ConfigurationParameter{
					{Name: "mlx_param", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
				}

				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				nvConfigQuery := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"NUM_OF_PLANES_P1": {"2"}},
				}

				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "NUM_OF_PLANES_P1").Return(nvConfigQuery, nil)
				dmsCli.On("GetParameters", []types.ConfigurationParameter{{Name: "dms_param", DMSPath: "/base/param", Value: "val1"}}).
					Return(map[string]string{"/base/param": "val1"}, nil)

				applied, err := manager.NvConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns false when mlxconfig in breakout config mismatches", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{}
				cfgs["v1"].BreakoutConfig.Hwplb[4] = []types.ConfigurationParameter{
					{Name: "mlx_param", MlxConfig: "NUM_OF_PLANES_P1", Value: "4"},
				}

				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4

				nvConfigQuery := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"NUM_OF_PLANES_P1": {"2"}}, // mismatch
				}

				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "NUM_OF_PLANES_P1").Return(nvConfigQuery, nil)

				applied, err := manager.BreakoutConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})
		})

		Context("Error handling", func() {
			It("returns error when QueryNvConfig fails for mlxconfig param", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
				}

				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "NUM_OF_PLANES_P1").
					Return(types.NvConfigQuery{}, errors.New("query failed"))

				_, err := manager.NvConfigApplied(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("query failed"))
			})

			It("returns error when QueryNvConfig fails before DMS query", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
					{Name: "dms_param", DMSPath: "/dms/path", Value: "val1"},
				}

				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "NUM_OF_PLANES_P1").
					Return(types.NvConfigQuery{}, errors.New("mlxconfig error"))

				_, err := manager.NvConfigApplied(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("mlxconfig error"))
				// DMS GetParameters should not be called
			})
		})

		Context("Device-specific filtering", func() {
			It("filters out non-matching DeviceId in mlxconfig params", func() {
				device.Status.Type = "1023"

				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_match", MlxConfig: "PARAM1", Value: "val1", DeviceId: "1023"},
					{Name: "mlx_skip", MlxConfig: "PARAM2", Value: "val2", DeviceId: consts.BlueField3DeviceID},
				}

				nvConfigQuery := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"PARAM1": {"val1"}},
				}

				// Only the matching param should be queried
				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "PARAM1").Return(nvConfigQuery, nil)
				dmsCli.On("GetParameters", []types.ConfigurationParameter{}).Return(map[string]string{}, nil)

				applied, err := manager.NvConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("handles mixed DeviceId filtering for mlxconfig and DMS params", func() {
				device.Status.Type = "1023"

				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_match", MlxConfig: "PARAM1", Value: "val1", DeviceId: "1023"},
					{Name: "mlx_skip", MlxConfig: "PARAM2", Value: "val2", DeviceId: consts.BlueField3DeviceID},
					{Name: "dms_match", DMSPath: "/path1", Value: "dms1", DeviceId: "1023"},
					{Name: "dms_skip", DMSPath: "/path2", Value: "dms2", DeviceId: consts.BlueField3DeviceID},
				}

				nvConfigQuery := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"PARAM1": {"val1"}},
				}

				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "PARAM1").Return(nvConfigQuery, nil)
				dmsCli.On("GetParameters", []types.ConfigurationParameter{{Name: "dms_match", DMSPath: "/path1", Value: "dms1", DeviceId: "1023"}}).
					Return(map[string]string{"/path1": "dms1"}, nil)

				applied, err := manager.NvConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})
		})

		Context("Edge cases", func() {
			It("handles mlxconfig param with array value in CurrentConfig", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param", MlxConfig: "ARRAY_PARAM", Value: "val1"},
				}

				nvConfigQuery := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"ARRAY_PARAM": {"val1", "val2", "val3"}},
				}

				nvConfigMgr.On("QueryNvConfig", ctx, "0000:00:00.0", "ARRAY_PARAM").Return(nvConfigQuery, nil)
				dmsCli.On("GetParameters", []types.ConfigurationParameter{}).Return(map[string]string{}, nil)

				applied, err := manager.NvConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue()) // Uses [0] element
			})

			It("uses first port PCI address for mlxconfig query", func() {
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:03:00.0", RdmaInterface: "mlx5_0"},
					{PCI: "0000:04:00.0", RdmaInterface: "mlx5_1"},
				}

				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
				}

				nvConfigQuery := types.NvConfigQuery{
					CurrentConfig: map[string][]string{"NUM_OF_PLANES_P1": {"2"}},
				}

				// Should use first port's PCI
				nvConfigMgr.On("QueryNvConfig", ctx, "0000:03:00.0", "NUM_OF_PLANES_P1").Return(nvConfigQuery, nil)
				dmsCli.On("GetParameters", []types.ConfigurationParameter{}).Return(map[string]string{}, nil)

				applied, err := manager.NvConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})
		})
	})

	Describe("ApplyNvConfig with MLXConfig", func() {
		Context("Basic MLXConfig setting", func() {
			It("sets mlxconfig params via SetNvConfigParameter", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param1", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
					{Name: "mlx_param2", MlxConfig: "ADVANCED_PCI_SETTINGS", Value: "1"},
				}

				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "NUM_OF_PLANES_P1", "2").Return(nil)
				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "ADVANCED_PCI_SETTINGS", "1").Return(nil)
				dmsCli.On("SetParameters", []types.ConfigurationParameter{}).Return(nil)

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})

			It("sets mixed DMS and mlxconfig params", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "dms_param", DMSPath: "/dms/path", Value: "val1"},
					{Name: "mlx_param1", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
					{Name: "mlx_param2", MlxConfig: "ADVANCED_PCI_SETTINGS", Value: "1"},
				}

				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "NUM_OF_PLANES_P1", "2").Return(nil)
				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "ADVANCED_PCI_SETTINGS", "1").Return(nil)
				dmsCli.On("SetParameters", []types.ConfigurationParameter{{Name: "dms_param", DMSPath: "/dms/path", Value: "val1"}}).Return(nil)

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})

			It("sets only DMS params when no mlxconfig params present", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "dms_param", DMSPath: "/dms/path", Value: "val1"},
				}

				dmsCli.On("SetParameters", cfgs["v1"].NVConfig).Return(nil)

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				// SetNvConfigParameter should not be called
			})
		})

		Context("MLXConfig with multiplane", func() {
			It("sets mlxconfig in base and DMS params in multiplane swplb", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param", MlxConfig: "PARAM1", Value: "val1"},
				}
				cfgs["v1"].BreakoutConfig.Swplb[2] = []types.ConfigurationParameter{
					{Name: "dms_param", DMSPath: "/swplb/param", Value: "val2"},
				}

				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "PARAM1", "val1").Return(nil)
				dmsCli.On("SetParameters", []types.ConfigurationParameter{{Name: "dms_param", DMSPath: "/swplb/param", Value: "val2"}}).Return(nil)

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})

			It("sets mlxconfig in multiplane hwplb config", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{}
				cfgs["v1"].BreakoutConfig.Hwplb[4] = []types.ConfigurationParameter{
					{Name: "mlx_param", MlxConfig: "NUM_OF_PLANES_P1", Value: "4"},
				}

				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4

				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "NUM_OF_PLANES_P1", "4").Return(nil)
				dmsCli.On("SetParameters", []types.ConfigurationParameter{}).Return(nil)

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})

			It("sets mlxconfig params from both base and multiplane uniplane", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_base", MlxConfig: "BASE_PARAM", Value: "base_val"},
				}
				cfgs["v1"].BreakoutConfig.Uniplane[2] = []types.ConfigurationParameter{
					{Name: "mlx_multiplane", MlxConfig: "MULTIPLANE_PARAM", Value: "mp_val"},
				}

				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeUniplane
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "BASE_PARAM", "base_val").Return(nil)
				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "MULTIPLANE_PARAM", "mp_val").Return(nil)
				dmsCli.On("SetParameters", []types.ConfigurationParameter{}).Return(nil)

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("Error handling", func() {
			It("returns error when SetNvConfigParameter fails", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
				}

				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "NUM_OF_PLANES_P1", "2").
					Return(errors.New("set failed"))

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("set failed"))
			})

			It("returns error when SetNvConfigParameter fails before DMS set", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
					{Name: "dms_param", DMSPath: "/dms/path", Value: "val1"},
				}

				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "NUM_OF_PLANES_P1", "2").
					Return(errors.New("mlxconfig set error"))

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("mlxconfig set error"))
			})

			It("returns error when SetNvConfigParameter fails on second param", func() {
				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param1", MlxConfig: "PARAM1", Value: "val1"},
					{Name: "mlx_param2", MlxConfig: "PARAM2", Value: "val2"},
				}

				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "PARAM1", "val1").Return(nil)
				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "PARAM2", "val2").
					Return(errors.New("second param failed"))

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("second param failed"))
			})
		})

		Context("Device-specific filtering", func() {
			It("filters out non-matching DeviceId in mlxconfig params before setting", func() {
				device.Status.Type = consts.BlueField3DeviceID

				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_match1", MlxConfig: "PARAM1", Value: "val1", DeviceId: consts.BlueField3DeviceID},
					{Name: "mlx_skip", MlxConfig: "PARAM2", Value: "val2", DeviceId: "1023"},
					{Name: "mlx_match2", MlxConfig: "PARAM3", Value: "val3", DeviceId: consts.BlueField3DeviceID},
				}

				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "PARAM1", "val1").Return(nil).Times(1)
				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "PARAM3", "val3").Return(nil).Times(1)
				dmsCli.On("SetParameters", []types.ConfigurationParameter{}).Return(nil)

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})

			It("applies mixed DeviceId filtering for mlxconfig and DMS params", func() {
				device.Status.Type = "1023"

				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_match", MlxConfig: "PARAM1", Value: "val1", DeviceId: "1023"},
					{Name: "mlx_skip", MlxConfig: "PARAM2", Value: "val2", DeviceId: consts.BlueField3DeviceID},
					{Name: "dms_match", DMSPath: "/path1", Value: "dms1", DeviceId: "1023"},
					{Name: "dms_skip", DMSPath: "/path2", Value: "dms2", DeviceId: consts.BlueField3DeviceID},
				}

				nvConfigMgr.On("SetNvConfigParameter", "0000:00:00.0", "PARAM1", "val1").Return(nil)
				dmsCli.On("SetParameters", []types.ConfigurationParameter{{Name: "dms_match", DMSPath: "/path1", Value: "dms1", DeviceId: "1023"}}).Return(nil)

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("PCI address usage", func() {
			It("uses first port PCI address for mlxconfig set", func() {
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:03:00.0", RdmaInterface: "mlx5_0"},
					{PCI: "0000:04:00.0", RdmaInterface: "mlx5_1"},
				}

				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "mlx_param", MlxConfig: "NUM_OF_PLANES_P1", Value: "2"},
				}

				// Should use first port's PCI
				nvConfigMgr.On("SetNvConfigParameter", "0000:03:00.0", "NUM_OF_PLANES_P1", "2").Return(nil)
				dmsCli.On("SetParameters", []types.ConfigurationParameter{}).Return(nil)

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("Parameter filtering by Breakout and Multiplane", func() {
		Context("Breakout filtering", func() {
			It("filters out non-matching Breakout parameters in NvConfigApplied", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "match_breakout", Value: "val1", DMSPath: "/breakout/match", Breakout: 2},
					{Name: "skip_breakout", Value: "val2", DMSPath: "/breakout/skip", Breakout: 4},
					{Name: "no_breakout", Value: "val3", DMSPath: "/no/breakout"},
				}
				cfgs["v1"].BreakoutConfig.Swplb[2] = []types.ConfigurationParameter{}

				expectedParams := []types.ConfigurationParameter{
					{Name: "match_breakout", Value: "val1", DMSPath: "/breakout/match", Breakout: 2},
					{Name: "no_breakout", Value: "val3", DMSPath: "/no/breakout"},
				}

				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/breakout/match": "val1",
					"/no/breakout":    "val3",
				}, nil)

				applied, err := manager.NvConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("filters out non-matching Breakout parameters in ApplyNvConfig", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4

				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "match_breakout", Value: "val1", DMSPath: "/breakout/match", Breakout: 4},
					{Name: "skip_breakout", Value: "val2", DMSPath: "/breakout/skip", Breakout: 2},
				}
				cfgs["v1"].BreakoutConfig.Hwplb[4] = []types.ConfigurationParameter{}

				expectedParams := []types.ConfigurationParameter{
					{Name: "match_breakout", Value: "val1", DMSPath: "/breakout/match", Breakout: 4},
				}

				dmsCli.On("SetParameters", expectedParams).Return(nil)

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("Multiplane mode filtering", func() {
			It("filters out non-matching Multiplane parameters in NvConfigApplied", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "match_multiplane", Value: "val1", DMSPath: "/mp/match", Multiplane: consts.MultiplaneModeHwplb},
					{Name: "skip_multiplane", Value: "val2", DMSPath: "/mp/skip", Multiplane: consts.MultiplaneModeSwplb},
					{Name: "no_multiplane", Value: "val3", DMSPath: "/no/mp"},
				}
				cfgs["v1"].BreakoutConfig.Hwplb[2] = []types.ConfigurationParameter{}

				expectedParams := []types.ConfigurationParameter{
					{Name: "match_multiplane", Value: "val1", DMSPath: "/mp/match", Multiplane: consts.MultiplaneModeHwplb},
					{Name: "no_multiplane", Value: "val3", DMSPath: "/no/mp"},
				}

				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/mp/match": "val1",
					"/no/mp":    "val3",
				}, nil)

				applied, err := manager.NvConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("filters out non-matching Multiplane parameters in ApplyNvConfig", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "match_multiplane", Value: "val1", DMSPath: "/mp/match", Multiplane: consts.MultiplaneModeSwplb},
					{Name: "skip_multiplane", Value: "val2", DMSPath: "/mp/skip", Multiplane: consts.MultiplaneModeUniplane},
				}
				cfgs["v1"].BreakoutConfig.Swplb[2] = []types.ConfigurationParameter{}

				expectedParams := []types.ConfigurationParameter{
					{Name: "match_multiplane", Value: "val1", DMSPath: "/mp/match", Multiplane: consts.MultiplaneModeSwplb},
				}

				dmsCli.On("SetParameters", expectedParams).Return(nil)

				err := manager.ApplyNvConfig(ctx, device)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("Combined filtering", func() {
			It("filters by DeviceId, Breakout, and Multiplane together", func() {
				device.Status.Type = "1023"
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				cfgs["v1"].NVConfig = []types.ConfigurationParameter{
					{Name: "all_match", Value: "val1", DMSPath: "/all/match", DeviceId: "1023", Breakout: 2, Multiplane: consts.MultiplaneModeHwplb},
					{Name: "wrong_device", Value: "val2", DMSPath: "/wrong/device", DeviceId: consts.BlueField3DeviceID, Breakout: 2, Multiplane: consts.MultiplaneModeHwplb},
					{Name: "wrong_breakout", Value: "val3", DMSPath: "/wrong/breakout", DeviceId: "1023", Breakout: 4, Multiplane: consts.MultiplaneModeHwplb},
					{Name: "wrong_multiplane", Value: "val4", DMSPath: "/wrong/mp", DeviceId: "1023", Breakout: 2, Multiplane: consts.MultiplaneModeSwplb},
					{Name: "no_filters", Value: "val5", DMSPath: "/no/filters"},
				}
				cfgs["v1"].BreakoutConfig.Hwplb[2] = []types.ConfigurationParameter{}

				expectedParams := []types.ConfigurationParameter{
					{Name: "all_match", Value: "val1", DMSPath: "/all/match", DeviceId: "1023", Breakout: 2, Multiplane: consts.MultiplaneModeHwplb},
					{Name: "no_filters", Value: "val5", DMSPath: "/no/filters"},
				}

				dmsCli.On("GetParameters", expectedParams).Return(map[string]string{
					"/all/match":  "val1",
					"/no/filters": "val5",
				}, nil)

				applied, err := manager.NvConfigApplied(ctx, device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})
		})
	})

	Describe("CNP DSCP", func() {
		var tmpDir string

		BeforeEach(func() {
			var err error
			tmpDir, err = os.MkdirTemp("", "cnp-dscp-test")
			Expect(err).NotTo(HaveOccurred())

			// Override the path template to use temp dir
			cnpDscpSysfsPathTemplate = filepath.Join(tmpDir, "%s", "ecn", "roce_np", "cnp_dscp")
		})

		AfterEach(func() {
			_ = os.RemoveAll(tmpDir)
			// Restore original path template
			cnpDscpSysfsPathTemplate = "/sys/class/net/%s/ecn/roce_np/cnp_dscp"
		})

		createCnpDscpFile := func(iface, value string) {
			dir := filepath.Join(tmpDir, iface, "ecn", "roce_np")
			Expect(os.MkdirAll(dir, 0755)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(dir, "cnp_dscp"), []byte(value), 0644)).To(Succeed())
		}

		Context("RuntimeConfigApplied", func() {
			It("returns true when CNP DSCP is set to expected value for swplb", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:00:00.0", NetworkInterface: "eth0", RdmaInterface: "mlx5_0"},
				}
				createCnpDscpFile("eth0", "24") // swplb expects 24

				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(map[string]string{"/r": "x"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(map[string]string{"/ar": "y"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(map[string]string{"/cc": "z"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(map[string]string{"/ipg": "25"}, nil)

				manager.ccProcesses[device.Status.Ports[0].RdmaInterface] = &ccProcess{port: device.Status.Ports[0]}
				manager.ccProcesses[device.Status.Ports[0].RdmaInterface].running.Store(true)

				nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
				applied, err := manager.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns true when CNP DSCP is set to expected value for hwplb", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:00:00.0", NetworkInterface: "eth0", RdmaInterface: "mlx5_0"},
				}
				createCnpDscpFile("eth0", "48") // hwplb expects 48
				manager.ccProcesses[device.Status.Ports[0].RdmaInterface] = &ccProcess{port: device.Status.Ports[0]}
				manager.ccProcesses[device.Status.Ports[0].RdmaInterface].running.Store(true)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(map[string]string{"/r": "x"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(map[string]string{"/ar": "y"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(map[string]string{"/cc": "z"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(map[string]string{"/ipg": "25"}, nil)

				nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
				applied, err := manager.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns false when CNP DSCP has wrong value", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:00:00.0", NetworkInterface: "eth0", RdmaInterface: "mlx5_0"},
				}
				createCnpDscpFile("eth0", "48") // wrong value for swplb (expects 24)

				manager.ccProcesses[device.Status.Ports[0].RdmaInterface] = &ccProcess{port: device.Status.Ports[0]}
				manager.ccProcesses[device.Status.Ports[0].RdmaInterface].running.Store(true)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(map[string]string{"/r": "x"}, nil)

				applied, err := manager.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeFalse())
			})

			It("returns error when CNP DSCP file doesn't exist", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:00:00.0", NetworkInterface: "eth0", RdmaInterface: "mlx5_0"},
				}
				// Don't create the file

				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(map[string]string{"/r": "x"}, nil)

				_, err := manager.RuntimeConfigApplied(device)
				Expect(err).To(HaveOccurred())
			})

			It("skips ports without network interface", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:00:00.0", NetworkInterface: "", RdmaInterface: "mlx5_0"},
				}
				// No file needed since port should be skipped

				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(map[string]string{"/r": "x"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(map[string]string{"/ar": "y"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(map[string]string{"/cc": "z"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(map[string]string{"/ipg": "25"}, nil)

				manager.ccProcesses[device.Status.Ports[0].RdmaInterface] = &ccProcess{port: device.Status.Ports[0]}
				manager.ccProcesses[device.Status.Ports[0].RdmaInterface].running.Store(true)

				nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
				applied, err := manager.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("checks CNP DSCP for multiple ports", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeUniplane
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:00:00.0", NetworkInterface: "eth0", RdmaInterface: "mlx5_0"},
					{PCI: "0000:00:00.1", NetworkInterface: "eth1", RdmaInterface: "mlx5_1"},
				}
				createCnpDscpFile("eth0", "24") // uniplane expects 24
				createCnpDscpFile("eth1", "24")

				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(map[string]string{"/r": "x"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(map[string]string{"/ar": "y"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(map[string]string{"/cc": "z"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(map[string]string{"/ipg": "25"}, nil)

				manager.ccProcesses[device.Status.Ports[0].RdmaInterface] = &ccProcess{port: device.Status.Ports[0]}
				manager.ccProcesses[device.Status.Ports[0].RdmaInterface].running.Store(true)
				manager.ccProcesses[device.Status.Ports[1].RdmaInterface] = &ccProcess{port: device.Status.Ports[1]}
				manager.ccProcesses[device.Status.Ports[1].RdmaInterface].running.Store(true)

				nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
				applied, err := manager.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})
		})

		Context("ApplyRuntimeConfig", func() {
			It("writes CNP DSCP value 24 for swplb", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:00:00.0", NetworkInterface: "eth0", RdmaInterface: "mlx5_0"},
				}
				// Create directory structure but not the file
				dir := filepath.Join(tmpDir, "eth0", "ecn", "roce_np")
				Expect(os.MkdirAll(dir, 0755)).To(Succeed())

				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == shutdownInterfaceParamName
				})).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == bringUpInterfaceParamName
				})).Return(nil)

				nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
				err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())

				// Verify the file was written with swplb value
				data, err := os.ReadFile(filepath.Join(dir, "cnp_dscp"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data)).To(Equal("24"))
			})

			It("writes CNP DSCP value 48 for hwplb", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:00:00.0", NetworkInterface: "eth0", RdmaInterface: "mlx5_0"},
				}
				dir := filepath.Join(tmpDir, "eth0", "ecn", "roce_np")
				Expect(os.MkdirAll(dir, 0755)).To(Succeed())

				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == shutdownInterfaceParamName
				})).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == bringUpInterfaceParamName
				})).Return(nil)

				nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
				err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())

				// Verify the file was written with hwplb value
				data, err := os.ReadFile(filepath.Join(dir, "cnp_dscp"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data)).To(Equal("48"))
			})

			It("writes CNP DSCP for multiple ports", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeUniplane
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:00:00.0", NetworkInterface: "eth0", RdmaInterface: "mlx5_0"},
					{PCI: "0000:00:00.1", NetworkInterface: "eth1", RdmaInterface: "mlx5_1"},
				}
				dir0 := filepath.Join(tmpDir, "eth0", "ecn", "roce_np")
				dir1 := filepath.Join(tmpDir, "eth1", "ecn", "roce_np")
				Expect(os.MkdirAll(dir0, 0755)).To(Succeed())
				Expect(os.MkdirAll(dir1, 0755)).To(Succeed())

				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == shutdownInterfaceParamName
				})).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == bringUpInterfaceParamName
				})).Return(nil)

				nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
				err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())

				// uniplane expects 24
				data0, err := os.ReadFile(filepath.Join(dir0, "cnp_dscp"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data0)).To(Equal("24"))

				data1, err := os.ReadFile(filepath.Join(dir1, "cnp_dscp"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data1)).To(Equal("24"))
			})

			It("skips ports without network interface", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:00:00.0", NetworkInterface: "", RdmaInterface: "mlx5_0"},
				}

				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == shutdownInterfaceParamName
				})).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == bringUpInterfaceParamName
				})).Return(nil)

				nextCmd = &fakeCmd{output: []byte("started"), err: nil, delay: 5 * time.Second}
				err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("Runtime Config Parameter Filtering", func() {
		Context("RuntimeConfigApplied filtering", func() {
			It("filters runtime config params by DeviceId", func() {
				device.Status.Type = "1023"
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeNone
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 1

				cfgs["v1"].RuntimeConfig.Roce = []types.ConfigurationParameter{
					{Name: "roce_match", Value: "val1", DMSPath: "/roce/match", DeviceId: "1023"},
					{Name: "roce_skip", Value: "val2", DMSPath: "/roce/skip", DeviceId: consts.BlueField3DeviceID},
				}
				cfgs["v1"].RuntimeConfig.AdaptiveRouting = []types.ConfigurationParameter{
					{Name: "ar_match", Value: "val3", DMSPath: "/ar/match", DeviceId: "1023"},
				}
				cfgs["v1"].RuntimeConfig.CongestionControl = []types.ConfigurationParameter{
					{Name: "cc_match", Value: "val4", DMSPath: "/cc/match"},
				}
				cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3 = []types.ConfigurationParameter{
					{Name: "ipg_match", Value: "val5", DMSPath: "/ipg/match"},
				}
				cfgs["v1"].UseSoftwareCCAlgorithm = false

				// Expect filtered params (without DeviceId: consts.BlueField3DeviceID)
				expectedRoce := []types.ConfigurationParameter{
					{Name: "roce_match", Value: "val1", DMSPath: "/roce/match", DeviceId: "1023"},
				}
				expectedAR := []types.ConfigurationParameter{
					{Name: "ar_match", Value: "val3", DMSPath: "/ar/match", DeviceId: "1023"},
				}

				dmsCli.On("GetParameters", expectedRoce).Return(map[string]string{"/roce/match": "val1"}, nil)
				dmsCli.On("GetParameters", expectedAR).Return(map[string]string{"/ar/match": "val3"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(map[string]string{"/cc/match": "val4"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(map[string]string{"/ipg/match": "val5"}, nil)

				applied, err := manager.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("filters runtime config params by Breakout (numberOfPlanes)", func() {
				device.Status.Type = "1023"
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				cfgs["v1"].RuntimeConfig.Roce = []types.ConfigurationParameter{
					{Name: "roce_match", Value: "val1", DMSPath: "/roce/match", Breakout: 2},
					{Name: "roce_skip", Value: "val2", DMSPath: "/roce/skip", Breakout: 4},
				}
				cfgs["v1"].RuntimeConfig.AdaptiveRouting = []types.ConfigurationParameter{
					{Name: "ar_no_filter", Value: "val3", DMSPath: "/ar/match"},
				}
				cfgs["v1"].RuntimeConfig.CongestionControl = []types.ConfigurationParameter{
					{Name: "cc_match", Value: "val4", DMSPath: "/cc/match", Breakout: 2},
					{Name: "cc_skip", Value: "val5", DMSPath: "/cc/skip", Breakout: 4},
				}
				cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3 = []types.ConfigurationParameter{
					{Name: "ipg_match", Value: "val6", DMSPath: "/ipg/match"},
				}
				cfgs["v1"].UseSoftwareCCAlgorithm = false

				expectedRoce := []types.ConfigurationParameter{
					{Name: "roce_match", Value: "val1", DMSPath: "/roce/match", Breakout: 2},
				}
				expectedCC := []types.ConfigurationParameter{
					{Name: "cc_match", Value: "val4", DMSPath: "/cc/match", Breakout: 2},
				}

				dmsCli.On("GetParameters", expectedRoce).Return(map[string]string{"/roce/match": "val1"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(map[string]string{"/ar/match": "val3"}, nil)
				dmsCli.On("GetParameters", expectedCC).Return(map[string]string{"/cc/match": "val4"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(map[string]string{"/ipg/match": "val6"}, nil)

				applied, err := manager.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("filters runtime config params by Multiplane mode", func() {
				device.Status.Type = "1023"
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				cfgs["v1"].RuntimeConfig.Roce = []types.ConfigurationParameter{
					{Name: "roce_no_filter", Value: "val1", DMSPath: "/roce/match"},
				}
				cfgs["v1"].RuntimeConfig.AdaptiveRouting = []types.ConfigurationParameter{
					{Name: "ar_match", Value: "val2", DMSPath: "/ar/match", Multiplane: consts.MultiplaneModeHwplb},
					{Name: "ar_skip_swplb", Value: "val3", DMSPath: "/ar/skip1", Multiplane: consts.MultiplaneModeSwplb},
					{Name: "ar_skip_uniplane", Value: "val4", DMSPath: "/ar/skip2", Multiplane: consts.MultiplaneModeUniplane},
				}
				cfgs["v1"].RuntimeConfig.CongestionControl = []types.ConfigurationParameter{
					{Name: "cc_match", Value: "val5", DMSPath: "/cc/match"},
				}
				cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3 = []types.ConfigurationParameter{
					{Name: "ipg_match", Value: "val6", DMSPath: "/ipg/match"},
				}
				cfgs["v1"].UseSoftwareCCAlgorithm = false

				expectedAR := []types.ConfigurationParameter{
					{Name: "ar_match", Value: "val2", DMSPath: "/ar/match", Multiplane: consts.MultiplaneModeHwplb},
				}

				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(map[string]string{"/roce/match": "val1"}, nil)
				dmsCli.On("GetParameters", expectedAR).Return(map[string]string{"/ar/match": "val2"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(map[string]string{"/cc/match": "val5"}, nil)
				dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(map[string]string{"/ipg/match": "val6"}, nil)

				applied, err := manager.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("filters InterPacketGap params by combined filters", func() {
				device.Status.Type = "1023"
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4
				device.Spec.Configuration.Template.SpectrumXOptimized.Overlay = consts.OverlayNone

				cfgs["v1"].RuntimeConfig.Roce = []types.ConfigurationParameter{}
				cfgs["v1"].RuntimeConfig.AdaptiveRouting = []types.ConfigurationParameter{}
				cfgs["v1"].RuntimeConfig.CongestionControl = []types.ConfigurationParameter{}
				cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3 = []types.ConfigurationParameter{
					{Name: "ipg_match", Value: "100", DMSPath: "/ipg/match", DeviceId: "1023", Breakout: 4, Multiplane: consts.MultiplaneModeSwplb},
					{Name: "ipg_skip_device", Value: "200", DMSPath: "/ipg/skip1", DeviceId: consts.BlueField3DeviceID, Breakout: 4, Multiplane: consts.MultiplaneModeSwplb},
					{Name: "ipg_skip_breakout", Value: "300", DMSPath: "/ipg/skip2", DeviceId: "1023", Breakout: 2, Multiplane: consts.MultiplaneModeSwplb},
					{Name: "ipg_skip_multiplane", Value: "400", DMSPath: "/ipg/skip3", DeviceId: "1023", Breakout: 4, Multiplane: consts.MultiplaneModeHwplb},
				}
				cfgs["v1"].UseSoftwareCCAlgorithm = false

				expectedIPG := []types.ConfigurationParameter{
					{Name: "ipg_match", Value: "100", DMSPath: "/ipg/match", DeviceId: "1023", Breakout: 4, Multiplane: consts.MultiplaneModeSwplb},
				}

				dmsCli.On("GetParameters", []types.ConfigurationParameter{}).Return(map[string]string{}, nil).Times(3)
				dmsCli.On("GetParameters", expectedIPG).Return(map[string]string{"/ipg/match": "100"}, nil)

				applied, err := manager.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})
		})

		Context("ApplyRuntimeConfig filtering", func() {
			It("filters runtime config params by DeviceId", func() {
				device.Status.Type = consts.BlueField3DeviceID
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeNone
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 1

				cfgs["v1"].RuntimeConfig.Roce = []types.ConfigurationParameter{
					{Name: "roce_match", Value: "val1", DMSPath: "/roce/match", DeviceId: consts.BlueField3DeviceID},
					{Name: "roce_skip", Value: "val2", DMSPath: "/roce/skip", DeviceId: "1023"},
				}
				cfgs["v1"].RuntimeConfig.AdaptiveRouting = []types.ConfigurationParameter{
					{Name: "ar_match", Value: "val3", DMSPath: "/ar/match", DeviceId: consts.BlueField3DeviceID},
				}
				cfgs["v1"].RuntimeConfig.CongestionControl = []types.ConfigurationParameter{
					{Name: "cc_no_filter", Value: "val4", DMSPath: "/cc/match"},
				}
				cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3 = []types.ConfigurationParameter{
					{Name: "ipg_match", Value: "val5", DMSPath: "/ipg/match"},
				}
				cfgs["v1"].UseSoftwareCCAlgorithm = false

				expectedRoce := []types.ConfigurationParameter{
					{Name: "roce_match", Value: "val1", DMSPath: "/roce/match", DeviceId: consts.BlueField3DeviceID},
				}
				expectedAR := []types.ConfigurationParameter{
					{Name: "ar_match", Value: "val3", DMSPath: "/ar/match", DeviceId: consts.BlueField3DeviceID},
				}

				dmsCli.On("SetParameters", expectedRoce).Return(nil)
				dmsCli.On("SetParameters", expectedAR).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == shutdownInterfaceParamName
				})).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == bringUpInterfaceParamName
				})).Return(nil)

				err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())
			})

			It("filters runtime config params by Breakout (numberOfPlanes)", func() {
				device.Status.Type = "1023"
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4

				cfgs["v1"].RuntimeConfig.Roce = []types.ConfigurationParameter{
					{Name: "roce_no_filter", Value: "val1", DMSPath: "/roce/match"},
				}
				cfgs["v1"].RuntimeConfig.AdaptiveRouting = []types.ConfigurationParameter{
					{Name: "ar_match_4", Value: "val2", DMSPath: "/ar/match", Breakout: 4},
					{Name: "ar_skip_2", Value: "val3", DMSPath: "/ar/skip", Breakout: 2},
				}
				cfgs["v1"].RuntimeConfig.CongestionControl = []types.ConfigurationParameter{
					{Name: "cc_match_4", Value: "val4", DMSPath: "/cc/match", Breakout: 4},
				}
				cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3 = []types.ConfigurationParameter{
					{Name: "ipg_no_filter", Value: "val5", DMSPath: "/ipg/match"},
				}
				cfgs["v1"].UseSoftwareCCAlgorithm = false

				expectedAR := []types.ConfigurationParameter{
					{Name: "ar_match_4", Value: "val2", DMSPath: "/ar/match", Breakout: 4},
				}
				expectedCC := []types.ConfigurationParameter{
					{Name: "cc_match_4", Value: "val4", DMSPath: "/cc/match", Breakout: 4},
				}

				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(nil)
				dmsCli.On("SetParameters", expectedAR).Return(nil)
				dmsCli.On("SetParameters", expectedCC).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == shutdownInterfaceParamName
				})).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == bringUpInterfaceParamName
				})).Return(nil)

				err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())
			})

			It("filters runtime config params by Multiplane mode", func() {
				device.Status.Type = "1023"
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeUniplane
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2

				cfgs["v1"].RuntimeConfig.Roce = []types.ConfigurationParameter{
					{Name: "roce_match", Value: "val1", DMSPath: "/roce/match", Multiplane: consts.MultiplaneModeUniplane},
					{Name: "roce_skip_hwplb", Value: "val2", DMSPath: "/roce/skip1", Multiplane: consts.MultiplaneModeHwplb},
					{Name: "roce_skip_swplb", Value: "val3", DMSPath: "/roce/skip2", Multiplane: consts.MultiplaneModeSwplb},
				}
				cfgs["v1"].RuntimeConfig.AdaptiveRouting = []types.ConfigurationParameter{
					{Name: "ar_no_filter", Value: "val4", DMSPath: "/ar/match"},
				}
				cfgs["v1"].RuntimeConfig.CongestionControl = []types.ConfigurationParameter{
					{Name: "cc_no_filter", Value: "val5", DMSPath: "/cc/match"},
				}
				cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3 = []types.ConfigurationParameter{
					{Name: "ipg_no_filter", Value: "val6", DMSPath: "/ipg/match"},
				}
				cfgs["v1"].UseSoftwareCCAlgorithm = false

				expectedRoce := []types.ConfigurationParameter{
					{Name: "roce_match", Value: "val1", DMSPath: "/roce/match", Multiplane: consts.MultiplaneModeUniplane},
				}

				dmsCli.On("SetParameters", expectedRoce).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(nil)
				dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == shutdownInterfaceParamName
				})).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == bringUpInterfaceParamName
				})).Return(nil)

				err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())
			})

			It("filters InterPacketGap params by combined filters", func() {
				device.Status.Type = consts.BlueField3DeviceID
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Spec.Configuration.Template.SpectrumXOptimized.Overlay = consts.OverlayL3

				cfgs["v1"].RuntimeConfig.Roce = []types.ConfigurationParameter{}
				cfgs["v1"].RuntimeConfig.AdaptiveRouting = []types.ConfigurationParameter{}
				cfgs["v1"].RuntimeConfig.CongestionControl = []types.ConfigurationParameter{}
				cfgs["v1"].RuntimeConfig.InterPacketGap.L3EVPN = []types.ConfigurationParameter{
					{Name: "ipg_match", Value: "100", DMSPath: "/ipg/match", DeviceId: consts.BlueField3DeviceID, Breakout: 2, Multiplane: consts.MultiplaneModeHwplb},
					{Name: "ipg_skip_device", Value: "200", DMSPath: "/ipg/skip1", DeviceId: "1023", Breakout: 2, Multiplane: consts.MultiplaneModeHwplb},
					{Name: "ipg_skip_breakout", Value: "300", DMSPath: "/ipg/skip2", DeviceId: consts.BlueField3DeviceID, Breakout: 4, Multiplane: consts.MultiplaneModeHwplb},
					{Name: "ipg_skip_multiplane", Value: "400", DMSPath: "/ipg/skip3", DeviceId: consts.BlueField3DeviceID, Breakout: 2, Multiplane: consts.MultiplaneModeSwplb},
					{Name: "ipg_no_filter", Value: "500", DMSPath: "/ipg/nofilter"},
				}
				cfgs["v1"].UseSoftwareCCAlgorithm = false

				expectedIPG := []types.ConfigurationParameter{
					{Name: "ipg_match", Value: "100", DMSPath: "/ipg/match", DeviceId: consts.BlueField3DeviceID, Breakout: 2, Multiplane: consts.MultiplaneModeHwplb},
					{Name: "ipg_no_filter", Value: "500", DMSPath: "/ipg/nofilter"},
				}

				dmsCli.On("SetParameters", []types.ConfigurationParameter{}).Return(nil).Times(3)
				dmsCli.On("SetParameters", expectedIPG).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == shutdownInterfaceParamName
				})).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == bringUpInterfaceParamName
				})).Return(nil)

				err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})
})
