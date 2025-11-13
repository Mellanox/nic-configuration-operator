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
	"fmt"
	"net"
	"os"
	"path"
	"sync"
	"time"

	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

const (
	basePort      = 9339
	dmsServerPath = "/opt/mellanox/doca/services/dms/dmsd"
)

// DMSManager interface defines methods for managing DOCA Management Service instances
type DMSManager interface {
	// StartDMSInstances starts DMS instances for given NIC devices
	StartDMSInstances(devices []v1alpha1.NicDeviceStatus) error
	// StopAllDMSInstances stops all running DMS instances
	StopAllDMSInstances() error
	// GetDMSClientBySerialNumber returns the DMS client for a specific device identified by its PCI address
	GetDMSClientBySerialNumber(serialNumber string) (DMSClient, error)
}

type dmsManager struct {
	instances     map[string]*dmsInstance
	mutex         sync.RWMutex
	nextPort      int
	execInterface execUtils.Interface
}

// NewDMSManager creates a new instance of DMSManager
func NewDMSManager() DMSManager {
	log.Log.V(2).Info("Creating new DMS Manager")
	return &dmsManager{
		instances:     make(map[string]*dmsInstance),
		nextPort:      basePort,
		execInterface: execUtils.New(),
	}
}

// StartDMSInstances starts DMS instances for given NIC devices
func (m *dmsManager) StartDMSInstances(devices []v1alpha1.NicDeviceStatus) error {
	log.Log.V(2).Info("StartDMSInstances", "deviceCount", len(devices))
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for _, device := range devices {
		log.Log.V(2).Info("Processing device", "serialNumber", device.SerialNumber, "pci", device.Ports[0].PCI)

		// Check if instance already exists
		if instance, exists := m.instances[device.SerialNumber]; exists {
			if instance.running.Load() {
				log.Log.V(2).Info("DMS instance already running for device", "device", device.SerialNumber)
				continue
			}
			log.Log.V(2).Info("Instance exists but not running, will restart", "device", device.SerialNumber)
		}

		// Find next available port
		bindAddress := ""
		log.Log.V(2).Info("Finding available port", "port", m.nextPort)
		for {
			bindAddress = fmt.Sprintf("localhost:%d", m.nextPort)
			m.nextPort++

			if !isPortInUse(bindAddress) {
				log.Log.V(2).Info("Found available port", "address", bindAddress)
				break
			}
			log.Log.V(2).Info("Port in use, trying next", "port", m.nextPort)
		}

		pciAddr := device.Ports[0].PCI

		imagesDir := path.Join("/tmp", "images", device.SerialNumber)
		err := os.MkdirAll(imagesDir, 0755)
		if err != nil {
			log.Log.Error(err, "failed to create images directory", "directory", imagesDir)
			return fmt.Errorf("failed to create images directory: %v", err)
		}

		log.Log.V(2).Info("Starting DMS server", "path", dmsServerPath, "bindAddress", bindAddress, "targetPCI", pciAddr)
		cmd := m.execInterface.Command(dmsServerPath,
			"-bind_address", bindAddress,
			"-target_pci", pciAddr,
			"-auth", "credentials",
			"-noauth", "-tls_enabled=false",
			"--image_folder", imagesDir)

		instance := &dmsInstance{
			device:        device,
			bindAddress:   bindAddress,
			cmd:           cmd,
			execInterface: m.execInterface,
		}

		instance.running.Store(true)

		go func() {
			output, err := instance.cmd.Output()
			if err != nil {
				instance.errMutex.Lock()
				instance.cmdErr = err
				instance.errMutex.Unlock()
			}
			log.Log.V(2).Info("DMS instance output", "device", instance.device.SerialNumber, "bind_address", instance.bindAddress, "output", string(output))
			instance.running.Store(false)
		}()

		log.Log.V(2).Info("Waiting 500ms for DMS server to start", "device", device.SerialNumber)
		time.Sleep(500 * time.Millisecond)

		if !instance.running.Load() {
			instance.errMutex.RLock()
			cmdErr := instance.cmdErr
			instance.errMutex.RUnlock()

			if cmdErr != nil {
				log.Log.Error(cmdErr, "Failed to start DMS server", "device", device.SerialNumber)
				return fmt.Errorf("failed to start DMS for device %s: %v", device.SerialNumber, cmdErr)
			}
			return fmt.Errorf("failed to start DMS for device %s: unknown error", device.SerialNumber)
		}

		log.Log.V(2).Info("DMS server process started", "device", device.SerialNumber)

		m.instances[device.SerialNumber] = instance

		log.Log.Info("Started DMS instance", "device", device.SerialNumber, "bind_address", instance.bindAddress)
	}

	log.Log.V(2).Info("All DMS instances started", "instanceCount", len(m.instances))
	return nil
}

// StopAllDMSInstances sends SIGTERM to all running DMS instances
func (m *dmsManager) StopAllDMSInstances() error {
	log.Log.V(2).Info("StopAllDMSInstances")
	m.mutex.Lock()
	defer m.mutex.Unlock()

	var lastErr error
	stoppedCount := 0

	for serial, instance := range m.instances {
		if !instance.running.Load() {
			log.Log.V(2).Info("Instance not running, skipping", "device", serial)
			continue
		}

		log.Log.V(2).Info("Stopping DMS instance", "device", serial)
		if instance.cmd != nil {
			instance.cmd.Stop()
		}

		stoppedCount++
		log.Log.Info("Stopped DMS instance", "device", serial)
	}

	log.Log.V(2).Info("StopAllDMSInstances completed", "stoppedCount", stoppedCount)
	return lastErr
}

// GetDMSClientBySerialNumber returns the DMS client for a specific device identified by its serial number
func (m *dmsManager) GetDMSClientBySerialNumber(serialNumber string) (DMSClient, error) {
	log.Log.V(2).Info("GetDMSClientBySerialNumber", "serialNumber", serialNumber)
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	instance, ok := m.instances[serialNumber]
	if !ok {
		log.Log.V(2).Info("No DMS client found", "serialNumber", serialNumber)
		return nil, fmt.Errorf("no DMS client found for device with serial number %s", serialNumber)
	}

	log.Log.V(2).Info("Found DMS client", "serialNumber", serialNumber, "running", instance.running.Load())
	return instance, nil
}

// isPortInUse checks if a port is already in use
func isPortInUse(addr string) bool {
	log.Log.V(2).Info("Checking if port is in use", "addr", addr)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Log.V(2).Info("Port is in not available", "addr", addr, "err", err)
		return true
	}
	_ = ln.Close()
	log.Log.V(2).Info("Port is available", "addr", addr)
	return false
}
