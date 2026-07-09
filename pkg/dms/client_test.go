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
	testPCI              = "0000:00:00.0"
	testPFC              = "0,0,0,1,0,0,0,0"
	testTrustMode        = consts.TrustModeDscp
	testToS              = 96
)

// createFakeCmd creates a fake exec Command with specified output and error
func createFakeCmd(output []byte, err error) *execTesting.FakeCmd {
	action := func() ([]byte, []byte, error) {
		return output, nil, err
	}
	return &execTesting.FakeCmd{
		OutputScript:         []execTesting.FakeAction{action},
		CombinedOutputScript: []execTesting.FakeAction{action},
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
//
//nolint:unparam
func createTrustModeSetPath(netInterface, value string) string {
	return "/interfaces/interface[name=" + netInterface + "]/nvidia/qos/config/trust-mode:::string:::" + value
}

//nolint:unparam
func createPFCSetPath(netInterface, value string) string {
	return "/interfaces/interface[name=" + netInterface + "]/nvidia/qos/config/pfc:::string:::" + value
}

//nolint:unparam
func createToSSetPath(netInterface string, value int) string {
	return fmt.Sprintf("/interfaces/interface[name=%s]/nvidia/roce/config/tos:::int:::%d", netInterface, value)
}

// makeGetOutput returns JSON output for RunGetPathCommand
// displayPath is the Path field value; valueKeyPath is the key expected by DMS client (original unfiltered path)
func makeGetOutput(displayPath, valueKeyPath, value string) []byte {
	return makeBatchedGetOutput(getOutputUpdate{displayPath: displayPath, valueKeyPath: valueKeyPath, value: value})
}

type getOutputUpdate struct {
	displayPath  string
	valueKeyPath string
	value        string
}

func makeBatchedGetOutput(updates ...getOutputUpdate) []byte {
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
		},
	}
	for _, update := range updates {
		t[0].Updates = append(t[0].Updates, struct {
			Path   string            `json:"Path"`
			Values map[string]string `json:"values"`
		}{
			Path:   update.displayPath,
			Values: map[string]string{strings.TrimPrefix(update.valueKeyPath, "/"): update.value},
		})
	}
	b, _ := json.Marshal(t)
	return b
}

