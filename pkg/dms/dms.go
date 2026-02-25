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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

const (
	basePort      = 9339
	dmsServerPath = "/opt/mellanox/doca/services/dms/dmsd"
	imagesDir     = "/tmp/images"
)

// DMSManager provides per-device DMS client lookup (used by all consumers)
type DMSManager interface {
	// GetDMSClientBySerialNumber returns the DMS client for a specific device identified by its serial number
	GetDMSClientBySerialNumber(serialNumber string) (DMSClient, error)
}

// DMSServer extends DMSManager with server lifecycle management
type DMSServer interface {
	DMSManager
	// StartDMSServer starts a single DMS server for all given NIC devices
	StartDMSServer(devices []v1alpha1.NicDeviceStatus) error
	// StopDMSServer stops the running DMS server
	StopDMSServer() error
	// IsRunning returns whether the DMS server is running
	IsRunning() bool
}

type dmsServer struct {
	clients       map[string]*dmsClient
	mutex         sync.RWMutex
	serverPort    int
	execInterface execUtils.Interface

	// DMS server process state
	cmd      execUtils.Cmd
	running  atomic.Bool
	errMutex sync.RWMutex
	cmdErr   error
}

// NewDMSServer creates a new instance of DMSServer that manages a local DMS server process
func NewDMSServer() DMSServer {
	log.Log.V(2).Info("Creating new DMS Server")
	return &dmsServer{
		clients:       make(map[string]*dmsClient),
		serverPort:    basePort,
		execInterface: execUtils.New(),
	}
}

// externalDMSManager implements DMSManager for an already-running external DMS server
type externalDMSManager struct {
	clients map[string]*dmsClient
}

// NewExternalDMSManager creates a DMSManager that connects to an external DMS server.
// devices: NIC devices to create clients for
// address: bind address of the external DMS server (e.g. "remotehost:9339")
// authParams: authentication/security flags passed to dmsc (e.g. []string{"--insecure"} or
// []string{"--tls-ca", "/path/ca.pem", "--tls-cert", "/path/cert.pem", "--tls-key", "/path/key.pem"})
func NewExternalDMSManager(devices []v1alpha1.NicDeviceStatus, address string, authParams []string) DMSManager {
	log.Log.V(2).Info("Creating new External DMS Manager", "address", address, "deviceCount", len(devices))
	clients := make(map[string]*dmsClient)
	for _, device := range devices {
		client := &dmsClient{
			device:        device,
			targetPCI:     device.Ports[0].PCI,
			bindAddress:   address,
			authParams:    authParams,
			execInterface: execUtils.New(),
		}
		clients[device.SerialNumber] = client
	}
	return &externalDMSManager{clients: clients}
}

// GetDMSClientBySerialNumber returns the DMS client for a specific device identified by its serial number
func (m *externalDMSManager) GetDMSClientBySerialNumber(serialNumber string) (DMSClient, error) {
	log.Log.V(2).Info("GetDMSClientBySerialNumber", "serialNumber", serialNumber)
	client, ok := m.clients[serialNumber]
	if !ok {
		log.Log.V(2).Info("No DMS client found", "serialNumber", serialNumber)
		return nil, fmt.Errorf("no DMS client found for device with serial number %s", serialNumber)
	}
	log.Log.V(2).Info("Found DMS client", "serialNumber", serialNumber)
	return client, nil
}

