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
	"context"
	"errors"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	execUtils "k8s.io/utils/exec"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

type fakeCmd struct {
	execUtils.Cmd
	output  []byte
	err     error
	release <-chan struct{}
}

func (command *fakeCmd) CombinedOutput() ([]byte, error) {
	if command.release != nil {
		<-command.release
	}
	return command.output, command.err
}

type fakeExec struct {
	execUtils.Interface
	command execUtils.Cmd
	calls   int
	name    string
	args    []string
}

func (executor *fakeExec) Command(name string, args ...string) execUtils.Cmd {
	executor.calls++
	executor.name = name
	executor.args = append([]string(nil), args...)
	return executor.command
}

func (executor *fakeExec) CommandContext(_ context.Context, name string, args ...string) execUtils.Cmd {
	return executor.Command(name, args...)
}

var _ = Describe("SpectrumXManager", func() {
	var originalStartupWait time.Duration

	BeforeEach(func() {
		originalStartupWait = ccStartupWait
		ccStartupWait = time.Millisecond
	})

	AfterEach(func() {
		ccStartupWait = originalStartupWait
	})

	It("constructs the doSPCX lifecycle internally", func() {
		manager := NewSpectrumXConfigManager()
		implementation, ok := manager.(*spectrumXConfigManager)

		Expect(ok).To(BeTrue())
		Expect(implementation.dospcxManager).NotTo(BeNil())
		Expect(implementation.execInterface).NotTo(BeNil())
	})

	It("starts and tracks DOCA SPC-X CC", func() {
		release := make(chan struct{})
		executor := &fakeExec{command: &fakeCmd{release: release}}
		manager := &spectrumXConfigManager{
			execInterface: executor, ccProcesses: map[string]*ccProcess{}, ccTerminationChan: make(chan string, 1),
		}
		port := v1alpha1.NicDevicePortSpec{PCI: "0000:64:00.0", RdmaInterface: "roce_r0"}

		Expect(manager.RunDocaSpcXCC(port)).To(Succeed())
		manager.ccProcessesMutex.Lock()
		process := manager.ccProcesses[port.RdmaInterface]
		manager.ccProcessesMutex.Unlock()
		Expect(process.running.Load()).To(BeTrue())
		Expect(executor.name).To(Equal(docaSpcXCCExecutable))
		Expect(executor.args).To(Equal([]string{"--device", port.RdmaInterface}))
		Expect(manager.RunDocaSpcXCC(port)).To(Succeed())
		Expect(executor.calls).To(Equal(1))
		close(release)
		Eventually(manager.GetCCTerminationChannel()).Should(Receive(Equal(port.RdmaInterface)))
	})

	It("returns a startup failure", func() {
		commandErr := errors.New("start failed")
		manager := &spectrumXConfigManager{
			execInterface: &fakeExec{command: &fakeCmd{err: commandErr}},
			ccProcesses:   map[string]*ccProcess{}, ccTerminationChan: make(chan string, 1),
		}

		err := manager.RunDocaSpcXCC(v1alpha1.NicDevicePortSpec{
			PCI: "0000:64:00.0", RdmaInterface: "roce_r0",
		})
		Expect(errors.Is(err, commandErr)).To(BeTrue())
	})

	It("rejects a port without an RDMA interface", func() {
		manager := &spectrumXConfigManager{}
		Expect(manager.RunDocaSpcXCC(v1alpha1.NicDevicePortSpec{PCI: "0000:64:00.0"})).
			To(MatchError(ContainSubstring("RDMA interface is empty")))
	})
})
