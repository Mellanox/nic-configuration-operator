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
	"errors"
	"net"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

var _ = Describe("DMSManager", func() {
	var (
		manager     *dmsManager
		fakeExec    *execTesting.FakeExec
		testDevices []v1alpha1.NicDeviceStatus
	)

	BeforeEach(func() {
		fakeExec = &execTesting.FakeExec{}
		manager = &dmsManager{
			clients:       make(map[string]*dmsClient),
			serverPort:    basePort,
			execInterface: fakeExec,
		}

		testDevices = []v1alpha1.NicDeviceStatus{
			{
				SerialNumber: "test-serial-1",
				Ports: []v1alpha1.NicDevicePortSpec{
					{
						PCI:              "0000:01:00.0",
						NetworkInterface: "enp1s0f0np0",
					},
				},
			},
			{
				SerialNumber: "test-serial-2",
				Ports: []v1alpha1.NicDevicePortSpec{
					{
						PCI:              "0000:02:00.0",
						NetworkInterface: "enp2s0f0np0",
					},
				},
			},
		}
	})

	Describe("StartDMSServer", func() {
		Context("when DMS server starts successfully", func() {
			var stopChan chan struct{}

			BeforeEach(func() {
				stopChan = make(chan struct{})

				cmdAction := func(cmd string, args ...string) exec.Cmd {
					// Verify it's calling the DMS server binary
					Expect(cmd).To(Equal(dmsServerPath))

					// Verify comma-separated PCI addresses in -target_pci arg
					for i, arg := range args {
						if arg == "-target_pci" {
							Expect(args[i+1]).To(Equal("0000:01:00.0,0000:02:00.0"))
						}
					}

					return &execTesting.FakeCmd{
						OutputScript: []execTesting.FakeAction{
							func() ([]byte, []byte, error) {
								<-stopChan
								return []byte("DMS server stopped"), nil, nil
							},
						},
					}
				}

				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction}
			})

			AfterEach(func() {
				close(stopChan)
			})

			It("should start a single DMS server and create clients for all devices", func() {
				err := manager.StartDMSServer(testDevices)
				Expect(err).NotTo(HaveOccurred())

				// Verify clients were created for both devices
				Expect(manager.clients).To(HaveLen(2))
				Expect(manager.clients).To(HaveKey("test-serial-1"))
				Expect(manager.clients).To(HaveKey("test-serial-2"))

				// Verify server is running
				Expect(manager.running.Load()).To(BeTrue())

				// Verify clients have correct target PCI
				Expect(manager.clients["test-serial-1"].targetPCI).To(Equal("0000:01:00.0"))
				Expect(manager.clients["test-serial-2"].targetPCI).To(Equal("0000:02:00.0"))
			})

			It("should not start another server if already running", func() {
				err := manager.StartDMSServer(testDevices)
				Expect(err).NotTo(HaveOccurred())

				// Try to start again — should be a no-op
				err = manager.StartDMSServer(testDevices)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("when DMS server fails to start", func() {
			BeforeEach(func() {
				cmdAction := func(cmd string, args ...string) exec.Cmd {
					return &execTesting.FakeCmd{
						OutputScript: []execTesting.FakeAction{
							func() ([]byte, []byte, error) {
								return nil, nil, errors.New("failed to start DMS server")
							},
						},
					}
				}

				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction}
			})

			It("should return an error", func() {
				err := manager.StartDMSServer(testDevices)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to start DMS server"))
			})

			It("should not have a running server after failure", func() {
				err := manager.StartDMSServer(testDevices)
				Expect(err).To(HaveOccurred())
				Expect(manager.running.Load()).To(BeFalse())
			})
		})

		Context("when no devices are provided", func() {
			It("should return nil without starting a server", func() {
				err := manager.StartDMSServer([]v1alpha1.NicDeviceStatus{})
				Expect(err).NotTo(HaveOccurred())
				Expect(manager.running.Load()).To(BeFalse())
			})
		})

		Context("when port is already in use", func() {
			var listener net.Listener
			var stopChan chan struct{}

			BeforeEach(func() {
				stopChan = make(chan struct{})

				// Occupy the port that would be used
				var err error
				listener, err = net.Listen("tcp", "localhost:9339")
				Expect(err).NotTo(HaveOccurred())

				cmdAction := func(cmd string, args ...string) exec.Cmd {
					return &execTesting.FakeCmd{
						OutputScript: []execTesting.FakeAction{
							func() ([]byte, []byte, error) {
								<-stopChan
								return []byte("DMS server stopped"), nil, nil
							},
						},
					}
				}

				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction}
			})

			AfterEach(func() {
				close(stopChan)
				if listener != nil {
					_ = listener.Close()
				}
			})

			It("should try next available port", func() {
				err := manager.StartDMSServer(testDevices[:1])
				Expect(err).NotTo(HaveOccurred())

				// Port should have been incremented past the in-use port
				Expect(manager.serverPort).To(BeNumerically(">", basePort))
			})
		})

		Context("when -target_pci contains all device PCIs", func() {
			var stopChan chan struct{}
			var capturedArgs []string

			BeforeEach(func() {
				stopChan = make(chan struct{})

				cmdAction := func(cmd string, args ...string) exec.Cmd {
					capturedArgs = args
					return &execTesting.FakeCmd{
						OutputScript: []execTesting.FakeAction{
							func() ([]byte, []byte, error) {
								<-stopChan
								return []byte("DMS server stopped"), nil, nil
							},
						},
					}
				}

				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction}
			})

			AfterEach(func() {
				close(stopChan)
			})

			It("should pass comma-separated PCI addresses", func() {
				err := manager.StartDMSServer(testDevices)
				Expect(err).NotTo(HaveOccurred())

				// Find -target_pci value in captured args
				for i, arg := range capturedArgs {
					if arg == "-target_pci" {
						pciValue := capturedArgs[i+1]
						pcis := strings.Split(pciValue, ",")
						Expect(pcis).To(HaveLen(2))
						Expect(pcis).To(ContainElement("0000:01:00.0"))
						Expect(pcis).To(ContainElement("0000:02:00.0"))
					}
				}
			})
		})
	})

	Describe("StopDMSServer", func() {
		Context("when server is running", func() {
			BeforeEach(func() {
				fakeCmd := &execTesting.FakeCmd{}
				manager.cmd = fakeCmd
				manager.running.Store(true)
			})

			It("should stop the server", func() {
				err := manager.StopDMSServer()
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("when server is not running", func() {
			It("should not return an error", func() {
				err := manager.StopDMSServer()
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("IsRunning", func() {
		It("should return false when server is not running", func() {
			Expect(manager.IsRunning()).To(BeFalse())
		})

		It("should return true when server is running", func() {
			manager.running.Store(true)
			Expect(manager.IsRunning()).To(BeTrue())
		})
	})

	Describe("GetDMSClientBySerialNumber", func() {
		Context("when server is running and the device exists", func() {
			BeforeEach(func() {
				client := &dmsClient{
					device:        testDevices[0],
					targetPCI:     testDevices[0].Ports[0].PCI,
					bindAddress:   "localhost:9339",
					execInterface: fakeExec,
				}
				manager.clients[testDevices[0].SerialNumber] = client
				manager.running.Store(true)
			})

			It("should return the client for the device", func() {
				client, err := manager.GetDMSClientBySerialNumber(testDevices[0].SerialNumber)
				Expect(err).NotTo(HaveOccurred())
				Expect(client).To(Equal(manager.clients[testDevices[0].SerialNumber]))
			})
		})

		Context("when server is running and the device does not exist", func() {
			BeforeEach(func() {
				manager.running.Store(true)
			})

			It("should return an error", func() {
				client, err := manager.GetDMSClientBySerialNumber("non-existent-serial")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no DMS client found for device"))
				Expect(client).To(BeNil())
			})
		})

		Context("when server is not running", func() {
			It("should return an error", func() {
				client, err := manager.GetDMSClientBySerialNumber(testDevices[0].SerialNumber)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("DMS server is not running"))
				Expect(client).To(BeNil())
			})
		})
	})
})

var _ = Describe("Utility Functions", func() {
	Describe("isPortInUse", func() {
		Context("when port is available", func() {
			It("should return false", func() {
				// Use a high port number unlikely to be in use
				result := isPortInUse("localhost:50123")
				Expect(result).To(BeFalse())
			})
		})

		Context("when port is in use", func() {
			var listener net.Listener

			BeforeEach(func() {
				var err error
				listener, err = net.Listen("tcp", "localhost:50124")
				Expect(err).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				if listener != nil {
					_ = listener.Close()
				}
			})

			It("should return true", func() {
				result := isPortInUse("localhost:50124")
				Expect(result).To(BeTrue())
			})
		})
	})
})
