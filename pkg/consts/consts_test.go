/*
2026 NVIDIA CORPORATION & AFFILIATES
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

package consts

import "testing"

func TestPortParam(t *testing.T) {
	cases := []struct {
		base string
		n    int
		want string
	}{
		{"LINK_TYPE", 1, "LINK_TYPE_P1"},
		{"LINK_TYPE", 4, "LINK_TYPE_P4"},
		{"LINK_TYPE", 8, "LINK_TYPE_P8"},
		{"ROCE_CC_PRIO_MASK", 3, "ROCE_CC_PRIO_MASK_P3"},
		{"CNP_DSCP", 10, "CNP_DSCP_P10"},
	}
	for _, c := range cases {
		got := PortParam(c.base, c.n)
		if got != c.want {
			t.Errorf("PortParam(%q, %d) = %q, want %q", c.base, c.n, got, c.want)
		}
	}
}

func TestPortSuffixNum(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"LINK_TYPE_P1", 1, true},
		{"LINK_TYPE_P2", 2, true},
		{"LINK_TYPE_P4", 4, true},
		{"LINK_TYPE_P10", 10, true},
		{"ROCE_CC_PRIO_MASK_P8", 8, true},
		// A leading _Px should match the last occurrence.
		{"FOO_P1_P3", 3, true},

		// Not port-suffixed.
		{"SRIOV_EN", 0, false},
		{"MAX_ACC_OUT_READ", 0, false},
		{"INTERNAL_CPU_MODEL", 0, false},
		{"", 0, false},
		{"_P", 0, false},
		{"_P0", 0, false},
		{"LINK_TYPE_P", 0, false},
		{"LINK_TYPE_PA", 0, false},
		{"LINK_TYPE_P1A", 0, false},
		{"SOMETHING_P2_EXTRA", 0, false},
	}
	for _, c := range cases {
		got, ok := PortSuffixNum(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("PortSuffixNum(%q) = (%d, %v), want (%d, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
