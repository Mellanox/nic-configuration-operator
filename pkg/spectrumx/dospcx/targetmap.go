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

package dospcx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// loadVRTargetMap preserves the VR topology authored in the installed data bundle.
// PCI addresses must match the selected NICs exactly; rail membership cannot be
// inferred from PCI ordering or physical Network Bay pairing.
// The caller holds planMutex to serialize this read with data bundle replacement.
func (m *Manager) loadVRTargetMap(devices []planDevice) (targetMap, error) {
	path := filepath.Join(m.dospcxDataRoot, "target-maps", "VR-SPX_Multiplane.yaml")
	content, err := os.ReadFile(path)
	if err != nil {
		return targetMap{}, fmt.Errorf("read VR reference target map %q: %w", path, err)
	}
	var reference targetMap
	if err := yaml.Unmarshal(content, &reference); err != nil {
		return targetMap{}, fmt.Errorf("decode VR reference target map %q: %w", path, err)
	}
	selected := make(map[string]struct{}, len(devices))
	for _, device := range devices {
		selected[device.bdf] = struct{}{}
	}
	seen := make(map[string]struct{}, len(reference.PreBreakout.Targets))
	for i := range reference.PreBreakout.Targets {
		target := &reference.PreBreakout.Targets[i]
		target.BDF = strings.ToLower(strings.TrimSpace(target.BDF))
		if !isCanonicalFunctionZeroBDF(target.BDF) {
			return targetMap{}, fmt.Errorf("VR reference target map %q has invalid function-zero PCI address %q", path, target.BDF)
		}
		if _, duplicate := seen[target.BDF]; duplicate {
			return targetMap{}, fmt.Errorf("VR reference target map %q has duplicate PCI address %q", path, target.BDF)
		}
		seen[target.BDF] = struct{}{}
		if _, found := selected[target.BDF]; !found {
			return targetMap{}, fmt.Errorf("VR reference target map %q PCI address %q is not selected for the Spectrum-X plan", path, target.BDF)
		}
	}
	for _, device := range devices {
		if _, found := seen[device.bdf]; !found {
			return targetMap{}, fmt.Errorf("selected PCI address %q is missing from VR reference target map %q", device.bdf, path)
		}
	}
	return reference, nil
}

// buildTargetMap assigns one rail per PCI-sorted device for non-VR platforms.
func buildTargetMap(config *planConfig) targetMap {
	targets := targetMap{
		SchemaVersion: targetMapSchemaVersion,
		PlatformType:  config.platformType,
		PreBreakout:   targetMapTarget{Targets: make([]preBreakoutTarget, 0, len(config.devices))},
	}
	for rail, device := range config.devices {
		targets.PreBreakout.Targets = append(targets.PreBreakout.Targets, preBreakoutTarget{
			ID:       fmt.Sprintf("ew-rail%d-prebreakout", rail),
			BDF:      device.bdf,
			DeviceID: device.deviceID,
			Role:     targetRoleEW,
			Rail:     rail,
		})
	}
	return targets
}
