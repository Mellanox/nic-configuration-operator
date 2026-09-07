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

package spectrumx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/dmscli"
)

const (
	defaultBlueprintsRoot     = "/opt/nvidia/blueprints"
	defaultBlueprintsStateDir = "/var/lib/blueprints"
	defaultDospcxDataRoot     = "/opt/mellanox/doca/services/dms/doSpcx/data"
	deploymentModeHostK8s     = "host-k8s"
)

// PlanStage identifies the doSPCX configuration phase represented by a plan.
type PlanStage string

const (
	// PlanStagePrepare contains persistent configuration applied before reboot.
	PlanStagePrepare PlanStage = "prepare"
	// PlanStageConfigure contains runtime configuration applied after reboot.
	PlanStageConfigure PlanStage = "configure"
)

// Plan is a generated doSPCX plan retrieved from the local plan store.
type Plan struct {
	Name     string
	Stage    PlanStage
	JSON     json.RawMessage
	Semantic *SemanticPlan
}

// PlanManager owns doSPCX target-map construction, plan generation, caching,
// persistence, and retrieval.
type PlanManager interface {
	PreparePlan(ctx context.Context, devices []*v1alpha1.NicDevice, stage PlanStage) error
	GetPreparedPlan(device *v1alpha1.NicDevice, stage PlanStage) (*Plan, error)
}

