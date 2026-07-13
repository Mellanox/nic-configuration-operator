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
	cmds  []*fakeCmd
	pos   int
	calls []commandCall
}

type commandCall struct {
	name string
	args []string
}

const (
	shutdownInterfaceParamName = "Shut down interface"
	bringUpInterfaceParamName  = "Bring up interface to apply IPG settings"

	// Realistic mlxreg ROCE_ACCL --get output snippets for CC Probe MP mode tests.
	// Includes other fields with 0x00000001 to verify parsing targets the correct field.
	mlxregGetCCProbeMPSet = `Sending access register...

Field Name                                     | Data
============================================================
roce_adp_retrans_en                            | 0x00000001
roce_tx_window_en                              | 0x00000001
adaptive_routing_forced_en                     | 0x00000001
cc_probe_mp_mode                               | 0x00000001
============================================================`

	mlxregGetCCProbeMPUnset = `Sending access register...

Field Name                                     | Data
============================================================
roce_adp_retrans_en                            | 0x00000001
roce_tx_window_en                              | 0x00000001
adaptive_routing_forced_en                     | 0x00000001
cc_probe_mp_mode                               | 0x00000000
============================================================`
)

var (
	nextCmd *fakeCmd
)

func ccProbeMPModeParam() types.ConfigurationParameter {
	return types.ConfigurationParameter{
		Name:       "CC Probe MP mode",
		Value:      "0x00000001",
		Multiplane: consts.MultiplaneModeHwplb,
		MlxReg: &types.MlxRegParameter{
			Register: "ROCE_ACCL",
			Field:    "cc_probe_mp_mode",
			SetFields: []types.MlxRegField{
				{Name: "cc_probe_mp_mode", Value: "0x1"},
				{Name: "cc_probe_mp_mode_field_select", Value: "0x1"},
			},
		},
	}
}

func (f *fakeExec) next() execUtils.Cmd {
	if f.cmds != nil && f.pos < len(f.cmds) {
		c := f.cmds[f.pos]
		f.pos++
		return c
	}
	return nextCmd
}
func (f *fakeExec) Command(cmd string, args ...string) execUtils.Cmd {
	f.calls = append(f.calls, commandCall{name: cmd, args: append([]string{}, args...)})
	return f.next()
}
func (f *fakeExec) CommandContext(ctx context.Context, cmd string, args ...string) execUtils.Cmd {
	f.calls = append(f.calls, commandCall{name: cmd, args: append([]string{}, args...)})
	return f.next()
}

