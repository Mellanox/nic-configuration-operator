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

type SpectrumXConfig struct {
	MultiplaneConfig       SpectrumXMultiplaneConfig `yaml:"multiplane"`
	NVConfig               []ConfigurationParameter  `yaml:"nvConfig"`
	RuntimeConfig          SpectrumXRuntimeConfig    `yaml:"runtimeConfig"`
	UseSoftwareCCAlgorithm bool                      `yaml:"useSoftwareCCAlgorithm"`
	DocaCCVersion          string                    `yaml:"docaCCVersion"`
}

type SpectrumXMultiplaneConfig struct {
	Swplb    map[int][]ConfigurationParameter `yaml:"swplb"`
	Hwplb    map[int][]ConfigurationParameter `yaml:"hwplb"`
	Uniplane map[int][]ConfigurationParameter `yaml:"uniplane"`
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
	Name             string `yaml:"name,omitempty"`
	MlxConfig        string `yaml:"mlxconfig,omitempty"`
	Value            string `yaml:"value,omitempty"`
	ValueType        string `yaml:"valueType,omitempty"`
	DMSPath          string `yaml:"dmsPath,omitempty"`
	AlternativeValue string `yaml:"alternativeValue,omitempty"`
	DeviceId         string `yaml:"deviceId,omitempty"`
	Breakout         int    `yaml:"breakout,omitempty"`
	Multiplane       string `yaml:"multiplane,omitempty"`
	IgnoreError      bool   `yaml:"ignoreError,omitempty"`
}

func LoadSpectrumXConfig(configPath string) (*SpectrumXConfig, error) {
	spectrumXConfig := &SpectrumXConfig{}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	if err := yaml.Unmarshal(data, spectrumXConfig); err != nil {
		return nil, err
	}

	return spectrumXConfig, nil
}

const ValuesDoNotMatchErrorPrefix = "values do not match"

func ValuesDoNotMatchError(param ConfigurationParameter, value string) error {
	return fmt.Errorf("%s: %s", ValuesDoNotMatchErrorPrefix, param.Name)
}

func IsValuesDoNotMatchError(err error) bool {
	return strings.HasPrefix(err.Error(), ValuesDoNotMatchErrorPrefix)
}
