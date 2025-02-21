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

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/firmware"
)

// NicFirmwareSourceReconciler reconciles a NicDevice object
type NicFirmwareSourceReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	FirmwareProvisioner firmware.FirmwareProvisioner
}

// Reconcile reconciles the NicFirmwareSource object
func (r *NicFirmwareSourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// Fetch the HostDeviceNetwork instance
	instance := &v1alpha1.NicFirmwareSource{}
	err := r.Get(ctx, req.NamespacedName, instance)

	// TODO use finalizers to clean up cache storage after CR deletion

	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	if err := r.FirmwareProvisioner.IsFWStorageAvailable(); err != nil {
		log.Log.Error(err, "NIC FW storage is not available")
		return reconcile.Result{}, err
	}

	cacheName := instance.Name

	urlsToProcess, err := r.FirmwareProvisioner.VerifyCachedBinaries(cacheName, instance.Spec.BinUrlSources)
	if err != nil {
		if err = r.updateStatus(ctx, instance, consts.FirmwareSourceCacheVerificationFailedStatus, err, nil); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, err
	}
	if len(urlsToProcess) != 0 {
		if err = r.updateStatus(ctx, instance, consts.FirmwareSourceDownloadingStatus, nil, nil); err != nil {
			return reconcile.Result{}, err
		}

		err = r.FirmwareProvisioner.DownloadAndUnzipFirmwareArchives(cacheName, urlsToProcess, true)
		if err != nil {
			if err = r.updateStatus(ctx, instance, consts.FirmwareSourceDownloadFailedStatus, err, nil); err != nil {
				return reconcile.Result{}, err
			}
			return reconcile.Result{}, err
		}
	} else {
		log.Log.Info("Files for all requested URLs already present, skipping download", "cacheName", instance.Name)
	}

	if err = r.updateStatus(ctx, instance, consts.FirmwareSourceProcessingStatus, nil, nil); err != nil {
		return reconcile.Result{}, err
	}

	err = r.FirmwareProvisioner.AddFirmwareBinariesToCacheByMetadata(cacheName)
	if err != nil {
		if err = r.updateStatus(ctx, instance, consts.FirmwareSourceProcessingFailedStatus, err, nil); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, err
	}

	return r.ValidateCache(ctx, instance)
}

func (r *NicFirmwareSourceReconciler) ValidateCache(ctx context.Context, instance *v1alpha1.NicFirmwareSource) (reconcile.Result, error) {
	versions, err := r.FirmwareProvisioner.ValidateCache(instance.Name)
	if err != nil {
		if err = r.updateStatus(ctx, instance, consts.FirmwareSourceProcessingFailedStatus, err, nil); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, err
	}

	if err = r.updateStatus(ctx, instance, consts.FirmwareSourceSuccessStatus, nil, versions); err != nil {
		return reconcile.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *NicFirmwareSourceReconciler) updateStatus(ctx context.Context, obj *v1alpha1.NicFirmwareSource, status string, statusError error, versions map[string][]string) error {
	// We change the status of the object several times during the reconciliation, need to get the latest version first
	err := r.Get(ctx, types.NamespacedName{Name: obj.Name, Namespace: obj.Namespace}, obj)
	if err != nil {
		return err
	}

	obj.Status.State = status
	if statusError != nil {
		obj.Status.Reason = statusError.Error()
	} else {
		obj.Status.Reason = ""
	}

	obj.Status.Versions = versions
	return r.Status().Update(ctx, obj)
}

// SetupWithManager sets up the controller with the Manager.
func (r *NicFirmwareSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	controller := ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.NicFirmwareSource{}, builder.WithPredicates(predicate.GenerationChangedPredicate{}))

	return controller.
		Named("nicFirmwareSourceReconciler").
		Complete(r)
}
