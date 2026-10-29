// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	dmsconfiguration "github.com/Mellanox/dms-lib/pkg/configuration"
	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

// ValidateRuntimePrerequisites preserves NCO's existing carrier policy before
// generic library runtime configuration. It only observes host link state.
func ValidateRuntimePrerequisites(nic *v1alpha1.NicDevice) error {
	if nic == nil {
		return fmt.Errorf("NIC device must not be nil")
	}
	return (&configValidationImpl{utils: newConfigurationUtils(nil)}).validatePortCarrierState(nic)
}

type ordinaryNVPort struct {
	request dmsconfiguration.NVConfigRequest
	index   int
}

// ValidateOrdinaryNV preserves NCO's per-PF unsupported-parameter policy for
// ordinary raw intent. Plan, NetworkBay and default-reset requests must retain
// their complete library scope and cannot use this adapter.
func ValidateOrdinaryNV(ctx context.Context, manager dmsconfiguration.Manager, request dmsconfiguration.NVConfigRequest, options dmsconfiguration.NVConfigOptions) (dmsconfiguration.Validation, error) {
	_, validation, err := prepareOrdinaryNV(ctx, manager, request, options)
	return validation, err
}

// ApplyOrdinaryNV validates every PF before any write, filters unsupported raw
// parameters only on the PF that rejected them, then delegates application and
// readback to the library. Force does not enable this filtering.
func ApplyOrdinaryNV(ctx context.Context, manager dmsconfiguration.Manager, request dmsconfiguration.NVConfigRequest, options dmsconfiguration.NVConfigOptions) (dmsconfiguration.Result, error) {
	ports, validation, err := prepareOrdinaryNV(ctx, manager, request, options)
	if len(validation.Devices) == 0 {
		return dmsconfiguration.Result{}, err
	}
	result := dmsconfiguration.Result{Devices: make([]dmsconfiguration.DeviceResult, len(validation.Devices))}
	for i, item := range validation.Devices {
		status := dmsconfiguration.Unchanged
		if item.Err != nil {
			status = dmsconfiguration.Failed
		} else if len(item.Blockers) > 0 {
			status = dmsconfiguration.Blocked
		}
		result.Devices[i] = dmsconfiguration.DeviceResult{DeviceID: item.DeviceID, Status: status, RebootNeeded: item.RebootNeeded, UnsupportedParameters: item.UnsupportedParameters, Blockers: item.Blockers, Err: item.Err}
	}
	for _, port := range ports {
		item := &result.Devices[port.index]
		if item.Err != nil || len(item.Blockers) > 0 || item.Status == dmsconfiguration.Partial || item.Status == dmsconfiguration.Failed || item.Status == dmsconfiguration.Blocked || len(port.request.Overrides[item.DeviceID]) == 0 {
			continue
		}
		applied, applyErr := manager.ApplyNVConfiguration(ctx, port.request, options)
		if len(applied.Devices) != 1 || applied.Devices[0].DeviceID != item.DeviceID {
			applyErr = errors.Join(applyErr, fmt.Errorf("unexpected library result for device %s", item.DeviceID))
			item.Err = errors.Join(item.Err, applyErr)
			item.Status = dmsconfiguration.Partial // The delegated call may have written.
			err = errors.Join(err, applyErr)
			continue
		}
		next := applied.Devices[0]
		if next.Err == nil {
			next.Err = applyErr
		} else if applyErr != nil && !errors.Is(next.Err, applyErr) && !errors.Is(applyErr, next.Err) {
			next.Err = errors.Join(next.Err, applyErr)
		}
		switch next.Status {
		case dmsconfiguration.Unchanged, dmsconfiguration.Applied, dmsconfiguration.Blocked, dmsconfiguration.Partial, dmsconfiguration.Failed:
		default:
			next.Err = errors.Join(next.Err, fmt.Errorf("unknown library apply status %q", next.Status))
			next.Status = dmsconfiguration.Partial
		}
		if next.Err != nil {
			switch next.Status {
			case dmsconfiguration.Unchanged:
				next.Status = dmsconfiguration.Failed
			case dmsconfiguration.Applied:
				next.Status = dmsconfiguration.Partial
			}
		}
		item.RebootNeeded = item.RebootNeeded || next.RebootNeeded
		item.UnsupportedParameters = appendUnique(item.UnsupportedParameters, next.UnsupportedParameters...)
		item.Blockers = append(item.Blockers, next.Blockers...)
		item.Err = errors.Join(item.Err, next.Err)
		if next.Status == dmsconfiguration.Partial || (item.Status == dmsconfiguration.Applied && (next.Err != nil || len(next.Blockers) > 0 || next.Status == dmsconfiguration.Failed || next.Status == dmsconfiguration.Blocked)) {
			item.Status = dmsconfiguration.Partial
		} else if next.Status != dmsconfiguration.Unchanged {
			item.Status = next.Status
		}
		err = errors.Join(err, next.Err)
	}
	return result, err
}

