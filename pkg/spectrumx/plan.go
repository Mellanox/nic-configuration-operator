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
	"fmt"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/dospcx"
)

// Plan is the homogeneous configuration intent compiled from doSPCX plans.
type Plan = dospcx.Plan

// OperationGroup is an ordered group of runtime configuration operations.
type OperationGroup = dospcx.OperationGroup

// PlanStage identifies the doSPCX configuration phase used to generate a plan.
type PlanStage = dospcx.PlanStage

const (
	// PlanStagePrepare contains persistent configuration applied before reboot.
	PlanStagePrepare = dospcx.PlanStagePrepare
	// PlanStageConfigure contains runtime configuration applied after reboot.
	PlanStageConfigure = dospcx.PlanStageConfigure
)

// PlanManager owns doSPCX target-map construction, plan generation, caching,
// persistence, and retrieval.
type PlanManager interface {
	PreparePlan(ctx context.Context, devices []*v1alpha1.NicDevice, stage PlanStage) error
	GetPreparedPlan(device *v1alpha1.NicDevice, stage PlanStage) (*Plan, error)
}

// BlueprintsDataManager installs the authored data consumed by the doSPCX planner.
type BlueprintsDataManager interface {
	InstallBlueprintsData(archive []byte) error
	RemoveBlueprintsData() error
}

type dospcxLifecycle interface {
	PlanManager
	BlueprintsDataManager
}

func (m *spectrumXConfigManager) PreparePlan(
	ctx context.Context,
	devices []*v1alpha1.NicDevice,
	stage PlanStage,
) error {
	if m == nil || m.dospcxManager == nil {
		return fmt.Errorf("doSPCX planner manager must not be nil")
	}
	return m.dospcxManager.PreparePlan(ctx, devices, stage)
}

func (m *spectrumXConfigManager) GetPreparedPlan(
	device *v1alpha1.NicDevice,
	stage PlanStage,
) (*Plan, error) {
	if m == nil || m.dospcxManager == nil {
		return nil, fmt.Errorf("doSPCX planner manager must not be nil")
	}
	return m.dospcxManager.GetPreparedPlan(device, stage)
}

func (m *spectrumXConfigManager) InstallBlueprintsData(archive []byte) error {
	if m == nil || m.dospcxManager == nil {
		return fmt.Errorf("doSPCX planner manager must not be nil")
	}
	return m.dospcxManager.InstallBlueprintsData(archive)
}

func (m *spectrumXConfigManager) RemoveBlueprintsData() error {
	if m == nil || m.dospcxManager == nil {
		return fmt.Errorf("doSPCX planner manager must not be nil")
	}
	return m.dospcxManager.RemoveBlueprintsData()
}
