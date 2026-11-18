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

package nvconfig

import (
	"context"
	"errors"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	execUtils "k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

var _ = Describe("native NVConfig query consistency", func() {
	const header = "Configurations: Default Current Next Boot\n"
	const stable = header + "NUM_OF_PF  1  4  4\nNUM_OF_VFS  16  1  1\n"
	var h *nvConfigUtils
	var executor *execTesting.FakeExec
	var port v1alpha1.NicDevicePortSpec

	BeforeEach(func() {
		port = v1alpha1.NicDevicePortSpec{PCI: "0000:66:00.0", FwctlDevice: "/dev/fwctl/fwctl4"}
		executor = &execTesting.FakeExec{CommandScript: nil}
		h = &nvConfigUtils{execInterface: executor}
	})

	enqueue := func(output, parameter string, cmdErr error) {
		target := port.FwctlDevice
		if target == "" {
			target = port.PCI
		}
		expectedArgs := []string{"-d", target, "-e", "query"}
		if parameter != "" {
			expectedArgs = append(expectedArgs, parameter)
		}
		executor.CommandScript = append(executor.CommandScript, func(cmd string, args ...string) execUtils.Cmd {
			Expect(cmd).To(Equal("mlxconfig"))
			Expect(args).To(Equal(expectedArgs))
			return &execTesting.FakeCmd{OutputScript: []execTesting.FakeAction{func() ([]byte, []byte, error) {
				return []byte(output), nil, cmdErr
			}}}
		})
	}

	expectEmpty := func(query types.NvConfigQuery) {
		Expect(query.DefaultConfig).To(BeEmpty())
		Expect(query.CurrentConfig).To(BeEmpty())
		Expect(query.NextBootConfig).To(BeEmpty())
		Expect(query.AllParameters).To(BeFalse())
	}

	It("confirms a complete fwctl snapshot without changing the port", func() {
		original := port
		enqueue(stable, "", nil)
		enqueue(stable, "", nil)
		query, err := h.QueryNvConfig(context.Background(), port, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(query.AllParameters).To(BeTrue())
		Expect(query.DefaultConfig["NUM_OF_PF"]).To(Equal([]string{"1"}))
		Expect(query.CurrentConfig["NUM_OF_PF"]).To(Equal([]string{"4"}))
		Expect(query.NextBootConfig["NUM_OF_PF"]).To(Equal([]string{"4"}))
		Expect(query.DefaultConfig).To(HaveLen(2))
		Expect(executor.CommandCalls).To(Equal(2))
		Expect(port).To(Equal(original))
	})

	It("retains the one-read PCI path", func() {
		port.FwctlDevice = ""
		enqueue(stable, "", nil)
		query, err := h.QueryNvConfig(context.Background(), port, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(query.AllParameters).To(BeTrue())
		Expect(query.NextBootConfig["NUM_OF_PF"]).To(Equal([]string{"4"}))
		Expect(executor.CommandCalls).To(Equal(1))
	})

	DescribeTable("rejects values or parameter membership that change between successful fwctl reads",
		func(changed, column, parameter string) {
			enqueue(stable, "", nil)
			enqueue(changed, "", nil)
			query, err := h.QueryNvConfig(context.Background(), port, nil)
			Expect(err).To(MatchError(And(
				ContainSubstring("inconsistent fwctl NVConfig reads"),
				ContainSubstring(port.PCI), ContainSubstring(port.FwctlDevice),
				ContainSubstring(column+" parameter "+parameter),
			)))
			expectEmpty(query)
			Expect(executor.CommandCalls).To(Equal(2))
		},
		Entry("default", strings.Replace(stable, "NUM_OF_PF  1  4  4", "NUM_OF_PF  9  4  4", 1), "default", "NUM_OF_PF"),
		Entry("current", strings.Replace(stable, "NUM_OF_PF  1  4  4", "NUM_OF_PF  1  1  4", 1), "current", "NUM_OF_PF"),
		Entry("next boot", strings.Replace(stable, "NUM_OF_PF  1  4  4", "NUM_OF_PF  1  4  1", 1), "next boot", "NUM_OF_PF"),
		Entry("missing parameter", strings.Replace(stable, "NUM_OF_VFS  16  1  1\n", "", 1), "default", "NUM_OF_VFS"),
		Entry("added parameter", stable+"ADDED_PARAM  1  1  1\n", "default", "ADDED_PARAM"),
	)

	It("repeats each requested selector without treating a filtered result as complete", func() {
		parameters := []string{"NUM_OF_VFS", "NUM_OF_PF"}
		for range 2 {
			enqueue(header+"NUM_OF_VFS  16  1  1\n", parameters[0], nil)
			enqueue(header+"NUM_OF_PF  1  4  4\n", parameters[1], nil)
		}
		query, err := h.QueryNvConfig(context.Background(), port, parameters)
		Expect(err).NotTo(HaveOccurred())
		Expect(query.AllParameters).To(BeFalse())
		Expect(query.NextBootConfig).To(HaveLen(2))
		Expect(executor.CommandCalls).To(Equal(4))
		Expect(parameters).To(Equal([]string{"NUM_OF_VFS", "NUM_OF_PF"}))
	})

	It("repeats recursive array expansion into independent snapshots", func() {
		for range 2 {
			enqueue(header+"NUM_OF_PF  1  4  4\nMODULE_SPLIT_M0  Array[0..1]  Array[0..1]  Array[0..1]\n", "", nil)
			enqueue(header+"MODULE_SPLIT_M0[0]  1  2  2\nMODULE_SPLIT_M0[1]  1  3  3\n", "MODULE_SPLIT_M0[0..1]", nil)
		}
		query, err := h.QueryNvConfig(context.Background(), port, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(query.AllParameters).To(BeTrue())
		Expect(query.NextBootConfig).To(Equal(map[string][]string{
			"NUM_OF_PF": {"4"}, "MODULE_SPLIT_M0[0]": {"2"}, "MODULE_SPLIT_M0[1]": {"3"},
		}))
		Expect(executor.CommandCalls).To(Equal(4))
	})

	It("rejects a change confined to an expanded array element", func() {
		for _, value := range []string{"2", "3"} {
			enqueue(header+"MODULE_SPLIT_M0  Array[0]  Array[0]  Array[0]\n", "", nil)
			enqueue(header+"MODULE_SPLIT_M0[0]  1  2  "+value+"\n", "MODULE_SPLIT_M0[0]", nil)
		}
		query, err := h.QueryNvConfig(context.Background(), port, nil)
		Expect(err).To(MatchError(ContainSubstring("next boot parameter MODULE_SPLIT_M0[0]")))
		expectEmpty(query)
		Expect(executor.CommandCalls).To(Equal(4))
	})

	DescribeTable("rejects unusable successful command output before confirming it", func(output, message string) {
		enqueue(output, "", nil)
		query, err := h.QueryNvConfig(context.Background(), port, nil)
		Expect(err).To(MatchError(ContainSubstring(message)))
		expectEmpty(query)
		Expect(executor.CommandCalls).To(Equal(1))
	},
		Entry("empty output", "", "returned no NVConfig parameters"),
		Entry("missing section", "NUM_OF_PF  1  4  4\n", "returned no NVConfig parameters"),
		Entry("empty section", header, "returned no NVConfig parameters"),
		Entry("malformed rows", header+"NUM_OF_PF  1  4\n", "returned no NVConfig parameters"),
		Entry("scanner truncation after parsed values", stable+strings.Repeat("X", 70*1024)+"\n", "token too long"),
	)

	It("discards the first snapshot when confirmation output cannot be parsed", func() {
		enqueue(stable, "", nil)
		enqueue(header, "", nil)
		query, err := h.QueryNvConfig(context.Background(), port, nil)
		Expect(err).To(MatchError(ContainSubstring("failed to confirm NVConfig")))
		expectEmpty(query)
		Expect(executor.CommandCalls).To(Equal(2))
	})

	It("does not accept scalar rows when a recursive array query returns no data", func() {
		enqueue(header+"NUM_OF_PF  1  4  4\nMODULE_SPLIT_M0  Array[0]  Array[0]  Array[0]\n", "", nil)
		enqueue(header, "MODULE_SPLIT_M0[0]", nil)
		query, err := h.QueryNvConfig(context.Background(), port, nil)
		Expect(err).To(HaveOccurred())
		expectEmpty(query)
		Expect(executor.CommandCalls).To(Equal(2))
	})

	DescribeTable("propagates command failures without exposing a snapshot", func(failConfirmation bool) {
		queryError := errors.New("firmware query failed")
		expectedCalls := 1
		if failConfirmation {
			enqueue(stable, "", nil)
			expectedCalls++
		}
		enqueue(stable, "", queryError)
		query, err := h.QueryNvConfig(context.Background(), port, nil)
		Expect(errors.Is(err, queryError)).To(BeTrue())
		expectEmpty(query)
		Expect(executor.CommandCalls).To(Equal(expectedCalls))
	}, Entry("first read", false), Entry("confirmation read", true))

	It("discards partial selected-parameter results when a later command fails", func() {
		queryError := errors.New("second parameter failed")
		enqueue(header+"NUM_OF_PF  1  4  4\n", "NUM_OF_PF", nil)
		enqueue("", "NUM_OF_VFS", queryError)
		query, err := h.QueryNvConfig(context.Background(), port, []string{"NUM_OF_PF", "NUM_OF_VFS"})
		Expect(errors.Is(err, queryError)).To(BeTrue())
		expectEmpty(query)
		Expect(executor.CommandCalls).To(Equal(2))
	})

	It("does not start a query for an already-cancelled context", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		query, err := h.QueryNvConfig(ctx, port, nil)
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		expectEmpty(query)
		Expect(executor.CommandCalls).To(BeZero())
	})

	It("does not start confirmation after cancellation", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		executor.CommandScript = []execTesting.FakeCommandAction{func(_ string, _ ...string) execUtils.Cmd {
			return &execTesting.FakeCmd{OutputScript: []execTesting.FakeAction{func() ([]byte, []byte, error) {
				cancel()
				return []byte(stable), nil, nil
			}}}
		}}
		query, err := h.QueryNvConfig(ctx, port, nil)
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		expectEmpty(query)
		Expect(executor.CommandCalls).To(Equal(1))
	})
})
