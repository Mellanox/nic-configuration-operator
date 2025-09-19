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

package dms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

// Test helpers and constants
const (
	testNetworkInterface = "enp3s0f0np0"
	testPFC              = "0,0,0,1,0,0,0,0"
	testTrustMode        = consts.TrustModeDscp
	testToS              = 96
)

// createFakeCmd creates a fake exec Command with specified output and error
func createFakeCmd(output []byte, err error) *execTesting.FakeCmd {
	return &execTesting.FakeCmd{
		OutputScript: []execTesting.FakeAction{
			func() ([]byte, []byte, error) {
				return output, nil, err
			},
		},
	}
}

// helpers to build expected paths

//nolint:unparam
func createTrustModePath(netInterface string) string {
	return "/interfaces/interface[name=" + netInterface + "]/nvidia/qos/config/trust-mode"
}

//nolint:unparam
func createPFCPath(netInterface string) string {
	return "/interfaces/interface[name=" + netInterface + "]/nvidia/qos/config/pfc"
}

//nolint:unparam
func createToSPath(netInterface string) string {
	return "/interfaces/interface[name=" + netInterface + "]/nvidia/roce/config/tos"
}

// createTrustModeSetPath returns the path payload used for setting trust mode
func createTrustModeSetPath(netInterface, value string) string {
	return "/interfaces/interface[name=" + netInterface + "]/nvidia/qos/config/trust-mode:::string:::" + value
}
func createPFCSetPath(netInterface, value string) string {
	return "/interfaces/interface[name=" + netInterface + "]/nvidia/qos/config/pfc:::string:::" + value
}
func createToSSetPath(netInterface string, value int) string {
	return fmt.Sprintf("/interfaces/interface[name=%s]/nvidia/roce/config/tos:::int:::%d", netInterface, value)
}

// makeGetOutput returns JSON output for RunGetPathCommand
// displayPath is the Path field value; valueKeyPath is the key expected by DMS client (original unfiltered path)
func makeGetOutput(displayPath, valueKeyPath, value string) []byte {
	t := []struct {
		Source    string `json:"source"`
		Timestamp int64  `json:"timestamp"`
		Time      string `json:"time"`
		Updates   []struct {
			Path   string            `json:"Path"`
			Values map[string]string `json:"values"`
		} `json:"updates"`
	}{
		{
			Source:    "test",
			Timestamp: 0,
			Time:      "",
			Updates: []struct {
				Path   string            `json:"Path"`
				Values map[string]string `json:"values"`
			}{
				{Path: displayPath, Values: map[string]string{valueKeyPath[1:]: value}},
			},
		},
	}
	b, _ := json.Marshal(t)
	return b
}

