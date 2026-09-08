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
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	execUtils "k8s.io/utils/exec"
	execTesting "k8s.io/utils/exec/testing"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

type preparePlanCommand struct {
	executable string
	args       []string
	command    *execTesting.FakeCmd
}

const (
	prepareStage   = "prepare"
	configureStage = "configure"
)

func newTestPlanManager(
	execInterface execUtils.Interface,
	blueprintsRoot string,
	stateDir string,
) PlanManager {
	return &spectrumXConfigManager{
		spectrumXConfigs:   nil,
		preparedPlans:      make(map[string]*preparedPlan),
		dmsManager:         nil,
		execInterface:      execInterface,
		blueprintsRoot:     blueprintsRoot,
		blueprintsStateDir: stateDir,
		dospcxDataRoot:     filepath.Join(stateDir, "dospcx-data"),
		dospcxDataDigest:   "",
		ccProcesses:        nil,
		ccTerminationChan:  nil,
	}
}

func generatePreparePlan(
	ctx context.Context,
	execInterface execUtils.Interface,
	nodeName string,
	devices []*v1alpha1.NicDevice,
	blueprintsRoot string,
	stateDir string,
) (string, error) {
	manager := newTestPlanManager(execInterface, blueprintsRoot, stateDir)
	for _, device := range devices {
		device.Status.Node = nodeName
	}
	if err := manager.PreparePlan(ctx, devices, PlanStagePrepare); err != nil {
		return "", err
	}
	plan, err := manager.GetPreparedPlan(devices[0], PlanStagePrepare)
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "plans", plan.Name, "plan.json"), nil
}

func generateConfigurePlan(
	ctx context.Context,
	execInterface execUtils.Interface,
	nodeName string,
	devices []*v1alpha1.NicDevice,
	blueprintsRoot string,
	stateDir string,
) (string, error) {
	manager := newTestPlanManager(execInterface, blueprintsRoot, stateDir)
	for _, device := range devices {
		device.Status.Node = nodeName
	}
	if err := manager.PreparePlan(ctx, devices, PlanStageConfigure); err != nil {
		return "", err
	}
	plan, err := manager.GetPreparedPlan(devices[0], PlanStageConfigure)
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "plans", plan.Name, "plan.json"), nil
}

func preparePlanFakeExecutor(output []byte, commands *[]preparePlanCommand) *execTesting.FakeExec {
	command := &execTesting.FakeCmd{}
	command.RunScript = append(command.RunScript, func() ([]byte, []byte, error) {
		return output, nil, nil
	})
	executor := &execTesting.FakeExec{}
	executor.CommandScript = []execTesting.FakeCommandAction{
		func(executable string, args ...string) execUtils.Cmd {
			*commands = append(*commands, preparePlanCommand{
				executable: executable,
				args:       append([]string(nil), args...),
				command:    command,
			})
			return command
		},
	}
	return executor
}

