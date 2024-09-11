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

package maintenance

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

type MaintenanceManager interface {
	ScheduleMaintenance(ctx context.Context) error
	MaintenanceAllowed(ctx context.Context) (bool, error)
	ReleaseMaintenance(ctx context.Context) error
	Reboot() error
}

type maintenanceManager struct{}

func (m maintenanceManager) ScheduleMaintenance(ctx context.Context) error {
	log.Log.Info("maintenanceManager.ScheduleMaintenance()")
	//TODO implement me
	return nil
}

func (m maintenanceManager) MaintenanceAllowed(ctx context.Context) (bool, error) {
	log.Log.Info("maintenanceManager.MaintenanceAllowed()")
	//TODO implement me
	return true, nil
}

func (m maintenanceManager) ReleaseMaintenance(ctx context.Context) error {
	log.Log.Info("maintenanceManager.ReleaseMaintenance()")
	//TODO implement me
	return nil
}

func (m maintenanceManager) Reboot() error {
	log.Log.Info("maintenanceManager.Reboot()")
	//TODO implement me
	return nil
}

func New() MaintenanceManager {
	return maintenanceManager{}
}