// StartDMSServer starts a single DMS server for all given NIC devices
func (m *dmsServer) StartDMSServer(devices []v1alpha1.NicDeviceStatus) error {
	log.Log.V(2).Info("StartDMSServer", "deviceCount", len(devices))
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.running.Load() {
		log.Log.V(2).Info("DMS server already running")
		return nil
	}

	if len(devices) == 0 {
		log.Log.V(2).Info("No devices to start DMS server for")
		return nil
	}

	// Find available port
	bindAddress := ""
	log.Log.V(2).Info("Finding available port", "port", m.serverPort)
	for {
		bindAddress = fmt.Sprintf("localhost:%d", m.serverPort)
		m.serverPort++

		if !isPortInUse(bindAddress) {
			log.Log.V(2).Info("Found available port", "address", bindAddress)
			break
		}
		log.Log.V(2).Info("Port in use, trying next", "port", m.serverPort)
	}

	// Create shared images directory
	err := os.MkdirAll(imagesDir, 0755)
	if err != nil {
		log.Log.Error(err, "failed to create images directory", "directory", imagesDir)
		return fmt.Errorf("failed to create images directory: %v", err)
	}

	// Collect all first-port PCI addresses
	pciAddresses := make([]string, 0, len(devices))
	for _, device := range devices {
		pciAddresses = append(pciAddresses, device.Ports[0].PCI)
	}
	targetPCI := strings.Join(pciAddresses, ",")

	log.Log.V(2).Info("Starting DMS server", "path", dmsServerPath, "bindAddress", bindAddress, "targetPCI", targetPCI)
	cmd := m.execInterface.Command(dmsServerPath,
		"-bind_address", bindAddress,
		"-target_pci", targetPCI,
		"-auth", "credentials",
		"-noauth", "-tls_enabled=false",
		"--image_folder", imagesDir)

	m.cmd = cmd
	m.running.Store(true)

	go func() {
		output, err := m.cmd.Output()
		if err != nil {
			m.errMutex.Lock()
			m.cmdErr = err
			m.errMutex.Unlock()
		}
		log.Log.V(2).Info("DMS server output", "bind_address", bindAddress, "output", string(output))
		m.running.Store(false)
	}()

	log.Log.V(2).Info("Waiting for DMS server to start")
	time.Sleep(3 * time.Second)

	if !m.running.Load() {
		m.errMutex.RLock()
		cmdErr := m.cmdErr
		m.errMutex.RUnlock()

		if cmdErr != nil {
			log.Log.Error(cmdErr, "Failed to start DMS server")
			return fmt.Errorf("failed to start DMS server: %v", cmdErr)
		}
		return fmt.Errorf("failed to start DMS server: unknown error")
	}

	// Create clients for each device
	for _, device := range devices {
		client := &dmsClient{
			device:        device,
			targetPCI:     device.Ports[0].PCI,
			bindAddress:   bindAddress,
			authParams:    []string{"--insecure"},
			execInterface: m.execInterface,
		}
		m.clients[device.SerialNumber] = client
	}

	log.Log.Info("Started DMS server", "bind_address", bindAddress, "targetPCI", targetPCI, "clientCount", len(m.clients))
	return nil
}

// StopDMSServer sends SIGTERM to the running DMS server
func (m *dmsServer) StopDMSServer() error {
	log.Log.V(2).Info("StopDMSServer")
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if !m.running.Load() {
		log.Log.V(2).Info("DMS server not running, nothing to stop")
		return nil
	}

	if m.cmd != nil {
		m.cmd.Stop()
	}

	log.Log.Info("Stopped DMS server")
	return nil
}

// IsRunning returns whether the DMS server is running
func (m *dmsServer) IsRunning() bool {
	return m.running.Load()
}

// GetDMSClientBySerialNumber returns the DMS client for a specific device identified by its serial number
func (m *dmsServer) GetDMSClientBySerialNumber(serialNumber string) (DMSClient, error) {
	log.Log.V(2).Info("GetDMSClientBySerialNumber", "serialNumber", serialNumber)
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if !m.running.Load() {
		return nil, fmt.Errorf("DMS server is not running")
	}

	client, ok := m.clients[serialNumber]
	if !ok {
		log.Log.V(2).Info("No DMS client found", "serialNumber", serialNumber)
		return nil, fmt.Errorf("no DMS client found for device with serial number %s", serialNumber)
	}

	log.Log.V(2).Info("Found DMS client", "serialNumber", serialNumber)
	return client, nil
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