var canonicalFunctionZeroBDF = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:([0-9a-f]{2})\.0$`)

type targetMap struct {
	SchemaVersion     int                         `json:"schema_version"`
	PlatformType      string                      `json:"platform_type"`
	DefaultRole       string                      `json:"default_role"`
	TargetConstraints map[string]targetConstraint `json:"target_constraints"`
	PreBreakout       targetMapTarget             `json:"pre_breakout"`
}

type targetTopology struct {
	PortsPerTarget int `json:"ports_per_target"`
}

type targetConstraint struct {
	HCATypes []string       `json:"hca_types"`
	Topology targetTopology `json:"topology"`
}

type targetMapTarget struct {
	Targets []preBreakoutTarget `json:"targets"`
}

type preBreakoutTarget struct {
	BDF  string `json:"bdf"`
	Rail int    `json:"rail"`
}

type planConfig struct {
	nodeName      string
	platformType  string
	profile       string
	version       string
	multiplane    string
	overlay       string
	planes        int
	targetMap     targetMap
	selectedCount int
}

// planMetadata contains only the inputs used to generate a doSPCX plan.
// Exact equality with the current inputs allows the saved plan to be reused.
type planMetadata struct {
	BlueprintsRoot       string   `json:"blueprints_root"`
	BlueprintsStateDir   string   `json:"blueprints_state_dir"`
	BlueprintsDataDigest string   `json:"blueprints_data_digest,omitempty"`
	PlanName             string   `json:"plan_name"`
	Stage                string   `json:"stage"`
	Profile              string   `json:"profile"`
	PlatformType         string   `json:"platform_type"`
	SpectrumXVersion     string   `json:"spectrum_x_version"`
	MultiplaneMode       string   `json:"multiplane_mode"`
	Overlay              string   `json:"overlay"`
	Planes               int      `json:"planes"`
	DeploymentMode       string   `json:"deployment_mode"`
	Parameters           []string `json:"parameters"`
	TargetMapFile        string   `json:"target_map_file"`
	TargetMapDigest      string   `json:"target_map_digest"`
}

// PreparePlan ensures that a matching node-scoped plan is cached for the
// requested phase. It is a no-op when none of the devices enable Spectrum-X.
func (m *spectrumXConfigManager) PreparePlan(
	ctx context.Context,
	devices []*v1alpha1.NicDevice,
	stage PlanStage,
) error {
	if !hasSpectrumXEnabledDevice(devices) {
		return nil
	}
	if m == nil || m.execInterface == nil {
		return fmt.Errorf("command executor must not be nil")
	}
	if err := validatePlanStage(stage); err != nil {
		return err
	}
	if strings.TrimSpace(m.blueprintsRoot) == "" || !filepath.IsAbs(m.blueprintsRoot) {
		return fmt.Errorf("blueprints root must be a non-empty absolute path")
	}
	stateDir := m.resolvedStateDir()
	if !filepath.IsAbs(stateDir) {
		return fmt.Errorf("blueprints state directory must be an absolute path")
	}

	config, err := buildPlanConfig(devices)
	if err != nil {
		return err
	}
	params, err := planParameters(config)
	if err != nil {
		return err
	}

	m.planMutex.Lock()
	defer m.planMutex.Unlock()

	generatedPlanName := planName(config.nodeName, stage)
	targetMapPath := filepath.Join(stateDir, "target-maps", targetMapName(config.nodeName)+".json")
	targetMapContent, err := marshalJSONFile(config.targetMap)
	if err != nil {
		return fmt.Errorf("marshal doSPCX target map: %w", err)
	}
	planPath := filepath.Join(stateDir, "plans", generatedPlanName, "plan.json")
	metadataPath := filepath.Join(filepath.Dir(planPath), "metadata.json")
	metadata := planMetadata{
		BlueprintsRoot:       m.blueprintsRoot,
		BlueprintsStateDir:   stateDir,
		BlueprintsDataDigest: m.dospcxDataDigest,
		PlanName:             generatedPlanName,
		Stage:                string(stage),
		Profile:              config.profile,
		PlatformType:         config.platformType,
		SpectrumXVersion:     config.version,
		MultiplaneMode:       config.multiplane,
		Overlay:              config.overlay,
		Planes:               config.planes,
		DeploymentMode:       deploymentModeHostK8s,
		Parameters:           append([]string(nil), params...),
		TargetMapFile:        targetMapPath,
		TargetMapDigest:      sha256Digest(targetMapContent),
	}
	if reusable, reason := reusablePlan(planPath, metadataPath, targetMapPath, metadata, config); reusable {
		log.FromContext(ctx).V(2).Info("reusing saved doSPCX plan",
			"node", config.nodeName,
			"stage", stage,
			"plan", planPath,
			"metadata", metadataPath)
		return nil
	} else {
		log.FromContext(ctx).V(2).Info("saved doSPCX plan cannot be reused",
			"node", config.nodeName,
			"stage", stage,
			"reason", reason)
	}

	if err := writeFileAtomic(targetMapPath, targetMapContent); err != nil {
		return fmt.Errorf("write doSPCX target map %q: %w", targetMapPath, err)
	}

	result, err := dmscli.GenerateBlueprintPlan(ctx, m.execInterface, dmscli.BlueprintPlanRequest{
		BlueprintsRoot:     m.blueprintsRoot,
		BlueprintsStateDir: stateDir,
		Profile:            config.profile,
		Name:               generatedPlanName,
		Stage:              string(stage),
		TargetMapFile:      targetMapPath,
		Params:             params,
	})
	if err != nil {
		return err
	}
	if _, err := validateGeneratedPlan(result.PlanJSON, generatedPlanName, config, string(stage)); err != nil {
		return err
	}

	if err := writeRawJSONFileAtomic(planPath, result.PlanJSON); err != nil {
		return fmt.Errorf("write doSPCX %s plan %q: %w", stage, planPath, err)
	}
	if err := writeJSONFileAtomic(metadataPath, metadata); err != nil {
		return fmt.Errorf("write doSPCX %s plan metadata %q: %w", stage, metadataPath, err)
	}
	log.FromContext(ctx).Info("generated doSPCX plan",
		"node", config.nodeName,
		"stage", stage,
		"profile", config.profile,
		"platformType", config.platformType,
		"devices", config.selectedCount,
		"blueprintsRoot", m.blueprintsRoot,
		"targetMap", targetMapPath,
		"plan", planPath,
		"metadata", metadataPath)

	return nil
}

// GetPreparedPlan retrieves a cached plan only when it still matches the
// supplied Spectrum-X device and includes that device in its target map.
func (m *spectrumXConfigManager) GetPreparedPlan(device *v1alpha1.NicDevice, stage PlanStage) (*Plan, error) {
	if m == nil {
		return nil, fmt.Errorf("plan manager must not be nil")
	}
	if err := validatePlanStage(stage); err != nil {
		return nil, err
	}
	if strings.TrimSpace(m.blueprintsRoot) == "" || !filepath.IsAbs(m.blueprintsRoot) {
		return nil, fmt.Errorf("blueprints root must be a non-empty absolute path")
	}
	if !spectrumXEnabledForPlan(device) {
		return nil, fmt.Errorf("device does not enable Spectrum-X optimization")
	}

	config, err := buildPlanConfig([]*v1alpha1.NicDevice{device})
	if err != nil {
		return nil, err
	}
	params, err := planParameters(config)
	if err != nil {
		return nil, err
	}

	m.planMutex.RLock()
	defer m.planMutex.RUnlock()

	stateDir := m.resolvedStateDir()
	if !filepath.IsAbs(stateDir) {
		return nil, fmt.Errorf("blueprints state directory must be an absolute path")
	}
	generatedPlanName := planName(config.nodeName, stage)
	targetMapPath := filepath.Join(stateDir, "target-maps", targetMapName(config.nodeName)+".json")
	metadataPath := filepath.Join(stateDir, "plans", generatedPlanName, "metadata.json")
	metadataContent, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("read doSPCX %s plan metadata %q: %w", stage, metadataPath, err)
	}
	var metadata planMetadata
	if err := json.Unmarshal(metadataContent, &metadata); err != nil {
		return nil, fmt.Errorf("decode doSPCX %s plan metadata %q: %w", stage, metadataPath, err)
	}
	expectedMetadata := planMetadata{
		BlueprintsRoot:       m.blueprintsRoot,
		BlueprintsStateDir:   stateDir,
		BlueprintsDataDigest: m.dospcxDataDigest,
		PlanName:             generatedPlanName,
		Stage:                string(stage),
		Profile:              config.profile,
		PlatformType:         config.platformType,
		SpectrumXVersion:     config.version,
		MultiplaneMode:       config.multiplane,
		Overlay:              config.overlay,
		Planes:               config.planes,
		DeploymentMode:       deploymentModeHostK8s,
		Parameters:           params,
		TargetMapFile:        targetMapPath,
		TargetMapDigest:      metadata.TargetMapDigest,
	}
	if metadata.TargetMapDigest == "" || !reflect.DeepEqual(metadata, expectedMetadata) {
		return nil, fmt.Errorf("cached doSPCX %s plan does not match device %q inputs", stage, device.Name)
	}

	targetMapContent, err := os.ReadFile(targetMapPath)
	if err != nil {
		return nil, fmt.Errorf("read doSPCX target map %q: %w", targetMapPath, err)
	}
	if sha256Digest(targetMapContent) != metadata.TargetMapDigest {
		return nil, fmt.Errorf("cached doSPCX %s plan target map digest does not match", stage)
	}
	var savedTargetMap targetMap
	if err := json.Unmarshal(targetMapContent, &savedTargetMap); err != nil {
		return nil, fmt.Errorf("decode doSPCX target map %q: %w", targetMapPath, err)
	}
	if err := validateDeviceInTargetMap(config, savedTargetMap); err != nil {
		return nil, fmt.Errorf("cached doSPCX %s plan does not match device %q: %w", stage, device.Name, err)
	}

	planPath := filepath.Join(stateDir, "plans", generatedPlanName, "plan.json")
	planContent, err := os.ReadFile(planPath)
	if err != nil {
		return nil, fmt.Errorf("read doSPCX %s plan %q: %w", stage, planPath, err)
	}
	savedConfig := &planConfig{
		nodeName:      config.nodeName,
		platformType:  metadata.PlatformType,
		profile:       metadata.Profile,
		version:       metadata.SpectrumXVersion,
		multiplane:    metadata.MultiplaneMode,
		overlay:       metadata.Overlay,
		planes:        metadata.Planes,
		targetMap:     savedTargetMap,
		selectedCount: len(savedTargetMap.PreBreakout.Targets),
	}
	semanticPlan, err := validateGeneratedPlan(planContent, generatedPlanName, savedConfig, string(stage))
	if err != nil {
		return nil, fmt.Errorf("validate cached doSPCX %s plan: %w", stage, err)
	}

	return &Plan{
		Name:     generatedPlanName,
		Stage:    stage,
		JSON:     append(json.RawMessage(nil), planContent...),
		Semantic: semanticPlan,
	}, nil
}

func planParameters(config *planConfig) ([]string, error) {
	params := make([]string, 0, 3)
	params = append(params,
		"deployment_mode="+deploymentModeHostK8s,
		"planes="+strconv.Itoa(config.planes))
	if config.overlay == "" {
		return params, nil
	}
	if config.profile == "hwmp" {
		if config.overlay != consts.OverlayNone {
			return nil, fmt.Errorf("doSPCX profile hwmp does not support overlay %q", config.overlay)
		}
		return params, nil
	}
	return append(params, "overlay="+config.overlay), nil
}

func validateDeviceInTargetMap(config *planConfig, saved targetMap) error {
	if saved.SchemaVersion != 3 || saved.PlatformType != config.platformType || saved.DefaultRole != "ew" {
		return fmt.Errorf("target map identity changed")
	}
	constraint, found := saved.TargetConstraints[saved.DefaultRole]
	if !found || constraint.Topology.PortsPerTarget != 1 {
		return fmt.Errorf("target map constraint changed")
	}
	expectedHCA := config.targetMap.TargetConstraints[config.targetMap.DefaultRole].HCATypes[0]
	hcaFound := false
	for _, hcaType := range constraint.HCATypes {
		if hcaType == expectedHCA {
			hcaFound = true
			break
		}
	}
	if !hcaFound {
		return fmt.Errorf("HCA type %q is absent from the target map", expectedHCA)
	}
	expectedBDF := config.targetMap.PreBreakout.Targets[0].BDF
	for _, target := range saved.PreBreakout.Targets {
		if target.BDF == expectedBDF {
			return nil
		}
	}
	return fmt.Errorf("BDF %q is absent from the target map", expectedBDF)
}

func (m *spectrumXConfigManager) resolvedStateDir() string {
	if strings.TrimSpace(m.blueprintsStateDir) != "" {
		return m.blueprintsStateDir
	}
	if stateDir := strings.TrimSpace(os.Getenv("BLUEPRINTS_STATE_DIR")); stateDir != "" {
		return stateDir
	}
	return defaultBlueprintsStateDir
}

func validatePlanStage(stage PlanStage) error {
	switch stage {
	case PlanStagePrepare, PlanStageConfigure:
		return nil
	default:
		return fmt.Errorf("unsupported doSPCX plan stage %q", stage)
	}
}

func hasSpectrumXEnabledDevice(devices []*v1alpha1.NicDevice) bool {
	for _, device := range devices {
		if spectrumXEnabledForPlan(device) {
			return true
		}
	}
	return false
}

func reusablePlan(
	planPath string,
	metadataPath string,
	targetMapPath string,
	expectedMetadata planMetadata,
	config *planConfig,
) (bool, string) {
	metadataContent, err := os.ReadFile(metadataPath)
	if err != nil {
		return false, fmt.Sprintf("read metadata: %v", err)
	}
	var savedMetadata planMetadata
	if err := json.Unmarshal(metadataContent, &savedMetadata); err != nil {
		return false, fmt.Sprintf("decode metadata: %v", err)
	}
	if !reflect.DeepEqual(savedMetadata, expectedMetadata) {
		return false, "planner inputs changed"
	}

	targetMapContent, err := os.ReadFile(targetMapPath)
	if err != nil {
		return false, fmt.Sprintf("read target map: %v", err)
	}
	if sha256Digest(targetMapContent) != expectedMetadata.TargetMapDigest {
		return false, "target map content changed"
	}

	planContent, err := os.ReadFile(planPath)
	if err != nil {
		return false, fmt.Sprintf("read plan: %v", err)
	}
	if _, err := validateGeneratedPlan(planContent, expectedMetadata.PlanName, config, expectedMetadata.Stage); err != nil {
		return false, fmt.Sprintf("validate plan: %v", err)
	}
	return true, ""
}

func buildPlanConfig(devices []*v1alpha1.NicDevice) (*planConfig, error) {
	selected := make([]*v1alpha1.NicDevice, 0, len(devices))
	for _, device := range devices {
		if spectrumXEnabledForPlan(device) {
			selected = append(selected, device)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no Spectrum-X-enabled devices were supplied for doSPCX planning")
	}

	firstSpec := selected[0].Spec.Configuration.Template.SpectrumXOptimized
	nodeName := strings.TrimSpace(selected[0].Status.Node)
	if nodeName == "" {
		return nil, fmt.Errorf("Spectrum-X device %q has no node name", selected[0].Name)
	}
	profile, err := blueprintProfile(firstSpec.MultiplaneMode)
	if err != nil {
		return nil, err
	}
	planes := firstSpec.NumberOfPlanes
	if planes == 0 {
		planes = 1
	}
	config := &planConfig{
		nodeName:      nodeName,
		platformType:  firstSpec.PlatformType,
		profile:       profile,
		version:       firstSpec.Version,
		multiplane:    normalizedMultiplaneMode(firstSpec.MultiplaneMode),
		overlay:       firstSpec.Overlay,
		planes:        planes,
		selectedCount: len(selected),
		targetMap: targetMap{
			SchemaVersion: 3,
			PlatformType:  firstSpec.PlatformType,
			DefaultRole:   "ew",
			TargetConstraints: map[string]targetConstraint{
				"ew": {Topology: targetTopology{PortsPerTarget: 1}},
			},
			PreBreakout: targetMapTarget{Targets: make([]preBreakoutTarget, 0, len(selected))},
		},
	}
	if strings.TrimSpace(config.platformType) == "" {
		return nil, fmt.Errorf("Spectrum-X platformType must not be empty")
	}

	type orderedDevice struct {
		bdf string
	}
	ordered := make([]orderedDevice, 0, len(selected))
	seenBDFs := map[string]string{}
	hcaTypes := map[string]struct{}{}
	for _, device := range selected {
		if strings.TrimSpace(device.Status.Node) != config.nodeName {
			return nil, fmt.Errorf("Spectrum-X devices in one plan must belong to the same node; device %q belongs to %q", device.Name, device.Status.Node)
		}
		spec := device.Spec.Configuration.Template.SpectrumXOptimized
		deviceProfile, profileErr := blueprintProfile(spec.MultiplaneMode)
		if profileErr != nil {
			return nil, fmt.Errorf("device %q: %w", device.Name, profileErr)
		}
		devicePlanes := spec.NumberOfPlanes
		if devicePlanes == 0 {
			devicePlanes = 1
		}
		if spec.PlatformType != config.platformType ||
			deviceProfile != config.profile ||
			spec.Version != config.version ||
			normalizedMultiplaneMode(spec.MultiplaneMode) != config.multiplane ||
			spec.Overlay != config.overlay ||
			devicePlanes != config.planes {
			return nil, fmt.Errorf("Spectrum-X devices on one node must use the same platformType, version, multiplaneMode, overlay, and numberOfPlanes; device %q differs", device.Name)
		}
		if len(device.Status.Ports) == 0 {
			return nil, fmt.Errorf("device %q has no discovered PCI ports", device.Name)
		}
		hcaType, hcaTypeErr := blueprintHCAType(device.Status.Type)
		if hcaTypeErr != nil {
			return nil, fmt.Errorf("device %q: %w", device.Name, hcaTypeErr)
		}
		hcaTypes[hcaType] = struct{}{}
		bdf := strings.ToLower(strings.TrimSpace(device.Status.Ports[0].PCI))
		if !isCanonicalFunctionZeroBDF(bdf) {
			return nil, fmt.Errorf("device %q first PCI port %q is not a canonical function-zero BDF", device.Name, device.Status.Ports[0].PCI)
		}
		if otherDevice, found := seenBDFs[bdf]; found {
			return nil, fmt.Errorf("devices %q and %q resolve to the same pre-breakout BDF %q", otherDevice, device.Name, bdf)
		}
		seenBDFs[bdf] = device.Name
		ordered = append(ordered, orderedDevice{bdf: bdf})
	}
	orderedHCATypes := make([]string, 0, len(hcaTypes))
	for hcaType := range hcaTypes {
		orderedHCATypes = append(orderedHCATypes, hcaType)
	}
	sort.Strings(orderedHCATypes)
	ewConstraint := config.targetMap.TargetConstraints[config.targetMap.DefaultRole]
	ewConstraint.HCATypes = orderedHCATypes
	config.targetMap.TargetConstraints[config.targetMap.DefaultRole] = ewConstraint

	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].bdf < ordered[j].bdf
	})
	for rail, item := range ordered {
		config.targetMap.PreBreakout.Targets = append(config.targetMap.PreBreakout.Targets, preBreakoutTarget{
			BDF:  item.bdf,
			Rail: rail,
		})
	}

	return config, nil
}

func isCanonicalFunctionZeroBDF(bdf string) bool {
	matches := canonicalFunctionZeroBDF.FindStringSubmatch(bdf)
	if len(matches) != 2 {
		return false
	}
	deviceNumber, err := strconv.ParseUint(matches[1], 16, 8)
	return err == nil && deviceNumber <= 0x1f
}

func spectrumXEnabledForPlan(device *v1alpha1.NicDevice) bool {
	return device != nil &&
		device.Spec.Configuration != nil &&
		device.Spec.Configuration.Template != nil &&
		device.Spec.Configuration.Template.SpectrumXOptimized != nil &&
		device.Spec.Configuration.Template.SpectrumXOptimized.Enabled
}

func normalizedMultiplaneMode(mode string) string {
	if mode == "" {
		return consts.MultiplaneModeNone
	}
	return mode
}

func blueprintProfile(mode string) (string, error) {
	switch normalizedMultiplaneMode(mode) {
	case consts.MultiplaneModeNone:
		return "single-plane", nil
	case consts.MultiplaneModeSwplb:
		return "swmp", nil
	case consts.MultiplaneModeHwplb:
		return "hwmp", nil
	default:
		return "", fmt.Errorf("unsupported Spectrum-X multiplaneMode %q", mode)
	}
}

func blueprintHCAType(deviceType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(deviceType)) {
	case "1021":
		return "ConnectX-7", nil
	case "1023":
		return "ConnectX-8", nil
	case "1025":
		return "ConnectX-9", nil
	case consts.BlueField3DeviceID:
		return "BlueField-3", nil
	default:
		return "", fmt.Errorf("unsupported device type %q for doSPCX target mapping", deviceType)
	}
}

func planName(nodeName string, stage PlanStage) string {
	return boundedPlanName("nco-" + strings.ToLower(nodeName) + "-spcx-" + string(stage))
}

func targetMapName(nodeName string) string {
	return boundedPlanName("nco-" + strings.ToLower(nodeName) + "-spcx")
}

func boundedPlanName(name string) string {
	name = regexp.MustCompile(`[^a-z0-9_.-]+`).ReplaceAllString(name, "-")
	for strings.Contains(name, "..") {
		name = strings.ReplaceAll(name, "..", ".")
	}
	if len(name) <= 128 {
		return name
	}
	digest := sha256.Sum256([]byte(name))
	hash := hex.EncodeToString(digest[:8])
	prefixLength := 128 - len(hash) - 1
	return strings.TrimRight(name[:prefixLength], ".-") + "-" + hash
}

func validateGeneratedPlan(
	planJSON []byte,
	expectedName string,
	config *planConfig,
	expectedStage string,
) (*SemanticPlan, error) {
	var document struct {
		Plan struct {
			Name    string `json:"name"`
			Family  string `json:"family"`
			Profile string `json:"profile"`
			Stage   string `json:"stage"`
			Params  struct {
				DeploymentMode string `json:"deployment_mode"`
				Planes         int    `json:"planes"`
			} `json:"params"`
			DetectedHW struct {
				PlatformType string `json:"platform_type"`
			} `json:"detected_hw"`
			Devices  []json.RawMessage `json:"devices"`
			Semantic *struct {
				Groups []json.RawMessage `json:"groups"`
			} `json:"semantic"`
			BareMetal *struct {
				Groups []json.RawMessage `json:"groups"`
			} `json:"bare_metal"`
		} `json:"plan"`
		Artifacts struct {
			Manifest []json.RawMessage `json:"manifest"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(planJSON, &document); err != nil {
		return nil, fmt.Errorf("decode generated doSPCX %s plan: %w", expectedStage, err)
	}
	if document.Plan.Name != expectedName {
		return nil, fmt.Errorf("generated doSPCX plan name is %q, expected %q", document.Plan.Name, expectedName)
	}
	if document.Plan.Family != "spcx" {
		return nil, fmt.Errorf("generated doSPCX plan family is %q, expected %q", document.Plan.Family, "spcx")
	}
	if document.Plan.Profile != config.profile {
		return nil, fmt.Errorf("generated doSPCX plan profile is %q, expected %q", document.Plan.Profile, config.profile)
	}
	if document.Plan.Stage != expectedStage {
		return nil, fmt.Errorf("generated doSPCX plan stage is %q, expected %q", document.Plan.Stage, expectedStage)
	}
	if document.Plan.Params.DeploymentMode != deploymentModeHostK8s {
		return nil, fmt.Errorf("generated doSPCX plan deployment mode is %q, expected %q", document.Plan.Params.DeploymentMode, deploymentModeHostK8s)
	}
	if document.Plan.Params.Planes != config.planes {
		return nil, fmt.Errorf("generated doSPCX plan plane count is %d, expected %d", document.Plan.Params.Planes, config.planes)
	}
	if document.Plan.DetectedHW.PlatformType != config.platformType {
		return nil, fmt.Errorf("generated doSPCX plan platform type is %q, expected %q", document.Plan.DetectedHW.PlatformType, config.platformType)
	}
	if document.Plan.Semantic == nil || len(document.Plan.Semantic.Groups) == 0 {
		return nil, fmt.Errorf("generated doSPCX %s plan does not contain semantic groups", expectedStage)
	}
	if document.Plan.BareMetal != nil && len(document.Plan.BareMetal.Groups) > 0 {
		return nil, fmt.Errorf("generated doSPCX %s plan unexpectedly contains bare-metal groups", expectedStage)
	}
	if len(document.Artifacts.Manifest) > 0 {
		return nil, fmt.Errorf("generated doSPCX %s plan unexpectedly contains rendered artifacts", expectedStage)
	}
	expectedDeviceCount := config.selectedCount
	if expectedStage == string(PlanStageConfigure) {
		expectedDeviceCount *= config.planes
	}
	if len(document.Plan.Devices) != expectedDeviceCount {
		return nil, fmt.Errorf("generated doSPCX %s plan has %d devices, expected %d", expectedStage, len(document.Plan.Devices), expectedDeviceCount)
	}
	semanticPlan, err := ParseSemanticPlan(planJSON, PlanStage(expectedStage))
	if err != nil {
		return nil, fmt.Errorf("validate generated doSPCX %s semantic plan: %w", expectedStage, err)
	}
	return semanticPlan, nil
}

func writeJSONFileAtomic(path string, value any) error {
	content, err := marshalJSONFile(value)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, content)
}

func marshalJSONFile(value any) ([]byte, error) {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	content = append(content, '\n')
	return content, nil
}

func sha256Digest(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func writeRawJSONFileAtomic(path string, value []byte) error {
	var formatted bytes.Buffer
	if err := json.Indent(&formatted, value, "", "  "); err != nil {
		return err
	}
	formatted.WriteByte('\n')
	return writeFileAtomic(path, formatted.Bytes())
}

func writeFileAtomic(path string, content []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".nco-dospcx-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
