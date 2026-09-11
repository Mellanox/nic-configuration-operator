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

package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/spectrumx"
)

// DospcxDataReconciler installs the single labeled doSPCX data bundle available to this daemon.
type DospcxDataReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	SpectrumXManager spectrumx.SpectrumXManager

	mutex sync.Mutex
}

//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

func (r *DospcxDataReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	configMaps := &corev1.ConfigMapList{}
	if err := r.List(ctx, configMaps); err != nil {
		return ctrl.Result{}, fmt.Errorf("list ConfigMaps while reconciling doSPCX data: %w", err)
	}

	bundles := make([]*corev1.ConfigMap, 0, 1)
	for index := range configMaps.Items {
		configMap := &configMaps.Items[index]
		if _, selected := configMap.Labels[consts.DospcxDataLabel]; selected && isDospcxDataConfigMap(configMap) {
			bundles = append(bundles, configMap)
		}
	}

	if len(bundles) == 0 {
		if err := r.SpectrumXManager.RemoveBlueprintsData(); err != nil {
			return ctrl.Result{}, fmt.Errorf("remove doSPCX data after its ConfigMap was removed: %w", err)
		}
		return ctrl.Result{}, nil
	}
	if len(bundles) > 1 {
		sources := make([]string, 0, len(bundles))
		for _, bundle := range bundles {
			sources = append(sources, client.ObjectKeyFromObject(bundle).String())
		}
		sort.Strings(sources)
		if err := r.SpectrumXManager.RemoveBlueprintsData(); err != nil {
			return ctrl.Result{}, fmt.Errorf(
				"multiple doSPCX data ConfigMaps are selected (%s) and the active bundle could not be deactivated: %w",
				strings.Join(sources, ", "), err)
		}
		return ctrl.Result{}, fmt.Errorf(
			"multiple doSPCX data ConfigMaps are selected (%s); exactly one bundle is supported",
			strings.Join(sources, ", "))
	}

	bundle := bundles[0]
	format := bundle.Data[consts.DospcxDataConfigMapFormatKey]
	if strings.TrimSpace(format) != consts.DospcxDataConfigMapFormat {
		return ctrl.Result{}, fmt.Errorf(
			"doSPCX data ConfigMap %s/%s has unsupported %q value %q",
			bundle.Namespace, bundle.Name, consts.DospcxDataConfigMapFormatKey, format)
	}
	archive := bundle.BinaryData[consts.DospcxDataConfigMapArchiveKey]
	if len(archive) == 0 {
		return ctrl.Result{}, fmt.Errorf(
			"doSPCX data ConfigMap %s/%s is missing or has an empty binaryData %q key",
			bundle.Namespace, bundle.Name, consts.DospcxDataConfigMapArchiveKey)
	}
	if err := r.SpectrumXManager.InstallBlueprintsData(archive); err != nil {
		return ctrl.Result{}, fmt.Errorf(
			"failed to install doSPCX data from ConfigMap %s/%s: %w", bundle.Namespace, bundle.Name, err)
	}
	log.FromContext(ctx).Info("Reconciled doSPCX data bundle",
		"configMap", client.ObjectKeyFromObject(bundle).String(),
		"format", format,
		"sourceCommit", bundle.Annotations[consts.DospcxDataCommitAnnotation],
		"sourceRef", bundle.Annotations[consts.DospcxDataRefAnnotation],
		"sourceTree", bundle.Annotations[consts.DospcxDataTreeAnnotation])
	return ctrl.Result{}, nil
}

func isDospcxDataConfigMap(configMap *corev1.ConfigMap) bool {
	if configMap == nil {
		return false
	}
	_, hasFormat := configMap.Data[consts.DospcxDataConfigMapFormatKey]
	_, hasArchive := configMap.BinaryData[consts.DospcxDataConfigMapArchiveKey]
	return hasFormat || hasArchive
}

func (r *DospcxDataReconciler) SetupWithManager(mgr ctrl.Manager) error {
	hasDospcxDataLabel := predicate.NewPredicateFuncs(func(object client.Object) bool {
		_, found := object.GetLabels()[consts.DospcxDataLabel]
		return found
	})
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.ConfigMap{}, builder.WithPredicates(hasDospcxDataLabel)).
		Complete(r)
}
