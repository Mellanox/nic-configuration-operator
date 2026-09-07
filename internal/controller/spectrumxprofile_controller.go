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
	"sort"
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

// SpectrumXProfileReconciler reconciles Spectrum-X ConfigMaps selected by
// consts.SpectrumXProfileLabel. Legacy profile ConfigMaps are loaded into SpectrumXManager by
// version, while doSPCX data bundles are restored into the DMS data directory.
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

	// blueprintsSources tracks manager-wide doSPCX bundle ConfigMaps separately from versioned
	// legacy profiles. Keeping the previous set lets delete and label-removal requests avoid the
	// legacy RemoveConfig path even though the object is no longer available from the cache.
	blueprintsMu      sync.Mutex
	blueprintsSources map[client.ObjectKey]struct{}
}

//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

// Reconcile loads (or removes) the Spectrum-X profile carried by the reconciled ConfigMap.
func (r *SpectrumXProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLog := log.FromContext(ctx)

	// Reconcile the doSPCX data bundle from the complete selected ConfigMap set. The bundle is
	// manager-wide rather than versioned, so accepting more than one source would make the active
	// tree depend on informer event order. This also handles deletion and label-removal events,
	// where the reconciled object itself is no longer available from the filtered cache.
	blueprintsRequest, err := r.reconcileBlueprintsData(ctx, req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, err
	}

	cm := &corev1.ConfigMap{}
	err = r.Get(ctx, req.NamespacedName, cm)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if !blueprintsRequest {
				// A legacy profile ConfigMap was deleted - drop it from the manager.
				r.removeProfile(ctx, req.NamespacedName)
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Defensive: if the selector label is gone (e.g. it was removed), treat as removal. The
	// cache informer is filtered by the label, so a label removal surfaces as a delete event,
	// but a stale object could still reach us here.
	if _, ok := cm.Labels[consts.SpectrumXProfileLabel]; !ok {
		if !blueprintsRequest {
			reqLog.Info("Spectrum-X profile label removed from ConfigMap, removing profile", "version", req.Name)
			r.removeProfile(ctx, req.NamespacedName)
		}
		return ctrl.Result{}, nil
	}

	if isBlueprintsDataConfigMap(cm) {
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

func (r *SpectrumXProfileReconciler) reconcileBlueprintsData(
	ctx context.Context,
	requestKey client.ObjectKey,
) (bool, error) {
	r.blueprintsMu.Lock()
	defer r.blueprintsMu.Unlock()

	_, requestWasBlueprintsSource := r.blueprintsSources[requestKey]
	configMaps := &corev1.ConfigMapList{}
	if err := r.List(ctx, configMaps); err != nil {
		return requestWasBlueprintsSource,
			fmt.Errorf("list Spectrum-X ConfigMaps while reconciling doSPCX data: %w", err)
	}

	bundles := make([]*corev1.ConfigMap, 0, 1)
	currentSources := map[client.ObjectKey]struct{}{}
	for index := range configMaps.Items {
		configMap := &configMaps.Items[index]
		if _, selected := configMap.Labels[consts.SpectrumXProfileLabel]; selected && isBlueprintsDataConfigMap(configMap) {
			bundles = append(bundles, configMap)
			currentSources[client.ObjectKeyFromObject(configMap)] = struct{}{}
		}
	}
	if _, requestIsBlueprintsSource := currentSources[requestKey]; requestIsBlueprintsSource {
		requestWasBlueprintsSource = true
	}
	r.blueprintsSources = currentSources
	sort.Slice(bundles, func(i, j int) bool {
		if bundles[i].Namespace == bundles[j].Namespace {
			return bundles[i].Name < bundles[j].Name
		}
		return bundles[i].Namespace < bundles[j].Namespace
	})

	if len(bundles) == 0 {
		if err := r.SpectrumXManager.RemoveBlueprintsData(); err != nil {
			return requestWasBlueprintsSource,
				fmt.Errorf("remove doSPCX data after its ConfigMap was removed: %w", err)
		}
		return requestWasBlueprintsSource, nil
	}
	if len(bundles) > 1 {
		sources := make([]string, 0, len(bundles))
		for _, bundle := range bundles {
			sources = append(sources, client.ObjectKeyFromObject(bundle).String())
		}
		if err := r.SpectrumXManager.RemoveBlueprintsData(); err != nil {
			return requestWasBlueprintsSource, fmt.Errorf(
				"multiple doSPCX data ConfigMaps are selected (%s) and the active bundle could not be deactivated: %w",
				strings.Join(sources, ", "), err)
		}
		return requestWasBlueprintsSource, fmt.Errorf(
			"multiple doSPCX data ConfigMaps are selected (%s); exactly one bundle is supported",
			strings.Join(sources, ", "))
	}

	bundle := bundles[0]
	format, hasFormat := bundle.Data[consts.SpectrumXBlueprintsConfigMapFormatKey]
	if !hasFormat || strings.TrimSpace(format) != consts.SpectrumXBlueprintsConfigMapFormat {
		return requestWasBlueprintsSource, fmt.Errorf(
			"spectrum-x doSPCX data ConfigMap %s/%s has unsupported %q value %q",
			bundle.Namespace, bundle.Name, consts.SpectrumXBlueprintsConfigMapFormatKey, format)
	}
	archive, hasArchive := bundle.BinaryData[consts.SpectrumXBlueprintsConfigMapArchiveKey]
	if !hasArchive || len(archive) == 0 {
		return requestWasBlueprintsSource, fmt.Errorf(
			"spectrum-x doSPCX data ConfigMap %s/%s is missing or has an empty binaryData %q key",
			bundle.Namespace, bundle.Name, consts.SpectrumXBlueprintsConfigMapArchiveKey)
	}
	if err := r.SpectrumXManager.InstallBlueprintsData(archive); err != nil {
		return requestWasBlueprintsSource, fmt.Errorf(
			"failed to install doSPCX data from ConfigMap %s/%s: %w", bundle.Namespace, bundle.Name, err)
	}
	log.FromContext(ctx).V(2).Info("Reconciled doSPCX data bundle",
		"configMap", client.ObjectKeyFromObject(bundle).String(),
		"format", format,
		"sourceCommit", bundle.Annotations[consts.SpectrumXBlueprintsCommitAnnotation],
		"sourceRef", bundle.Annotations[consts.SpectrumXBlueprintsRefAnnotation],
		"sourceTree", bundle.Annotations[consts.SpectrumXBlueprintsTreeAnnotation])
	return requestWasBlueprintsSource, nil
}

func isBlueprintsDataConfigMap(configMap *corev1.ConfigMap) bool {
	if configMap == nil {
		return false
	}
	_, hasFormat := configMap.Data[consts.SpectrumXBlueprintsConfigMapFormatKey]
	_, hasArchive := configMap.BinaryData[consts.SpectrumXBlueprintsConfigMapArchiveKey]
	return hasFormat || hasArchive
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
	if r.blueprintsSources == nil {
		r.blueprintsSources = map[client.ObjectKey]struct{}{}
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