var _ = Describe("SpectrumXConfigManager", func() {
	var (
		dmsMgr   dmsmocks.DMSManager
		dmsCli   dmsmocks.DMSClient
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
		execFake = &fakeExec{}

		cfgs = map[string]*types.SpectrumXConfig{
			"v1": {
				MlxConfig: map[string]map[string]types.SpectrumXDeviceConfig{
					"none": {
						"1023": {
							Breakout: map[int]map[string]string{
								1: {"NUM_OF_PF": "1", "NUM_OF_PLANES_P1": "0"},
							},
							PostBreakout: map[string]string{"LINK_TYPE_P1": "2"},
						},
					},
					"swplb": {
						"1023": {
							Breakout: map[int]map[string]string{
								2: {"NUM_OF_PF": "2", "NUM_OF_PLANES_P1": "0"},
								4: {"NUM_OF_PF": "4", "NUM_OF_PLANES_P1": "0"},
							},
							PostBreakout: map[string]string{"LINK_TYPE_P1": "2"},
						},
					},
					"hwplb": {
						"1023": {
							Breakout: map[int]map[string]string{
								2: {"NUM_OF_PF": "2", "NUM_OF_PLANES_P1": "2"},
								4: {"NUM_OF_PF": "4", "NUM_OF_PLANES_P1": "4"},
							},
							PostBreakout: map[string]string{"LINK_TYPE_P1": "2"},
						},
					},
					"uniplane": {
						"1023": {
							Breakout: map[int]map[string]string{
								2: {"NUM_OF_PF": "2", "NUM_OF_PLANES_P1": "0"},
								4: {"NUM_OF_PF": "4", "NUM_OF_PLANES_P1": "0"},
							},
							PostBreakout: map[string]string{"LINK_TYPE_P1": "2"},
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
			ccProcesses:       map[string]*ccProcess{},
			ccTerminationChan: make(chan string, 10),
		}

		beforeDevice()
		dmsMgr.On("GetDMSClientByPCIAddress", "0000:00:00").Return(&dmsCli, nil).Maybe()
	})

	Describe("GetBreakoutMlxConfig", func() {
		It("returns breakout map for matching mode/device/planes", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
			device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
			result, err := manager.GetBreakoutMlxConfig(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveKeyWithValue("NUM_OF_PF", "2"))
			Expect(result).To(HaveKeyWithValue("NUM_OF_PLANES_P1", "0"))
		})

		It("returns nil for missing multiplane mode", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = "nonexistent"
			result, err := manager.GetBreakoutMlxConfig(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeNil())
		})

		It("returns nil for missing device type", func() {
			device.Status.Type = "9999"
			device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
			device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
			result, err := manager.GetBreakoutMlxConfig(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeNil())
		})

		It("returns nil for missing plane count", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
			device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 8
			result, err := manager.GetBreakoutMlxConfig(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeNil())
		})

		It("returns error for missing config version", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.Version = "missing"
			_, err := manager.GetBreakoutMlxConfig(device)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("GetPostBreakoutMlxConfig", func() {
		It("returns postBreakout map for matching mode/device", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
			result, err := manager.GetPostBreakoutMlxConfig(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(HaveKeyWithValue("LINK_TYPE_P1", "2"))
		})

		It("returns nil for missing device type", func() {
			device.Status.Type = "9999"
			device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
			result, err := manager.GetPostBreakoutMlxConfig(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeNil())
		})

		It("returns error for missing config version", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.Version = "missing"
			_, err := manager.GetPostBreakoutMlxConfig(device)
			Expect(err).To(HaveOccurred())
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
			_, err := manager.ApplyRuntimeConfig(device)
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
			_, err := manager.ApplyRuntimeConfig(device)
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
			_, err := manager.ApplyRuntimeConfig(device)
			Expect(err).To(HaveOccurred())
			Expect(strings.ToLower(err.Error())).To(ContainSubstring("invalid overlay"))
		})

		It("bubbles up DMS errors", func() {
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(errors.New("roce set error"))
			_, err := manager.ApplyRuntimeConfig(device)
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
				createCnpDscpFile("eth0", "48") // swplb expects 48

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

				applied, err := manager.RuntimeConfigApplied(device)
				Expect(err).NotTo(HaveOccurred())
				Expect(applied).To(BeTrue())
			})

			It("returns true when CNP DSCP is set to expected value for none", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeNone
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 1
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:00:00.0", NetworkInterface: "eth0", RdmaInterface: "mlx5_0"},
				}
				createCnpDscpFile("eth0", "48") // none expects 48

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

			It("returns false when CNP DSCP has wrong value", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeSwplb
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
				device.Status.Ports = []v1alpha1.NicDevicePortSpec{
					{PCI: "0000:00:00.0", NetworkInterface: "eth0", RdmaInterface: "mlx5_0"},
				}
				createCnpDscpFile("eth0", "12") // wrong value for swplb (expects 48)

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
				createCnpDscpFile("eth0", "48") // uniplane expects 48
				createCnpDscpFile("eth1", "48")

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
			It("writes CNP DSCP value 48 for swplb", func() {
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
				_, err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())

				// Verify the file was written with swplb value
				data, err := os.ReadFile(filepath.Join(dir, "cnp_dscp"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data)).To(Equal("48"))
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
				_, err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())

				// Verify the file was written with hwplb value
				data, err := os.ReadFile(filepath.Join(dir, "cnp_dscp"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data)).To(Equal("48"))
			})

			It("writes CNP DSCP value 48 for none", func() {
				device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeNone
				device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 1
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
				_, err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())

				// Verify the file was written with none value
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
				_, err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())

				// uniplane expects 48
				data0, err := os.ReadFile(filepath.Join(dir0, "cnp_dscp"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data0)).To(Equal("48"))

				data1, err := os.ReadFile(filepath.Join(dir1, "cnp_dscp"))
				Expect(err).NotTo(HaveOccurred())
				Expect(string(data1)).To(Equal("48"))
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
				_, err := manager.ApplyRuntimeConfig(device)
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

				_, err := manager.ApplyRuntimeConfig(device)
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

				_, err := manager.ApplyRuntimeConfig(device)
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

				_, err := manager.ApplyRuntimeConfig(device)
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

				dmsCli.On("SetParameters", expectedIPG).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == shutdownInterfaceParamName
				})).Return(nil)
				dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
					return len(params) == 1 && params[0].Name == bringUpInterfaceParamName
				})).Return(nil)

				_, err := manager.ApplyRuntimeConfig(device)
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("mlxreg runtime parameters", func() {
		It("checks an mlxreg parameter on every PF", func() {
			device.Status.Ports = []v1alpha1.NicDevicePortSpec{
				{PCI: "0000:00:00.0", RdmaInterface: "mlx5_0"},
				{PCI: "0000:00:00.1", RdmaInterface: "mlx5_1"},
			}
			execFake.cmds = []*fakeCmd{
				{output: []byte(mlxregGetCCProbeMPSet), err: nil},
				{output: []byte(mlxregGetCCProbeMPSet), err: nil},
			}

			applied, err := manager.checkMlxRegParamApplied(device, ccProbeMPModeParam())
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeTrue())
		})

		It("returns false when an mlxreg value does not match", func() {
			execFake.cmds = []*fakeCmd{{output: []byte(mlxregGetCCProbeMPUnset), err: nil}}

			applied, err := manager.checkMlxRegParamApplied(device, ccProbeMPModeParam())
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeFalse())
		})

		It("matches the exact mlxreg field name when a suffixed field appears first", func() {
			output := `Sending access register...

Field Name                                     | Data
============================================================
cc_probe_mp_mode_field_select                  | 0x00000000
cc_probe_mp_mode                               | 0x00000001
============================================================`
			execFake.cmds = []*fakeCmd{{output: []byte(output), err: nil}}

			applied, err := manager.checkMlxRegParamApplied(device, ccProbeMPModeParam())
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeTrue())
		})

		It("matches equivalent mlxreg numeric values with different hex widths", func() {
			param := ccProbeMPModeParam()
			param.Value = "0x1"
			execFake.cmds = []*fakeCmd{{output: []byte(mlxregGetCCProbeMPSet), err: nil}}

			applied, err := manager.checkMlxRegParamApplied(device, param)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeTrue())
		})

		It("returns false when the mlxreg field is missing", func() {
			execFake.cmds = []*fakeCmd{{output: []byte("other_field | 0x00000001"), err: nil}}

			applied, err := manager.checkMlxRegParamApplied(device, ccProbeMPModeParam())
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeFalse())
		})

		It("returns error when mlxreg get fails", func() {
			execFake.cmds = []*fakeCmd{{output: []byte("failed"), err: errors.New("mlxreg failed")}}

			_, err := manager.checkMlxRegParamApplied(device, ccProbeMPModeParam())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("mlxreg"))
		})

		It("returns an error when an mlxreg parameter also has a DMS path", func() {
			param := ccProbeMPModeParam()
			param.DMSPath = "/interfaces/interface/nvidia/roce/config/cc-probe-mp-mode"

			err := manager.setMlxRegParam(device, param)
			Expect(err).To(MatchError(ContainSubstring("cannot define both dmsPath and mlxreg")))
			Expect(execFake.calls).To(BeEmpty())
		})

		It("returns an error when an mlxreg set field value is empty", func() {
			param := ccProbeMPModeParam()
			param.MlxReg.SetFields[0].Value = ""

			err := manager.setMlxRegParam(device, param)
			Expect(err).To(MatchError(ContainSubstring("without value")))
			Expect(execFake.calls).To(BeEmpty())
		})

		It("builds mlxreg set from setFields", func() {
			execFake.cmds = []*fakeCmd{{output: []byte("ok"), err: nil}}

			err := manager.setMlxRegParam(device, ccProbeMPModeParam())
			Expect(err).NotTo(HaveOccurred())
			Expect(execFake.calls).To(HaveLen(1))
			Expect(execFake.calls[0].name).To(Equal(mlxregBinary))
			Expect(execFake.calls[0].args).To(Equal([]string{
				"-d", "0000:00:00.0",
				"--reg_name", "ROCE_ACCL",
				"--set", "cc_probe_mp_mode=0x1,cc_probe_mp_mode_field_select=0x1",
				"--yes",
			}))
		})

		It("returns error when mlxreg set fails", func() {
			execFake.cmds = []*fakeCmd{{output: []byte("failed"), err: errors.New("mlxreg set error")}}

			err := manager.setMlxRegParam(device, ccProbeMPModeParam())
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("mlxreg"))
		})

		It("checks ordered runtime params with DMS batches split by mlxreg barriers", func() {
			params := []types.ConfigurationParameter{
				{Name: "dms before 1", Value: "a", DMSPath: "/before/1"},
				{Name: "dms before 2", Value: "b", DMSPath: "/before/2"},
				ccProbeMPModeParam(),
				{Name: "dms after", Value: "c", DMSPath: "/after"},
			}
			dmsCli.On("GetParameters", params[:2]).Return(map[string]string{"/before/1": "a", "/before/2": "b"}, nil).Once()
			dmsCli.On("GetParameters", params[3:]).Return(map[string]string{"/after": "c"}, nil).Once()
			execFake.cmds = []*fakeCmd{{output: []byte(mlxregGetCCProbeMPSet), err: nil}}

			applied, err := manager.checkRuntimeParamsApplied(device, params, &dmsCli)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeTrue())
		})

		It("applies ordered runtime params with DMS batches split by mlxreg barriers", func() {
			params := []types.ConfigurationParameter{
				{Name: "dms before 1", Value: "a", DMSPath: "/before/1"},
				{Name: "dms before 2", Value: "b", DMSPath: "/before/2"},
				ccProbeMPModeParam(),
				{Name: "dms after", Value: "c", DMSPath: "/after"},
			}
			dmsCli.On("SetParameters", params[:2]).Return(nil).Once()
			dmsCli.On("SetParameters", params[3:]).Return(nil).Once()
			execFake.cmds = []*fakeCmd{{output: []byte("ok"), err: nil}}

			err := manager.applyRuntimeParams(device, params, &dmsCli)
			Expect(err).NotTo(HaveOccurred())
			Expect(execFake.calls).To(HaveLen(1))
		})

		It("validates mlxreg parameters before flushing a pending DMS batch", func() {
			mixedParam := ccProbeMPModeParam()
			mixedParam.DMSPath = "/mixed"
			params := []types.ConfigurationParameter{
				{Name: "dms before", Value: "a", DMSPath: "/before"},
				mixedParam,
			}

			err := manager.applyRuntimeParams(device, params, &dmsCli)
			Expect(err).To(MatchError(ContainSubstring("cannot define both dmsPath and mlxreg")))
			dmsCli.AssertNotCalled(GinkgoT(), "SetParameters", mock.Anything)
			Expect(execFake.calls).To(BeEmpty())
		})

		It("uses profile order for adaptive routing mlxreg parameters", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
			device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
			cfgs["v1"].RuntimeConfig.AdaptiveRouting = []types.ConfigurationParameter{
				{Name: "ar before", Value: "a", DMSPath: "/ar/before"},
				ccProbeMPModeParam(),
				{Name: "ar after", Value: "b", DMSPath: "/ar/after"},
			}
			cfgs["v1"].UseSoftwareCCAlgorithm = false

			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting[:1]).Return(nil).Once()
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.AdaptiveRouting[2:]).Return(nil).Once()
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(nil)
			dmsCli.On("SetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(nil)
			dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
				return len(params) == 1 && params[0].Name == shutdownInterfaceParamName
			})).Return(nil)
			dmsCli.On("SetParameters", mock.MatchedBy(func(params []types.ConfigurationParameter) bool {
				return len(params) == 1 && params[0].Name == bringUpInterfaceParamName
			})).Return(nil)
			execFake.cmds = []*fakeCmd{{output: []byte("ok"), err: nil}}

			_, err := manager.ApplyRuntimeConfig(device)
			Expect(err).NotTo(HaveOccurred())
		})

		It("checks mlxreg parameters on multiple PFs when they are listed in the profile", func() {
			device.Spec.Configuration.Template.SpectrumXOptimized.MultiplaneMode = consts.MultiplaneModeHwplb
			device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 2
			device.Status.Ports = []v1alpha1.NicDevicePortSpec{
				{PCI: "0000:00:00.0", RdmaInterface: "mlx5_0"},
				{PCI: "0000:00:00.1", RdmaInterface: "mlx5_1"},
			}
			cfgs["v1"].RuntimeConfig.AdaptiveRouting = []types.ConfigurationParameter{ccProbeMPModeParam()}
			cfgs["v1"].UseSoftwareCCAlgorithm = false

			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.Roce).Return(map[string]string{"/r": "x"}, nil)
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.CongestionControl).Return(map[string]string{"/cc": "z"}, nil)
			dmsCli.On("GetParameters", cfgs["v1"].RuntimeConfig.InterPacketGap.PureL3).Return(map[string]string{"/ipg": "25"}, nil)
			execFake.cmds = []*fakeCmd{
				{output: []byte(mlxregGetCCProbeMPSet), err: nil},
				{output: []byte(mlxregGetCCProbeMPSet), err: nil},
			}

			applied, err := manager.RuntimeConfigApplied(device)
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(BeTrue())
		})
	})

	Describe("filterParameters", func() {
		It("should keep HwplbFirstPortOnly when in hwplb mode", func() {
			params := []types.ConfigurationParameter{
				{Name: "rp_enabled", DMSPath: "/interfaces/interface/nvidia/cc/config/priority/rp_enabled", HwplbFirstPortOnly: true},
				{Name: "other", DMSPath: "/interfaces/interface/nvidia/cc/slot[id=0]/config/enabled"},
			}
			filtered := filterParameters(params, "", 0, consts.MultiplaneModeHwplb)
			Expect(filtered).To(HaveLen(2))
			Expect(filtered[0].HwplbFirstPortOnly).To(BeTrue())
			Expect(filtered[1].HwplbFirstPortOnly).To(BeFalse())
		})

		It("should clear HwplbFirstPortOnly when not in hwplb mode", func() {
			params := []types.ConfigurationParameter{
				{Name: "rp_enabled", DMSPath: "/interfaces/interface/nvidia/cc/config/priority/rp_enabled", HwplbFirstPortOnly: true},
			}
			filtered := filterParameters(params, "", 0, consts.MultiplaneModeSwplb)
			Expect(filtered).To(HaveLen(1))
			Expect(filtered[0].HwplbFirstPortOnly).To(BeFalse())
		})
	})
})
