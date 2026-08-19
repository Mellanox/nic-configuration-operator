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

// Package dmscli provides operations backed by the agentless dms-cli executable.
package dmscli

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-logr/logr"
	execUtils "k8s.io/utils/exec"
)

const (
	dmsCLIExecutable  = "/opt/mellanox/doca/services/dms/dms-cli"
	nvConfigApplyPath = "/nvidia/nvconfig/apply"
)

// NVConfigXPathOperation describes typed NVConfig intent using a DMS XPath and
// the values to apply below that path.
type NVConfigXPathOperation struct {
	Path   string         `json:"path"`
	Values map[string]any `json:"values"`
}

// NVConfigParam is one raw native NVConfig assignment.
type NVConfigParam struct {
	Param string `json:"param"`
	Value any    `json:"value"`
}

// ApplyNVConfigRequest is the payload for /nvidia/nvconfig/apply. Target is
// supplied through dms-cli's -t flag and is therefore excluded from the JSON
// payload. Ports provide fanout context for typed mappings containing {port}.
type ApplyNVConfigRequest struct {
	Target      string                   `json:"-"`
	Ports       []int                    `json:"ports,omitempty"`
	Typed       []NVConfigXPathOperation `json:"typed,omitempty"`
	Raw         []NVConfigParam          `json:"raw,omitempty"`
	WithDefault bool                     `json:"with-default"`
	Force       bool                     `json:"force"`
}

// ApplyNVConfigResult is the command-level result returned by DMS after the
// compiled NVConfig batch is executed.
type ApplyNVConfigResult struct {
	Status        string   `json:"status"`
	PrimaryTarget string   `json:"primary-target,omitempty"`
	CompiledCount int      `json:"compiled-count,omitempty"`
	RequiresReset bool     `json:"requires-reset,omitempty"`
	WithDefault   bool     `json:"with-default,omitempty"`
	Force         bool     `json:"force,omitempty"`
	Params        []string `json:"params,omitempty"`
	ErrorMessage  string   `json:"error_msg,omitempty"`
}

// ApplyNVConfig applies one typed/raw NVConfig batch to a PCI target.
func ApplyNVConfig(ctx context.Context, execInterface execUtils.Interface, request ApplyNVConfigRequest) (*ApplyNVConfigResult, error) {
	if execInterface == nil {
		return nil, fmt.Errorf("command executor must not be nil")
	}
	if err := validateApplyNVConfigRequest(request); err != nil {
		return nil, err
	}

	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("marshal NVConfig request for target %q: %w", request.Target, err)
	}

	args := []string{
		"--json",
		"-t", request.Target,
		"--input", string(payload),
		nvConfigApplyPath,
	}
	command := execInterface.CommandContext(ctx, dmsCLIExecutable, args...)
	output, commandErr := command.CombinedOutput()
	commandAndArgs := append([]string{dmsCLIExecutable}, args...)
	logr.FromContextOrDiscard(ctx).V(2).Info("command output",
		"command", commandAndArgs,
		"target", request.Target,
		"output", string(output))

	result, decodeErr := decodeApplyNVConfigResult(output)
	if commandErr != nil {
		detail := strings.TrimSpace(string(output))
		if result != nil && result.ErrorMessage != "" {
			detail = result.ErrorMessage
		}
		if detail != "" {
			return result, fmt.Errorf("apply NVConfig to target %q: %w: %s", request.Target, commandErr, detail)
		}
		return result, fmt.Errorf("apply NVConfig to target %q: %w", request.Target, commandErr)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("decode NVConfig apply result for target %q: %w", request.Target, decodeErr)
	}
	if result.Status != "ok" {
		detail := result.ErrorMessage
		if detail == "" {
			detail = fmt.Sprintf("unexpected status %q", result.Status)
		}
		return result, fmt.Errorf("apply NVConfig to target %q: %s", request.Target, detail)
	}

	return result, nil
}

func validateApplyNVConfigRequest(request ApplyNVConfigRequest) error {
	if strings.TrimSpace(request.Target) == "" {
		return fmt.Errorf("NVConfig target must not be empty")
	}
	if len(request.Typed) == 0 && len(request.Raw) == 0 {
		return fmt.Errorf("NVConfig request must contain at least one typed or raw operation")
	}
	for index, port := range request.Ports {
		if port < 1 || port > 255 {
			return fmt.Errorf("NVConfig port at index %d must be between 1 and 255", index)
		}
	}
	for index, operation := range request.Typed {
		if strings.TrimSpace(operation.Path) == "" {
			return fmt.Errorf("typed NVConfig operation at index %d must have a path", index)
		}
		if operation.Values == nil {
			return fmt.Errorf("typed NVConfig operation at index %d must have a values object", index)
		}
		if len(operation.Values) == 0 {
			return fmt.Errorf("typed NVConfig operation at index %d must have at least one value", index)
		}
	}
	for index, param := range request.Raw {
		if strings.TrimSpace(param.Param) == "" {
			return fmt.Errorf("raw NVConfig parameter at index %d must have a name", index)
		}
		if !isJSONScalar(param.Value) {
			return fmt.Errorf("raw NVConfig parameter %q must have a string, boolean, or numeric value", param.Param)
		}
	}
	return nil
}

func isJSONScalar(value any) bool {
	if value == nil {
		return false
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func decodeApplyNVConfigResult(output []byte) (*ApplyNVConfigResult, error) {
	if len(strings.TrimSpace(string(output))) == 0 {
		return nil, fmt.Errorf("dms-cli returned an empty response")
	}

	result := &ApplyNVConfigResult{}
	if err := json.Unmarshal(output, result); err != nil {
		return nil, fmt.Errorf("invalid dms-cli JSON response: %w", err)
	}
	return result, nil
}
