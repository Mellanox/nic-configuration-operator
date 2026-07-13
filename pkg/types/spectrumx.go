/*
2025 NVIDIA CORPORATION & AFFILIATES
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
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// SpectrumXConfig is the hierarchical config format.
// MlxConfig: multiplaneMode -> deviceId -> {breakout, postBreakout}
type SpectrumXConfig struct {
	MlxConfig              map[string]map[string]SpectrumXDeviceConfig `yaml:"mlxConfig"`
	RuntimeConfig          SpectrumXRuntimeConfig                      `yaml:"runtimeConfig"`
	UseSoftwareCCAlgorithm bool                                        `yaml:"useSoftwareCCAlgorithm"`
	DocaCCVersion          string                                      `yaml:"docaCCVersion"`
}

// SpectrumXDeviceConfig holds per-device mlxconfig parameters.
type SpectrumXDeviceConfig struct {
	Breakout     map[int]map[string]string `yaml:"breakout"`     // breakoutNum -> rawMlxConfig
	PostBreakout map[string]string         `yaml:"postBreakout"` // rawMlxConfig applied after breakout
}

type SpectrumXRuntimeConfig struct {
	Roce              []ConfigurationParameter `yaml:"roce"`
	AdaptiveRouting   []ConfigurationParameter `yaml:"adaptiveRouting"`
	CongestionControl []ConfigurationParameter `yaml:"congestionControl"`
	InterPacketGap    InterPacketGapConfig     `yaml:"interPacketGap"`
}

type InterPacketGapConfig struct {
	PureL3 []ConfigurationParameter `yaml:"pureL3"`
	L3EVPN []ConfigurationParameter `yaml:"l3EVPN"`
}

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

// ParseSpectrumXConfig unmarshals a SpectrumXConfig from raw YAML bytes.
// Used to load profiles delivered via in-cluster ConfigMaps.
func ParseSpectrumXConfig(data []byte) (*SpectrumXConfig, error) {
	spectrumXConfig := &SpectrumXConfig{}

	if err := yaml.Unmarshal(data, spectrumXConfig); err != nil {
		return nil, err
	}
	if err := validateSpectrumXConfig(spectrumXConfig); err != nil {
		return nil, err
	}

	return spectrumXConfig, nil
}

func validateSpectrumXConfig(config *SpectrumXConfig) error {
	if config == nil {
		return nil
	}
	runtimeParams := map[string][]ConfigurationParameter{
		"roce":                  config.RuntimeConfig.Roce,
		"adaptiveRouting":       config.RuntimeConfig.AdaptiveRouting,
		"congestionControl":     config.RuntimeConfig.CongestionControl,
		"interPacketGap.pureL3": config.RuntimeConfig.InterPacketGap.PureL3,
		"interPacketGap.l3EVPN": config.RuntimeConfig.InterPacketGap.L3EVPN,
	}
	for section, params := range runtimeParams {
		for i, param := range params {
			if err := validateRuntimeParameter(section, i, param); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateRuntimeParameter(section string, index int, param ConfigurationParameter) error {
	if param.MlxReg == nil {
		return nil
	}
	paramID := fmt.Sprintf("%s[%d] parameter %q", section, index, param.Name)
	if param.DMSPath != "" {
		return fmt.Errorf("%s cannot define both dmsPath and mlxreg", paramID)
	}
	if param.Value == "" {
		return fmt.Errorf("%s is missing value", paramID)
	}
	if param.MlxReg.Register == "" {
		return fmt.Errorf("%s is missing mlxreg register", paramID)
	}
	if param.MlxReg.Field == "" {
		return fmt.Errorf("%s is missing mlxreg field", paramID)
	}
	if len(param.MlxReg.SetFields) == 0 {
		return fmt.Errorf("%s has no mlxreg setFields", paramID)
	}
	for i, field := range param.MlxReg.SetFields {
		if field.Name == "" {
			return fmt.Errorf("%s has mlxreg setFields[%d] without name", paramID, i)
		}
		if field.Value == "" {
			return fmt.Errorf("%s has mlxreg setFields[%d] without value", paramID, i)
		}
	}
	return nil
}

func LoadSpectrumXConfig(configPath string) (*SpectrumXConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	return ParseSpectrumXConfig(data)
}

const ValuesDoNotMatchErrorPrefix = "values do not match"

func ValuesDoNotMatchError(param ConfigurationParameter, value string) error {
	return fmt.Errorf("%s: %s", ValuesDoNotMatchErrorPrefix, param.Name)
}

func IsValuesDoNotMatchError(err error) bool {
	return strings.HasPrefix(err.Error(), ValuesDoNotMatchErrorPrefix)
}
