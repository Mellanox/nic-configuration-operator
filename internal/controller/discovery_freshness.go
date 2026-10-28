// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/udev"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// freshDevices checks one new inventory before this reconciliation may mutate
// hardware. Last-known CR status is retained for failed ASICs; a True condition
// from an earlier pass is never an authorization to use it.
func (r *NicDeviceReconciler) freshDevices(ctx context.Context, statuses nicDeviceConfigurationStatuses) (nicDeviceConfigurationStatuses, bool, error) {
	if r.Discover == nil {
		return nil, true, errors.New("device discovery is not configured")
	}
	inventory, err := r.Discover(ctx)
	observed, issues, globalErr := inventoryDevices(inventory, r.NodeName, err)
	list := &v1alpha1.NicDeviceList{}
	if err := r.List(ctx, list, &client.ListOptions{FieldSelector: fields.OneTermEqualSelector("status.node", r.NodeName)}); err != nil {
		return nil, true, err
	}
	stale := make(map[client.ObjectKey]bool)
	for _, status := range statuses {
		stale[client.ObjectKeyFromObject(status.device)] = true
	}
	presentKeys := make(map[string]bool)
	staleKeys := make(map[string]bool)
	for i := range list.Items {
		d := &list.Items[i]
		var issue error
		if len(d.Status.Ports) == 0 {
			issue = errors.New("device has no observed PF endpoints")
		} else {
			key := utils.PCIDeviceAddress(d.Status.Ports[0].PCI)
			presentKeys[key] = true
			issue = errors.Join(globalErr, issues[key])
			fresh, exists := observed[key]
			if issue == nil && (!exists || !sameDiscoveredDevice(d.Status, fresh.Status)) {
				issue = errors.New("device identity, endpoints, or firmware changed; waiting for discovery status refresh")
			}
			if issue != nil {
				staleKeys[key] = true
			}
		}
		stale[client.ObjectKeyFromObject(d)] = issue != nil
		if setDiscoveryCondition(d, issue) {
			if err := r.Status().Update(ctx, d); err != nil {
				return nil, true, err
			}
		}
		// Keep resource versions current for later status updates in this pass.
		for _, status := range statuses {
			if client.ObjectKeyFromObject(status.device) == client.ObjectKeyFromObject(d) {
				*status.device = *d.DeepCopy()
			}
		}
	}
	markStaleDiscoveryGroups(statuses, stale, presentKeys, staleKeys)

	pending := globalErr != nil || len(issues) > 0
	for _, isStale := range stale {
		pending = pending || isStale
	}
	// Naming owns host-wide files and activation. Retain the entire existing
	// domain when one owner is stale; never rewrite it from a filtered subset.
	var expected map[string]udev.ExpectedInterfaceNames
	if pending {
		all := make([]*v1alpha1.NicDevice, 0, len(statuses))
		for _, status := range statuses {
			all = append(all, status.device)
		}
		var err error
		expected, err = udev.ExpectedNames(all)
		if err != nil {
			return nil, true, err
		}
	}
	if pending {
		for _, status := range statuses {
			d := status.device
			key := client.ObjectKeyFromObject(d)
			if stale[key] {
				continue
			}
			// Current names cannot prove that a removed template's rules were
			// cleaned up. That requires a successful full-domain Apply.
			if d.Spec.InterfaceNameTemplate == nil && meta.FindStatusCondition(d.Status.Conditions, consts.InterfaceNameCondition) != nil {
				stale[key] = true
				continue
			}
			if err := r.refreshInterfaceNames(ctx, d, expected); err != nil {
				stale[key] = true
			}
		}
		// Naming eligibility can invalidate a member after inventory validation.
		// Re-close all shared domains before returning any mutating workset.
		markStaleDiscoveryGroups(statuses, stale, presentKeys, staleKeys)
	}
	fresh := make(nicDeviceConfigurationStatuses, 0, len(statuses))
	for _, status := range statuses {
		if !stale[client.ObjectKeyFromObject(status.device)] {
			fresh = append(fresh, status)
		}
	}

	return fresh, pending, nil
}

// markStaleDiscoveryGroups expands the blocked workset to NCO's shared
// configuration domains. It only changes this pass's stale map; it performs no IO.
func markStaleDiscoveryGroups(statuses nicDeviceConfigurationStatuses, stale map[client.ObjectKey]bool, presentKeys, staleKeys map[string]bool) {
	planDevices := spectrumXDevicesForPlan(statuses)
	// Domains can overlap: a Bay peer may participate in the node plan while
	// its sibling does not. Iterate monotonically until no domain adds a block.
	for changed := true; changed; {
		changed = false
		mark := func(d *v1alpha1.NicDevice) {
			key := client.ObjectKeyFromObject(d)
			if !stale[key] {
				stale[key] = true
				changed = true
			}
		}
		for _, status := range statuses {
			d := status.device
			if stale[client.ObjectKeyFromObject(d)] && len(d.Status.Ports) > 0 {
				staleKeys[utils.PCIDeviceAddress(d.Status.Ports[0].PCI)] = true
			}
		}
		for _, status := range statuses {
			d := status.device
			if d.Status.NetworkBay != nil {
				peer := utils.PCIDeviceAddress(d.Status.NetworkBay.PeerPCI)
				if !presentKeys[peer] || staleKeys[peer] {
					mark(d)
				}
			}
		}
		planStale := false
		for _, d := range planDevices {
			planStale = planStale || stale[client.ObjectKeyFromObject(d)]
		}
		if planStale {
			for _, d := range planDevices {
				mark(d)
			}
		}
	}

}

// refreshInterfaceNames reads each PF atomically. A failed read keeps the last
// known values, reports the failure, and never publishes invented empty names.
func (r *NicDeviceReconciler) refreshInterfaceNames(ctx context.Context, d *v1alpha1.NicDevice, expected map[string]udev.ExpectedInterfaceNames) error {
	if r.ReadInterfaceNames == nil {
		return errors.New("interface name discovery is not configured")
	}
	var failures []error
	for i := range d.Status.Ports {
		port := &d.Status.Ports[i]
		names, err := r.ReadInterfaceNames(ctx, port.PCI)
		if err != nil {
			failures = append(failures, fmt.Errorf("read interface names for %s: %w", port.PCI, err))
			continue
		}
		port.NetworkInterface, port.RdmaInterface = names.Netdev, names.RDMA
		want := expected[port.PCI]
		if want.NetDevice != "" && want.NetDevice != names.Netdev {
			failures = append(failures, fmt.Errorf("net:%s expected=%s actual=%s", port.PCI, want.NetDevice, names.Netdev))
		}
		if want.RdmaDevice != "" && want.RdmaDevice != names.RDMA {
			failures = append(failures, fmt.Errorf("rdma:%s expected=%s actual=%s", port.PCI, want.RdmaDevice, names.RDMA))
		}
	}
	failure := errors.Join(failures...)
	if d.Spec.InterfaceNameTemplate != nil {
		reason, status, message := consts.InterfaceNameAppliedReason, metav1.ConditionTrue, "Interface names applied successfully"
		if failure != nil {
			reason, status, message = consts.InterfaceNameMismatchReason, metav1.ConditionFalse, failure.Error()
		}
		meta.SetStatusCondition(&d.Status.Conditions, metav1.Condition{Type: consts.InterfaceNameCondition, Reason: reason, Status: status, Message: message, ObservedGeneration: d.Generation})
	}
	if err := r.Status().Update(ctx, d); err != nil {
		failure = errors.Join(failure, err)
	}
	return failure
}
