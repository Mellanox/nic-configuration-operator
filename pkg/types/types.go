/*
2024 NVIDIA CORPORATION & AFFILIATES
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

package types

import (
	"fmt"
	"strings"
)

// VPD represents the Vital Product Data of a device
type VPD struct {
	PartNumber   string
	SerialNumber string
	ModelName    string
}

// NvConfigQuery contains a nv config query for a single device, values can contain both the string alias and the numeric value
type NvConfigQuery struct {
	DefaultConfig  map[string][]string
	CurrentConfig  map[string][]string
	NextBootConfig map[string][]string
}

func NewNvConfigQuery() NvConfigQuery {
	return NvConfigQuery{
		DefaultConfig:  make(map[string][]string),
		CurrentConfig:  make(map[string][]string),
		NextBootConfig: make(map[string][]string),
	}
}

const IncorrectSpecErrorPrefix = "incorrect spec"

func IncorrectSpecError(msg string) error {
	return fmt.Errorf("%s: %s", IncorrectSpecErrorPrefix, msg)
}

func IsIncorrectSpecError(err error) bool {
	return strings.HasPrefix(err.Error(), IncorrectSpecErrorPrefix)
}

const FirmwareSourceNotReadyErrorPrefix = "requested firmware source not ready"

func FirmwareSourceNotReadyError(firmwareSourceName, msg string) error {
	return fmt.Errorf("%s (%s): %s", FirmwareSourceNotReadyErrorPrefix, firmwareSourceName, msg)
}

func IsFirmwareSourceNotReadyError(err error) bool {
	return strings.HasPrefix(err.Error(), FirmwareSourceNotReadyErrorPrefix)
}

// ApplyStatus represents the outcome of a configuration apply operation
type ApplyStatus int

const (
	ApplyStatusNothingToDo ApplyStatus = iota
	ApplyStatusSuccess
	ApplyStatusFailed
	ApplyStatusPartiallyApplied
)

// ConfigurationApplyResult represents the result of applying NV configuration
type ConfigurationApplyResult struct {
	Status         ApplyStatus
	RebootRequired bool
	Error          error
}

// RuntimeConfigurationApplyResult represents the result of applying runtime configuration
type RuntimeConfigurationApplyResult struct {
	Status ApplyStatus
	Error  error
}

// FirmwareInstallOptions contains options for firmware installation
type FirmwareInstallOptions struct {
	Version    string // FW version (for K8s cache lookup in operator mode)
	FwFilePath string // Direct path to .bin/.bfb file (library mode)
	SkipReset  bool   // Skip mlxfwreset after burning
}

// ConfigurationOptions contains options for NV configuration apply
type ConfigurationOptions struct {
	SkipReset   bool // Skip mlxfwreset after NV config apply
	WithDefault bool // Add --with_default flag to mlxconfig set
}
