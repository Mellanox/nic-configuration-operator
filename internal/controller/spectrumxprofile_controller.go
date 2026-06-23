/*
2026 NVIDIA CORPORATION & AFFILIATES
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
	"fmt"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/spectrumx"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

// SpectrumXProfileReconciler reconciles Spectrum-X profile ConfigMaps (selected by
// consts.SpectrumXProfileLabel, in any namespace) and pushes the parsed profile into the
// SpectrumXManager. The ConfigMap name is used as the Spectrum-X version key referenced by
// template.spectrumXOptimized.version.
type SpectrumXProfileReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	SpectrumXManager spectrumx.SpectrumXManager

	// owners records which ConfigMap currently provides each Spectrum-X version. Since the
	// version key is the ConfigMap name and ConfigMaps are watched cluster-wide, two same-named
	// ConfigMaps in different namespaces map to the same version. owners makes that conflict
	// visible and ensures that deleting a non-owning duplicate does not wipe the active profile.
	ownersMu sync.Mutex
	owners   map[string]client.ObjectKey
}

//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

// Reconcile loads (or removes) the Spectrum-X profile carried by the reconciled ConfigMap.
func (r *SpectrumXProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLog := log.FromContext(ctx)

	cm := &corev1.ConfigMap{}
	err := r.Get(ctx, req.NamespacedName, cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// ConfigMap was deleted - drop the corresponding profile from the manager.
			r.removeProfile(ctx, req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Defensive: if the selector label is gone (e.g. it was removed), treat as removal. The
	// cache informer is filtered by the label, so a label removal surfaces as a delete event,
	// but a stale object could still reach us here.
	if _, ok := cm.Labels[consts.SpectrumXProfileLabel]; !ok {
		reqLog.Info("Spectrum-X profile label removed from ConfigMap, removing profile", "version", req.Name)
		r.removeProfile(ctx, req.NamespacedName)
		return ctrl.Result{}, nil
	}

	data, ok := cm.Data[consts.SpectrumXProfileConfigMapDataKey]
	if !ok || strings.TrimSpace(data) == "" {
		// Missing or blank profile payload - return an error to requeue until it is fixed.
		return ctrl.Result{}, fmt.Errorf(
			"spectrum-x profile ConfigMap %s/%s is missing or has an empty %q data key",
			req.Namespace, req.Name, consts.SpectrumXProfileConfigMapDataKey)
	}

	config, err := types.ParseSpectrumXConfig([]byte(data))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf(
			"failed to parse spectrum-x profile from ConfigMap %s/%s: %w", req.Namespace, req.Name, err)
	}

	r.ownersMu.Lock()
	defer r.ownersMu.Unlock()
	if r.owners == nil {
		r.owners = map[string]client.ObjectKey{}
	}
	if owner, exists := r.owners[req.Name]; exists && owner != req.NamespacedName {
		reqLog.Info("Multiple ConfigMaps define the same Spectrum-X version; the latest reconcile wins",
			"version", req.Name, "previousOwner", owner.String(), "newOwner", req.String())
	}
	r.owners[req.Name] = req.NamespacedName

	reqLog.Info("Loaded Spectrum-X profile", "version", req.Name, "configMap", req.String())
	r.SpectrumXManager.SetConfig(req.Name, config)

	return ctrl.Result{}, nil
}

// removeProfile drops the profile for a version, but only when the reconciled ConfigMap is the
// current owner (or no owner is recorded). This prevents a duplicate same-named ConfigMap in a
// different namespace from wiping the profile that another ConfigMap is actively providing.
func (r *SpectrumXProfileReconciler) removeProfile(ctx context.Context, key client.ObjectKey) {
	reqLog := log.FromContext(ctx)
	r.ownersMu.Lock()
	defer r.ownersMu.Unlock()
	if owner, exists := r.owners[key.Name]; exists && owner != key {
		reqLog.Info("Ignoring removal of non-owning Spectrum-X profile ConfigMap",
			"version", key.Name, "removed", key.String(), "owner", owner.String())
		return
	}
	delete(r.owners, key.Name)
	reqLog.Info("Removing Spectrum-X profile", "version", key.Name, "configMap", key.String())
	r.SpectrumXManager.RemoveConfig(key.Name)
}

// SetupWithManager sets up the controller with the Manager.
func (r *SpectrumXProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.owners == nil {
		r.owners = map[string]client.ObjectKey{}
	}

	// Only reconcile ConfigMaps carrying the Spectrum-X profile label. This predicate complements
	// the manager's cache selector (see cmd/nic-configuration-daemon/main.go), which already
	// restricts the cached ConfigMap informer to labeled objects.
	hasProfileLabel := predicate.NewPredicateFuncs(func(o client.Object) bool {
		_, ok := o.GetLabels()[consts.SpectrumXProfileLabel]
		return ok
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, builder.WithPredicates(hasProfileLabel)).
		Complete(r)
}
