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
	"errors"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

// Test helpers and constants
const (
	testNetworkInterface = "enp3s0f0np0"
	testPFC              = "0,0,0,1,0,0,0,0"
	testTrustMode        = "dscp"
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

// createTrustModePath returns the path used for trust mode queries
func createTrustModePath(netInterface string) string {
	return "/interfaces/interface[name=" + netInterface + "]/nvidia/qos/config/trust-mode"
}

// createTrustModeSetPath returns the path used for setting trust mode
func createTrustModeSetPath(netInterface, value string) string {
	return "/interfaces/interface[name=" + netInterface + "]/nvidia/qos/config/trust-mode:::string:::" + value
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
					// Prepare successful responses for both trust mode and PFC
					fakeTrustCmd := createFakeCmd([]byte(`[{"updates":[{"Path":"/interfaces/interface/nvidia/qos/config/trust-mode","values":{"interfaces/interface/nvidia/qos/config/trust-mode":"dscp"}}]}]`), nil)
					fakePFCCmd := createFakeCmd([]byte(`[{"updates":[{"Path":"/interfaces/interface/nvidia/qos/config/pfc","values":{"interfaces/interface/nvidia/qos/config/pfc":"00001000"}}]}]`), nil)

					cmdAction := func(cmd string, args ...string) exec.Cmd {
						if args[5] == createTrustModePath(testNetworkInterface) {
							return fakeTrustCmd
						}
						return fakePFCCmd
					}

					fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction}
				})

				It("should return correct QoS settings", func() {
					trust, pfc, err := instance.GetQoSSettings(testNetworkInterface)
					Expect(err).NotTo(HaveOccurred())
					Expect(trust).To(Equal("dscp"))
					Expect(pfc).To(Equal("0,0,0,0,1,0,0,0"))
				})
			})

			Context("with trust mode command error", func() {
				BeforeEach(func() {
					fakeCmd := createFakeCmd(nil, errors.New("command failed"))
					cmdAction := func(cmd string, args ...string) exec.Cmd {
						return fakeCmd
					}
					fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction}
				})

				It("should handle trust mode command error", func() {
					trust, pfc, err := instance.GetQoSSettings(testNetworkInterface)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("failed to get trust mode"))
					Expect(trust).To(BeEmpty())
					Expect(pfc).To(BeEmpty())
				})
			})

			Context("with PFC command error", func() {
				BeforeEach(func() {
					fakeTrustCmd := createFakeCmd([]byte(`[{"updates":[{"Path":"/interfaces/interface/nvidia/qos/config/trust-mode","values":{"interfaces/interface/nvidia/qos/config/trust-mode":"dscp"}}]}]`), nil)
					fakePFCCmd := createFakeCmd(nil, errors.New("command failed"))

					cmdAction := func(cmd string, args ...string) exec.Cmd {
						if args[5] == createTrustModePath(testNetworkInterface) {
							return fakeTrustCmd
						}
						return fakePFCCmd
					}

					fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction}
				})

				It("should handle PFC command error", func() {
					trust, pfc, err := instance.GetQoSSettings(testNetworkInterface)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("failed to get PFC configuration"))
					Expect(trust).To(BeEmpty())
					Expect(pfc).To(BeEmpty())
				})
			})
		})

		Context("when the instance is not running", func() {
			BeforeEach(func() {
				instance.running.Store(false)
			})

			It("should return error", func() {
				trust, pfc, err := instance.GetQoSSettings(testNetworkInterface)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("DMS instance is not running"))
				Expect(trust).To(BeEmpty())
				Expect(pfc).To(BeEmpty())
			})
		})
	})

	Describe("SetQoSSettings", func() {
		Context("when the instance is running", func() {
			Context("with successful commands", func() {
				BeforeEach(func() {
					fakeTrustCmd := createFakeCmd(nil, nil)
					fakePFCCmd := createFakeCmd(nil, nil)

					cmdAction := func(cmd string, args ...string) exec.Cmd {
						if strings.Contains(args[5], ValueNameTrustMode) {
							return fakeTrustCmd
						}
						return fakePFCCmd
					}

					fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction}
				})

				It("should successfully set QoS settings", func() {
					err := instance.SetQoSSettings(testTrustMode, testPFC)
					Expect(err).NotTo(HaveOccurred())
				})
			})

			Context("with trust mode command error", func() {
				BeforeEach(func() {
					fakeCmd := createFakeCmd(nil, errors.New("command failed"))
					cmdAction := func(cmd string, args ...string) exec.Cmd {
						return fakeCmd
					}
					fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction}
				})

				It("should handle trust mode command error", func() {
					err := instance.SetQoSSettings(testTrustMode, testPFC)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("failed to set trust mode"))
				})
			})

			Context("with PFC command error", func() {
				BeforeEach(func() {
					fakeTrustCmd := createFakeCmd(nil, nil)
					fakePFCCmd := createFakeCmd(nil, errors.New("command failed"))

					cmdAction := func(cmd string, args ...string) exec.Cmd {
						if args[5] == createTrustModeSetPath(testNetworkInterface, testTrustMode) {
							return fakeTrustCmd
						}
						return fakePFCCmd
					}

					fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction}
				})

				It("should handle PFC command error", func() {
					err := instance.SetQoSSettings(testTrustMode, testPFC)
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring("failed to set PFC configuration"))
				})
			})
		})

		Context("when the instance is not running", func() {
			BeforeEach(func() {
				instance.running.Store(false)
			})

			It("should return error", func() {
				err := instance.SetQoSSettings(testTrustMode, testPFC)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(Equal("DMS instance is not running"))
			})
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
							// First call should be install command
							Expect(args).To(ContainElement("--insecure"))
							Expect(args).To(ContainElement("os"))
							Expect(args).To(ContainElement("install"))
							Expect(args).To(ContainElement("--version"))
							Expect(args).To(ContainElement(testBFBVersion))
							Expect(args).To(ContainElement("--pkg"))
							Expect(args).To(ContainElement(testBFBPath))
							return fakeInstallCmd
						} else {
							// Second call should be activate command
							Expect(args).To(ContainElement("--insecure"))
							Expect(args).To(ContainElement("os"))
							Expect(args).To(ContainElement("activate"))
							Expect(args).To(ContainElement("--version"))
							Expect(args).To(ContainElement(testBFBVersion))
							return fakeActivateCmd
						}
					}

					fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction}

					err := instance.InstallBFB(context.Background(), testBFBVersion, testBFBPath)
					Expect(err).NotTo(HaveOccurred())
				})

				It("should return error when install command fails", func() {
					fakeInstallCmd := createFakeCmd(nil, errors.New("install failed"))

					cmdAction := func(cmd string, args ...string) exec.Cmd {
						return fakeInstallCmd
					}

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
