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

package configuration

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
)

var ccGlobalStatusPath = regexp.MustCompile(`^/nvidia/cc/global-status/\[(np|rp),([0-7])\]$`)
var ccGlobalStatusTarget = regexp.MustCompile(`^pci/([[:xdigit:]]{4}:[[:xdigit:]]{2}:[[:xdigit:]]{2}\.[0-7])$`)

// readCCGlobalStatus reads the same ECN files as DMS's sysfs-wrapper, avoiding
// device enumeration. Unsupported targets, missing files, and invalid values
// fall back to DMS; in particular, a failed read must never mean "disabled".
func readCCGlobalStatus(sysRoot, target string, query dmscli.XPathQuery) (bool, bool) {
	if len(query.Leaves) != 1 || query.Leaves[0] != "enabled" {
		return false, false
	}
	pathMatch := ccGlobalStatusPath.FindStringSubmatch(query.Path)
	targetMatch := ccGlobalStatusTarget.FindStringSubmatch(target)
	if pathMatch == nil || targetMatch == nil {
		return false, false
	}
	for _, subsystem := range []string{"net", "infiniband"} {
		pattern := filepath.Join(sysRoot, "bus/pci/devices", targetMatch[1], subsystem,
			"*", "ecn", "roce_"+pathMatch[1], "enable", pathMatch[2])
		paths, err := filepath.Glob(pattern)
		if err != nil || len(paths) > 1 {
			return false, false
		}
		if len(paths) == 0 {
			continue
		}
		data, err := os.ReadFile(paths[0])
		if err != nil {
			return false, false
		}
		switch strings.TrimSpace(string(data)) {
		case "0":
			return false, true
		case "1":
			return true, true
		default:
			return false, false
		}
	}
	return false, false
}
