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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	execUtils "k8s.io/utils/exec"

	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

const blueprintsPlanPath = "/nvidia/blueprints/plan"

// BlueprintPlanRequest describes one DMS Blueprints planning action.
type BlueprintPlanRequest struct {
	BlueprintsStateDir string
	Profile            string
	Name               string
	Stage              string
	TargetMapFile      string
	Params             []string
}

// BlueprintPlanResult is the normalized result of /nvidia/blueprints/plan.
// PlanJSON contains the complete JSON document returned in the plan-json field.
type BlueprintPlanResult struct {
	Status       string
	PlanJSON     json.RawMessage
	ErrorMessage string
}

// BlueprintTargetUnavailableError identifies a configure plan whose expected
// target is not present in the current live inventory. It does not establish
// whether applying prepare settings will make that target available.
type BlueprintTargetUnavailableError struct {
	cause error
}

func (e *BlueprintTargetUnavailableError) Error() string { return e.cause.Error() }

// Unwrap preserves the original command failure for callers.
func (e *BlueprintTargetUnavailableError) Unwrap() error { return e.cause }

// classifyBlueprintPlanError deliberately uses only a valid structured response.
// An exit code or a diagnostic embedded in raw stderr is not sufficient evidence
// that a failure can be deferred.
func classifyBlueprintPlanError(ctx context.Context, request BlueprintPlanRequest,
	result *BlueprintPlanResult, decodeErr, err error,
) error {
	if request.Stage != "configure" || ctx.Err() != nil || decodeErr != nil || result == nil ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		(result.Status != "error" && result.Status != "failed") {
		return err
	}
	message := strings.TrimSpace(result.ErrorMessage)
	message = strings.TrimPrefix(message, "planning started\n")
	const prefix = "target map resolution failed: target map fingerprint mismatch: no live device matches "
	target, found := strings.CutPrefix(message, prefix)
	// Target IDs are single tokens. Reject trailing diagnostics or additional
	// failures rather than accidentally hiding them behind a retryable error.
	if !found || target == "" || len(strings.Fields(target)) != 1 || strings.TrimSpace(target) != target ||
		strings.ContainsAny(target, "\r\n\t ") {
		return err
	}
	return &BlueprintTargetUnavailableError{cause: err}
}

// GenerateBlueprintPlan invokes the agentless DMS Blueprints planner.
func GenerateBlueprintPlan(
	ctx context.Context,
	execInterface execUtils.Interface,
	request BlueprintPlanRequest,
) (*BlueprintPlanResult, error) {
	if execInterface == nil {
		return nil, fmt.Errorf("command executor must not be nil")
	}
	if err := validateBlueprintPlanRequest(request); err != nil {
		return nil, err
	}

	args := []string{
		jsonOutputFlag,
		blueprintsPlanPath,
		"profile=" + request.Profile,
		"name=" + request.Name,
		"stage=" + request.Stage,
		"target-map-file=file:" + request.TargetMapFile,
	}
	if len(request.Params) > 0 {
		// The doSPCX planner declares params as a StringArray flag: every repeated
		// params=key=value argument appends one element. Comma-joining parameters
		// would pass a single malformed key/value string to the planner.
		for _, param := range request.Params {
			args = append(args, "params="+param)
		}
	}

	command := execInterface.CommandContext(ctx, dmsCLIExecutable, args...)
	environment := environmentWithOverride(os.Environ(), "BP_STATE_DIR", request.BlueprintsStateDir)
	command.SetEnv(environment)
	output, commandErr := utils.RunCommandWithStreams(command)
	commandAndArgs := append([]string{dmsCLIExecutable}, args...)

	result, decodeErr := decodeBlueprintPlanResult(output.Stdout)
	logBlueprintPlanResult(ctx, request, commandAndArgs, result, output, commandErr, decodeErr)
	if commandErr != nil {
		structured := ""
		if result != nil {
			structured = result.ErrorMessage
		}
		detail := commandErrorDetail(output.Stdout, output.Stderr, structured)
		if detail != "" {
			err := fmt.Errorf("generate Blueprint plan %q: %w: %s", request.Name, commandErr, detail)
			return result, classifyBlueprintPlanError(ctx, request, result, decodeErr, err)
		}
		return result, fmt.Errorf("generate Blueprint plan %q: %w", request.Name, commandErr)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("decode Blueprint plan result %q: %w", request.Name, decodeErr)
	}
	if result.Status != "" && result.Status != "ok" {
		detail := boundedCommandOutput([]byte(result.ErrorMessage))
		if detail == "" {
			detail = fmt.Sprintf("unexpected status %q", result.Status)
		}
		err := fmt.Errorf("generate Blueprint plan %q: %s", request.Name, detail)
		return result, classifyBlueprintPlanError(ctx, request, result, decodeErr, err)
	}
	return result, nil
}

