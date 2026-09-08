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
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	execUtils "k8s.io/utils/exec"

	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
)

// XPathOperation describes typed intent below one DMS XPath.
type XPathOperation struct {
	Path   string         `json:"path"`
	Values map[string]any `json:"values"`
}

// XPathQuery identifies leaves to read below one DMS XPath.
type XPathQuery struct {
	Path   string
	Leaves []string
}

// QueryXPathsResult is the normalized result of a typed DMS GET.
type QueryXPathsResult struct {
	Status       string
	Values       map[string]map[string]any
	Failures     map[string]any
	ErrorMessage string
	ErrorCode    any
}

// SetXPathsResult is the normalized result of a typed DMS SET.
type SetXPathsResult struct {
	Status       string
	Successes    map[string]any
	Failures     map[string]any
	ErrorMessage string
	ErrorCode    any
}

// QueryXPaths reads one or more leaf batches from a DMS target.
func QueryXPaths(
	ctx context.Context,
	execInterface execUtils.Interface,
	target string,
	queries []XPathQuery,
) (*QueryXPathsResult, error) {
	if execInterface == nil {
		return nil, fmt.Errorf("command executor must not be nil")
	}
	if err := validateXPathQueries(target, queries); err != nil {
		return nil, err
	}

	args := xpathQueryArgs(target, queries)
	command := execInterface.CommandContext(ctx, dmsCLIExecutable, args...)
	output, commandErr := utils.RunCommandWithStreams(command)
	logDMSCLIOutput(ctx, append([]string{dmsCLIExecutable}, args...), target, output)

	if commandErr != nil {
		result, _ := decodeXPathFailure(output.Stdout, queries)
		detail := ""
		if result != nil {
			detail = result.ErrorMessage
		}
		return result, xpathCommandError("query", target, commandErrorDetail(output.Stdout, output.Stderr, detail), commandErr)
	}
	result, err := decodeXPathQuerySuccess(output.Stdout, queries)
	if err != nil {
		return nil, fmt.Errorf("decode XPath query result for target %q: %w", target, err)
	}
	return result, nil
}

// SetXPaths applies an ordered sequence of typed operations to a DMS target.
func SetXPaths(
	ctx context.Context,
	execInterface execUtils.Interface,
	target string,
	operations []XPathOperation,
) (*SetXPathsResult, error) {
	if execInterface == nil {
		return nil, fmt.Errorf("command executor must not be nil")
	}
	args, err := xpathSetArgs(target, operations)
	if err != nil {
		return nil, err
	}

	command := execInterface.CommandContext(ctx, dmsCLIExecutable, args...)
	output, commandErr := utils.RunCommandWithStreams(command)
	logDMSCLIOutput(ctx, append([]string{dmsCLIExecutable}, args...), target, output)

	if commandErr != nil {
		result, _ := decodeXPathSetFailure(output.Stdout)
		detail := ""
		if result != nil {
			detail = result.ErrorMessage
		}
		return result, xpathCommandError("set", target, commandErrorDetail(output.Stdout, output.Stderr, detail), commandErr)
	}
	result, err := decodeXPathSetSuccess(output.Stdout)
	if err != nil {
		return nil, fmt.Errorf("decode XPath set result for target %q: %w", target, err)
	}
	return result, nil
}

func logDMSCLIOutput(ctx context.Context, command []string, target string, output utils.CommandOutput) {
	logr.FromContextOrDiscard(ctx).V(2).Info("command output",
		"command", command,
		"target", target,
		"stdout", boundedCommandOutput(output.Stdout),
		"stderr", boundedCommandOutput(output.Stderr))
}

func xpathQueryArgs(target string, queries []XPathQuery) []string {
	args := []string{"--json", "-t", target}
	for index, query := range queries {
		if index > 0 {
			args = append(args, ";")
		}
		args = append(args, query.Path)
		args = append(args, query.Leaves...)
	}
	return args
}

func validateXPathQueries(target string, queries []XPathQuery) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("XPath target must not be empty")
	}
	if len(queries) == 0 {
		return fmt.Errorf("XPath query must contain at least one path")
	}
	paths := make(map[string]struct{}, len(queries))
	for queryIndex, query := range queries {
		if err := validateXPath(query.Path); err != nil {
			return fmt.Errorf("XPath query at index %d: %w", queryIndex, err)
		}
		if _, found := paths[query.Path]; found {
			return fmt.Errorf("XPath query path %q is duplicated", query.Path)
		}
		paths[query.Path] = struct{}{}
		if len(query.Leaves) == 0 {
			return fmt.Errorf("XPath query %q must contain at least one leaf", query.Path)
		}
		leaves := make(map[string]struct{}, len(query.Leaves))
		for leafIndex, leaf := range query.Leaves {
			if err := validateXPathLeaf(leaf); err != nil {
				return fmt.Errorf("XPath query %q leaf at index %d: %w", query.Path, leafIndex, err)
			}
			if _, found := leaves[leaf]; found {
				return fmt.Errorf("XPath query %q leaf %q is duplicated", query.Path, leaf)
			}
			leaves[leaf] = struct{}{}
		}
	}
	return nil
}

