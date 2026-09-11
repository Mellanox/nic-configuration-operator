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

package types

import (
	"fmt"
	"strings"
)

// ConfigurationParameter is retained by the legacy DMS client API. Spectrum-X configuration is
// no longer loaded into this type; doSPCX plans use typed XPathOperation values instead.
type ConfigurationParameter struct {
	Name               string           `yaml:"name,omitempty"`
	MlxConfig          string           `yaml:"mlxconfig,omitempty"`
	Value              string           `yaml:"value,omitempty"`
	ValueType          string           `yaml:"valueType,omitempty"`
	DMSPath            string           `yaml:"dmsPath,omitempty"`
	MlxReg             *MlxRegParameter `yaml:"mlxreg,omitempty"`
	AlternativeValue   string           `yaml:"alternativeValue,omitempty"`
	DeviceId           string           `yaml:"deviceId,omitempty"`
	Breakout           int              `yaml:"breakout,omitempty"`
	Multiplane         string           `yaml:"multiplane,omitempty"`
	IgnoreError        bool             `yaml:"ignoreError,omitempty"`
	HwplbFirstPortOnly bool             `yaml:"hwplbFirstPortOnly,omitempty"`
}

type MlxRegParameter struct {
	Register  string        `yaml:"register,omitempty"`
	Field     string        `yaml:"field,omitempty"`
	SetFields []MlxRegField `yaml:"setFields,omitempty"`
}

type MlxRegField struct {
	Name  string `yaml:"name,omitempty"`
	Value string `yaml:"value,omitempty"`
}

const ValuesDoNotMatchErrorPrefix = "values do not match"

func ValuesDoNotMatchError(param ConfigurationParameter, value string) error {
	return fmt.Errorf("%s: %s", ValuesDoNotMatchErrorPrefix, param.Name)
}

func IsValuesDoNotMatchError(err error) bool {
	return strings.HasPrefix(err.Error(), ValuesDoNotMatchErrorPrefix)
}
