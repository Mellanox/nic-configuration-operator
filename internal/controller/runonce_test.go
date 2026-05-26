// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	deviceDiscoveryMocks "github.com/Mellanox/nic-configuration-operator/pkg/devicediscovery/mocks"
	hostMocks "github.com/Mellanox/nic-configuration-operator/pkg/host/mocks"
)

const (
	runOnceTestNode      = "test-node"
	runOnceTestNamespace = "test-ns"
	runOnceTestPCI       = "0000:3b:00.0"
)

// buildRunOnceFixture returns a *DeviceDiscoveryController wired to a fake k8s client and mocks.
// existing seeds NicDevice CRs into the fake client. observed is what DiscoverNicDevices will
// return. interceptorFuncs lets the caller force errors on specific client operations.
func buildRunOnceFixture(
	t *testing.T,
	existing []*v1alpha1.NicDevice,
	observed map[string]v1alpha1.NicDevice,
	interceptorFuncs interceptor.Funcs,
) *DeviceDiscoveryController {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add v1alpha1 scheme: %v", err)
	}

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: runOnceTestNode}}

	objs := make([]client.Object, 0, 1+len(existing))
	objs = append(objs, node)
	for _, d := range existing {
		objs = append(objs, d)
	}

	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&v1alpha1.NicDevice{}, "status.node", func(o client.Object) []string {
			return []string{o.(*v1alpha1.NicDevice).Status.Node}
		}).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.NicDevice{}).
		WithInterceptorFuncs(interceptorFuncs)

	k8sClient := builder.Build()

	dd := &deviceDiscoveryMocks.DeviceDiscovery{}
	dd.On("DiscoverNicDevices").Return(observed, nil)

	hu := &hostMocks.HostUtils{}
	hu.On("DiscoverOfedVersion").Return("")

	return NewDeviceDiscoveryController(k8sClient, dd, hu, runOnceTestNode, runOnceTestNamespace)
}

func newObservedDevice(pci string, deviceType string) v1alpha1.NicDevice {
	return v1alpha1.NicDevice{
		Status: v1alpha1.NicDeviceStatus{
			Type:  deviceType,
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: pci}},
		},
	}
}

func newExistingCR(name, pci, node string) *v1alpha1.NicDevice {
	return &v1alpha1.NicDevice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: runOnceTestNamespace},
		Status: v1alpha1.NicDeviceStatus{
			Node:  node,
			Type:  "nic",
			Ports: []v1alpha1.NicDevicePortSpec{{PCI: pci}},
		},
	}
}

func TestRunOnce_AllSucceed_ReturnsNil(t *testing.T) {
	observed := map[string]v1alpha1.NicDevice{
		"0000:3b:00": newObservedDevice(runOnceTestPCI, "nic"),
	}

	ctrl := buildRunOnceFixture(t, nil, observed, interceptor.Funcs{})

	if err := ctrl.RunOnce(context.Background()); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}

	// Verify the CR was created with populated status.
	list := &v1alpha1.NicDeviceList{}
	if err := ctrl.List(context.Background(), list); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("expected 1 CR, got %d", len(list.Items))
	}
	if list.Items[0].Status.Type != "nic" {
		t.Fatalf("expected populated status, got %+v", list.Items[0].Status)
	}
}

func TestRunOnce_StatusUpdateFails_ReturnsPerDeviceError(t *testing.T) {
	observed := map[string]v1alpha1.NicDevice{
		"0000:3b:00": newObservedDevice(runOnceTestPCI, "nic"),
	}

	funcs := interceptor.Funcs{
		SubResourceUpdate: func(_ context.Context, _ client.Client, _ string, _ client.Object, _ ...client.SubResourceUpdateOption) error {
			return errors.New("simulated status update failure")
		},
	}
	ctrl := buildRunOnceFixture(t, nil, observed, funcs)

	err := ctrl.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected per-device error, got nil")
	}
	if !strings.Contains(err.Error(), "per-device error") {
		t.Fatalf("expected error to mention 'per-device error', got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "simulated status update failure") {
		t.Fatalf("expected error to wrap the underlying failure, got %q", err.Error())
	}
}

func TestRunOnce_DeleteFails_ReturnsPerDeviceError(t *testing.T) {
	// A CR exists for a device that's no longer observed → reconcile tries to delete it.
	existing := []*v1alpha1.NicDevice{
		newExistingCR("stale-cr", "0000:aa:00.0", runOnceTestNode),
	}
	// observed map doesn't contain 0000:aa:00, so reconcile will attempt to delete the stale CR.
	observed := map[string]v1alpha1.NicDevice{}

	funcs := interceptor.Funcs{
		Delete: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.DeleteOption) error {
			return errors.New("simulated delete failure")
		},
	}
	ctrl := buildRunOnceFixture(t, existing, observed, funcs)

	err := ctrl.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected per-device error, got nil")
	}
	if !strings.Contains(err.Error(), "per-device error") {
		t.Fatalf("expected error to mention 'per-device error', got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "simulated delete failure") {
		t.Fatalf("expected error to wrap the underlying failure, got %q", err.Error())
	}
}

func TestReconcile_PeriodicMode_SwallowsPerDeviceErrors(t *testing.T) {
	// Regression check: passing nil for perDeviceErrs (operator's periodic loop path) must
	// preserve the existing best-effort behavior — Delete failures are logged but reconcile
	// still returns nil so the 5-minute tick keeps the loop alive.
	existing := []*v1alpha1.NicDevice{
		newExistingCR("stale-cr", "0000:aa:00.0", runOnceTestNode),
	}
	observed := map[string]v1alpha1.NicDevice{}

	funcs := interceptor.Funcs{
		Delete: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.DeleteOption) error {
			return errors.New("simulated delete failure")
		},
	}
	ctrl := buildRunOnceFixture(t, existing, observed, funcs)

	if err := ctrl.reconcile(context.Background(), nil); err != nil {
		t.Fatalf("periodic-mode reconcile should swallow per-device errors, got %v", err)
	}
}

func TestRunUntilSuccess_RetriesPerDeviceErrorsThenSucceeds(t *testing.T) {
	// First call: Status().Update fails → RunOnce surfaces an error → retry triggers.
	// Second call: interceptor lets the update through → RunOnce returns nil → retry exits.
	observed := map[string]v1alpha1.NicDevice{
		"0000:3b:00": newObservedDevice(runOnceTestPCI, "nic"),
	}

	var statusUpdateCalls atomic.Int32
	funcs := interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if statusUpdateCalls.Add(1) == 1 {
				return errors.New("transient status update failure")
			}
			return c.Status().Update(ctx, obj, opts...)
		},
	}
	ctrl := buildRunOnceFixture(t, nil, observed, funcs)

	// Use a microsecond interval to keep the test fast.
	if err := ctrl.RunUntilSuccess(context.Background(), 5, 0); err != nil {
		t.Fatalf("expected retry to recover, got %v", err)
	}
	if got := statusUpdateCalls.Load(); got != 2 {
		t.Fatalf("expected 2 Status().Update attempts (1 fail + 1 success), got %d", got)
	}
}
