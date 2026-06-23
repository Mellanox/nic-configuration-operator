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
	"testing"

	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	spectrumxmocks "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/mocks"
)

const validSpectrumXProfile = `
useSoftwareCCAlgorithm: true
docaCCVersion: "1.0"
mlxConfig:
  swplb:
    "1023":
      postBreakout:
        SRIOV_EN: "1"
runtimeConfig:
  roce: []
`

func newProfileReconciler(t *testing.T, objs ...*corev1.ConfigMap) (*SpectrumXProfileReconciler, *spectrumxmocks.SpectrumXManager) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add scheme: %v", err)
	}

	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, o := range objs {
		builder = builder.WithObjects(o)
	}

	mgr := spectrumxmocks.NewSpectrumXManager(t)
	return &SpectrumXProfileReconciler{
		Client:           builder.Build(),
		Scheme:           scheme,
		SpectrumXManager: mgr,
	}, mgr
}

// All tests use a single (version, namespace) pair; the controller keys the manager map on the
// ConfigMap name, and namespace is irrelevant since profile ConfigMaps may live in any namespace.
const (
	testProfileVersion = "RA2.0"
	testProfileNS      = "any-ns"
)

func profileConfigMap(withLabel bool, data map[string]string) *corev1.ConfigMap {
	labels := map[string]string{}
	if withLabel {
		labels[consts.SpectrumXProfileLabel] = ""
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testProfileVersion, Namespace: testProfileNS, Labels: labels},
		Data:       data,
	}
}

func reconcileProfile(r *SpectrumXProfileReconciler) (ctrl.Result, error) {
	return r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testProfileVersion, Namespace: testProfileNS},
	})
}

func TestSpectrumXProfile_SetConfigOnValidConfigMap(t *testing.T) {
	cm := profileConfigMap(true, map[string]string{
		consts.SpectrumXProfileConfigMapDataKey: validSpectrumXProfile,
	})
	r, mgr := newProfileReconciler(t, cm)
	mgr.On("SetConfig", testProfileVersion, mock.AnythingOfType("*types.SpectrumXConfig")).Once()

	if _, err := reconcileProfile(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSpectrumXProfile_RemoveConfigOnDeletedConfigMap(t *testing.T) {
	// No ConfigMap in the fake client => Get returns NotFound => RemoveConfig.
	r, mgr := newProfileReconciler(t)
	mgr.On("RemoveConfig", testProfileVersion).Once()

	if _, err := reconcileProfile(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSpectrumXProfile_RemoveConfigWhenLabelMissing(t *testing.T) {
	cm := profileConfigMap(false, map[string]string{
		consts.SpectrumXProfileConfigMapDataKey: validSpectrumXProfile,
	})
	r, mgr := newProfileReconciler(t, cm)
	mgr.On("RemoveConfig", testProfileVersion).Once()

	if _, err := reconcileProfile(r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSpectrumXProfile_ErrorWhenDataKeyMissing(t *testing.T) {
	cm := profileConfigMap(true, map[string]string{"other": "x"})
	r, _ := newProfileReconciler(t, cm)

	if _, err := reconcileProfile(r); err == nil {
		t.Fatal("expected error for missing profile data key, got nil")
	}
}

func TestSpectrumXProfile_ErrorWhenYAMLMalformed(t *testing.T) {
	cm := profileConfigMap(true, map[string]string{
		consts.SpectrumXProfileConfigMapDataKey: "mlxConfig: [bad: yaml",
	})
	r, _ := newProfileReconciler(t, cm)

	if _, err := reconcileProfile(r); err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
}

func TestSpectrumXProfile_ErrorWhenDataKeyWhitespaceOnly(t *testing.T) {
	cm := profileConfigMap(true, map[string]string{
		consts.SpectrumXProfileConfigMapDataKey: "   \n\t ",
	})
	r, _ := newProfileReconciler(t, cm)

	if _, err := reconcileProfile(r); err == nil {
		t.Fatal("expected error for whitespace-only profile data, got nil")
	}
}

func TestSpectrumXProfile_DeleteOfNonOwnerKeepsActiveProfile(t *testing.T) {
	// ConfigMap "RA2.0" in ns-a is the active owner of the version.
	cmA := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: testProfileVersion, Namespace: "ns-a",
			Labels: map[string]string{consts.SpectrumXProfileLabel: ""},
		},
		Data: map[string]string{consts.SpectrumXProfileConfigMapDataKey: validSpectrumXProfile},
	}
	r, mgr := newProfileReconciler(t, cmA)
	// Establish ownership; SetConfig is expected exactly once. RemoveConfig must NOT be called.
	mgr.On("SetConfig", testProfileVersion, mock.AnythingOfType("*types.SpectrumXConfig")).Once()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testProfileVersion, Namespace: "ns-a"},
	}); err != nil {
		t.Fatalf("unexpected error establishing ownership: %v", err)
	}

	// A same-named ConfigMap in ns-b is deleted (not present in the client => NotFound). Because
	// ns-a owns the version, the active profile must be preserved (no RemoveConfig call). The
	// mock cleanup would fail if RemoveConfig were called without an expectation.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: testProfileVersion, Namespace: "ns-b"},
	}); err != nil {
		t.Fatalf("unexpected error on non-owner delete: %v", err)
	}
}
