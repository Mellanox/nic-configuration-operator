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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

	log.Log.V(2).Info("reconciling NicFirmwareSource object", "name", instance.Name)

	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected.
			// Return and don't requeue
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}

	cacheName := instance.Name

	// Check that FW storage is available in the pod
	if err = r.FirmwareProvisioner.IsFWStorageAvailable(); err != nil {
		log.Log.Error(err, "NIC FW storage is not available")
		return reconcile.Result{}, err
	}

	// the fw source is being deleted, we need to clean up the cache
	if instance.DeletionTimestamp != nil {
		err = r.FirmwareProvisioner.DeleteCache(instance.Name)
		if err != nil {
			return reconcile.Result{}, err
		}

		log.Log.Info("cache deleted, removing finalizer on NicFirmwareSource object", "name", instance.Name)

		// Remove the finalizer if cache was deleted
		updated := controllerutil.RemoveFinalizer(instance, consts.FirmwareSourceFinalizerName)
		if updated {
			err = r.Update(ctx, instance)
		}

		return reconcile.Result{}, err
	}

	updated := controllerutil.AddFinalizer(instance, consts.FirmwareSourceFinalizerName)
	if updated {
		err = r.Update(ctx, instance)
		if err != nil {
			log.Log.Error(err, "failed to set finalizer on NicFirmwareSource object", "name", instance.Name)
			return ctrl.Result{}, err
		}
	}

	urlsToProcess, err := r.FirmwareProvisioner.VerifyCachedBinaries(cacheName, instance.Spec.BinUrlSources)
	if err != nil {
		log.Log.Error(err, "failed to verify cached binaries", "name", instance.Name)

		if updateErr := r.updateStatus(ctx, instance, consts.FirmwareSourceCacheVerificationFailedStatus, err, nil); updateErr != nil {
			return reconcile.Result{}, updateErr
		}
		return reconcile.Result{}, err
	}
	if len(urlsToProcess) != 0 {
		if err = r.updateStatus(ctx, instance, consts.FirmwareSourceDownloadingStatus, nil, nil); err != nil {
			return reconcile.Result{}, err
		}

		err = r.FirmwareProvisioner.DownloadAndUnzipFirmwareArchives(cacheName, urlsToProcess, true)
		if err != nil {
			log.Log.Error(err, "failed to download fw binaries archives", "name", instance.Name)

			if updateErr := r.updateStatus(ctx, instance, consts.FirmwareSourceDownloadFailedStatus, err, nil); updateErr != nil {
				return reconcile.Result{}, updateErr
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
		log.Log.Error(err, "failed to add fw binaries to cache", "name", instance.Name)

		if updateErr := r.updateStatus(ctx, instance, consts.FirmwareSourceProcessingFailedStatus, err, nil); updateErr != nil {
			return reconcile.Result{}, updateErr
		}
		return reconcile.Result{}, err
	}

	return r.ValidateCache(ctx, instance)
}

func (r *NicFirmwareSourceReconciler) ValidateCache(ctx context.Context, instance *v1alpha1.NicFirmwareSource) (reconcile.Result, error) {
	versions, err := r.FirmwareProvisioner.ValidateCache(instance.Name)
	if err != nil {
		log.Log.Error(err, "failed to validate fw binaries cache", "name", instance.Name)
		if updateErr := r.updateStatus(ctx, instance, consts.FirmwareSourceProcessingFailedStatus, err, nil); updateErr != nil {
			return reconcile.Result{}, updateErr
		}
		return reconcile.Result{}, err
	}

	if err = r.updateStatus(ctx, instance, consts.FirmwareSourceSuccessStatus, nil, versions); err != nil {
		return reconcile.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *NicFirmwareSourceReconciler) updateStatus(ctx context.Context, obj *v1alpha1.NicFirmwareSource, status string, statusError error, versions map[string][]string) error {
	obj.Status.State = status
	if statusError != nil {
		obj.Status.Reason = statusError.Error()
	} else {
		obj.Status.Reason = ""
	}

	obj.Status.Versions = versions
	err := r.Status().Update(ctx, obj)
	if err != nil {
		log.Log.Error(err, "failed to update the status of NicFirmwareSource object", "name", obj.Name)
	}

	return err
}

// SetupWithManager sets up the controller with the Manager.
func (r *NicFirmwareSourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Allow multiple concurrent reconciles as each FW source gets a separate folder in the cache
		WithOptions(controller.Options{MaxConcurrentReconciles: 50}).
		For(&v1alpha1.NicFirmwareSource{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Named("nicFirmwareSourceReconciler").
		Complete(r)
}