func xpathSetArgs(target string, operations []XPathOperation) ([]string, error) {
	if strings.TrimSpace(target) == "" {
		return nil, fmt.Errorf("XPath target must not be empty")
	}
	if len(operations) == 0 {
		return nil, fmt.Errorf("XPath set must contain at least one operation")
	}

	args := []string{"--json", "-t", target}
	for operationIndex, operation := range operations {
		if err := validateXPath(operation.Path); err != nil {
			return nil, fmt.Errorf("XPath operation at index %d: %w", operationIndex, err)
		}
		if len(operation.Values) == 0 {
			return nil, fmt.Errorf("XPath operation %q must contain at least one value", operation.Path)
		}
		if operationIndex > 0 {
			args = append(args, ";")
		}
		args = append(args, operation.Path)

		leaves := make([]string, 0, len(operation.Values))
		for leaf := range operation.Values {
			leaves = append(leaves, leaf)
		}
		sort.Strings(leaves)
		for _, leaf := range leaves {
			if err := validateXPathLeaf(leaf); err != nil {
				return nil, fmt.Errorf("XPath operation %q: %w", operation.Path, err)
			}
			values, err := formatXPathValues(operation.Values[leaf])
			if err != nil {
				return nil, fmt.Errorf("XPath operation %q leaf %q: %w", operation.Path, leaf, err)
			}
			for _, value := range values {
				args = append(args, leaf+"="+value)
			}
		}
	}
	return args, nil
}

func validateXPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path must not be empty")
	}
	if path != strings.TrimSpace(path) || !strings.HasPrefix(path, "/nvidia/") {
		return fmt.Errorf("path %q must be an absolute /nvidia XPath", path)
	}
	if strings.ContainsAny(path, " \t\r\n;") {
		return fmt.Errorf("path %q contains unsupported characters", path)
	}
	return nil
}

func validateXPathLeaf(leaf string) error {
	if strings.TrimSpace(leaf) == "" {
		return fmt.Errorf("leaf must not be empty")
	}
	if leaf != strings.TrimSpace(leaf) || strings.ContainsAny(leaf, "/=; \t\r\n") {
		return fmt.Errorf("leaf %q contains unsupported characters", leaf)
	}
	return nil
}

func formatXPathValues(value any) ([]string, error) {
	if value == nil {
		return nil, fmt.Errorf("value must not be null")
	}
	reflected := reflect.ValueOf(value)
	if reflected.Kind() != reflect.Array && reflected.Kind() != reflect.Slice {
		formatted, err := formatXPathScalar(value)
		if err != nil {
			return nil, err
		}
		return []string{formatted}, nil
	}

	if reflected.Len() == 0 {
		return nil, fmt.Errorf("leaf-list must contain at least one value")
	}
	result := make([]string, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		formatted, err := formatXPathScalar(reflected.Index(index).Interface())
		if err != nil {
			return nil, fmt.Errorf("leaf-list item at index %d: %w", index, err)
		}
		result[index] = formatted
	}
	return result, nil
}

