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

	maintenanceoperator "github.com/Mellanox/maintenance-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/host"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
)

type MaintenanceManager interface {
	ScheduleMaintenance(ctx context.Context) error
	MaintenanceAllowed(ctx context.Context) (bool, error)
	ReleaseMaintenance(ctx context.Context) error
	Reboot() error
}

type maintenanceManager struct {
	client    client.Client
	hostUtils host.HostUtils
	nodeName  string
	namespace string
}

func (m maintenanceManager) getNodeMaintenanceObject(ctx context.Context) (*maintenanceoperator.NodeMaintenance, error) {
	list := maintenanceoperator.NodeMaintenanceList{}
	err := m.client.List(ctx, &list, client.InNamespace(m.namespace))
	if err != nil {
		log.Log.Error(err, "failed to get node maintenance objects")
		return nil, err
	}

	for _, obj := range list.Items {
		if obj.Spec.RequestorID == consts.MaintenanceRequestor && obj.Spec.NodeName == m.nodeName {
			return &obj, nil
		}
	}

	return nil, nil
}

func (m maintenanceManager) ScheduleMaintenance(ctx context.Context) error {
	log.Log.Info("maintenanceManager.ScheduleMaintenance()")

	scheduledMaintenance, err := m.getNodeMaintenanceObject(ctx)
	if err != nil {
		log.Log.Error(err, "failed to schedule node maintenance")
		return err
	}

	if scheduledMaintenance != nil {
		// Maintenance already scheduled by us, nothing to do
		return nil
	}

	maintenanceRequest := &maintenanceoperator.NodeMaintenance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      consts.MaintenanceRequestName,
			Namespace: m.namespace,
		},
		Spec: maintenanceoperator.NodeMaintenanceSpec{
			RequestorID:          consts.MaintenanceRequestor,
			AdditionalRequestors: nil,
			NodeName:             m.nodeName,
			Cordon:               true,
			WaitForPodCompletion: nil,
			DrainSpec: &maintenanceoperator.DrainSpec{
				Force:          true,
				DeleteEmptyDir: true,
			},
		},
	}

	err = m.client.Create(ctx, maintenanceRequest)
	if err != nil {
		log.Log.Error(err, "failed to schedule node maintenance")
		return err
	}

	return nil
}

func (m maintenanceManager) MaintenanceAllowed(ctx context.Context) (bool, error) {
	log.Log.Info("maintenanceManager.MaintenanceAllowed()")
	scheduledMaintenance, err := m.getNodeMaintenanceObject(ctx)
	if err != nil {
		log.Log.Error(err, "failed to get node maintenance")
		return false, err
	}

	if scheduledMaintenance == nil {
		// We want to perform maintenance on NICs only when node is properly prepared
		return false, nil
	}

	readyCondition := meta.FindStatusCondition(scheduledMaintenance.Status.Conditions, maintenanceoperator.ConditionTypeReady)
	if readyCondition == nil {
		log.Log.V(2).Info("couldn't retrieve maintenance condition, retry")
		return false, nil
	}

	if readyCondition.Status != metav1.ConditionTrue {
		log.Log.V(2).Info("maintenance is not ready yet", "reason", readyCondition.Reason, "message", readyCondition.Message)
		return false, nil
	}

	return true, nil
}

func (m maintenanceManager) ReleaseMaintenance(ctx context.Context) error {
	log.Log.Info("maintenanceManager.ReleaseMaintenance()")

	scheduledMaintenance, err := m.getNodeMaintenanceObject(ctx)
	if err != nil {
		log.Log.Error(err, "failed to get node maintenance")
		return err
	}

	if scheduledMaintenance != nil {
		err = m.client.Delete(ctx, scheduledMaintenance)
		if err != nil {
			log.Log.Error(err, "failed to release node maintenance")
			return err
		}
	}

	return nil
}

func (m maintenanceManager) Reboot() error {
	log.Log.Info("maintenanceManager.Reboot()")

	return m.hostUtils.ScheduleReboot()
}

func New(client client.Client, hostUtils host.HostUtils, nodeName string, namespace string) MaintenanceManager {
	return maintenanceManager{client: client, hostUtils: hostUtils, nodeName: nodeName, namespace: namespace}
}