var _ = Describe("DMSClient", func() {
	var (
		client   *dmsClient
		device   v1alpha1.NicDeviceStatus
		fakeExec *execTesting.FakeExec
	)

	BeforeEach(func() {
		device = v1alpha1.NicDeviceStatus{
			SerialNumber: "test-serial",
			Ports: []v1alpha1.NicDevicePortSpec{
				{
					PCI:              testPCI,
					NetworkInterface: testNetworkInterface,
				},
			},
		}
		fakeExec = &execTesting.FakeExec{}
		client = &dmsClient{
			device:        device,
			targetPCI:     testPCI,
			bindAddress:   ":9339",
			authParams:    []string{"--insecure"},
			execInterface: fakeExec,
		}
	})

	// verifyTargetFlag checks that --target PCI is present in command args
	verifyTargetFlag := func(args []string) {
		found := false
		for i, arg := range args {
			if arg == "--target" && i+1 < len(args) && args[i+1] == testPCI {
				found = true
				break
			}
		}
		Expect(found).To(BeTrue(), "Expected --target %s in args: %v", testPCI, args)
	}

	Describe("GetQoSSettings", func() {
		Context("with successful commands", func() {
			BeforeEach(func() {
				fakeTrustCmd := createFakeCmd(makeGetOutput(createTrustModePath(testNetworkInterface), QoSTrustModePath, "dscp"), nil)
				fakePFCCmd := createFakeCmd(makeGetOutput(createPFCPath(testNetworkInterface), QoSPFCPath, "00001000"), nil)
				fakeToSCmd := createFakeCmd(makeGetOutput(createToSPath(testNetworkInterface), ToSPath, fmt.Sprintf("%d", testToS)), nil)

				cmdAction := func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
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
				spec, err := client.GetQoSSettings(testNetworkInterface)
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

			spec, err := client.GetQoSSettings(testNetworkInterface)
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

			spec, err := client.GetQoSSettings(testNetworkInterface)
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

			spec, err := client.GetQoSSettings(testNetworkInterface)
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

			spec, err := client.GetQoSSettings(testNetworkInterface)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to convert ToS to int"))
			Expect(spec).To(BeNil())
		})
	})

	Describe("SetQoSSettings", func() {
		Context("with successful commands", func() {
			BeforeEach(func() {
				fakeTrustCmd := createFakeCmd(nil, nil)
				fakePFCCmd := createFakeCmd(nil, nil)
				fakeToSCmd := createFakeCmd(nil, nil)

				cmdAction := func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
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
				err := client.SetQoSSettings(&v1alpha1.QosSpec{Trust: testTrustMode, PFC: testPFC, ToS: testToS})
				Expect(err).NotTo(HaveOccurred())
			})
		})

		It("should handle trust mode command error", func() {
			fakeCmd := createFakeCmd(nil, errors.New("command failed"))
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd { return fakeCmd },
			}

			err := client.SetQoSSettings(&v1alpha1.QosSpec{Trust: testTrustMode, PFC: testPFC, ToS: testToS})
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

			err := client.SetQoSSettings(&v1alpha1.QosSpec{Trust: testTrustMode, PFC: testPFC, ToS: testToS})
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

			err := client.SetQoSSettings(&v1alpha1.QosSpec{Trust: testTrustMode, PFC: testPFC, ToS: testToS})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to set ToS configuration"))
		})

		It("should return error for invalid trust mode", func() {
			err := client.SetQoSSettings(&v1alpha1.QosSpec{Trust: "invalid", PFC: testPFC, ToS: testToS})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid trust mode"))
		})
	})

	Describe("RunGetPathCommands", func() {
		It("returns values keyed by original parameter paths", func() {
			paramPath := "/interfaces/interface/nvidia/cc/config/priority/rp_enabled"
			requests := []getPathRequest{
				newGetPathRequest(paramPath, mergeFilterRules(interfaceNameFilter(testNetworkInterface), priorityIdFilter(0))),
				newGetPathRequest(paramPath, mergeFilterRules(interfaceNameFilter(testNetworkInterface), priorityIdFilter(1))),
			}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd(makeBatchedGetOutput(
						getOutputUpdate{displayPath: requests[0].queryPath, valueKeyPath: paramPath, value: "true"},
						getOutputUpdate{displayPath: requests[1].queryPath, valueKeyPath: paramPath, value: "true"},
					), nil)
				},
			}

			values, err := client.RunGetPathCommands(getPathRequestBatch{
				key:      getPathRequestBatchKey(interfaceNameFilter(testNetworkInterface)),
				requests: requests,
			})

			Expect(err).ToNot(HaveOccurred())
			Expect(values).To(Equal(map[string]string{
				paramPath: "true",
			}))
		})

		It("returns a values-do-not-match error for conflicting values of one parameter", func() {
			paramPath := "/interfaces/interface/nvidia/cc/config/priority/rp_enabled"
			requests := []getPathRequest{
				newGetPathRequest(paramPath, mergeFilterRules(interfaceNameFilter(testNetworkInterface), priorityIdFilter(0))),
				newGetPathRequest(paramPath, mergeFilterRules(interfaceNameFilter(testNetworkInterface), priorityIdFilter(1))),
			}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd(makeBatchedGetOutput(
						getOutputUpdate{displayPath: requests[0].queryPath, valueKeyPath: paramPath, value: "true"},
						getOutputUpdate{displayPath: requests[1].queryPath, valueKeyPath: paramPath, value: "false"},
					), nil)
				},
			}

			_, err := client.RunGetPathCommands(getPathRequestBatch{
				key:      getPathRequestBatchKey(interfaceNameFilter(testNetworkInterface)),
				requests: requests,
			})

			Expect(err).To(HaveOccurred())
			Expect(types.IsValuesDoNotMatchError(err)).To(BeTrue())
		})
	})

	Describe("parseGetPathCommandOutput", func() {
		It("maps multiple parameter values from a single batched command output", func() {
			requests := []getPathRequest{
				newGetPathRequest(QoSTrustModePath, interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest(QoSPFCPath, interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest(ToSPath, interfaceNameFilter(testNetworkInterface)),
			}
			output := makeBatchedGetOutput(
				getOutputUpdate{displayPath: createTrustModePath(testNetworkInterface), valueKeyPath: QoSTrustModePath, value: "dscp"},
				getOutputUpdate{displayPath: createPFCPath(testNetworkInterface), valueKeyPath: QoSPFCPath, value: "00001000"},
				getOutputUpdate{displayPath: createToSPath(testNetworkInterface), valueKeyPath: ToSPath, value: "96"},
			)

			values, err := parseGetPathCommandOutput(output, requests)
			Expect(err).ToNot(HaveOccurred())
			Expect(values).To(Equal(map[string]string{
				QoSTrustModePath: "dscp",
				QoSPFCPath:       "00001000",
				ToSPath:          "96",
			}))
		})

		It("matches batched output by returned path when updates are out of request order", func() {
			requests := []getPathRequest{
				newGetPathRequest(QoSTrustModePath, interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest(QoSPFCPath, interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest(ToSPath, interfaceNameFilter(testNetworkInterface)),
			}
			output := makeBatchedGetOutput(
				getOutputUpdate{displayPath: createToSPath(testNetworkInterface), valueKeyPath: ToSPath, value: "96"},
				getOutputUpdate{displayPath: createPFCPath(testNetworkInterface), valueKeyPath: QoSPFCPath, value: "00001000"},
				getOutputUpdate{displayPath: createTrustModePath(testNetworkInterface), valueKeyPath: QoSTrustModePath, value: "dscp"},
			)

			values, err := parseGetPathCommandOutput(output, requests)
			Expect(err).ToNot(HaveOccurred())
			Expect(values).To(Equal(map[string]string{
				QoSTrustModePath: "dscp",
				QoSPFCPath:       "00001000",
				ToSPath:          "96",
			}))
		})

		It("parses a raw batched dmsc output with many parameter values", func() {
			requests := []getPathRequest{
				newGetPathRequest("/interfaces/interface/nvidia/qos/config/trust-mode", interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest("/interfaces/interface/nvidia/qos/config/pfc", interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest("/interfaces/interface/nvidia/roce/config/cc-per-plane", interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest("/interfaces/interface/nvidia/roce/config/adaptive-retransmission", interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest("/interfaces/interface/nvidia/roce/config/tx-window", interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest("/interfaces/interface/nvidia/roce/config/slow-restart", interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest("/interfaces/interface/nvidia/roce/config/adaptive-routing-force", interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest("/interfaces/interface/nvidia/cc/config/priority/rp_enabled", mergeFilterRules(interfaceNameFilter(testNetworkInterface), priorityIdFilter(0))),
				newGetPathRequest("/interfaces/interface/nvidia/cc/config/priority/rp_enabled", mergeFilterRules(interfaceNameFilter(testNetworkInterface), priorityIdFilter(1))),
				newGetPathRequest("/interfaces/interface/nvidia/cc/config/priority/np_enabled", mergeFilterRules(interfaceNameFilter(testNetworkInterface), priorityIdFilter(0))),
				newGetPathRequest("/interfaces/interface/nvidia/cc/config/priority/np_enabled", mergeFilterRules(interfaceNameFilter(testNetworkInterface), priorityIdFilter(1))),
				newGetPathRequest("/interfaces/interface/nvidia/cc/slot[id=0]/config/enabled", interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest("/interfaces/interface/nvidia/cc/slot[id=0]/config/counter_enable", interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest("/interfaces/interface/nvidia/cc/slot[id=15]/config/enabled", interfaceNameFilter(testNetworkInterface)),
				newGetPathRequest("/interfaces/interface/ethernet/nvidia/config/inter-packet-gap", interfaceNameFilter(testNetworkInterface)),
			}
			rawOutput := []byte(`[
  {
    "source": "dmsc",
    "timestamp": 1783591000,
    "time": "2026-07-09T11:16:40Z",
    "updates": [
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/qos/config/trust-mode",
        "values": {
          "interfaces/interface/nvidia/qos/config/trust-mode": "dscp"
        }
      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/qos/config/pfc",
        "values": {
          "interfaces/interface/nvidia/qos/config/pfc": "00001000"
        }
      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/roce/config/cc-per-plane",
        "values": {
          "interfaces/interface/nvidia/roce/config/cc-per-plane": "true"
        }
      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/roce/config/adaptive-retransmission",
        "values": {
          "interfaces/interface/nvidia/roce/config/adaptive-retransmission": "true"
        }
      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/roce/config/tx-window",
        "values": {
          "interfaces/interface/nvidia/roce/config/tx-window": "64"
        }
      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/roce/config/slow-restart",
        "values": {
          "interfaces/interface/nvidia/roce/config/slow-restart": "false"
        }
      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/roce/config/adaptive-routing-force",
        "values": {
          "interfaces/interface/nvidia/roce/config/adaptive-routing-force": "true"
        }
      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/cc/config/priority[id=0]/rp_enabled",
        "values": {
          "interfaces/interface/nvidia/cc/config/priority/rp_enabled": "true"
        }
      },
	      {
	        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/cc/config/priority[id=1]/rp_enabled",
	        "values": {
	          "interfaces/interface/nvidia/cc/config/priority/rp_enabled": "true"
	        }
	      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/cc/config/priority[id=0]/np_enabled",
        "values": {
          "interfaces/interface/nvidia/cc/config/priority/np_enabled": "true"
        }
      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/cc/config/priority[id=1]/np_enabled",
        "values": {
          "interfaces/interface/nvidia/cc/config/priority/np_enabled": "true"
        }
      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/cc/slot[id=0]/config/enabled",
        "values": {
          "interfaces/interface/nvidia/cc/slot/config/enabled": "true"
        }
      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/cc/slot[id=0]/config/counter_enable",
        "values": {
          "interfaces/interface/nvidia/cc/slot/config/counter_enable": "true"
        }
      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/nvidia/cc/slot[id=15]/config/enabled",
        "values": {
          "interfaces/interface/nvidia/cc/slot/config/enabled": "false"
        }
      },
      {
        "Path": "/interfaces/interface[name=enp3s0f0np0]/ethernet/nvidia/config/inter-packet-gap",
        "values": {
          "interfaces/interface/ethernet/nvidia/config/inter-packet-gap": "12"
        }
      }
    ]
  }
]`)

			values, err := parseGetPathCommandOutput(rawOutput, requests)
			Expect(err).ToNot(HaveOccurred())
			Expect(values).To(Equal(map[string]string{
				"/interfaces/interface/nvidia/qos/config/trust-mode":               "dscp",
				"/interfaces/interface/nvidia/qos/config/pfc":                      "00001000",
				"/interfaces/interface/nvidia/roce/config/cc-per-plane":            "true",
				"/interfaces/interface/nvidia/roce/config/adaptive-retransmission": "true",
				"/interfaces/interface/nvidia/roce/config/tx-window":               "64",
				"/interfaces/interface/nvidia/roce/config/slow-restart":            "false",
				"/interfaces/interface/nvidia/roce/config/adaptive-routing-force":  "true",
				"/interfaces/interface/nvidia/cc/config/priority/rp_enabled":       "true",
				"/interfaces/interface/nvidia/cc/config/priority/np_enabled":       "true",
				"/interfaces/interface/nvidia/cc/slot[id=0]/config/enabled":        "true",
				"/interfaces/interface/nvidia/cc/slot[id=0]/config/counter_enable": "true",
				"/interfaces/interface/nvidia/cc/slot[id=15]/config/enabled":       "false",
				"/interfaces/interface/ethernet/nvidia/config/inter-packet-gap":    "12",
			}))
		})

		It("parses repeated paths from multiple batched result objects", func() {
			requests := []getPathRequest{
				newGetPathRequest("/interfaces/interface/nvidia/roce/config/tx-window", interfaceNameFilter("eth_p0_r0")),
				newGetPathRequest("/interfaces/interface/nvidia/roce/config/slow-restart", interfaceNameFilter("eth_p0_r0")),
				newGetPathRequest("/interfaces/interface/nvidia/roce/config/tx-window", interfaceNameFilter("eth_p1_r0")),
				newGetPathRequest("/interfaces/interface/nvidia/roce/config/slow-restart", interfaceNameFilter("eth_p1_r0")),
			}
			rawOutput := []byte(`[
  {
    "source": "localhost:9339",
    "timestamp": 1783591180747365539,
    "time": "2026-07-09T09:59:40.747365539Z",
    "target": "0000:c9:00.1",
    "updates": [
      {
        "Path": "interfaces/interface[name=eth_p0_r0]/nvidia/roce/config/tx-window",
        "values": {
          "interfaces/interface/nvidia/roce/config/tx-window": "false"
        }
      }
    ]
  },
  {
    "source": "localhost:9339",
    "timestamp": 1783591180873488751,
    "time": "2026-07-09T09:59:40.873488751Z",
    "target": "0000:c9:00.1",
    "updates": [
      {
        "Path": "interfaces/interface[name=eth_p0_r0]/nvidia/roce/config/slow-restart",
        "values": {
          "interfaces/interface/nvidia/roce/config/slow-restart": "true"
        }
      }
    ]
  },
  {
    "source": "localhost:9339",
    "timestamp": 1783591180975087880,
    "time": "2026-07-09T09:59:40.97508788Z",
    "target": "0000:c9:00.1",
    "updates": [
      {
        "Path": "interfaces/interface[name=eth_p1_r0]/nvidia/roce/config/tx-window",
        "values": {
          "interfaces/interface/nvidia/roce/config/tx-window": "false"
        }
      }
    ]
  },
  {
    "source": "localhost:9339",
    "timestamp": 1783591181073981538,
    "time": "2026-07-09T09:59:41.073981538Z",
    "target": "0000:c9:00.1",
    "updates": [
      {
        "Path": "interfaces/interface[name=eth_p1_r0]/nvidia/roce/config/slow-restart",
        "values": {
          "interfaces/interface/nvidia/roce/config/slow-restart": "true"
        }
      }
    ]
  },
  {
    "source": "localhost:9339",
    "timestamp": 1783591181173245802,
    "time": "2026-07-09T09:59:41.173245802Z",
    "target": "0000:c9:00.1",
    "updates": [
      {
        "Path": "interfaces/interface[name=eth_p0_r0]/nvidia/roce/config/tx-window",
        "values": {
          "interfaces/interface/nvidia/roce/config/tx-window": "false"
        }
      }
    ]
  },
  {
    "source": "localhost:9339",
    "timestamp": 1783591181271181677,
    "time": "2026-07-09T09:59:41.271181677Z",
    "target": "0000:c9:00.1",
    "updates": [
      {
        "Path": "interfaces/interface[name=eth_p0_r0]/nvidia/roce/config/slow-restart",
        "values": {
          "interfaces/interface/nvidia/roce/config/slow-restart": "true"
        }
      }
    ]
  },
  {
    "source": "localhost:9339",
    "timestamp": 1783591181369848140,
    "time": "2026-07-09T09:59:41.36984814Z",
    "target": "0000:c9:00.1",
    "updates": [
      {
        "Path": "interfaces/interface[name=eth_p1_r0]/nvidia/roce/config/tx-window",
        "values": {
          "interfaces/interface/nvidia/roce/config/tx-window": "false"
        }
      }
    ]
  },
  {
    "source": "localhost:9339",
    "timestamp": 1783591181469246758,
    "time": "2026-07-09T09:59:41.469246758Z",
    "target": "0000:c9:00.1",
    "updates": [
      {
        "Path": "interfaces/interface[name=eth_p1_r0]/nvidia/roce/config/slow-restart",
        "values": {
          "interfaces/interface/nvidia/roce/config/slow-restart": "true"
        }
      }
    ]
  }
]`)

			values, err := parseGetPathCommandOutput(rawOutput, requests)
			Expect(err).ToNot(HaveOccurred())
			Expect(values).To(Equal(map[string]string{
				"/interfaces/interface/nvidia/roce/config/tx-window":    "false",
				"/interfaces/interface/nvidia/roce/config/slow-restart": "true",
			}))
		})

		It("returns a values-do-not-match error for conflicting values of one parameter", func() {
			paramPath := "/interfaces/interface/nvidia/roce/config/tx-window"
			requests := []getPathRequest{
				newGetPathRequest(paramPath, interfaceNameFilter("eth_p0_r0")),
				newGetPathRequest(paramPath, interfaceNameFilter("eth_p1_r0")),
			}
			output := makeBatchedGetOutput(
				getOutputUpdate{displayPath: requests[0].queryPath, valueKeyPath: paramPath, value: "false"},
				getOutputUpdate{displayPath: requests[1].queryPath, valueKeyPath: paramPath, value: "true"},
			)

			_, err := parseGetPathCommandOutput(output, requests)

			Expect(err).To(HaveOccurred())
			Expect(types.IsValuesDoNotMatchError(err)).To(BeTrue())
		})

		It("returns error when repeated paths have conflicting values", func() {
			requests := []getPathRequest{
				newGetPathRequest(QoSTrustModePath, interfaceNameFilter(testNetworkInterface)),
			}
			output := makeBatchedGetOutput(
				getOutputUpdate{displayPath: createTrustModePath(testNetworkInterface), valueKeyPath: QoSTrustModePath, value: "dscp"},
				getOutputUpdate{displayPath: createTrustModePath(testNetworkInterface), valueKeyPath: QoSTrustModePath, value: "pfc"},
			)

			_, err := parseGetPathCommandOutput(output, requests)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("conflicting value found for path /interfaces/interface[name=enp3s0f0np0]/nvidia/qos/config/trust-mode"))
		})
	})

	Describe("GetParameters", func() {
		collectPathPayloads := func(args []string) []string {
			var payloads []string
			for i, arg := range args {
				if arg == "--path" && i+1 < len(args) {
					payloads = append(payloads, args[i+1])
				}
			}
			return payloads
		}

		It("returns value for simple path", func() {
			param := types.ConfigurationParameter{DMSPath: "/nvidia/mode/config/mode"}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					paths := collectPathPayloads(args)
					Expect(paths).To(Equal([]string{param.DMSPath}))
					return createFakeCmd(makeGetOutput(param.DMSPath, param.DMSPath, "NIC"), nil)
				},
			}

			vals, err := client.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
			Expect(vals[param.DMSPath]).To(Equal("NIC"))
		})

		It("returns value for interface path in a batched command", func() {
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/qos/config/trust-mode"}

			filtered := createTrustModePath(testNetworkInterface)
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					paths := collectPathPayloads(args)
					Expect(paths).To(Equal([]string{filtered}))
					return createFakeCmd(makeGetOutput(filtered, param.DMSPath, "dscp"), nil)
				},
			}

			vals, err := client.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
			Expect(vals[param.DMSPath]).To(Equal("dscp"))
		})

		It("fails on mismatch across interfaces of the same device", func() {
			const secondNetworkInterface = "enp3s0f1np1"
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/qos/config/trust-mode"}
			multiPortClient := &dmsClient{
				device: v1alpha1.NicDeviceStatus{
					SerialNumber: "test-serial",
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: testPCI, NetworkInterface: testNetworkInterface},
						{PCI: "0000:00:00.1", NetworkInterface: secondNetworkInterface},
					},
				},
				targetPCI:     testPCI,
				bindAddress:   ":9339",
				authParams:    []string{"--insecure"},
				execInterface: fakeExec,
			}
			expectedPaths := []string{
				createTrustModePath(testNetworkInterface),
				createTrustModePath(secondNetworkInterface),
			}

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					paths := collectPathPayloads(args)
					Expect(paths).To(Equal([]string{expectedPaths[0]}))
					return createFakeCmd(makeBatchedGetOutput(
						getOutputUpdate{displayPath: expectedPaths[0], valueKeyPath: param.DMSPath, value: "dscp"},
					), nil)
				},
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					paths := collectPathPayloads(args)
					Expect(paths).To(Equal([]string{expectedPaths[1]}))
					return createFakeCmd(makeBatchedGetOutput(
						getOutputUpdate{displayPath: expectedPaths[1], valueKeyPath: param.DMSPath, value: "pfc"},
					), nil)
				},
			}

			_, err := multiPortClient.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).To(HaveOccurred())
			Expect(types.IsValuesDoNotMatchError(err)).To(BeTrue())
		})

		It("returns value when per-interface batches agree across device ports", func() {
			const secondNetworkInterface = "enp3s0f1np1"
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/qos/config/trust-mode"}
			multiPortClient := &dmsClient{
				device: v1alpha1.NicDeviceStatus{
					SerialNumber: "test-serial",
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: testPCI, NetworkInterface: testNetworkInterface},
						{PCI: "0000:00:00.1", NetworkInterface: secondNetworkInterface},
					},
				},
				targetPCI:     testPCI,
				bindAddress:   ":9339",
				authParams:    []string{"--insecure"},
				execInterface: fakeExec,
			}
			expectedPaths := []string{
				createTrustModePath(testNetworkInterface),
				createTrustModePath(secondNetworkInterface),
			}

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					paths := collectPathPayloads(args)
					Expect(paths).To(Equal([]string{expectedPaths[0]}))
					return createFakeCmd(makeBatchedGetOutput(
						getOutputUpdate{displayPath: expectedPaths[0], valueKeyPath: param.DMSPath, value: "dscp"},
					), nil)
				},
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					paths := collectPathPayloads(args)
					Expect(paths).To(Equal([]string{expectedPaths[1]}))
					return createFakeCmd(makeBatchedGetOutput(
						getOutputUpdate{displayPath: expectedPaths[1], valueKeyPath: param.DMSPath, value: "dscp"},
					), nil)
				},
			}

			vals, err := multiPortClient.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
			Expect(vals[param.DMSPath]).To(Equal("dscp"))
		})

		It("fails on mismatch when the first interface value is empty", func() {
			const secondNetworkInterface = "enp3s0f1np1"
			param := types.ConfigurationParameter{
				Name:    "trust-mode",
				DMSPath: "/interfaces/interface/nvidia/qos/config/trust-mode",
			}
			multiPortClient := &dmsClient{
				device: v1alpha1.NicDeviceStatus{
					SerialNumber: "test-serial",
					Ports: []v1alpha1.NicDevicePortSpec{
						{PCI: testPCI, NetworkInterface: testNetworkInterface},
						{PCI: "0000:00:00.1", NetworkInterface: secondNetworkInterface},
					},
				},
				targetPCI:     testPCI,
				bindAddress:   ":9339",
				authParams:    []string{"--insecure"},
				execInterface: fakeExec,
			}
			firstPath := createTrustModePath(testNetworkInterface)
			secondPath := createTrustModePath(secondNetworkInterface)
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd(makeGetOutput(firstPath, param.DMSPath, ""), nil)
				},
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd(makeGetOutput(secondPath, param.DMSPath, "dscp"), nil)
				},
			}

			_, err := multiPortClient.GetParameters([]types.ConfigurationParameter{param})

			Expect(err).To(HaveOccurred())
			Expect(types.IsValuesDoNotMatchError(err)).To(BeTrue())
		})

		It("keeps configured slot and param filters as distinct result keys", func() {
			params := []types.ConfigurationParameter{
				{Name: "param-0", DMSPath: "/interfaces/interface/nvidia/cc/slot[id=0]/param[id=0]/config/value"},
				{Name: "param-11", DMSPath: "/interfaces/interface/nvidia/cc/slot[id=0]/param[id=11]/config/value"},
			}
			queryPaths := []string{
				injectFilterRules(params[0].DMSPath, interfaceNameFilter(testNetworkInterface)),
				injectFilterRules(params[1].DMSPath, interfaceNameFilter(testNetworkInterface)),
			}
			valuePath := "/interfaces/interface/nvidia/cc/slot/param/config/value"
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd(makeBatchedGetOutput(
						getOutputUpdate{displayPath: queryPaths[0], valueKeyPath: valuePath, value: "1"},
						getOutputUpdate{displayPath: queryPaths[1], valueKeyPath: valuePath, value: "2"},
					), nil)
				},
			}

			values, err := client.GetParameters(params)

			Expect(err).ToNot(HaveOccurred())
			Expect(values).To(Equal(map[string]string{
				params[0].DMSPath: "1",
				params[1].DMSPath: "2",
			}))
		})

		It("fails on mismatch across priorities", func() {
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/cc/config/priority/np_enabled"}
			updates := make([]getOutputUpdate, 0, 8)
			for id := 0; id < 8; id++ {
				value := "1"
				if id == 4 {
					value = "0"
				}
				updates = append(updates, getOutputUpdate{
					displayPath:  fmt.Sprintf("/interfaces/interface[name=%s]/nvidia/cc/config/priority[id=%d]/np_enabled", testNetworkInterface, id),
					valueKeyPath: param.DMSPath,
					value:        value,
				})
			}

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					paths := collectPathPayloads(args)
					Expect(paths).To(HaveLen(8))
					return createFakeCmd(makeBatchedGetOutput(updates...), nil)
				},
			}

			_, err := client.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).To(HaveOccurred())
			Expect(types.IsValuesDoNotMatchError(err)).To(BeTrue())
		})

		It("returns value for interface+priority path across all IDs", func() {
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/cc/config/priority/np_enabled"}
			expectedFiltered := make([]string, 0, 8)
			for id := 0; id < 8; id++ {
				expectedFiltered = append(expectedFiltered, fmt.Sprintf("/interfaces/interface[name=%s]/nvidia/cc/config/priority[id=%d]/np_enabled", testNetworkInterface, id))
			}

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					paths := collectPathPayloads(args)
					Expect(paths).To(Equal(expectedFiltered))
					updates := make([]getOutputUpdate, 0, len(expectedFiltered))
					for _, path := range expectedFiltered {
						updates = append(updates, getOutputUpdate{displayPath: path, valueKeyPath: param.DMSPath, value: "1"})
					}
					return createFakeCmd(makeBatchedGetOutput(updates...), nil)
				},
			}

			vals, err := client.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
			Expect(vals[param.DMSPath]).To(Equal("1"))
		})

		It("batches get paths by concrete interface", func() {
			params := []types.ConfigurationParameter{
				{DMSPath: "/nvidia/mode/config/mode"},
				{DMSPath: "/interfaces/interface/nvidia/qos/config/trust-mode"},
				{DMSPath: "/interfaces/interface/nvidia/cc/config/priority/np_enabled"},
			}

			expectedInterfacePaths := []string{createTrustModePath(testNetworkInterface)}
			for id := 0; id < 8; id++ {
				expectedInterfacePaths = append(expectedInterfacePaths, fmt.Sprintf("/interfaces/interface[name=%s]/nvidia/cc/config/priority[id=%d]/np_enabled", testNetworkInterface, id))
			}

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					paths := collectPathPayloads(args)
					Expect(paths).To(Equal([]string{"/nvidia/mode/config/mode"}))

					updates := []getOutputUpdate{
						{displayPath: params[0].DMSPath, valueKeyPath: params[0].DMSPath, value: "NIC"},
					}
					return createFakeCmd(makeBatchedGetOutput(updates...), nil)
				},
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					paths := collectPathPayloads(args)
					Expect(paths).To(Equal(expectedInterfacePaths))

					updates := make([]getOutputUpdate, 0, len(expectedInterfacePaths))
					updates = append(updates, getOutputUpdate{
						displayPath:  expectedInterfacePaths[0],
						valueKeyPath: params[1].DMSPath,
						value:        "dscp",
					})
					for _, path := range expectedInterfacePaths[1:] {
						updates = append(updates, getOutputUpdate{displayPath: path, valueKeyPath: params[2].DMSPath, value: "1"})
					}
					return createFakeCmd(makeBatchedGetOutput(updates...), nil)
				},
			}

			vals, err := client.GetParameters(params)
			Expect(err).ToNot(HaveOccurred())
			Expect(vals[params[0].DMSPath]).To(Equal("NIC"))
			Expect(vals[params[1].DMSPath]).To(Equal("dscp"))
			Expect(vals[params[2].DMSPath]).To(Equal("1"))
		})

		It("returns error when batched output does not include matching Path values", func() {
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/cc/config/priority/np_enabled"}

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					paths := collectPathPayloads(args)
					Expect(paths).To(HaveLen(8))
					return createFakeCmd(makeBatchedGetOutput(getOutputUpdate{displayPath: param.DMSPath, valueKeyPath: param.DMSPath, value: "1"}), nil)
				},
			}

			_, err := client.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no update found for path /interfaces/interface[name=enp3s0f0np0]/nvidia/cc/config/priority[id=0]/np_enabled"))
		})

		It("returns a descriptive error when batched output is missing a requested value", func() {
			param := types.ConfigurationParameter{DMSPath: "/nvidia/mode/config/mode"}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					output := makeBatchedGetOutput(getOutputUpdate{displayPath: param.DMSPath, valueKeyPath: "/some/other/path", value: "NIC"})
					return createFakeCmd(output, nil)
				},
			}

			_, err := client.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("value not found for path /nvidia/mode/config/mode"))
		})

		It("returns a descriptive error when batched get command fails", func() {
			param := types.ConfigurationParameter{DMSPath: "/nvidia/mode/config/mode"}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd([]byte("boom"), errors.New("command failed"))
				},
			}

			_, err := client.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to run get path command"))
		})
	})

	Describe("SetParameters", func() {
		// collectUpdatePayloads extracts all --update values from args
		collectUpdatePayloads := func(args []string) []string {
			var payloads []string
			for i, arg := range args {
				if arg == "--update" && i+1 < len(args) {
					payloads = append(payloads, args[i+1])
				}
			}
			return payloads
		}

		It("sets value for simple path in single command", func() {
			param := types.ConfigurationParameter{DMSPath: "/nvidia/mode/config/mode", Value: "NIC", ValueType: ValueTypeString}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					payloads := collectUpdatePayloads(args)
					Expect(payloads).To(HaveLen(1))
					Expect(payloads[0]).To(Equal("/nvidia/mode/config/mode:::string:::NIC"))
					return createFakeCmd(nil, nil)
				},
			}
			err := client.SetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
		})

		It("sets value per interface in single command", func() {
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/qos/config/trust-mode", Value: "dscp", ValueType: ValueTypeString}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					payloads := collectUpdatePayloads(args)
					Expect(payloads).To(HaveLen(1))
					Expect(payloads[0]).To(Equal(createTrustModeSetPath(testNetworkInterface, "dscp")))
					return createFakeCmd(nil, nil)
				},
			}
			err := client.SetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
		})

		It("sets value per interface+priority across all IDs in single command", func() {
			param := types.ConfigurationParameter{DMSPath: "/interfaces/interface/nvidia/cc/config/priority/np_enabled", Value: "1", ValueType: ValueTypeBool}
			expectedPayloads := make([]string, 0, 8)
			for id := 0; id < 8; id++ {
				expectedPayloads = append(expectedPayloads, fmt.Sprintf("/interfaces/interface[name=%s]/nvidia/cc/config/priority[id=%d]/np_enabled:::bool:::1", testNetworkInterface, id))
			}

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					payloads := collectUpdatePayloads(args)
					Expect(payloads).To(HaveLen(8))
					for i, expected := range expectedPayloads {
						Expect(payloads[i]).To(Equal(expected))
					}
					return createFakeCmd(nil, nil)
				},
			}

			err := client.SetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
		})

		It("batches mixed params into single command", func() {
			params := []types.ConfigurationParameter{
				{DMSPath: "/nvidia/mode/config/mode", Value: "NIC", ValueType: ValueTypeString},
				{DMSPath: "/interfaces/interface/nvidia/qos/config/trust-mode", Value: "dscp", ValueType: ValueTypeString},
			}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					verifyTargetFlag(args)
					payloads := collectUpdatePayloads(args)
					Expect(payloads).To(HaveLen(2))
					Expect(payloads[0]).To(Equal("/nvidia/mode/config/mode:::string:::NIC"))
					Expect(payloads[1]).To(Equal(createTrustModeSetPath(testNetworkInterface, "dscp")))
					return createFakeCmd(nil, nil)
				},
			}
			err := client.SetParameters(params)
			Expect(err).ToNot(HaveOccurred())
		})

		It("returns nil for empty params without executing command", func() {
			err := client.SetParameters([]types.ConfigurationParameter{})
			Expect(err).ToNot(HaveOccurred())
			Expect(fakeExec.CommandCalls).To(Equal(0))
		})

		It("returns error when batch command fails", func() {
			param := types.ConfigurationParameter{DMSPath: "/nvidia/mode/config/mode", Value: "NIC", ValueType: ValueTypeString}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd(nil, errors.New("command failed"))
				},
			}
			err := client.SetParameters([]types.ConfigurationParameter{param})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to set parameters"))
		})

		It("ignores error when all params have IgnoreError set", func() {
			params := []types.ConfigurationParameter{
				{DMSPath: "/nvidia/mode/config/mode", Value: "NIC", ValueType: ValueTypeString, IgnoreError: true},
				{DMSPath: "/interfaces/interface/nvidia/qos/config/trust-mode", Value: "dscp", ValueType: ValueTypeString, IgnoreError: true},
			}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd(nil, errors.New("command failed"))
				},
			}
			err := client.SetParameters(params)
			Expect(err).ToNot(HaveOccurred())
		})

		It("returns error when command fails and some params do not have IgnoreError", func() {
			params := []types.ConfigurationParameter{
				{DMSPath: "/nvidia/mode/config/mode", Value: "NIC", ValueType: ValueTypeString, IgnoreError: true},
				{DMSPath: "/interfaces/interface/nvidia/qos/config/trust-mode", Value: "dscp", ValueType: ValueTypeString, IgnoreError: false},
			}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					return createFakeCmd(nil, errors.New("command failed"))
				},
			}
			err := client.SetParameters(params)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to set parameters"))
		})
	})

	Describe("HwplbFirstPortOnly", func() {
		const (
			secondNetworkInterface = "enp3s0f1np1"
			secondPCI              = "0000:00:00.1"
		)

		var multiPortClient *dmsClient

		BeforeEach(func() {
			multiPortDevice := v1alpha1.NicDeviceStatus{
				SerialNumber: "test-serial",
				Ports: []v1alpha1.NicDevicePortSpec{
					{PCI: testPCI, NetworkInterface: testNetworkInterface},
					{PCI: secondPCI, NetworkInterface: secondNetworkInterface},
				},
			}
			multiPortClient = &dmsClient{
				device:        multiPortDevice,
				targetPCI:     testPCI,
				bindAddress:   ":9339",
				authParams:    []string{"--insecure"},
				execInterface: fakeExec,
			}
		})

		collectUpdatePayloads := func(args []string) []string {
			var payloads []string
			for i, arg := range args {
				if arg == "--update" && i+1 < len(args) {
					payloads = append(payloads, args[i+1])
				}
			}
			return payloads
		}

		It("should iterate only first port when HwplbFirstPortOnly is true", func() {
			param := types.ConfigurationParameter{
				DMSPath:            "/interfaces/interface/nvidia/cc/config/priority/rp_enabled",
				Value:              "1",
				ValueType:          ValueTypeBool,
				HwplbFirstPortOnly: true,
			}

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					payloads := collectUpdatePayloads(args)
					// 8 priorities × 1 port = 8 updates (not 16)
					Expect(payloads).To(HaveLen(8))
					for _, p := range payloads {
						Expect(p).To(ContainSubstring(testNetworkInterface))
						Expect(p).NotTo(ContainSubstring(secondNetworkInterface))
					}
					return createFakeCmd(nil, nil)
				},
			}

			err := multiPortClient.SetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
		})

		It("should iterate all ports when HwplbFirstPortOnly is false", func() {
			param := types.ConfigurationParameter{
				DMSPath:            "/interfaces/interface/nvidia/cc/config/priority/rp_enabled",
				Value:              "1",
				ValueType:          ValueTypeBool,
				HwplbFirstPortOnly: false,
			}

			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					payloads := collectUpdatePayloads(args)
					// 8 priorities × 2 ports = 16 updates
					Expect(payloads).To(HaveLen(16))
					return createFakeCmd(nil, nil)
				},
			}

			err := multiPortClient.SetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
		})

		It("should query only first port when HwplbFirstPortOnly is true", func() {
			param := types.ConfigurationParameter{
				DMSPath:            "/interfaces/interface/nvidia/cc/config/priority/rp_enabled",
				HwplbFirstPortOnly: true,
			}

			expectedPaths := make([]string, 0, 8)
			for id := 0; id < 8; id++ {
				expectedPaths = append(expectedPaths, fmt.Sprintf("/interfaces/interface[name=%s]/nvidia/cc/config/priority[id=%d]/rp_enabled", testNetworkInterface, id))
			}
			fakeExec.CommandScript = []execTesting.FakeCommandAction{
				func(cmd string, args ...string) exec.Cmd {
					paths := make([]string, 0, len(expectedPaths))
					for i, arg := range args {
						if arg == "--path" && i+1 < len(args) {
							paths = append(paths, args[i+1])
						}
					}
					Expect(paths).To(Equal(expectedPaths))
					for _, path := range paths {
						Expect(path).NotTo(ContainSubstring(secondNetworkInterface))
					}
					updates := make([]getOutputUpdate, 0, len(expectedPaths))
					for _, path := range expectedPaths {
						updates = append(updates, getOutputUpdate{displayPath: path, valueKeyPath: param.DMSPath, value: "1"})
					}
					return createFakeCmd(makeBatchedGetOutput(updates...), nil)
				},
			}

			vals, err := multiPortClient.GetParameters([]types.ConfigurationParameter{param})
			Expect(err).ToNot(HaveOccurred())
			Expect(vals[param.DMSPath]).To(Equal("1"))
		})
	})

	Describe("InstallBFB", func() {
		const (
			testBFBVersion = "24.35.1000"
			testBFBPath    = "/path/to/firmware.bfb"
		)

		Context("on BlueField device", func() {
			BeforeEach(func() {
				device.Type = "a2d6" // BlueField2 device ID
				client.device = device
			})

			It("should install and activate BFB successfully", func() {
				fakeInstallCmd := createFakeCmd([]byte("BFB install successful"), nil)
				fakeActivateCmd := createFakeCmd([]byte("BFB activate successful"), nil)

				callCount := 0
				cmdAction := func(cmd string, args ...string) exec.Cmd {
					callCount++
					Expect(cmd).To(Equal(dmsClientPath))
					verifyTargetFlag(args)

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

				err := client.InstallBFB(context.Background(), testBFBVersion, testBFBPath)
				Expect(err).NotTo(HaveOccurred())
			})

			It("should return error when install command fails", func() {
				fakeInstallCmd := createFakeCmd(nil, errors.New("install failed"))

				cmdAction := func(cmd string, args ...string) exec.Cmd { return fakeInstallCmd }
				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction}

				err := client.InstallBFB(context.Background(), testBFBVersion, testBFBPath)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("install failed"))
			})

			It("should return error when activate command fails", func() {
				fakeInstallCmd := createFakeCmd([]byte("BFB install successful"), nil)
				fakeActivateCmd := createFakeCmd(nil, errors.New("activate failed"))

				cmdAction := func(cmd string, args ...string) exec.Cmd {
					if len(args) >= 5 && args[4] == "install" {
						return fakeInstallCmd
					}
					return fakeActivateCmd
				}

				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction}

				err := client.InstallBFB(context.Background(), testBFBVersion, testBFBPath)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("activate failed"))
			})
		})

		Context("on non-BlueField device", func() {
			BeforeEach(func() {
				device.Type = "cx6"
				client.device = device
			})

			It("should return error for non-BlueField device", func() {
				err := client.InstallBFB(context.Background(), testBFBVersion, testBFBPath)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("cannot install BFB file on non-BlueField device"))
			})
		})
	})
})
