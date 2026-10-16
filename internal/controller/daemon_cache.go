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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

// NicDeviceNodeSelectorSupported reports whether the API server accepts a status.node field
// selector on NicDevices. It is false when the installed NicDevice CRD does not declare
// .status.node in selectableFields, or the API server does not support CRD selectable fields.
func NicDeviceNodeSelectorSupported(ctx context.Context, reader client.Reader, namespace, nodeName string) (bool, error) {
	err := reader.List(ctx, &v1alpha1.NicDeviceList{},
		client.InNamespace(namespace),
		client.MatchingFields{"status.node": nodeName},
		client.Limit(1))
	if err == nil {
		return true, nil
	}
	if apierrors.IsBadRequest(err) {
		return false, nil
	}
	return false, err
}

// AddNodeScopedCacheObjects limits the Node informer, and the NicDevice informer when
// scopeNicDevices is true, to objects belonging to nodeName. Without it every daemon caches
// every Node and NicDevice in the cluster, so per-pod memory grows with cluster size.
// A NicDevice enters the scoped cache only once its status.node is set, so a device that has
// not been assigned a node yet must be read through an uncached reader.
func AddNodeScopedCacheObjects(byObject map[client.Object]cache.ByObject, nodeName string, scopeNicDevices bool) {
	byObject[&corev1.Node{}] = cache.ByObject{
		Field: fields.OneTermEqualSelector("metadata.name", nodeName),
	}
	if scopeNicDevices {
		byObject[&v1alpha1.NicDevice{}] = cache.ByObject{
			Field: fields.OneTermEqualSelector("status.node", nodeName),
		}
	}
}