func prepareOrdinaryNV(ctx context.Context, manager dmsconfiguration.Manager, request dmsconfiguration.NVConfigRequest, options dmsconfiguration.NVConfigOptions) ([]ordinaryNVPort, dmsconfiguration.Validation, error) {
	if ctx == nil || manager == nil || len(request.Devices) == 0 || request.Plan != nil || request.NetworkBay != nil || options.WithDefault {
		return nil, dmsconfiguration.Validation{}, fmt.Errorf("ordinary NV adapter requires context, manager, devices and raw-only intent without default reset")
	}
	if err := ctx.Err(); err != nil {
		return nil, dmsconfiguration.Validation{}, err
	}
	if err := validateOrdinaryDevices(request); err != nil {
		return nil, dmsconfiguration.Validation{}, err
	}
	validation := dmsconfiguration.Validation{Devices: make([]dmsconfiguration.DeviceValidation, len(request.Devices))}
	var ports []ordinaryNVPort
	var failures []error
	for i, observed := range request.Devices {
		item := &validation.Devices[i]
		item.DeviceID = observed.ID
		for _, port := range observed.Ports {
			endpoint := observed
			endpoint.Ports = []device.Port{port}
			perPF := dmsconfiguration.NVConfigRequest{Devices: []device.Device{endpoint}, Overrides: map[string][]dmsconfiguration.NVConfigParam{observed.ID: slices.Clone(request.Overrides[observed.ID])}}
			if len(perPF.Overrides[observed.ID]) == 0 {
				continue
			}
			row, err := validateOrdinaryPF(ctx, manager, perPF, options)
			unsupported := slices.Clone(row.UnsupportedParameters)
			otherBlocker := slices.ContainsFunc(row.Blockers, func(blocker dmsconfiguration.Blocker) bool {
				return blocker.Reason != dmsconfiguration.UnsupportedConfiguration
			})
			if err == nil && row.Err == nil && !otherBlocker && len(unsupported) > 0 && !options.Force {
				filtered := slices.DeleteFunc(perPF.Overrides[observed.ID], func(parameter dmsconfiguration.NVConfigParam) bool {
					return slices.Contains(unsupported, parameter.Name)
				})
				perPF.Overrides[observed.ID] = filtered
				if len(filtered) == 0 {
					// No operation remains after NCO skips unsupported parameters.
					// The library rejects empty raw intent; it cannot revalidate it.
					row = dmsconfiguration.DeviceValidation{DeviceID: observed.ID}
				} else {
					row, err = validateOrdinaryPF(ctx, manager, perPF, options)
				}
			}
			item.UpdateNeeded = item.UpdateNeeded || row.UpdateNeeded
			item.RebootNeeded = item.RebootNeeded || row.RebootNeeded
			item.UnsupportedParameters = appendUnique(item.UnsupportedParameters, append(unsupported, row.UnsupportedParameters...)...)
			item.Blockers = append(item.Blockers, row.Blockers...)
			if row.Err == nil {
				row.Err = err
			} else if err != nil && !errors.Is(row.Err, err) && !errors.Is(err, row.Err) {
				row.Err = errors.Join(row.Err, err)
			}
			item.Err = errors.Join(item.Err, row.Err)
			ports = append(ports, ordinaryNVPort{request: perPF, index: i})
		}
		if item.Err != nil {
			item.UpdateNeeded, item.RebootNeeded = false, false
			failures = append(failures, item.Err)
		}
	}
	return ports, validation, errors.Join(failures...)
}

// Preserve ASIC identity while allowing only independent, observed PF requests.
func validateOrdinaryDevices(request dmsconfiguration.NVConfigRequest) error {
	ids, pcis := map[string]bool{}, map[string]bool{}
	for _, observed := range request.Devices {
		if observed.ID == "" || ids[observed.ID] || len(observed.Ports) == 0 {
			return fmt.Errorf("invalid or duplicate ordinary NV device %q", observed.ID)
		}
		ids[observed.ID] = true
		for _, port := range observed.Ports {
			if pcis[port.PCI] {
				return fmt.Errorf("duplicate ordinary NV endpoint %s", port.PCI)
			}
			pcis[port.PCI] = true
		}
	}
	for id := range request.Overrides {
		if !ids[id] {
			return fmt.Errorf("unknown ordinary NV override device %s", id)
		}
	}
	return nil
}

func validateOrdinaryPF(ctx context.Context, manager dmsconfiguration.Manager, request dmsconfiguration.NVConfigRequest, options dmsconfiguration.NVConfigOptions) (dmsconfiguration.DeviceValidation, error) {
	validation, err := manager.ValidateNVConfiguration(ctx, request, options)
	if len(validation.Devices) != 1 || validation.Devices[0].DeviceID != request.Devices[0].ID {
		return dmsconfiguration.DeviceValidation{}, errors.Join(err, fmt.Errorf("unexpected library validation for device %s", request.Devices[0].ID))
	}
	return validation.Devices[0], err
}

func appendUnique(values []string, more ...string) []string {
	for _, value := range more {
		if !slices.Contains(values, value) {
			values = append(values, value)
		}
	}
	sort.Strings(values)
	return values
}
