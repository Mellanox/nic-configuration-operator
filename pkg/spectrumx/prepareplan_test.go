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
		dmsManager:         nil,
		execInterface:      execInterface,
		blueprintsRoot:     blueprintsRoot,
		blueprintsStateDir: stateDir,
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
	command.CombinedOutputScript = append(command.CombinedOutputScript, func() ([]byte, []byte, error) {
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
		devices := make([]map[string]any, deviceCount)
		for index := range devices {
			devices[index] = map[string]any{"bdf": index}
		}
		response, err := json.Marshal(map[string]any{
			"plan":    name,
			"family":  "spcx",
			"profile": profile,
			"stage":   stage,
			"plan-json": map[string]any{
				"plan": map[string]any{
					"name":        name,
					"family":      "spcx",
					"profile":     profile,
					"stage":       stage,
					"params":      map[string]any{"deployment_mode": "host-k8s", "planes": planes},
					"detected_hw": map[string]any{"platform_type": platform},
					"devices":     devices,
					"semantic":    map[string]any{"groups": []any{map[string]any{"name": stage}}},
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

	It("builds a schema-v3 target map and saves the returned prepare plan", func() {
		stateDir := GinkgoT().TempDir()
		commands := []preparePlanCommand{}
		planName := planName(nodeName, prepareStage)
		executor := preparePlanFakeExecutor(planResponse(planName, "hwmp", prepareStage, 2, 3), &commands)
		devices := []*v1alpha1.NicDevice{
			newDevice("last-by-bdf", "0001:15:00.0", "hwplb"),
			newDevice("second-by-bdf", "0000:65:00.0", "hwplb"),
			newDevice("first-by-bdf", "0000:64:00.0", "hwplb"),
		}

		manager := newTestPlanManager(executor, blueprintsRoot, stateDir)
		err := manager.PreparePlan(context.Background(), devices, PlanStagePrepare)

		Expect(err).NotTo(HaveOccurred())
		plan, err := manager.GetPreparedPlan(devices[0], PlanStagePrepare)
		Expect(err).NotTo(HaveOccurred())
		Expect(plan.Name).To(Equal(planName))
		Expect(plan.Stage).To(Equal(PlanStagePrepare))
		Expect(plan.JSON).NotTo(BeEmpty())
		planPath := filepath.Join(stateDir, "plans", planName, "plan.json")
		planContent, err := os.ReadFile(planPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(planContent)).To(ContainSubstring(`"profile": "hwmp"`))
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
		Expect(generated.SchemaVersion).To(Equal(3))
		Expect(generated.PlatformType).To(Equal("gb300"))
		Expect(generated.DefaultRole).To(Equal("ew"))
		Expect(generated.TargetConstraints).To(Equal(map[string]targetConstraint{
			"ew": {HCATypes: []string{"ConnectX-8"}, Topology: targetTopology{PortsPerTarget: 1}},
		}))
		Expect(generated.PreBreakout.Targets).To(Equal([]preBreakoutTarget{
			{BDF: "0000:64:00.0", Rail: 0},
			{BDF: "0000:65:00.0", Rail: 1},
			{BDF: "0001:15:00.0", Rail: 2},
		}))
		Expect(string(targetMapContent)).NotTo(ContainSubstring("nic_index_in_rail"))

		Expect(commands).To(HaveLen(1))
		Expect(commands[0].executable).To(Equal("dms-cli"))
		Expect(commands[0].args).To(ContainElements(
			"profile=hwmp",
			"name="+planName,
			"stage=prepare",
			"target-map-file=file:"+targetMapPath,
			"params=deployment_mode=host-k8s,planes=2",
		))
		Expect(commands[0].args).NotTo(ContainElement("params=deployment_mode=host-k8s,planes=2,overlay=none"))
		Expect(commands[0].command.Env).To(ContainElement("BLUEPRINTS_ROOT=" + blueprintsRoot))
		Expect(commands[0].command.Env).To(ContainElement("BLUEPRINTS_STATE_DIR=" + stateDir))

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
			Profile:            "hwmp",
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

	DescribeTable("scopes generated plans to the requested node",
		func(stage PlanStage, deviceCount int) {
			const otherNode = "worker-02"
			stateDir := GinkgoT().TempDir()
			commands := []preparePlanCommand{}
			generatedPlanName := planName(otherNode, stage)
			executor := preparePlanFakeExecutor(
				planResponse(generatedPlanName, "hwmp", string(stage), 2, deviceCount), &commands,
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
			planResponse(configurePlanName, "hwmp", configureStage, 2, 4), &commands,
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
			planResponse(generatedPlanName, "hwmp", prepareStage, 2, 1), &firstCommands,
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

	DescribeTable("rejects a cached plan that does not match the requesting device",
		func(mutate func(*v1alpha1.NicDevice), expected string) {
			stateDir := GinkgoT().TempDir()
			device := newDevice("rail-0", "0000:64:00.0", "hwplb")
			generatedPlanName := planName(nodeName, prepareStage)
			commands := []preparePlanCommand{}
			manager := newTestPlanManager(
				preparePlanFakeExecutor(
					planResponse(generatedPlanName, "hwmp", prepareStage, 2, 1), &commands,
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
			device.Status.Ports[0].PCI = "0000:65:00.0"
		}, "is absent from the target map"),
	)

	DescribeTable("regenerates a saved plan when an input changes",
		func(mutate func(*v1alpha1.NicDevice), expectedPlatform string, expectedPlanes int) {
			stateDir := GinkgoT().TempDir()
			device := newDevice("rail-0", "0000:64:00.0", "hwplb")
			generatedPlanName := planName(nodeName, prepareStage)
			firstCommands := []preparePlanCommand{}
			_, err := generatePreparePlan(
				context.Background(), preparePlanFakeExecutor(
					planResponse(generatedPlanName, "hwmp", prepareStage, 2, 1), &firstCommands,
				), nodeName, []*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
			)
			Expect(err).NotTo(HaveOccurred())

			mutate(device)
			secondCommands := []preparePlanCommand{}
			_, err = generatePreparePlan(
				context.Background(), preparePlanFakeExecutor(
					planResponseForPlatform(
						generatedPlanName, "hwmp", prepareStage, expectedPlatform, expectedPlanes, 1,
					), &secondCommands,
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
			device.Status.Ports[0].PCI = "0000:65:00.0"
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
					planResponse(generatedPlanName, "hwmp", prepareStage, 2, 1), &firstCommands,
				), nodeName, []*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
			)
			Expect(err).NotTo(HaveOccurred())
			corrupt(stateDir, generatedPlanName)

			secondCommands := []preparePlanCommand{}
			_, err = generatePreparePlan(
				context.Background(), preparePlanFakeExecutor(
					planResponse(generatedPlanName, "hwmp", prepareStage, 2, 1), &secondCommands,
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

	It("maps swplb to swmp and passes its overlay", func() {
		stateDir := GinkgoT().TempDir()
		commands := []preparePlanCommand{}
		planName := planName(nodeName, prepareStage)
		executor := preparePlanFakeExecutor(planResponse(planName, "swmp", prepareStage, 2, 1), &commands)
		device := newDevice("swmp", "0000:64:00.0", "swplb")

		_, err := generatePreparePlan(
			context.Background(), executor, nodeName, []*v1alpha1.NicDevice{device}, blueprintsRoot, stateDir,
		)

		Expect(err).NotTo(HaveOccurred())
		Expect(commands).To(HaveLen(1))
		Expect(commands[0].args).To(ContainElements("profile=swmp", "params=deployment_mode=host-k8s,planes=2,overlay=none"))
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

	It("rejects an unsupported HWMP overlay before writing the target map", func() {
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
		Entry("software multiplane", "swplb", "swmp"),
		Entry("hardware multiplane", "hwplb", "hwmp"),
	)

	It("rejects unsupported multiplane modes", func() {
		_, err := blueprintProfile("uniplane")
		Expect(err).To(MatchError(ContainSubstring("unsupported")))
	})

	DescribeTable("maps NCO device types to doSPCX HCA names",
		func(deviceType, expected string) {
			hcaType, err := blueprintHCAType(deviceType)
			Expect(err).NotTo(HaveOccurred())
			Expect(hcaType).To(Equal(expected))
		},
		Entry("ConnectX-7", "1021", "ConnectX-7"),
		Entry("ConnectX-8", "1023", "ConnectX-8"),
		Entry("ConnectX-9", "1025", "ConnectX-9"),
		Entry("BlueField-3", "a2dc", "BlueField-3"),
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
		Entry("missing platform type", func(devices []*v1alpha1.NicDevice) {
			devices[0].Spec.Configuration.Template.SpectrumXOptimized.PlatformType = ""
			devices[1].Spec.Configuration.Template.SpectrumXOptimized.PlatformType = ""
		}, "platformType"),
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
			{BDF: "0000:64:00.0", Rail: 0},
			{BDF: "0001:15:00.0", Rail: 1},
		}))
	})

	It("rejects an unexpected generated plan stage before saving it", func() {
		stateDir := GinkgoT().TempDir()
		commands := []preparePlanCommand{}
		planName := planName(nodeName, prepareStage)
		response := planResponse(planName, "hwmp", prepareStage, 2, 1)
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
			response := planResponse(generatedPlanName, "hwmp", prepareStage, 2, 1)
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
	)
})