var _ = Describe("DMSClient", func() {
	var (
		instance *dmsInstance
		device   v1alpha1.NicDeviceStatus
		fakeExec *execTesting.FakeExec
	)

	BeforeEach(func() {
		device = v1alpha1.NicDeviceStatus{
			SerialNumber: "test-serial",
			Ports: []v1alpha1.NicDevicePortSpec{
				{
					PCI:              "0000:00:00.0",
					NetworkInterface: testNetworkInterface,
				},
			},
		}
		fakeExec = &execTesting.FakeExec{}
		instance = &dmsInstance{
			device:        device,
			bindAddress:   ":9339",
			execInterface: fakeExec,
		}
		instance.running.Store(true)
	})

	Describe("GetQoSSettings", func() {
		Context("when the instance is running", func() {
			Context("with successful commands", func() {
				BeforeEach(func() {
					fakeTrustCmd := createFakeCmd(makeGetOutput(createTrustModePath(testNetworkInterface), QoSTrustModePath, "dscp"), nil)
					fakePFCCmd := createFakeCmd(makeGetOutput(createPFCPath(testNetworkInterface), QoSPFCPath, "00001000"), nil)
					fakeToSCmd := createFakeCmd(makeGetOutput(createToSPath(testNetworkInterface), ToSPath, fmt.Sprintf("%d", testToS)), nil)

					cmdAction := func(cmd string, args ...string) exec.Cmd {
						path := args[len(args)-1]
						if path == createTrustModePath(testNetworkInterface) {
							return fakeTrustCmd
						}
						if path == createPFCPath(testNetworkInterface) {
							return fakePFCCmd
						}
						return fakeToSCmd
					}

					fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction, cmdAction}
				})

				It("should return correct QoS spec", func() {
					spec, err := instance.GetQoSSettings(testNetworkInterface)
					Expect(err).NotTo(HaveOccurred())
					Expect(spec).NotTo(BeNil())
					Expect(spec.Trust).To(Equal("dscp"))
					Expect(spec.PFC).To(Equal("0,0,0,0,1,0,0,0"))
					Expect(spec.ToS).To(Equal(testToS))
				})
			})

			It("should handle trust mode command error", func() {
				fakeCmd := createFakeCmd(nil, errors.New("command failed"))
				fakeExec.CommandScript = []execTesting.FakeCommandAction{
					func(cmd string, args ...string) exec.Cmd { return fakeCmd },
				}

				spec, err := instance.GetQoSSettings(testNetworkInterface)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to get trust mode"))
				Expect(spec).To(BeNil())
			})

			It("should handle PFC command error", func() {
				fakeTrustCmd := createFakeCmd(makeGetOutput(createTrustModePath(testNetworkInterface), QoSTrustModePath, "dscp"), nil)
				fakePFCCmd := createFakeCmd(nil, errors.New("command failed"))

				cmdAction := func(cmd string, args ...string) exec.Cmd {
					path := args[len(args)-1]
					if path == createTrustModePath(testNetworkInterface) {
						return fakeTrustCmd
					}
					return fakePFCCmd
				}
				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction}

				spec, err := instance.GetQoSSettings(testNetworkInterface)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to get PFC configuration"))
				Expect(spec).To(BeNil())
			})

			It("should handle ToS command error", func() {
				fakeTrustCmd := createFakeCmd(makeGetOutput(createTrustModePath(testNetworkInterface), QoSTrustModePath, "dscp"), nil)
				fakePFCCmd := createFakeCmd(makeGetOutput(createPFCPath(testNetworkInterface), QoSPFCPath, "00001000"), nil)
				fakeToSCmd := createFakeCmd(nil, errors.New("command failed"))

				cmdAction := func(cmd string, args ...string) exec.Cmd {
					path := args[len(args)-1]
					if path == createTrustModePath(testNetworkInterface) {
						return fakeTrustCmd
					}
					if path == createPFCPath(testNetworkInterface) {
						return fakePFCCmd
					}
					return fakeToSCmd
				}
				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction, cmdAction}

				spec, err := instance.GetQoSSettings(testNetworkInterface)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to get ToS configuration"))
				Expect(spec).To(BeNil())
			})

			It("should handle ToS parse error", func() {
				fakeTrustCmd := createFakeCmd(makeGetOutput(createTrustModePath(testNetworkInterface), QoSTrustModePath, "dscp"), nil)
				fakePFCCmd := createFakeCmd(makeGetOutput(createPFCPath(testNetworkInterface), QoSPFCPath, "00001000"), nil)
				fakeToSCmd := createFakeCmd(makeGetOutput(createToSPath(testNetworkInterface), ToSPath, "not-an-int"), nil)

				cmdAction := func(cmd string, args ...string) exec.Cmd {
					path := args[len(args)-1]
					if path == createTrustModePath(testNetworkInterface) {
						return fakeTrustCmd
					}
					if path == createPFCPath(testNetworkInterface) {
						return fakePFCCmd
					}
					return fakeToSCmd
				}
				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction, cmdAction}

				spec, err := instance.GetQoSSettings(testNetworkInterface)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to convert ToS to int"))
				Expect(spec).To(BeNil())
			})
		})

		Context("when the instance is not running", func() {
			BeforeEach(func() { instance.running.Store(false) })

			It("should return error", func() {
				spec, err := instance.GetQoSSettings(testNetworkInterface)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("DMS instance is not running"))
				Expect(spec).To(BeNil())
			})
		})
	})

	Describe("SetQoSSettings", func() {
		Context("when the instance is running", func() {
			Context("with successful commands", func() {
				BeforeEach(func() {
					fakeTrustCmd := createFakeCmd(nil, nil)
					fakePFCCmd := createFakeCmd(nil, nil)
					fakeToSCmd := createFakeCmd(nil, nil)

					cmdAction := func(cmd string, args ...string) exec.Cmd {
						payload := args[len(args)-1]
						if strings.Contains(payload, createTrustModeSetPath(testNetworkInterface, "dscp")) {
							return fakeTrustCmd
						}
						if strings.Contains(payload, createPFCSetPath(testNetworkInterface, "00010000")) {
							return fakePFCCmd
						}
						if strings.Contains(payload, createToSSetPath(testNetworkInterface, testToS)) {
							return fakeToSCmd
						}
						return createFakeCmd(nil, nil)
					}

					fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction, cmdAction}
				})

				It("should successfully set QoS settings", func() {
					err := instance.SetQoSSettings(&v1alpha1.QosSpec{Trust: testTrustMode, PFC: testPFC, ToS: testToS})
					Expect(err).NotTo(HaveOccurred())
				})
			})

			It("should handle trust mode command error", func() {
				fakeCmd := createFakeCmd(nil, errors.New("command failed"))
				fakeExec.CommandScript = []execTesting.FakeCommandAction{
					func(cmd string, args ...string) exec.Cmd { return fakeCmd },
				}

				err := instance.SetQoSSettings(&v1alpha1.QosSpec{Trust: testTrustMode, PFC: testPFC, ToS: testToS})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to set trust mode"))
			})

			It("should handle PFC command error", func() {
				fakeTrustCmd := createFakeCmd(nil, nil)
				fakePFCCmd := createFakeCmd(nil, errors.New("command failed"))

				cmdAction := func(cmd string, args ...string) exec.Cmd {
					payload := args[len(args)-1]
					if strings.Contains(payload, createTrustModeSetPath(testNetworkInterface, "dscp")) {
						return fakeTrustCmd
					}
					return fakePFCCmd
				}
				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction}

				err := instance.SetQoSSettings(&v1alpha1.QosSpec{Trust: testTrustMode, PFC: testPFC, ToS: testToS})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to set PFC configuration"))
			})

			It("should handle ToS command error", func() {
				fakeTrustCmd := createFakeCmd(nil, nil)
				fakePFCCmd := createFakeCmd(nil, nil)
				fakeToSCmd := createFakeCmd(nil, errors.New("command failed"))

				cmdAction := func(cmd string, args ...string) exec.Cmd {
					payload := args[len(args)-1]
					if strings.Contains(payload, createTrustModeSetPath(testNetworkInterface, "dscp")) {
						return fakeTrustCmd
					}
					if strings.Contains(payload, createPFCSetPath(testNetworkInterface, "00010000")) {
						return fakePFCCmd
					}
					if strings.Contains(payload, createToSSetPath(testNetworkInterface, testToS)) {
						return fakeToSCmd
					}
					return fakeToSCmd
				}
				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction, cmdAction}

				err := instance.SetQoSSettings(&v1alpha1.QosSpec{Trust: testTrustMode, PFC: testPFC, ToS: testToS})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to set ToS configuration"))
			})

			It("should return error for invalid trust mode", func() {
				err := instance.SetQoSSettings(&v1alpha1.QosSpec{Trust: "invalid", PFC: testPFC, ToS: testToS})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid trust mode"))
			})
		})

		Context("when the instance is not running", func() {
			BeforeEach(func() { instance.running.Store(false) })

			It("should return error", func() {
				err := instance.SetQoSSettings(&v1alpha1.QosSpec{Trust: testTrustMode, PFC: testPFC, ToS: testToS})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("DMS instance is not running"))
			})
		})
	})

	Describe("GetParameters", func() {
		It("returns value for simple path", func() {
			param := types.ConfigurationParameter{DMSPath: "/nvidia/mode/config/mode"}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					path := args[len(args)-1]
					Expect(path).To(Equal(param.DMSPath))
					return createFakeCmd(makeGetOutput(param.DMSPath, param.DMSPath, "NIC"), nil)
				},
			}

			vals, err := instance.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
			Expect(vals[param.DMSPath]).To(Equal("NIC"))
		})

		It("returns value for interface path with final unfiltered get", func() {
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/qos/config/trust-mode"}

			filtered := createTrustModePath(testNetworkInterface)
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				// filtered get per port
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd(makeGetOutput(filtered, param.DMSPath, "dscp"), nil)
				},
				// final unfiltered get
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd(makeGetOutput(param.DMSPath, param.DMSPath, "dscp"), nil)
				},
			}

			vals, err := instance.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
			Expect(vals[param.DMSPath]).To(Equal("dscp"))
		})

		It("fails on mismatch across priorities", func() {
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/cc/config/priority/np_enabled"}
			filtered := "/interfaces/interface[name=" + testNetworkInterface + "]/nvidia/cc/config/priority/np_enabled"

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd(makeGetOutput(filtered, param.DMSPath, "1"), nil)
				},
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd(makeGetOutput(filtered, param.DMSPath, "0"), nil)
				},
			}

			_, err := instance.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).To(HaveOccurred())
			Expect(types.IsValuesDoNotMatchError(err)).To(BeTrue())
		})

		It("returns value for interface+priority path across all IDs", func() {
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/cc/config/priority/np_enabled"}
			expectedFiltered := make([]string, 0, 8)
			for id := 0; id < 8; id++ {
				expectedFiltered = append(expectedFiltered, fmt.Sprintf("/interfaces/interface[name=%s]/nvidia/cc/config/priority[id=%d]/np_enabled", testNetworkInterface, id))
			}

			fakeExec.CommandScript = []execTesting.FakeCommandAction{}
			// eight filtered gets
			for i := 0; i < 8; i++ {
				path := expectedFiltered[i]
				fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
					reqPath := args[len(args)-1]
					Expect(reqPath).To(Equal(path))
					return createFakeCmd(makeGetOutput(path, param.DMSPath, "1"), nil)
				})
			}
			// final unfiltered get
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				reqPath := args[len(args)-1]
				Expect(reqPath).To(Equal(param.DMSPath))
				return createFakeCmd(makeGetOutput(param.DMSPath, param.DMSPath, "1"), nil)
			})

			vals, err := instance.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
			Expect(vals[param.DMSPath]).To(Equal("1"))
		})
	})

	Describe("SetParameters", func() {
		It("sets value for simple path", func() {
			param := types.ConfigurationParameter{DMSPath: "/nvidia/mode/config/mode", Value: "NIC", ValueType: ValueTypeString}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd { return createFakeCmd(nil, nil) },
			}
			err := instance.SetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
		})

		It("sets value per interface and unfiltered", func() {
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/qos/config/trust-mode", Value: "dscp", ValueType: ValueTypeString}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				// filtered set
				func(cmd string, args ...string) exec.Cmd { return createFakeCmd(nil, nil) },
				// final unfiltered set
				func(cmd string, args ...string) exec.Cmd { return createFakeCmd(nil, nil) },
			}
			err := instance.SetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
		})

		It("sets value per interface+priority id across all IDs and unfiltered", func() {
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/cc/config/priority/np_enabled", Value: "1", ValueType: ValueTypeBool}
			expectedFiltered := make([]string, 0, 8)
			for id := 0; id < 8; id++ {
				expectedFiltered = append(expectedFiltered, fmt.Sprintf("/interfaces/interface[name=%s]/nvidia/cc/config/priority[id=%d]/np_enabled", testNetworkInterface, id))
			}

			fakeExec.CommandScript = []execTesting.FakeCommandAction{}
			// eight filtered sets
			for i := 0; i < 8; i++ {
				path := expectedFiltered[i]
				fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
					payload := args[len(args)-1]
					Expect(payload).To(ContainSubstring(path))
					Expect(payload).To(ContainSubstring(":::bool:::"))
					Expect(payload).To(ContainSubstring(":::1"))
					return createFakeCmd(nil, nil)
				})
			}
			// final unfiltered set
			fakeExec.CommandScript = append(fakeExec.CommandScript, func(cmd string, args ...string) exec.Cmd {
				payload := args[len(args)-1]
				Expect(payload).To(ContainSubstring(param.DMSPath))
				Expect(payload).To(ContainSubstring(":::bool:::"))
				Expect(payload).To(ContainSubstring(":::1"))
				return createFakeCmd(nil, nil)
			})

			err := instance.SetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Describe("InstallBFB", func() {
		const (
			testBFBVersion = "24.35.1000"
			testBFBPath    = "/path/to/firmware.bfb"
		)

		Context("when the instance is running", func() {
			Context("on BlueField device", func() {
				BeforeEach(func() {
					device.Type = "a2d6" // BlueField2 device ID
					instance.device = device
					instance.running.Store(true)
				})

				It("should install and activate BFB successfully", func() {
					fakeInstallCmd := createFakeCmd([]byte("BFB install successful"), nil)
					fakeActivateCmd := createFakeCmd([]byte("BFB activate successful"), nil)

					callCount := 0
					cmdAction := func(cmd string, args ...string) exec.Cmd {
						callCount++
						Expect(cmd).To(Equal(dmsClientPath))

						if callCount == 1 {
							Expect(args).To(ContainElement("--insecure"))
							Expect(args).To(ContainElement("os"))
							Expect(args).To(ContainElement("install"))
							Expect(args).To(ContainElement("--version"))
							Expect(args).To(ContainElement(testBFBVersion))
							Expect(args).To(ContainElement("--pkg"))
							Expect(args).To(ContainElement(testBFBPath))
							return fakeInstallCmd
						}
						Expect(args).To(ContainElement("--insecure"))
						Expect(args).To(ContainElement("os"))
						Expect(args).To(ContainElement("activate"))
						Expect(args).To(ContainElement("--version"))
						Expect(args).To(ContainElement(testBFBVersion))
						return fakeActivateCmd
					}

					fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction}

					err := instance.InstallBFB(context.Background(), testBFBVersion, testBFBPath)
					Expect(err).NotTo(HaveOccurred())
				})

				It("should return error when install command fails", func() {
					fakeInstallCmd := createFakeCmd(nil, errors.New("install failed"))

					cmdAction := func(cmd string, args ...string) exec.Cmd { return fakeInstallCmd }
					fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction}

					err := instance.InstallBFB(context.Background(), testBFBVersion, testBFBPath)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("install failed"))
				})

				It("should return error when activate command fails", func() {
					fakeInstallCmd := createFakeCmd([]byte("BFB install successful"), nil)
					fakeActivateCmd := createFakeCmd(nil, errors.New("activate failed"))

					cmdAction := func(cmd string, args ...string) exec.Cmd {
						if len(args) >= 3 && args[2] == "install" {
							return fakeInstallCmd
						}
						return fakeActivateCmd
					}

					fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction}

					err := instance.InstallBFB(context.Background(), testBFBVersion, testBFBPath)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("activate failed"))
				})
			})

			Context("on non-BlueField device", func() {
				BeforeEach(func() {
					device.Type = "cx6"
					instance.device = device
					instance.running.Store(true)
				})

				It("should return error for non-BlueField device", func() {
					err := instance.InstallBFB(context.Background(), testBFBVersion, testBFBPath)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("cannot install BFB file on non-BlueField device"))
				})
			})
		})

		Context("when the instance is not running", func() {
			BeforeEach(func() {
				device.Type = "a2d6" // BlueField2 device ID
				instance.device = device
				instance.running.Store(false)
			})

			It("should return error", func() {
				err := instance.InstallBFB(context.Background(), testBFBVersion, testBFBPath)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("DMS instance is not running"))
			})
		})
	})
})
