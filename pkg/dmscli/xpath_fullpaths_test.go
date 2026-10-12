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

package dmscli

import (
	"context"
	"encoding/json"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("full XPath queries", func() {
	const target = "pci/0000:64:00.0"
	var commands []recordedCommand
	BeforeEach(func() { commands = nil })
	It("keeps np/rp and nested slot indices distinct in one invocation", func() {
		queries := []XPathQuery{
			{Path: "/nvidia/cc/global-status/[np,0]", Leaves: []string{"enabled"}},
			{Path: "/nvidia/cc/global-status/[rp,0]", Leaves: []string{"enabled"}},
			{Path: "/nvidia/cc/algo/slot/[0]/param/[15]", Leaves: []string{"value"}},
			{Path: "/nvidia/cc/algo/slot/[1]/param/[15]", Leaves: []string{"value"}},
		}
		executor := fakeExecutor([]byte(`{"status":"ok","values":{
   "/nvidia/cc/global-status/[np,0]/enabled":true,
   "/nvidia/cc/global-status/[rp,0]/enabled":false,
   "/nvidia/cc/algo/slot/[0]/param/[15]/value":1,
   "/nvidia/cc/algo/slot/[1]/param/[15]/value":3
  },"failures":{}}`), nil, &commands)
		result, err := QueryXPathsFullPaths(context.Background(), executor, target, queries)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Values[queries[0].Path]["enabled"]).To(BeTrue())
		Expect(result.Values[queries[1].Path]["enabled"]).To(BeFalse())
		Expect(result.Values[queries[2].Path]["value"]).To(Equal(json.Number("1")))
		Expect(result.Values[queries[3].Path]["value"]).To(Equal(json.Number("3")))
		Expect(commands).To(HaveLen(1))
		Expect(commands[0].args[0]).To(Equal("--batch-xpaths"))
	})
	DescribeTable("rejects incomplete or ambiguous responses", func(response string) {
		executor := fakeExecutor([]byte(response), nil, &commands)
		_, err := QueryXPathsFullPaths(context.Background(), executor, target, []XPathQuery{{Path: "/nvidia/x/[0]", Leaves: []string{"value"}}})
		Expect(err).To(HaveOccurred())
		Expect(commands).To(HaveLen(1))
	},
		Entry("legacy response", `{"value":1}`),
		Entry("missing value", `{"status":"ok","values":{},"failures":{}}`),
		Entry("unexpected index", `{"status":"ok","values":{"/nvidia/x/[1]/value":1},"failures":{}}`),
		Entry("null value", `{"status":"ok","values":{"/nvidia/x/[0]/value":null},"failures":{}}`),
		Entry("missing failures", `{"status":"ok","values":{"/nvidia/x/[0]/value":1}}`),
		Entry("partial success exit", `{"status":"partial","values":{},"failures":{"/nvidia/x/[0]/value":"failed"}}`),
		Entry("ok with failures", `{"status":"ok","values":{},"failures":{"/nvidia/x/[0]/value":"failed"}}`),
		Entry("value and failure", `{"status":"partial","values":{"/nvidia/x/[0]/value":1},"failures":{"/nvidia/x/[0]/value":"failed"}}`),
		Entry("invalid failure", `{"status":"error","values":{},"failures":{"/nvidia/x/[0]/value":false}}`),
		Entry("invalid metadata", `{"status":"ok","values":{"/nvidia/x/[0]/value":1},"failures":{},"_nvconfig":{"/nvidia/x/[0]/value":""}}`),
	)
	It("retains successes and failures when the command fails", func() {
		executor := fakeExecutor([]byte(`{"status":"partial","values":{"/nvidia/x/[0]/value":1},"failures":{"/nvidia/x/[1]/value":"register failed"}}`), errors.New("exit status 9"), &commands)
		result, err := QueryXPathsFullPaths(context.Background(), executor, target, []XPathQuery{
			{Path: "/nvidia/x/[0]", Leaves: []string{"value"}}, {Path: "/nvidia/x/[1]", Leaves: []string{"value"}},
		})
		Expect(err).To(MatchError(ContainSubstring("register failed")))
		Expect(result.Values["/nvidia/x/[0]"]["value"]).To(Equal(json.Number("1")))
		Expect(result.Failures).To(HaveKey("/nvidia/x/[1]/value"))
	})
	It("returns unsupported flag errors without retrying legacy reads", func() {
		executor := fakeExecutorWithStderr(nil, []byte("unknown flag: --batch-xpaths"), errors.New("exit status 1"), &commands)
		_, err := QueryXPathsFullPaths(context.Background(), executor, target, []XPathQuery{{Path: "/nvidia/x/[0]", Leaves: []string{"value"}}})
		Expect(err).To(MatchError(ContainSubstring("unknown flag")))
		Expect(commands).To(HaveLen(1))
	})
	It("preserves optional native ownership metadata", func() {
		executor := fakeExecutor([]byte(`{"status":"ok","values":{"/nvidia/x/[0]/value":1},"failures":{},"_nvconfig":{"/nvidia/x/[0]/value":"PARAM_P1"}}`), nil, &commands)
		result, err := QueryXPathsFullPaths(context.Background(), executor, target, []XPathQuery{{Path: "/nvidia/x/[0]", Leaves: []string{"value"}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.NVConfig).To(HaveKeyWithValue("/nvidia/x/[0]/value", "PARAM_P1"))
	})
	It("rejects nil executors and invalid requests before execution", func() {
		_, err := QueryXPathsFullPaths(context.Background(), nil, target, nil)
		Expect(err).To(HaveOccurred())
		executor := fakeExecutor(nil, nil, &commands)
		_, err = QueryXPathsFullPaths(context.Background(), executor, target, nil)
		Expect(err).To(HaveOccurred())
		Expect(commands).To(BeEmpty())
	})
})