func formatXPathScalar(value any) (string, error) {
	if value == nil {
		return "", fmt.Errorf("value must not be null")
	}
	if number, ok := value.(json.Number); ok {
		if _, err := strconv.ParseFloat(string(number), 64); err != nil {
			return "", fmt.Errorf("invalid JSON number %q", number)
		}
		return string(number), nil
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.String:
		return reflected.String(), nil
	case reflect.Bool:
		return strconv.FormatBool(reflected.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(reflected.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(reflected.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		floating := reflected.Float()
		if math.IsNaN(floating) || math.IsInf(floating, 0) {
			return "", fmt.Errorf("floating-point value must be finite")
		}
		return strconv.FormatFloat(floating, 'g', -1, reflected.Type().Bits()), nil
	default:
		return "", fmt.Errorf("value must be a string, boolean, or number")
	}
}

type xpathFailureEnvelope struct {
	Status       string         `json:"status"`
	Successes    map[string]any `json:"successes"`
	Failures     map[string]any `json:"failures"`
	ErrorMessage string         `json:"error_msg"`
	ErrorCode    any            `json:"error_code"`
}

func decodeXPathQuerySuccess(output []byte, queries []XPathQuery) (*QueryXPathsResult, error) {
	root, err := decodeJSONObject(output)
	if err != nil {
		return nil, err
	}
	result := &QueryXPathsResult{Status: "ok", Values: make(map[string]map[string]any, len(queries))}
	if len(queries) == 1 {
		values, err := decodeValues(output)
		if err != nil {
			return nil, err
		}
		result.Values[queries[0].Path] = values
		return result, nil
	}
	for _, query := range queries {
		raw, found := root[query.Path]
		if !found {
			return nil, fmt.Errorf("dms-cli response does not contain query path %q", query.Path)
		}
		values, err := decodeValues(raw)
		if err != nil {
			return nil, fmt.Errorf("decode response for query path %q: %w", query.Path, err)
		}
		result.Values[query.Path] = values
	}
	return result, nil
}

func decodeXPathSetSuccess(output []byte) (*SetXPathsResult, error) {
	var response struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(output, &response); err != nil {
		return nil, err
	}
	if response.Status != "ok" {
		return nil, fmt.Errorf("dms-cli response has unexpected status %q", response.Status)
	}
	return &SetXPathsResult{Status: response.Status}, nil
}

func decodeXPathFailure(output []byte, queries []XPathQuery) (*QueryXPathsResult, error) {
	envelope, err := decodeFailureEnvelope(output)
	if err != nil {
		return nil, err
	}
	result := &QueryXPathsResult{
		Status:       envelope.Status,
		Values:       make(map[string]map[string]any, len(queries)),
		Failures:     envelope.Failures,
		ErrorMessage: failureMessage(envelope),
		ErrorCode:    envelope.ErrorCode,
	}
	for _, query := range queries {
		result.Values[query.Path] = map[string]any{}
	}
	for path, value := range envelope.Successes {
		queryPath, leaf, found := matchingQuery(path, queries)
		if !found {
			return nil, fmt.Errorf("dms-cli response contains unexpected success path %q", path)
		}
		result.Values[queryPath][leaf] = value
	}
	return result, nil
}

func decodeXPathSetFailure(output []byte) (*SetXPathsResult, error) {
	envelope, err := decodeFailureEnvelope(output)
	if err != nil {
		return nil, err
	}
	return &SetXPathsResult{
		Status:       envelope.Status,
		Successes:    envelope.Successes,
		Failures:     envelope.Failures,
		ErrorMessage: failureMessage(envelope),
		ErrorCode:    envelope.ErrorCode,
	}, nil
}

func decodeFailureEnvelope(output []byte) (xpathFailureEnvelope, error) {
	var envelope xpathFailureEnvelope
	if err := decodeJSON(output, &envelope); err != nil {
		return envelope, err
	}
	if envelope.Status != "error" && envelope.Status != "partial" {
		return envelope, fmt.Errorf("dms-cli failure response has unexpected status %q", envelope.Status)
	}
	return envelope, nil
}

func failureMessage(envelope xpathFailureEnvelope) string {
	if envelope.ErrorMessage != "" {
		return envelope.ErrorMessage
	}
	if len(envelope.Failures) == 0 {
		return ""
	}
	paths := make([]string, 0, len(envelope.Failures))
	for path := range envelope.Failures {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return formatFailureValue(envelope.Failures[paths[0]])
}

func formatFailureValue(value any) string {
	if message, ok := value.(string); ok {
		return message
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

func matchingQuery(path string, queries []XPathQuery) (string, string, bool) {
	for _, query := range queries {
		prefix := query.Path + "/"
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		leaf := strings.TrimPrefix(path, prefix)
		for _, requestedLeaf := range query.Leaves {
			if leaf == requestedLeaf {
				return query.Path, leaf, true
			}
		}
	}
	return "", "", false
}

func decodeJSONObject(output []byte) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(output)) == 0 {
		return nil, fmt.Errorf("dms-cli returned an empty response")
	}
	root := map[string]json.RawMessage{}
	if err := decodeJSON(output, &root); err != nil {
		return nil, fmt.Errorf("invalid dms-cli JSON response: %w", err)
	}
	return root, nil
}

func decodeValues(raw []byte) (map[string]any, error) {
	values := map[string]any{}
	if err := decodeJSON(raw, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func decodeJSON(content []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("response contains trailing JSON data")
		}
		return fmt.Errorf("decode trailing JSON data: %w", err)
	}
	return nil
}

func xpathCommandError(operation, target, detail string, commandErr error) error {
	if detail != "" {
		return fmt.Errorf("%s XPaths on target %q: %w: %s", operation, target, commandErr, detail)
	}
	return fmt.Errorf("%s XPaths on target %q: %w", operation, target, commandErr)
}
