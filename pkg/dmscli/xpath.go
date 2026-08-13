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
)

const (
	xpathStatusError   = "error"
	xpathStatusPartial = "partial"
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

// XPathStatus is one normalized per-path DMS result.
type XPathStatus struct {
	Path         string `json:"path"`
	Status       string `json:"status"`
	ErrorCode    int    `json:"error_code,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorMessage string `json:"error_msg,omitempty"`
}

// QueryXPathsResult is the normalized result of a typed DMS GET. Values and
// NotSupported are keyed by the requested container or keyed-list XPath.
type QueryXPathsResult struct {
	Status       string
	Values       map[string]map[string]any
	NotSupported map[string][]string
	Results      []XPathStatus
	ErrorMessage string
}

// SetXPathsResult is the normalized result of a typed DMS SET.
type SetXPathsResult struct {
	Status       string
	Results      []XPathStatus
	ErrorMessage string
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

	args := []string{"--json", "-t", target}
	for index, query := range queries {
		if index > 0 {
			args = append(args, ";")
		}
		args = append(args, query.Path)
		args = append(args, query.Leaves...)
	}

	command := execInterface.CommandContext(ctx, dmsCLIExecutable, args...)
	output, commandErr := command.CombinedOutput()
	commandAndArgs := append([]string{dmsCLIExecutable}, args...)
	logr.FromContextOrDiscard(ctx).V(2).Info("command output",
		"command", commandAndArgs,
		"target", target,
		"output", string(output))

	result, decodeErr := decodeQueryXPathsResult(output, queries)
	if commandErr != nil {
		return result, xpathCommandError("query", target, output, resultErrorMessage(result), commandErr)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("decode XPath query result for target %q: %w", target, decodeErr)
	}
	if queryResultFailed(result) {
		detail := result.ErrorMessage
		if detail == "" {
			detail = fmt.Sprintf("unexpected status %q", result.Status)
		}
		return result, fmt.Errorf("query XPaths from target %q: %s", target, detail)
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
	output, commandErr := command.CombinedOutput()
	commandAndArgs := append([]string{dmsCLIExecutable}, args...)
	logr.FromContextOrDiscard(ctx).V(2).Info("command output",
		"command", commandAndArgs,
		"target", target,
		"output", string(output))

	result, decodeErr := decodeSetXPathsResult(output)
	if commandErr != nil {
		return result, xpathCommandError("set", target, output, setResultErrorMessage(result), commandErr)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("decode XPath set result for target %q: %w", target, decodeErr)
	}
	if setResultFailed(result) {
		detail := result.ErrorMessage
		if detail == "" {
			detail = fmt.Sprintf("unexpected status %q", result.Status)
		}
		return result, fmt.Errorf("set XPaths on target %q: %s", target, detail)
	}

	return result, nil
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

type xpathResponse struct {
	Status       string         `json:"status"`
	Values       map[string]any `json:"values"`
	NotSupported []string       `json:"not-supported"`
	Results      []XPathStatus  `json:"results"`
	Error        string         `json:"error"`
	ErrorMessage string         `json:"error_msg"`
}

func decodeQueryXPathsResult(output []byte, queries []XPathQuery) (*QueryXPathsResult, error) {
	root, err := decodeXPathResponseObject(output)
	if err != nil {
		return nil, err
	}
	result := &QueryXPathsResult{
		Status:       "ok",
		Values:       map[string]map[string]any{},
		NotSupported: map[string][]string{},
		Results:      nil,
		ErrorMessage: "",
	}

	if isXPathResponseEnvelope(root) {
		response, err := decodeXPathResponse(root)
		if err != nil {
			return nil, err
		}
		if len(queries) != 1 {
			if response.Status == "ok" {
				return nil, fmt.Errorf("unkeyed successful dms-cli response is ambiguous for %d query paths", len(queries))
			}
			result.Status = response.Status
			result.Results = append(result.Results, response.Results...)
			result.ErrorMessage = responseErrorMessage(response)
			return result, nil
		}
		mergeQueryResponse(result, queries[0].Path, response)
		result.Status = response.Status
		if result.Status == "ok" && len(response.NotSupported) > 0 {
			result.Status = xpathStatusPartial
		}
		return result, nil
	}
	if len(queries) == 1 {
		if _, found := root[queries[0].Path]; !found && !hasXPathKey(root) {
			values := map[string]any{}
			if err := decodeJSON(output, &values); err != nil {
				return nil, err
			}
			result.Values[queries[0].Path] = values
			return result, nil
		}
	}

	succeeded := 0
	failed := 0
	hasPartial := false
	for _, query := range queries {
		raw, found := root[query.Path]
		if !found {
			return nil, fmt.Errorf("dms-cli response does not contain query path %q", query.Path)
		}
		response, err := decodeKeyedQueryResponse(raw)
		if err != nil {
			return nil, fmt.Errorf("decode response for query path %q: %w", query.Path, err)
		}
		mergeQueryResponse(result, query.Path, response)
		if queryResponseFailed(response) {
			failed++
			hasPartial = hasPartial || response.Status == xpathStatusPartial
		} else {
			succeeded++
		}
	}
	switch {
	case failed == 0:
		result.Status = "ok"
	case succeeded > 0 || hasPartial:
		result.Status = xpathStatusPartial
	default:
		result.Status = xpathStatusError
	}
	return result, nil
}

func hasXPathKey(object map[string]json.RawMessage) bool {
	for key := range object {
		if strings.HasPrefix(key, "/") {
			return true
		}
	}
	return false
}

func isXPathResponseEnvelope(object map[string]json.RawMessage) bool {
	if rawStatus, found := object["status"]; found {
		var status string
		if err := json.Unmarshal(rawStatus, &status); err == nil {
			switch status {
			case "ok", xpathStatusError, xpathStatusPartial:
				return true
			}
		}
	}
	return false
}

func decodeKeyedQueryResponse(raw json.RawMessage) (xpathResponse, error) {
	object := map[string]json.RawMessage{}
	if err := decodeJSON(raw, &object); err != nil {
		return xpathResponse{}, err
	}
	if isXPathResponseEnvelope(object) {
		var response xpathResponse
		if err := decodeJSON(raw, &response); err != nil {
			return xpathResponse{}, err
		}
		return response, nil
	}

	values := map[string]any{}
	if err := decodeJSON(raw, &values); err != nil {
		return xpathResponse{}, err
	}
	return xpathResponse{Status: "ok", Values: values}, nil
}

func decodeSetXPathsResult(output []byte) (*SetXPathsResult, error) {
	root, err := decodeXPathResponseObject(output)
	if err != nil {
		return nil, err
	}
	result := &SetXPathsResult{Status: "ok", Results: nil, ErrorMessage: ""}
	if _, hasStatus := root["status"]; hasStatus {
		response, err := decodeXPathResponse(root)
		if err != nil {
			return nil, err
		}
		result.Status = response.Status
		result.Results = response.Results
		result.ErrorMessage = response.ErrorMessage
		if result.ErrorMessage == "" {
			result.ErrorMessage = response.Error
		}
		return result, nil
	}

	paths := make([]string, 0, len(root))
	for path := range root {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	failed := 0
	for _, path := range paths {
		var response xpathResponse
		if err := decodeJSON(root[path], &response); err != nil {
			return nil, fmt.Errorf("decode response for set path %q: %w", path, err)
		}
		status := XPathStatus{
			Path:         path,
			Status:       response.Status,
			Error:        response.Error,
			ErrorMessage: response.ErrorMessage,
		}
		result.Results = append(result.Results, status)
		if response.Status != "ok" {
			failed++
			if result.ErrorMessage == "" {
				result.ErrorMessage = response.ErrorMessage
				if result.ErrorMessage == "" {
					result.ErrorMessage = response.Error
				}
			}
		}
	}
	if failed == len(paths) {
		result.Status = xpathStatusError
	} else if failed > 0 {
		result.Status = xpathStatusPartial
	}
	return result, nil
}

func decodeXPathResponseObject(output []byte) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(output)) == 0 {
		return nil, fmt.Errorf("dms-cli returned an empty response")
	}
	root := map[string]json.RawMessage{}
	if err := decodeJSON(output, &root); err != nil {
		return nil, fmt.Errorf("invalid dms-cli JSON response: %w", err)
	}
	if len(root) == 0 {
		return nil, fmt.Errorf("dms-cli returned an empty JSON object")
	}
	return root, nil
}

func decodeXPathResponse(root map[string]json.RawMessage) (xpathResponse, error) {
	encoded, err := json.Marshal(root)
	if err != nil {
		return xpathResponse{}, err
	}
	var response xpathResponse
	if err := decodeJSON(encoded, &response); err != nil {
		return xpathResponse{}, err
	}
	return response, nil
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

func mergeQueryResponse(result *QueryXPathsResult, path string, response xpathResponse) {
	result.Values[path] = response.Values
	if len(response.NotSupported) > 0 {
		result.NotSupported[path] = append([]string(nil), response.NotSupported...)
	}
	result.Results = append(result.Results, response.Results...)
	if response.ErrorMessage != "" && result.ErrorMessage == "" {
		result.ErrorMessage = response.ErrorMessage
	}
	if response.Error != "" && result.ErrorMessage == "" {
		result.ErrorMessage = response.Error
	}
}

func queryResponseFailed(response xpathResponse) bool {
	if response.Status != "ok" || len(response.NotSupported) > 0 {
		return true
	}
	for _, result := range response.Results {
		if result.Status != "ok" {
			return true
		}
	}
	return false
}

func responseErrorMessage(response xpathResponse) string {
	if response.ErrorMessage != "" {
		return response.ErrorMessage
	}
	return response.Error
}

func queryResultFailed(result *QueryXPathsResult) bool {
	if result == nil || result.Status != "ok" {
		return true
	}
	for _, unsupported := range result.NotSupported {
		if len(unsupported) > 0 {
			return true
		}
	}
	for _, status := range result.Results {
		if status.Status != "ok" {
			return true
		}
	}
	return false
}

func setResultFailed(result *SetXPathsResult) bool {
	if result == nil || result.Status != "ok" {
		return true
	}
	for _, status := range result.Results {
		if status.Status != "ok" {
			return true
		}
	}
	return false
}

func resultErrorMessage(result *QueryXPathsResult) string {
	if result == nil {
		return ""
	}
	return result.ErrorMessage
}

func setResultErrorMessage(result *SetXPathsResult) string {
	if result == nil {
		return ""
	}
	return result.ErrorMessage
}

func xpathCommandError(operation, target string, output []byte, detail string, commandErr error) error {
	if detail == "" {
		detail = strings.TrimSpace(string(output))
	}
	if detail != "" {
		return fmt.Errorf("%s XPaths on target %q: %w: %s", operation, target, commandErr, detail)
	}
	return fmt.Errorf("%s XPaths on target %q: %w", operation, target, commandErr)
}
