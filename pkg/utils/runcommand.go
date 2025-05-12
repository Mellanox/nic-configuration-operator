// Copyright 2025 NVIDIA CORPORATION & AFFILIATES
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"bytes"
	"fmt"
	"strings"

	execUtils "k8s.io/utils/exec"
)

// RunCommand runs a command and captures stderr separately for better error reporting
// Returns stdout, error (with stderr included in the error message if command fails)
func RunCommand(cmd execUtils.Cmd) ([]byte, error) {
	var stderr bytes.Buffer
	cmd.SetStderr(&stderr)
	stdout, err := cmd.Output()
	if err != nil {
		stderrOutput := strings.TrimSpace(stderr.String())
		if stderrOutput != "" {
			err = fmt.Errorf("%w: %s", err, stderrOutput)
		}
	}

	return stdout, err
}
