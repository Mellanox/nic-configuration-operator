// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"context"
	"errors"
	"fmt"

	dmsconfiguration "github.com/Mellanox/dms-lib/pkg/configuration"
	"github.com/Mellanox/dms-lib/pkg/device"
	dmsfirmware "github.com/Mellanox/dms-lib/pkg/firmware"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

// ApplyRuntimeConfiguration retains NCO carrier/template policy while the library
// owns native observations, writes and convergence. Discovery supplies real RDMA
// endpoints; the adapter never derives them from a netdev or PCI function index.
func (h configurationManager) ApplyRuntimeConfiguration(ctx context.Context, nic *v1alpha1.NicDevice) (*types.RuntimeConfigurationApplyResult, error) {
	result := &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}
	if ctx == nil || nic == nil {
		return result, fmt.Errorf("context and device are required")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if nic.Spec.Configuration == nil || nic.Spec.Configuration.Template == nil {
		result.Status = types.ApplyStatusNothingToDo
		return result, nil
	}
	if err := (&configValidationImpl{utils: h.configurationUtils}).validatePortCarrierState(nic); err != nil {
		return result, err
	}
	desired := templateRuntimeConfig(nic.Spec.Configuration.Template)
	if desired.MaxReadRequestSize == 0 && desired.RoceMode == 0 && desired.Qos == nil && desired.RuntimePerf == nil {
		result.Status = types.ApplyStatusNothingToDo
		return result, nil
	}
	if h.runtime == nil || h.discover == nil {
		return result, fmt.Errorf("runtime manager and discovery must be initialized")
	}
	inventory, err := h.discover(ctx)
	if err != nil {
		return result, err
	}
	observed, err := runtimeDevice(nic, inventory)
	if err != nil {
		return result, err
	}
	ports, err := TranslateRuntime(nic.Spec.Configuration.Template, observed)
	if err != nil {
		return result, err
	}
	PreserveUnboundPFPolicy(ports, observed)
	applied, err := h.runtime.ApplyRuntimeConfiguration(ctx, dmsconfiguration.RuntimeRequest{Devices: []device.Device{observed}, Ports: ports})
	if len(applied.Devices) != 1 || applied.Devices[0].DeviceID != observed.ID {
		result.Status = types.ApplyStatusPartiallyApplied
		return result, errors.Join(err, fmt.Errorf("unexpected runtime result for device %s", observed.ID))
	}
	row := applied.Devices[0]
	err = errors.Join(err, row.Err)
	for _, blocker := range row.Blockers {
		err = errors.Join(err, fmt.Errorf("runtime configuration blocked: %s", blocker.Reason))
	}
	switch row.Status {
	case dmsconfiguration.Unchanged:
		if err == nil {
			result.Status = types.ApplyStatusNothingToDo
		}
	case dmsconfiguration.Applied:
		result.Status = types.ApplyStatusSuccess
		if err != nil {
			result.Status = types.ApplyStatusPartiallyApplied
		}
	case dmsconfiguration.Partial:
		result.Status = types.ApplyStatusPartiallyApplied
	case dmsconfiguration.Failed, dmsconfiguration.Blocked:
	default:
		result.Status = types.ApplyStatusPartiallyApplied
		err = errors.Join(err, fmt.Errorf("unknown runtime apply status %q", row.Status))
	}
	if result.Status != types.ApplyStatusSuccess && result.Status != types.ApplyStatusNothingToDo && err == nil {
		err = fmt.Errorf("runtime configuration %s", row.Status)
	}
	return result, err
}

// Select only the caller's PFs from one observed ASIC. Unrelated discovery issues
// do not prevent a standalone caller from configuring its readable device.
func runtimeDevice(nic *v1alpha1.NicDevice, inventory device.Inventory) (device.Device, error) {
	if len(nic.Status.Ports) == 0 {
		return device.Device{}, fmt.Errorf("device has no ports")
	}
	requested := map[string]bool{}
	for _, port := range nic.Status.Ports {
		if port.PCI == "" || requested[port.PCI] {
			return device.Device{}, fmt.Errorf("invalid or duplicate device port %q", port.PCI)
		}
		requested[port.PCI] = true
	}
	var selected device.Device
	for _, observed := range inventory.Devices {
		var ports []device.Port
		for _, port := range observed.Ports {
			if requested[port.PCI] {
				ports = append(ports, port)
			}
		}
		if len(ports) == 0 {
			continue
		}
		if selected.ID != "" || len(ports) != len(requested) {
			return device.Device{}, fmt.Errorf("requested PFs do not identify one complete observed ASIC selection")
		}
		selected = observed
		selected.Ports = ports
	}
	if selected.ID == "" {
		return device.Device{}, fmt.Errorf("selected device is absent from inventory")
	}
	for _, issue := range inventory.Issues {
		if issue.DeviceID == selected.ID || requested[issue.PCI] || issue.DeviceID == "" && issue.PCI == "" {
			return device.Device{}, fmt.Errorf("selected device has discovery issue: %w", issue.Err)
		}
	}
	return selected, nil
}

// PreserveUnboundPFPolicy retains ordinary-template behavior for a PF without a
// netdev. PCI MRRS and basic QoS remain requested; compiled plan intent must not
// use this policy.
func PreserveUnboundPFPolicy(ports []dmsconfiguration.PortRuntimeConfig, observed device.Device) {
	unbound := map[string]bool{}
	for _, port := range observed.Ports {
		unbound[port.PCI] = port.Netdev == ""
	}
	for i := range ports {
		if !unbound[ports[i].PCI] {
			continue
		}

		ports[i].RoCEMode = nil
		ports[i].CableLengthMeters = nil
		ports[i].ECN = nil
		ports[i].Pause = nil
		ports[i].RXRingSize = nil
		ports[i].TXRingSize = nil
		ports[i].CombinedChannels = nil
		ports[i].LRO = nil
	}
}

// ResetNicFirmware retains the caller's first-PF reset scope. The controller owns
// maintenance and reboot fallback; this method does not add a skip-VM policy.
func (h configurationManager) ResetNicFirmware(ctx context.Context, nic *v1alpha1.NicDevice) error {
	if ctx == nil || nic == nil || len(nic.Status.Ports) == 0 {
		return fmt.Errorf("context and device ports are required")
	}
	if h.firmware == nil {
		return fmt.Errorf("firmware manager is not initialized")
	}
	return h.firmware.Reset(ctx, device.Device{ID: nic.Name, Type: nic.Status.Type, Ports: []device.Port{{PCI: nic.Status.Ports[0].PCI}}}, dmsfirmware.ResetOptions{})
}