var _ = Describe("doSPCX planning", func() {
	const (
		nodeName       = "worker-01"
		blueprintsRoot = "/opt/nvidia/blueprints"
		secondBDF      = "0000:65:00.0"
	)

	newDevice := func(name, bdf, mode string) *v1alpha1.NicDevice {
		return &v1alpha1.NicDevice{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.NicDeviceSpec{
				Configuration: &v1alpha1.NicDeviceConfigurationSpec{
					Template: &v1alpha1.ConfigurationTemplateSpec{
						NumVfs: 1,
						SpectrumXOptimized: &v1alpha1.SpectrumXOptimizedSpec{
							Enabled:        true,
							Version:        "RA2.2",
							PlatformType:   "gb300",
							Overlay:        "none",
							MultiplaneMode: mode,
							NumberOfPlanes: 2,
						},
					},
				},
			},
			Status: v1alpha1.NicDeviceStatus{
				Node:  nodeName,
				Type:  "1023",
				Ports: []v1alpha1.NicDevicePortSpec{{PCI: bdf}},
			},
		}
	}

	planResponseForPlatform := func(name, profile, stage, platform string, planes, deviceCount int) []byte {
		prepareBDFs := []string{"0000:64:00.0", secondBDF, "0001:15:00.0"}
		configureBDFs := []string{"0000:64:00.0", "0001:15:00.0"}
		devices := make([]map[string]any, deviceCount)
		for index := range devices {
			rail := index
			plane := 0
			bdf := ""
			if stage == configureStage {
				rail = index / planes
				plane = index % planes
				var err error
				bdf, err = bdfForPlane(configureBDFs[rail], plane)
				Expect(err).NotTo(HaveOccurred())
			} else {
				bdf = prepareBDFs[index]
			}
			devices[index] = map[string]any{
				"bdf":        bdf,
				"device_id":  "0x1023",
				"dms_target": "pci/" + bdf,
				"network":    "ew",
				"rail":       rail,
				"plane":      plane,
			}
		}
		groupName := "breakout"
		groups := []any{}
		if stage == configureStage {
			groupName = "link-runtime"
		}
		operationID := "test." + groupName
		groups = append(groups, map[string]any{
			"name":           groupName,
			"stage":          stage,
			"order":          10,
			"scope":          "per_device",
			"operation_refs": []string{operationID},
		})
		if stage == prepareStage {
			groups = append(groups, map[string]any{
				"name":            "post-breakout",
				"stage":           stage,
				"order":           20,
				"scope":           "mixed",
				"device_view":     "post_breakout",
				"requires_reboot": true,
				"operation_refs":  []string{},
			})
		}
		response, err := json.Marshal(map[string]any{
			"plan":    name,
			"family":  "spcx",
			"profile": profile,
			"stage":   stage,
			"plan-json": map[string]any{
				"plan": map[string]any{
					"name":         name,
					"family":       "spcx",
					"profile":      profile,
					"stage":        stage,
					"path_dialect": semanticPathDialect,
					"params":       map[string]any{"deployment_mode": "host-k8s", "planes": planes},
					"detected_hw":  map[string]any{"platform_type": platform},
					"devices":      devices,
					"runtime_ctx":  map[string]any{"deployment_mode": "host-k8s", "rdma_topology": "per_pf"},
					"operations": map[string]any{
						operationID: map[string]any{
							"path":           "/nvidia/test",
							"values":         map[string]any{"enabled": true},
							"source_feature": "test",
							"target_class":   "pf_netdev_all",
						},
					},
					"semantic": map[string]any{"groups": groups},
				},
				"artifacts": map[string]any{"manifest": []any{}},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		return response
	}
	planResponse := func(name, profile, stage string, planes, deviceCount int) []byte {
		return planResponseForPlatform(name, profile, stage, "gb300", planes, deviceCount)
	}

	It("builds a schema-v1 target map and saves the returned prepare plan", func() {
		stateDir := GinkgoT().TempDir()
		commands := []preparePlanCommand{}
		planName := planName(nodeName, prepareStage)
		executor := preparePlanFakeExecutor(planResponse(planName, "SPX_Multiplane", prepareStage, 2, 3), &commands)
		devices := []*v1alpha1.NicDevice{
			newDevice("last-by-bdf", "0001:15:00.0", "hwplb"),
			newDevice("second-by-bdf", secondBDF, "hwplb"),
			newDevice("first-by-bdf", "0000:64:00.0", "hwplb"),
		}

		manager := newTestPlanManager(executor, blueprintsRoot, stateDir)
		err := manager.PreparePlan(context.Background(), devices, PlanStagePrepare)

		Expect(err).NotTo(HaveOccurred())
		plan, err := manager.GetPreparedPlan(devices[0], PlanStagePrepare)
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Name).To(Equal(planName))
		Expect(plan.Stage).To(Equal(PlanStagePrepare))
		Expect(plan.Groups).To(HaveLen(2))
		Expect(plan.Groups[0].Name).To(Equal("breakout"))
		planPath := filepath.Join(stateDir, "plans", planName, "plan.json")
		planContent, err := os.ReadFile(planPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(planContent)).To(ContainSubstring(`"profile": "SPX_Multiplane"`))
		storedPlan, err := manager.GetPreparedPlan(devices[0], PlanStagePrepare)
		Expect(err).NotTo(HaveOccurred())
		Expect(storedPlan).To(Equal(plan))
		storedPlan, err = manager.GetPreparedPlan(devices[2], PlanStagePrepare)
		Expect(err).NotTo(HaveOccurred())
		Expect(storedPlan).To(Equal(plan))

		targetMapPath := filepath.Join(stateDir, "target-maps", targetMapName(nodeName)+".json")
		targetMapContent, err := os.ReadFile(targetMapPath)
		Expect(err).NotTo(HaveOccurred())
		var generated targetMap
		Expect(json.Unmarshal(targetMapContent, &generated)).To(Succeed())
		Expect(generated.SchemaVersion).To(Equal(1))
		Expect(generated.PlatformType).To(Equal("gb300"))
		Expect(generated.PreBreakout.Targets).To(Equal([]preBreakoutTarget{
			{ID: "ew-rail0-prebreakout", BDF: "0000:64:00.0", DeviceID: "0x1023", Role: "ew", Rail: 0},
			{ID: "ew-rail1-prebreakout", BDF: secondBDF, DeviceID: "0x1023", Role: "ew", Rail: 1},
			{ID: "ew-rail2-prebreakout", BDF: "0001:15:00.0", DeviceID: "0x1023", Role: "ew", Rail: 2},
		}))
		Expect(string(targetMapContent)).NotTo(ContainSubstring("default_role"))
		Expect(string(targetMapContent)).NotTo(ContainSubstring("target_constraints"))
		Expect(string(targetMapContent)).NotTo(ContainSubstring("nic_index_in_rail"))

		Expect(commands).To(HaveLen(1))
		Expect(commands[0].executable).To(Equal("/opt/mellanox/doca/services/dms/dms-cli"))
		Expect(commands[0].args).To(ContainElements(
			"profile=SPX_Multiplane",
			"name="+planName,
			"stage=prepare",
			"target-map-file=file:"+targetMapPath,
			"params=deployment_mode=host-k8s,planes=2",
		))
		Expect(commands[0].args).NotTo(ContainElement("params=deployment_mode=host-k8s,planes=2,overlay=none"))
		Expect(commands[0].command.Env).To(ContainElement("BLUEPRINTS_ROOT=" + blueprintsRoot))
		Expect(commands[0].command.Env).To(ContainElement("BP_STATE_DIR=" + stateDir))

		metadataPath := filepath.Join(stateDir, "plans", planName, "metadata.json")
		metadataContent, err := os.ReadFile(metadataPath)
		Expect(err).NotTo(HaveOccurred())
		var metadata planMetadata
		Expect(json.Unmarshal(metadataContent, &metadata)).To(Succeed())
		Expect(metadata).To(Equal(planMetadata{
			BlueprintsRoot:     blueprintsRoot,
			BlueprintsStateDir: stateDir,
			PlanName:           planName,
			Stage:              prepareStage,
			Profile:            "SPX_Multiplane",
			PlatformType:       "gb300",
			SpectrumXVersion:   "RA2.2",
			MultiplaneMode:     "hwplb",
			Overlay:            "none",
			Planes:             2,
			DeploymentMode:     "host-k8s",
			Parameters:         []string{"deployment_mode=host-k8s", "planes=2"},
			TargetMapFile:      targetMapPath,
			TargetMapDigest:    sha256Digest(targetMapContent),
		}))
		Expect(string(metadataContent)).NotTo(ContainSubstring(`"inputs"`))
	})

	It("serves prepared plans from memory without rereading persisted files", func() {
		stateDir := GinkgoT().TempDir()
		commands := []preparePlanCommand{}
		generatedPlanName := planName(nodeName, prepareStage)
		manager := newTestPlanManager(
			preparePlanFakeExecutor(
				planResponse(generatedPlanName, "SPX_Multiplane", prepareStage, 2, 1), &commands,
			), blueprintsRoot, stateDir,
		)
		device := newDevice("rail-0", "0000:64:00.0", "hwplb")
		Expect(manager.PreparePlan(context.Background(), []*v1alpha1.NicDevice{device}, PlanStagePrepare)).To(Succeed())

		Expect(os.RemoveAll(filepath.Join(stateDir, "plans"))).To(Succeed())
		Expect(os.RemoveAll(filepath.Join(stateDir, "target-maps"))).To(Succeed())
		Expect(manager.PreparePlan(context.Background(), []*v1alpha1.NicDevice{device}, PlanStagePrepare)).To(Succeed())
		Expect(commands).To(HaveLen(1))
		plan, err := manager.GetPreparedPlan(device, PlanStagePrepare)

		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Name).To(Equal(generatedPlanName))
		Expect(plan.Groups).To(HaveLen(2))
		plan.Groups[0].Name = "mutated-by-caller"
		plan, err = manager.GetPreparedPlan(device, PlanStagePrepare)
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Groups[0].Name).To(Equal("breakout"))
	})

	It("does not generate a plan when Spectrum-X is not enabled", func() {
		commands := []preparePlanCommand{}
		manager := newTestPlanManager(preparePlanFakeExecutor(nil, &commands), blueprintsRoot, GinkgoT().TempDir())

		err := manager.PreparePlan(context.Background(), []*v1alpha1.NicDevice{{}}, PlanStagePrepare)

		Expect(err).NotTo(HaveOccurred())
		Expect(commands).To(BeEmpty())
	})

	It("rejects retrieval for an unsupported plan stage", func() {
		manager := newTestPlanManager(nil, blueprintsRoot, GinkgoT().TempDir())

		_, err := manager.GetPreparedPlan(newDevice("device", "0000:64:00.0", "hwplb"), PlanStage("unknown"))

		Expect(err).To(MatchError(ContainSubstring("unsupported")))
	})

	It("does not load a persisted plan through the per-device retrieval path", func() {
		manager := newTestPlanManager(nil, blueprintsRoot, GinkgoT().TempDir())
		device := newDevice("device", "0000:64:00.0", "hwplb")

		plan, err := manager.GetPreparedPlan(device, PlanStagePrepare)

		Expect(plan).To(BeNil())
		Expect(err).To(MatchError(ContainSubstring("is not prepared")))
	})

	DescribeTable("scopes generated plans to the requested node",
		func(stage PlanStage, deviceCount int) {
			const otherNode = "worker-02"
			stateDir := GinkgoT().TempDir()
			commands := []preparePlanCommand{}
			generatedPlanName := planName(otherNode, stage)
			executor := preparePlanFakeExecutor(
				planResponse(generatedPlanName, "SPX_Multiplane", string(stage), 2, deviceCount), &commands,
			)
			device := newDevice("rail-0", "0000:64:00.0", "hwplb")

			var (
				path string
				err  error
			)
			if stage == PlanStagePrepare {
				path, err = generatePreparePlan(
					context.Background(), executor, otherNode, []*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
				)
			} else {
				path, err = generateConfigurePlan(
					context.Background(), executor, otherNode, []*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
				)
			}

			Expect(err).NotTo(HaveOccurred())
			Expect(path).To(Equal(filepath.Join(stateDir, "plans", generatedPlanName, "plan.json")))
		},
		Entry("prepare", PlanStagePrepare, 1),
		Entry("configure", PlanStageConfigure, 2),
	)

	It("reuses the node target map and saves a configure plan", func() {
		stateDir := GinkgoT().TempDir()
		commands := []preparePlanCommand{}
		configurePlanName := planName(nodeName, configureStage)
		executor := preparePlanFakeExecutor(
			planResponse(configurePlanName, "SPX_Multiplane", configureStage, 2, 4), &commands,
		)
		devices := []*v1alpha1.NicDevice{
			newDevice("rail-1", "0001:15:00.0", "hwplb"),
			newDevice("rail-0", "0000:64:00.0", "hwplb"),
		}
		devices[1].Status.Ports = append(devices[1].Status.Ports,
			v1alpha1.NicDevicePortSpec{PCI: "0000:64:00.1"})

		planPath, err := generateConfigurePlan(
			context.Background(), executor, nodeName, devices, blueprintsRoot, stateDir,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(planPath).To(Equal(filepath.Join(stateDir, "plans", configurePlanName, "plan.json")))
		targetMapPath := filepath.Join(stateDir, "target-maps", targetMapName(nodeName)+".json")
		Expect(targetMapPath).To(BeAnExistingFile())
		targetMapContent, err := os.ReadFile(targetMapPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(targetMapContent)).To(ContainSubstring("0000:64:00.0"))
		Expect(string(targetMapContent)).NotTo(ContainSubstring("0000:64:00.1"))
		Expect(commands).To(HaveLen(1))
		Expect(commands[0].args).To(ContainElements(
			"name="+configurePlanName,
			"stage=configure",
			"target-map-file=file:"+targetMapPath,
			"params=deployment_mode=host-k8s,planes=2",
		))
	})

	It("reuses a saved plan when its flat input metadata still matches", func() {
		stateDir := GinkgoT().TempDir()
		device := newDevice("rail-0", "0000:64:00.0", "hwplb")
		generatedPlanName := planName(nodeName, prepareStage)
		firstCommands := []preparePlanCommand{}
		firstExecutor := preparePlanFakeExecutor(
			planResponse(generatedPlanName, "SPX_Multiplane", prepareStage, 2, 1), &firstCommands,
		)

		firstPath, err := generatePreparePlan(
			context.Background(), firstExecutor, nodeName,
			[]*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(firstCommands).To(HaveLen(1))

		secondCommands := []preparePlanCommand{}
		secondPath, err := generatePreparePlan(
			context.Background(), preparePlanFakeExecutor(nil, &secondCommands), nodeName,
			[]*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(secondPath).To(Equal(firstPath))
		Expect(secondCommands).To(BeEmpty())
	})

	It("regenerates a saved plan when the doSPCX data bundle changes", func() {
		stateDir := GinkgoT().TempDir()
		device := newDevice("rail-0", "0000:64:00.0", "hwplb")
		generatedPlanName := planName(nodeName, prepareStage)
		firstCommands := []preparePlanCommand{}
		manager := newTestPlanManager(
			preparePlanFakeExecutor(
				planResponse(generatedPlanName, "SPX_Multiplane", prepareStage, 2, 1), &firstCommands,
			), blueprintsRoot, stateDir,
		).(*spectrumXConfigManager)
		manager.dospcxDataDigest = "first-bundle"

		Expect(manager.PreparePlan(context.Background(), []*v1alpha1.NicDevice{device}, PlanStagePrepare)).To(Succeed())
		Expect(firstCommands).To(HaveLen(1))

		secondCommands := []preparePlanCommand{}
		manager.execInterface = preparePlanFakeExecutor(
			planResponse(generatedPlanName, "SPX_Multiplane", prepareStage, 2, 1), &secondCommands,
		)
		manager.dospcxDataDigest = "second-bundle"

		Expect(manager.PreparePlan(context.Background(), []*v1alpha1.NicDevice{device}, PlanStagePrepare)).To(Succeed())
		Expect(secondCommands).To(HaveLen(1))
		metadataContent, err := os.ReadFile(filepath.Join(stateDir, "plans", generatedPlanName, "metadata.json"))
		Expect(err).NotTo(HaveOccurred())
		var metadata planMetadata
		Expect(json.Unmarshal(metadataContent, &metadata)).To(Succeed())
		Expect(metadata.BlueprintsDataDigest).To(Equal("second-bundle"))
	})

	DescribeTable("rejects a cached plan that does not match the requesting device",
		func(mutate func(*v1alpha1.NicDevice), expected string) {
			stateDir := GinkgoT().TempDir()
			device := newDevice("rail-0", "0000:64:00.0", "hwplb")
			generatedPlanName := planName(nodeName, prepareStage)
			commands := []preparePlanCommand{}
			manager := newTestPlanManager(
				preparePlanFakeExecutor(
					planResponse(generatedPlanName, "SPX_Multiplane", prepareStage, 2, 1), &commands,
				), blueprintsRoot, stateDir,
			)
			Expect(manager.PreparePlan(context.Background(), []*v1alpha1.NicDevice{device}, PlanStagePrepare)).To(Succeed())

			mutate(device)
			_, err := manager.GetPreparedPlan(device, PlanStagePrepare)

			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("planner inputs changed", func(device *v1alpha1.NicDevice) {
			device.Spec.Configuration.Template.SpectrumXOptimized.PlatformType = "b300"
		}, "does not match"),
		Entry("device is absent from the target map", func(device *v1alpha1.NicDevice) {
			device.Status.Ports[0].PCI = secondBDF
		}, "is absent from the target map"),
		Entry("device type differs from the target map", func(device *v1alpha1.NicDevice) {
			device.Status.Type = "1025"
		}, "device ID"),
	)

	DescribeTable("regenerates a saved plan when an input changes",
		func(mutate func(*v1alpha1.NicDevice), expectedPlatform string, expectedPlanes int) {
			stateDir := GinkgoT().TempDir()
			device := newDevice("rail-0", "0000:64:00.0", "hwplb")
			generatedPlanName := planName(nodeName, prepareStage)
			firstCommands := []preparePlanCommand{}
			_, err := generatePreparePlan(
				context.Background(), preparePlanFakeExecutor(
					planResponse(generatedPlanName, "SPX_Multiplane", prepareStage, 2, 1), &firstCommands,
				), nodeName, []*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
			)
			Expect(err).NotTo(HaveOccurred())

			mutate(device)
			secondCommands := []preparePlanCommand{}
			secondResponse := planResponseForPlatform(
				generatedPlanName, "SPX_Multiplane", prepareStage, expectedPlatform, expectedPlanes, 1,
			)
			var responseDocument map[string]any
			Expect(json.Unmarshal(secondResponse, &responseDocument)).To(Succeed())
			planDevices := responseDocument["plan-json"].(map[string]any)["plan"].(map[string]any)["devices"].([]any)
			planDevices[0].(map[string]any)["bdf"] = device.Status.Ports[0].PCI
			planDevices[0].(map[string]any)["dms_target"] = "pci/" + device.Status.Ports[0].PCI
			secondResponse, err = json.Marshal(responseDocument)
			Expect(err).NotTo(HaveOccurred())
			_, err = generatePreparePlan(
				context.Background(), preparePlanFakeExecutor(
					secondResponse, &secondCommands,
				), nodeName, []*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(secondCommands).To(HaveLen(1))
			metadataContent, readErr := os.ReadFile(filepath.Join(
				stateDir, "plans", generatedPlanName, "metadata.json",
			))
			Expect(readErr).NotTo(HaveOccurred())
			var metadata planMetadata
			Expect(json.Unmarshal(metadataContent, &metadata)).To(Succeed())
			Expect(metadata.PlatformType).To(Equal(expectedPlatform))
			Expect(metadata.Planes).To(Equal(expectedPlanes))
		},
		Entry("platform type", func(device *v1alpha1.NicDevice) {
			device.Spec.Configuration.Template.SpectrumXOptimized.PlatformType = "custom-platform"
		}, "custom-platform", 2),
		Entry("plane count", func(device *v1alpha1.NicDevice) {
			device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4
		}, "gb300", 4),
		Entry("target-map topology", func(device *v1alpha1.NicDevice) {
			device.Status.Ports[0].PCI = secondBDF
		}, "gb300", 2),
	)

	DescribeTable("regenerates when a saved cache artifact is invalid",
		func(corrupt func(stateDir, generatedPlanName string)) {
			stateDir := GinkgoT().TempDir()
			device := newDevice("rail-0", "0000:64:00.0", "hwplb")
			generatedPlanName := planName(nodeName, prepareStage)
			firstCommands := []preparePlanCommand{}
			_, err := generatePreparePlan(
				context.Background(), preparePlanFakeExecutor(
					planResponse(generatedPlanName, "SPX_Multiplane", prepareStage, 2, 1), &firstCommands,
				), nodeName, []*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
			)
			Expect(err).NotTo(HaveOccurred())
			corrupt(stateDir, generatedPlanName)

			secondCommands := []preparePlanCommand{}
			_, err = generatePreparePlan(
				context.Background(), preparePlanFakeExecutor(
					planResponse(generatedPlanName, "SPX_Multiplane", prepareStage, 2, 1), &secondCommands,
				), nodeName, []*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(secondCommands).To(HaveLen(1))
		},
		Entry("metadata JSON", func(stateDir, generatedPlanName string) {
			Expect(os.WriteFile(
				filepath.Join(stateDir, "plans", generatedPlanName, "metadata.json"), []byte("{"), 0o644,
			)).To(Succeed())
		}),
		Entry("target map content", func(stateDir, _ string) {
			Expect(os.WriteFile(
				filepath.Join(stateDir, "target-maps", targetMapName(nodeName)+".json"), []byte("{}\n"), 0o644,
			)).To(Succeed())
		}),
		Entry("plan JSON", func(stateDir, generatedPlanName string) {
			Expect(os.WriteFile(
				filepath.Join(stateDir, "plans", generatedPlanName, "plan.json"), []byte("{}\n"), 0o644,
			)).To(Succeed())
		}),
	)

	It("maps swplb to SPX_NetPlugin and passes its overlay", func() {
		stateDir := GinkgoT().TempDir()
		commands := []preparePlanCommand{}
		planName := planName(nodeName, prepareStage)
		executor := preparePlanFakeExecutor(planResponse(planName, "SPX_NetPlugin", prepareStage, 2, 1), &commands)
		device := newDevice("SPX_NetPlugin", "0000:64:00.0", "swplb")

		_, err := generatePreparePlan(
			context.Background(), executor, nodeName, []*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(commands).To(HaveLen(1))
		Expect(commands[0].args).To(ContainElements("profile=SPX_NetPlugin", "params=deployment_mode=host-k8s,planes=2,overlay=none"))
	})

	It("maps an omitted multiplane mode to a one-plane plan", func() {
		stateDir := GinkgoT().TempDir()
		commands := []preparePlanCommand{}
		planName := planName(nodeName, prepareStage)
		executor := preparePlanFakeExecutor(planResponse(planName, "single-plane", prepareStage, 1, 1), &commands)
		device := newDevice("single-plane", "0000:64:00.0", "")
		device.Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 0

		_, err := generatePreparePlan(
			context.Background(), executor, nodeName, []*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(commands).To(HaveLen(1))
		Expect(commands[0].args).To(ContainElements("profile=single-plane", "params=deployment_mode=host-k8s,planes=1,overlay=none"))
	})

	It("rejects an unsupported SPX_Multiplane overlay before writing the target map", func() {
		stateDir := GinkgoT().TempDir()
		commands := []preparePlanCommand{}
		device := newDevice("hwmp-l3", "0000:64:00.0", "hwplb")
		device.Spec.Configuration.Template.SpectrumXOptimized.Overlay = "l3"

		_, err := generatePreparePlan(
			context.Background(), preparePlanFakeExecutor(nil, &commands), nodeName,
			[]*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
		)

		Expect(err).To(MatchError(ContainSubstring("does not support overlay")))
		Expect(commands).To(BeEmpty())
		_, statErr := os.Stat(filepath.Join(stateDir, "target-maps", targetMapName(nodeName)+".json"))
		Expect(os.IsNotExist(statErr)).To(BeTrue())
	})

	DescribeTable("rejects invalid planner paths before writing the target map",
		func(root, stateDir, expected string) {
			commands := []preparePlanCommand{}
			device := newDevice("hwmp", "0000:64:00.0", "hwplb")

			_, err := generatePreparePlan(
				context.Background(), preparePlanFakeExecutor(nil, &commands), nodeName,
				[]*v1alpha1.NicDevice{device}, root, stateDir,
			)

			Expect(err).To(MatchError(ContainSubstring(expected)))
			Expect(commands).To(BeEmpty())
		},
		Entry("relative Blueprints root", "blueprints", "", "blueprints root"),
		Entry("relative state directory", blueprintsRoot, "blueprints-state", "state directory"),
	)

	DescribeTable("maps NCO multiplane modes to public doSPCX profiles",
		func(mode, expected string) {
			profile, err := blueprintProfile(mode)
			Expect(err).NotTo(HaveOccurred())
			Expect(profile).To(Equal(expected))
		},
		Entry("default", "", "single-plane"),
		Entry("none", "none", "single-plane"),
		Entry("software multiplane", "swplb", "SPX_NetPlugin"),
		Entry("hardware multiplane", "hwplb", "SPX_Multiplane"),
	)

	It("rejects unsupported multiplane modes", func() {
		_, err := blueprintProfile("uniplane")
		Expect(err).To(MatchError(ContainSubstring("unsupported")))
	})

	DescribeTable("maps NCO device types to doSPCX device IDs",
		func(deviceType, expected string) {
			deviceID, err := blueprintDeviceID(deviceType)
			Expect(err).NotTo(HaveOccurred())
			Expect(deviceID).To(Equal(expected))
		},
		Entry("ConnectX-7", "1021", "0x1021"),
		Entry("ConnectX-8", "1023", "0x1023"),
		Entry("ConnectX-9", "1025", "0x1025"),
		Entry("BlueField-3", "A2DC", "0xa2dc"),
	)

	DescribeTable("rejects incomplete or inconsistent target-map inputs",
		func(mutate func([]*v1alpha1.NicDevice), expected string) {
			devices := []*v1alpha1.NicDevice{
				newDevice("first", "0000:64:00.0", "hwplb"),
				newDevice("second", "0001:15:00.0", "hwplb"),
			}
			mutate(devices)

			_, err := buildPlanConfig(devices)

			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("inconsistent platform type", func(devices []*v1alpha1.NicDevice) {
			devices[1].Spec.Configuration.Template.SpectrumXOptimized.PlatformType = "b300"
		}, "must use the same"),
		Entry("missing node name", func(devices []*v1alpha1.NicDevice) {
			devices[0].Status.Node = ""
		}, "has no node name"),
		Entry("inconsistent node name", func(devices []*v1alpha1.NicDevice) {
			devices[1].Status.Node = "worker-02"
		}, "same node"),
		Entry("inconsistent planes", func(devices []*v1alpha1.NicDevice) {
			devices[1].Spec.Configuration.Template.SpectrumXOptimized.NumberOfPlanes = 4
		}, "must use the same"),
		Entry("missing ports", func(devices []*v1alpha1.NicDevice) {
			devices[1].Status.Ports = nil
		}, "no discovered PCI ports"),
		Entry("unsupported device type", func(devices []*v1alpha1.NicDevice) {
			devices[1].Status.Type = "ffff"
		}, "unsupported device type"),
		Entry("nonzero first function", func(devices []*v1alpha1.NicDevice) {
			devices[1].Status.Ports[0].PCI = "0001:15:00.1"
		}, "function-zero"),
		Entry("out-of-range PCI device number", func(devices []*v1alpha1.NicDevice) {
			devices[1].Status.Ports[0].PCI = "0001:15:20.0"
		}, "function-zero"),
		Entry("duplicate BDF", func(devices []*v1alpha1.NicDevice) {
			devices[1].Status.Ports[0].PCI = "0000:64:00.0"
		}, "same pre-breakout BDF"),
	)

	It("uses gb300 when platformType is absent", func() {
		devices := []*v1alpha1.NicDevice{
			newDevice("first", "0000:64:00.0", "hwplb"),
			newDevice("second", "0001:15:00.0", "hwplb"),
		}
		devices[0].Spec.Configuration.Template.SpectrumXOptimized.PlatformType = ""
		devices[1].Spec.Configuration.Template.SpectrumXOptimized.PlatformType = " "

		config, err := buildPlanConfig(devices)

		Expect(err).NotTo(HaveOccurred())
		Expect(config.platformType).To(Equal("gb300"))
		Expect(config.targetMap.PlatformType).To(Equal("gb300"))
	})

	It("ignores interface-name configuration when assigning target-map rails", func() {
		first := newDevice("first", "0000:64:00.0", "hwplb")
		first.Spec.InterfaceNameTemplate = &v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex:  99,
			RailIndex: 99,
		}
		second := newDevice("second", "0001:15:00.0", "hwplb")

		config, err := buildPlanConfig([]*v1alpha1.NicDevice{second, first})

		Expect(err).NotTo(HaveOccurred())
		Expect(config.targetMap.PreBreakout.Targets).To(Equal([]preBreakoutTarget{
			{ID: "ew-rail0-prebreakout", BDF: "0000:64:00.0", DeviceID: "0x1023", Role: "ew", Rail: 0},
			{ID: "ew-rail1-prebreakout", BDF: "0001:15:00.0", DeviceID: "0x1023", Role: "ew", Rail: 1},
		}))
	})

	It("rejects an unexpected generated plan stage before saving it", func() {
		stateDir := GinkgoT().TempDir()
		commands := []preparePlanCommand{}
		planName := planName(nodeName, prepareStage)
		response := planResponse(planName, "SPX_Multiplane", prepareStage, 2, 1)
		var document map[string]any
		Expect(json.Unmarshal(response, &document)).To(Succeed())
		document["plan-json"].(map[string]any)["plan"].(map[string]any)["stage"] = "configure"
		response, err := json.Marshal(document)
		Expect(err).NotTo(HaveOccurred())
		executor := preparePlanFakeExecutor(response, &commands)
		device := newDevice("hwmp", "0000:64:00.0", "hwplb")

		planPath, err := generatePreparePlan(
			context.Background(), executor, nodeName, []*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
		)

		Expect(planPath).To(BeEmpty())
		Expect(err).To(MatchError(ContainSubstring(`stage is "configure"`)))
		_, statErr := os.Stat(filepath.Join(stateDir, "plans", planName, "plan.json"))
		Expect(os.IsNotExist(statErr)).To(BeTrue())
	})

	DescribeTable("rejects a successful response with the wrong plan shape",
		func(mutate func(map[string]any), expected string) {
			stateDir := GinkgoT().TempDir()
			commands := []preparePlanCommand{}
			generatedPlanName := planName(nodeName, prepareStage)
			response := planResponse(generatedPlanName, "SPX_Multiplane", prepareStage, 2, 1)
			var document map[string]any
			Expect(json.Unmarshal(response, &document)).To(Succeed())
			mutate(document["plan-json"].(map[string]any))
			response, err := json.Marshal(document)
			Expect(err).NotTo(HaveOccurred())

			planPath, err := generatePreparePlan(
				context.Background(), preparePlanFakeExecutor(response, &commands), nodeName,
				[]*v1alpha1.NicDevice{newDevice("hwmp", "0000:64:00.0", "hwplb")},
				blueprintsRoot, stateDir,
			)

			Expect(planPath).To(BeEmpty())
			Expect(err).To(MatchError(ContainSubstring(expected)))
		},
		Entry("bare-metal deployment mode", func(bundle map[string]any) {
			plan := bundle["plan"].(map[string]any)
			plan["params"].(map[string]any)["deployment_mode"] = "bare-metal"
		}, "deployment mode"),
		Entry("missing semantic groups", func(bundle map[string]any) {
			delete(bundle["plan"].(map[string]any), "semantic")
		}, "semantic groups"),
		Entry("bare-metal groups", func(bundle map[string]any) {
			bundle["plan"].(map[string]any)["bare_metal"] = map[string]any{
				"groups": []any{map[string]any{"name": "breakout"}},
			}
		}, "bare-metal groups"),
		Entry("rendered artifacts", func(bundle map[string]any) {
			bundle["artifacts"] = map[string]any{"manifest": []any{map[string]any{"type": "systemd-unit"}}}
		}, "rendered artifacts"),
		Entry("unexpected device BDF", func(bundle map[string]any) {
			device := bundle["plan"].(map[string]any)["devices"].([]any)[0].(map[string]any)
			device["bdf"] = secondBDF
			device["dms_target"] = "pci/" + secondBDF
		}, "unexpected device BDF"),
		Entry("wrong device rail", func(bundle map[string]any) {
			device := bundle["plan"].(map[string]any)["devices"].([]any)[0].(map[string]any)
			device["rail"] = 1
		}, "topology does not match"),
		Entry("unknown semantic group", func(bundle map[string]any) {
			group := bundle["plan"].(map[string]any)["semantic"].(map[string]any)["groups"].([]any)[0].(map[string]any)
			group["name"] = "unknown-prepare-group"
		}, "unsupported doSPCX semantic group"),
		Entry("eSwitch operation in an executable group", func(bundle map[string]any) {
			plan := bundle["plan"].(map[string]any)
			group := plan["semantic"].(map[string]any)["groups"].([]any)[0].(map[string]any)
			operationID := group["operation_refs"].([]any)[0].(string)
			plan["operations"].(map[string]any)[operationID].(map[string]any)["path"] = "/nvidia/eswitch"
		}, "outside the current NCO execution scope"),
	)
})
