/*
Copyright 2026 NVIDIA CORPORATION & AFFILIATES
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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	execUtils "k8s.io/utils/exec"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/dospcx"
)

const docaSpcXCCExecutable = "/opt/mellanox/doca/tools/doca_spcx_cc"

var ccStartupWait = 3 * time.Second

// SpectrumXManager owns the doSPCX lifecycle and the DOCA SPC-X CC processes.
type SpectrumXManager interface {
	PlanManager
	BlueprintsDataManager
	// RunDocaSpcXCC launches and tracks the DOCA SPC-X CC process for a port.
	RunDocaSpcXCC(port v1alpha1.NicDevicePortSpec) error
	// GetCCTerminationChannel reports CC processes that terminate after startup.
	GetCCTerminationChannel() <-chan string
}

var _ SpectrumXManager = (*spectrumXConfigManager)(nil)

type spectrumXConfigManager struct {
	dospcxManager dospcxLifecycle
	execInterface execUtils.Interface

	ccProcessesMutex  sync.Mutex
	ccProcesses       map[string]*ccProcess
	ccTerminationChan chan string
}

type ccProcess struct {
	cmd execUtils.Cmd

	running            atomic.Bool
	startupCheckPassed atomic.Bool
	startupDone        chan struct{}

	errMutex   sync.RWMutex
	cmdErr     error
	startupErr error
}

func (process *ccProcess) waitForStartup() error {
	<-process.startupDone
	process.errMutex.RLock()
	defer process.errMutex.RUnlock()
	return process.startupErr
}

func (process *ccProcess) completeStartup(err error) {
	process.errMutex.Lock()
	process.startupErr = err
	process.errMutex.Unlock()
	close(process.startupDone)
}

// RunDocaSpcXCC launches and tracks the DOCA SPC-X CC process for the given port.
func (m *spectrumXConfigManager) RunDocaSpcXCC(port v1alpha1.NicDevicePortSpec) error {
	if strings.TrimSpace(port.RdmaInterface) == "" {
		return fmt.Errorf("cannot start DOCA SPC-X CC for port %q: RDMA interface is empty", port.PCI)
	}

	m.ccProcessesMutex.Lock()
	if process, found := m.ccProcesses[port.RdmaInterface]; found && process.running.Load() {
		m.ccProcessesMutex.Unlock()
		if err := process.waitForStartup(); err != nil {
			return err
		}
		if !process.running.Load() {
			return fmt.Errorf("DOCA SPC-X CC process for RDMA interface %q terminated after startup", port.RdmaInterface)
		}
		log.Log.V(2).Info("DOCA SPC-X CC process is already running", "rdma", port.RdmaInterface)
		return nil
	}

	log.Log.Info("Starting DOCA SPC-X CC process", "rdma", port.RdmaInterface)
	process := &ccProcess{
		cmd:         m.execInterface.Command(docaSpcXCCExecutable, "--device", port.RdmaInterface),
		startupDone: make(chan struct{}),
	}
	process.running.Store(true)
	m.ccProcesses[port.RdmaInterface] = process
	m.ccProcessesMutex.Unlock()

	go func() {
		output, err := process.cmd.CombinedOutput()
		if err != nil {
			process.errMutex.Lock()
			process.cmdErr = err
			process.errMutex.Unlock()
			log.Log.Error(err, "DOCA SPC-X CC process failed", "rdma", port.RdmaInterface)
		}
		log.Log.V(2).Info("DOCA SPC-X CC process output", "rdma", port.RdmaInterface, "output", string(output))
		process.running.Store(false)
		m.ccProcessesMutex.Lock()
		if m.ccProcesses[port.RdmaInterface] == process {
			delete(m.ccProcesses, port.RdmaInterface)
		}
		m.ccProcessesMutex.Unlock()

		if process.startupCheckPassed.Load() {
			log.Log.Info("DOCA SPC-X CC process terminated unexpectedly", "rdma", port.RdmaInterface)
			select {
			case m.ccTerminationChan <- port.RdmaInterface:
			default:
				log.Log.V(2).Info("DOCA SPC-X CC termination notification dropped because the channel is full",
					"rdma", port.RdmaInterface)
			}
		}
	}()

	log.Log.V(2).Info("Waiting for DOCA SPC-X CC process startup", "rdma", port.RdmaInterface, "wait", ccStartupWait)
	time.Sleep(ccStartupWait)
	if !process.running.Load() {
		process.errMutex.RLock()
		cmdErr := process.cmdErr
		process.errMutex.RUnlock()
		var startupErr error
		if cmdErr != nil {
			startupErr = fmt.Errorf("failed to start DOCA SPC-X CC for port %q: %w", port.PCI, cmdErr)
		} else {
			startupErr = fmt.Errorf("failed to start DOCA SPC-X CC for RDMA interface %q: process exited during startup", port.RdmaInterface)
		}
		process.completeStartup(startupErr)
		return startupErr
	}

	process.startupCheckPassed.Store(true)
	process.completeStartup(nil)
	log.Log.Info("Started DOCA SPC-X CC process", "rdma", port.RdmaInterface)
	return nil
}

// GetCCTerminationChannel returns a read-only channel for CC process termination notifications.
func (m *spectrumXConfigManager) GetCCTerminationChannel() <-chan string {
	return m.ccTerminationChan
}

// NewSpectrumXConfigManager creates the doSPCX and CC lifecycle manager.
func NewSpectrumXConfigManager() SpectrumXManager {
	execInterface := execUtils.New()
	return &spectrumXConfigManager{
		dospcxManager:     dospcx.NewManager(execInterface),
		execInterface:     execInterface,
		ccProcesses:       make(map[string]*ccProcess),
		ccTerminationChan: make(chan string, 10),
	}
}