func logBlueprintPlanResult(
	ctx context.Context,
	request BlueprintPlanRequest,
	command []string,
	result *BlueprintPlanResult,
	output utils.CommandOutput,
	commandErr error,
	decodeErr error,
) {
	fields := []any{
		commandDurationLogKey, float64(output.Duration.Microseconds()) / 1000,
		commandLogKey, command,
		"plan", request.Name,
		"blueprintsStateDir", request.BlueprintsStateDir,
	}
	if result != nil {
		fields = append(fields,
			"status", result.Status,
			"planJSONBytes", len(result.PlanJSON))
	}
	if commandErr != nil || decodeErr != nil {
		fields = append(fields, "stdout", boundedCommandOutput(output.Stdout))
	}
	fields = appendCommandStderr(fields, output.Stderr, commandErr)
	logr.FromContextOrDiscard(ctx).V(2).Info("command output", fields...)
}

func validateBlueprintPlanRequest(request BlueprintPlanRequest) error {
	if strings.TrimSpace(request.BlueprintsStateDir) == "" {
		return fmt.Errorf("blueprints state directory must not be empty")
	}
	if !filepath.IsAbs(request.BlueprintsStateDir) {
		return fmt.Errorf("blueprints state directory must be an absolute path")
	}
	if strings.TrimSpace(request.Profile) == "" {
		return fmt.Errorf("blueprint profile must not be empty")
	}
	if strings.TrimSpace(request.Name) == "" {
		return fmt.Errorf("blueprint plan name must not be empty")
	}
	if request.Stage != "prepare" && request.Stage != "configure" {
		return fmt.Errorf("blueprint stage must be prepare or configure")
	}
	if strings.TrimSpace(request.TargetMapFile) == "" {
		return fmt.Errorf("blueprint target map file must not be empty")
	}
	for index, param := range request.Params {
		key, _, found := strings.Cut(param, "=")
		if !found || strings.TrimSpace(key) == "" {
			return fmt.Errorf("blueprint parameter at index %d must use key=value syntax", index)
		}
		if strings.Contains(param, ",") {
			return fmt.Errorf("blueprint parameter at index %d must not contain a comma", index)
		}
	}
	return nil
}

func environmentWithOverride(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func decodeBlueprintPlanResult(output []byte) (*BlueprintPlanResult, error) {
	if len(bytes.TrimSpace(output)) == 0 {
		return nil, fmt.Errorf("dms-cli returned an empty response")
	}

	var response struct {
		Status       string          `json:"status"`
		PlanJSON     json.RawMessage `json:"plan-json"`
		Error        string          `json:"error"`
		ErrorMessage string          `json:"error_msg"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("invalid dms-cli JSON response: %w", err)
	}

	result := &BlueprintPlanResult{
		Status:       response.Status,
		ErrorMessage: response.ErrorMessage,
	}
	if result.ErrorMessage == "" {
		result.ErrorMessage = response.Error
	}
	if result.Status != "" && result.Status != "ok" {
		return result, nil
	}
	if len(response.PlanJSON) == 0 || bytes.Equal(bytes.TrimSpace(response.PlanJSON), []byte("null")) {
		return result, fmt.Errorf("dms-cli response does not contain plan-json")
	}

	planJSON := response.PlanJSON
	if bytes.HasPrefix(bytes.TrimSpace(planJSON), []byte(`"`)) {
		var encoded string
		if err := json.Unmarshal(planJSON, &encoded); err != nil {
			return result, fmt.Errorf("invalid encoded plan-json: %w", err)
		}
		planJSON = json.RawMessage(encoded)
	}
	if !json.Valid(planJSON) {
		return result, fmt.Errorf("plan-json is not valid JSON")
	}
	result.PlanJSON = append(json.RawMessage(nil), planJSON...)

	return result, nil
}
