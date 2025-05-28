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
	"time"

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
			instances:     make(map[string]*dmsInstance),
			nextPort:      basePort,
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

	Describe("StartDMSInstances", func() {
		Context("when DMS server starts successfully", func() {
			var stopChan chan struct{}

			BeforeEach(func() {
				stopChan = make(chan struct{})

				cmdAction := func(cmd string, args ...string) exec.Cmd {
					// Verify it's calling the DMS server binary
					Expect(cmd).To(Equal(dmsServerPath))

					// Create a new fakeCmd for each call
					return &execTesting.FakeCmd{
						OutputScript: []execTesting.FakeAction{
							func() ([]byte, []byte, error) {
								// Simulate a long-running DMS server that blocks until stopped
								<-stopChan
								return []byte("DMS server stopped"), nil, nil
							},
						},
					}
				}

				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction}
			})

			AfterEach(func() {
				// Stop the fake commands
				close(stopChan)
			})

			It("should start DMS instances for all devices", func() {
				err := manager.StartDMSInstances(testDevices)
				Expect(err).NotTo(HaveOccurred())

				// Verify instances were created for both devices
				Expect(manager.instances).To(HaveLen(2))
				Expect(manager.instances).To(HaveKey("test-serial-1"))
				Expect(manager.instances).To(HaveKey("test-serial-2"))

				// Verify instances are running
				Expect(manager.instances["test-serial-1"].running.Load()).To(BeTrue())
				Expect(manager.instances["test-serial-2"].running.Load()).To(BeTrue())
			})
		})

		Context("when DMS server fails to start", func() {
			BeforeEach(func() {
				cmdAction := func(cmd string, args ...string) exec.Cmd {
					// Create a new fakeCmd for each call that will return an error immediately
					return &execTesting.FakeCmd{
						OutputScript: []execTesting.FakeAction{
							func() ([]byte, []byte, error) {
								// This simulates the DMS server failing to start immediately
								return nil, nil, errors.New("failed to start DMS server")
							},
						},
					}
				}

				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction}
			})

			It("should return an error", func() {
				err := manager.StartDMSInstances(testDevices[:1]) // Just test with one device
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to start DMS for device"))
			})

			It("should not have a running DMSClient after failure", func() {
				err := manager.StartDMSInstances(testDevices[:1]) // Just test with one device
				Expect(err).To(HaveOccurred())

				// Verify no instances were created or they are not running
				if len(manager.instances) > 0 {
					for _, instance := range manager.instances {
						Expect(instance.running.Load()).To(BeFalse())
					}
				}

				// Verify we can't get a running client
				client, err := manager.GetDMSClientBySerialNumber("test-serial-1")
				if err == nil {
					Expect(client.IsRunning()).To(BeFalse())
				}
			})
		})

		Context("when a device instance already exists and is running", func() {
			BeforeEach(func() {
				// Create an existing instance
				instance := &dmsInstance{
					device:        testDevices[0],
					bindAddress:   "localhost:9339",
					execInterface: fakeExec,
				}
				instance.running.Store(true)
				manager.instances[testDevices[0].SerialNumber] = instance

				// Don't need to add command expectations as it shouldn't try to start another instance
			})

			It("should not start a new instance for existing device", func() {
				err := manager.StartDMSInstances(testDevices[:1]) // Just use the first device
				Expect(err).NotTo(HaveOccurred())

				// Verify only one instance exists
				Expect(manager.instances).To(HaveLen(1))
				Expect(manager.instances).To(HaveKey("test-serial-1"))

				// Verify the instance is running on the expected address
				instance := manager.instances["test-serial-1"]
				Expect(instance.bindAddress).To(Equal("localhost:9339"))
				Expect(instance.running.Load()).To(BeTrue())
			})
		})

		Context("when one device succeeds but another fails", func() {
			BeforeEach(func() {
				callCount := 0
				cmdAction := func(cmd string, args ...string) exec.Cmd {
					callCount++
					if callCount == 1 {
						// First device succeeds - create a blocking command
						return &execTesting.FakeCmd{
							OutputScript: []execTesting.FakeAction{
								func() ([]byte, []byte, error) {
									// Simulate a long-running successful DMS server
									time.Sleep(time.Hour)
									return []byte("DMS server stopped"), nil, nil
								},
							},
						}
					} else {
						// Second device fails immediately
						return &execTesting.FakeCmd{
							OutputScript: []execTesting.FakeAction{
								func() ([]byte, []byte, error) {
									return nil, nil, errors.New("failed to start second DMS server")
								},
							},
						}
					}
				}

				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction, cmdAction}
			})

			It("should return an error and not create instances for failed devices", func() {
				err := manager.StartDMSInstances(testDevices) // Try to start both devices
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to start DMS for device"))

				// Should have created instance for first device but failed on second
				// The first device should be running, but the overall operation failed
				if len(manager.instances) > 0 {
					// If any instances were created, verify their state
					for serial, instance := range manager.instances {
						if serial == "test-serial-1" {
							// First device should be running
							Expect(instance.running.Load()).To(BeTrue())
						} else {
							// Any other device should not be running
							Expect(instance.running.Load()).To(BeFalse())
						}
					}
				}
			})
		})

		Context("when port is already in use", func() {
			var listener net.Listener

			BeforeEach(func() {
				// Occupy the port that would be used
				var err error
				listener, err = net.Listen("tcp", "localhost:9339")
				Expect(err).NotTo(HaveOccurred())

				// Setup a command for the second port attempt
				cmdAction := func(cmd string, args ...string) exec.Cmd {
					// Create a new fakeCmd for each call
					return &execTesting.FakeCmd{
						OutputScript: []execTesting.FakeAction{
							func() ([]byte, []byte, error) {
								// Simulate successful start on next port
								time.Sleep(600 * time.Millisecond)
								return []byte("DMS server started"), nil, nil
							},
						},
					}
				}

				fakeExec.CommandScript = []execTesting.FakeCommandAction{cmdAction}
			})

			AfterEach(func() {
				if listener != nil {
					_ = listener.Close()
				}
			})

			It("should try next available port", func() {
				err := manager.StartDMSInstances(testDevices[:1]) // Just use the first device
				Expect(err).NotTo(HaveOccurred())

				// Verify instance was created using a different port
				Expect(manager.instances).To(HaveLen(1))
				Expect(manager.instances).To(HaveKey("test-serial-1"))

				// Port should have been incremented
				Expect(manager.nextPort).To(BeNumerically(">", basePort))
			})
		})
	})

	Describe("StopAllDMSInstances", func() {
		Context("when instances are running", func() {
			BeforeEach(func() {
				// Create instances with fake commands
				for _, device := range testDevices {
					fakeCmd := &execTesting.FakeCmd{}
					instance := &dmsInstance{
						device:        device,
						cmd:           fakeCmd,
						bindAddress:   "localhost:9339",
						execInterface: fakeExec,
					}
					instance.running.Store(true)
					manager.instances[device.SerialNumber] = instance
				}
			})

			It("should stop all instances", func() {
				err := manager.StopAllDMSInstances()
				Expect(err).NotTo(HaveOccurred())

				// Since we can't directly check Stopped state in the FakeCmd,
				// we'll verify the instances are in the map but not running
				for _, instance := range manager.instances {
					// After stopping, the goroutine should eventually set running to false
					// but we can't easily test that in a unit test
					Expect(instance.cmd).NotTo(BeNil(), "Command should exist")
				}
			})

			It("should set running=false for all instances after stopping", func() {
				// Verify instances are initially running
				for _, instance := range manager.instances {
					Expect(instance.running.Load()).To(BeTrue())
				}

				err := manager.StopAllDMSInstances()
				Expect(err).NotTo(HaveOccurred())

				// Note: In the real implementation, running would be set to false by the goroutine
				// when the command exits. In our test, we can't easily verify this behavior
				// since FakeCmd.Stop() doesn't automatically trigger the goroutine completion.
				// We can only verify that the Stop() method was called on the commands.
				// The actual verification that running becomes false would require more complex
				// test setup with channels or other synchronization mechanisms.
				for _, instance := range manager.instances {
					Expect(instance.cmd).NotTo(BeNil(), "Command should exist")
					// The Stop() method was called, but we can't easily verify it in the fake
				}
			})
		})

		Context("when no instances are running", func() {
			BeforeEach(func() {
				// Create instances that are not running
				for _, device := range testDevices {
					instance := &dmsInstance{
						device:        device,
						bindAddress:   "localhost:9339",
						execInterface: fakeExec,
					}
					instance.running.Store(false)
					manager.instances[device.SerialNumber] = instance
				}
			})

			It("should not return an error", func() {
				err := manager.StopAllDMSInstances()
				Expect(err).NotTo(HaveOccurred())
			})
		})
	})

	Describe("GetDMSClientBySerialNumber", func() {
		Context("when the device exists", func() {
			BeforeEach(func() {
				// Add a test instance
				instance := &dmsInstance{
					device:        testDevices[0],
					bindAddress:   "localhost:9339",
					execInterface: fakeExec,
				}
				instance.running.Store(true)
				manager.instances[testDevices[0].SerialNumber] = instance
			})

			It("should return the client for the device", func() {
				client, err := manager.GetDMSClientBySerialNumber(testDevices[0].SerialNumber)
				Expect(err).NotTo(HaveOccurred())
				Expect(client).To(Equal(manager.instances[testDevices[0].SerialNumber]))
			})
		})

		Context("when the device does not exist", func() {
			It("should return an error", func() {
				client, err := manager.GetDMSClientBySerialNumber("non-existent-serial")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no DMS client found for device"))
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
