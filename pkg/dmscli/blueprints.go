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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	execUtils "k8s.io/utils/exec"
)

const blueprintsPlanPath = "/nvidia/blueprints/plan"

// BlueprintPlanRequest describes one DMS Blueprints planning action.
type BlueprintPlanRequest struct {
	BlueprintsRoot     string
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
	Status             string
	PlanName           string
	Family             string
	Profile            string
	Stage              string
	Devices            int
	Operations         int
	SemanticGroupCount int
	PlanJSON           json.RawMessage
	ErrorMessage       string
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
		"--json",
		blueprintsPlanPath,
		"profile=" + request.Profile,
		"name=" + request.Name,
		"stage=" + request.Stage,
		"target-map-file=file:" + request.TargetMapFile,
	}
	if len(request.Params) > 0 {
		// The DMS action input transport used by the daemon image applies
		// last-value-wins semantics to repeated leaf-list arguments. Keep the
		// complete planner parameter list in one action argument; the compatible
		// Blueprints action wrapper expands this comma-separated value.
		args = append(args, "params="+strings.Join(request.Params, ","))
	}

	command := execInterface.CommandContext(ctx, dmsCLIExecutable, args...)
	environment := environmentWithOverride(os.Environ(), "BLUEPRINTS_ROOT", request.BlueprintsRoot)
	environment = environmentWithOverride(environment, "BLUEPRINTS_STATE_DIR", request.BlueprintsStateDir)
	command.SetEnv(environment)
	output, commandErr := command.CombinedOutput()
	commandAndArgs := append([]string{dmsCLIExecutable}, args...)
	logr.FromContextOrDiscard(ctx).V(2).Info("command output",
		"command", commandAndArgs,
		"plan", request.Name,
		"blueprintsRoot", request.BlueprintsRoot,
		"blueprintsStateDir", request.BlueprintsStateDir,
		"output", string(output))

	result, decodeErr := decodeBlueprintPlanResult(output)
	if commandErr != nil {
		detail := strings.TrimSpace(string(output))
		if result != nil && result.ErrorMessage != "" {
			detail = result.ErrorMessage
		}
		if detail != "" {
			return result, fmt.Errorf("generate Blueprint plan %q: %w: %s", request.Name, commandErr, detail)
		}
		return result, fmt.Errorf("generate Blueprint plan %q: %w", request.Name, commandErr)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("decode Blueprint plan result %q: %w", request.Name, decodeErr)
	}
	if result.Status != "" && result.Status != "ok" {
		detail := result.ErrorMessage
		if detail == "" {
			detail = fmt.Sprintf("unexpected status %q", result.Status)
		}
		return result, fmt.Errorf("generate Blueprint plan %q: %s", request.Name, detail)
	}
	if result.PlanName != "" && result.PlanName != request.Name {
		return result, fmt.Errorf("generate Blueprint plan %q: response contains plan %q", request.Name, result.PlanName)
	}

	return result, nil
}

func validateBlueprintPlanRequest(request BlueprintPlanRequest) error {
	if strings.TrimSpace(request.BlueprintsRoot) == "" {
		return fmt.Errorf("blueprints root must not be empty")
	}
	if !filepath.IsAbs(request.BlueprintsRoot) {
		return fmt.Errorf("blueprints root must be an absolute path")
	}
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
		Status             string          `json:"status"`
		PlanName           string          `json:"plan"`
		Family             string          `json:"family"`
		Profile            string          `json:"profile"`
		Stage              string          `json:"stage"`
		Devices            int             `json:"devices"`
		Operations         int             `json:"operations"`
		SemanticGroupCount int             `json:"semantic_group_count"`
		PlanJSON           json.RawMessage `json:"plan-json"`
		Error              string          `json:"error"`
		ErrorMessage       string          `json:"error_msg"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("invalid dms-cli JSON response: %w", err)
	}

	result := &BlueprintPlanResult{
		Status:             response.Status,
		PlanName:           response.PlanName,
		Family:             response.Family,
		Profile:            response.Profile,
		Stage:              response.Stage,
		Devices:            response.Devices,
		Operations:         response.Operations,
		SemanticGroupCount: response.SemanticGroupCount,
		ErrorMessage:       response.ErrorMessage,
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
