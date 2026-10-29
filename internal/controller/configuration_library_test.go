// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"

	dmsconfiguration "github.com/Mellanox/dms-lib/pkg/configuration"
	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/configuration"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// controllerLibraryFixture keeps the existing controller policy scenarios while
// moving their mocked hardware boundary to the shared library result contract.
// It never invokes the legacy implementation; manager is the scenario's mock.
func controllerLibraryFixture(reader client.Reader, manager configuration.ConfigurationManager) (*planManagerStub, func(context.Context) (device.Inventory, error)) {
	list := func(ctx context.Context) ([]v1alpha1.NicDevice, error) {
		objects := &v1alpha1.NicDeviceList{}
		err := reader.List(ctx, objects)
		return objects.Items, err
	}
	find := func(ctx context.Context, id string) (*v1alpha1.NicDevice, error) {
		objects, err := list(ctx)
		if err != nil {
			return nil, err
		}
		for i := range objects {
			if len(objects[i].Status.Ports) > 0 && utils.PCIDeviceAddress(objects[i].Status.Ports[0].PCI) == id {
				return &objects[i], nil
			}
		}
		return nil, fmt.Errorf("fixture device %s not found", id)
	}
	inventory := func(ctx context.Context) (device.Inventory, error) {
		objects, err := list(ctx)
		if err != nil {
			return device.Inventory{}, err
		}
		result := device.Inventory{Complete: true}
		for _, nic := range objects {
			if len(nic.Status.Ports) == 0 {
				continue
			}
			observed := device.Device{ID: utils.PCIDeviceAddress(nic.Status.Ports[0].PCI), Type: nic.Status.Type}
			for _, port := range nic.Status.Ports {
				observed.Ports = append(observed.Ports, device.Port{PCI: port.PCI, Netdev: port.NetworkInterface, RDMA: port.RdmaInterface})
			}
			result.Devices = append(result.Devices, observed)
		}
		return result, nil
	}
	stub := &planManagerStub{defaults: func(device.Port) (map[string]string, error) {
		return map[string]string{consts.LinkTypeP1Param: "2"}, nil
	}}
	stub.validateNV = func(request dmsconfiguration.NVConfigRequest, _ dmsconfiguration.NVConfigOptions) (dmsconfiguration.Validation, error) {
		result := dmsconfiguration.Validation{}
		var failures []error
		for _, observed := range request.Devices {
			nic, err := find(context.Background(), observed.ID)
			if err != nil {
				return result, err
			}
			update, reboot, unsupported, err := manager.ValidateDeviceNvSpec(context.Background(), nic)
			result.Devices = append(result.Devices, dmsconfiguration.DeviceValidation{DeviceID: observed.ID, UpdateNeeded: update, RebootNeeded: reboot, UnsupportedParameters: unsupported, Err: err})
			failures = append(failures, err)
		}
		return result, errors.Join(failures...)
	}
	stub.applyNV = func(request dmsconfiguration.NVConfigRequest, options dmsconfiguration.NVConfigOptions) (dmsconfiguration.Result, error) {
		result := dmsconfiguration.Result{}
		var failures []error
		for _, observed := range request.Devices {
			nic, err := find(context.Background(), observed.ID)
			if err != nil {
				return result, err
			}
			value, err := manager.ApplyNVConfiguration(context.Background(), nic, &types.ConfigurationOptions{SkipReset: true, Force: options.Force, WithDefault: options.WithDefault})
			row := dmsconfiguration.DeviceResult{DeviceID: observed.ID, Status: dmsconfiguration.Applied, Err: err}
			if value != nil {
				row.RebootNeeded = value.RebootRequired
			}
			if err != nil {
				row.Status = dmsconfiguration.Failed
			}
			result.Devices = append(result.Devices, row)
			failures = append(failures, err)
		}
		return result, errors.Join(failures...)
	}
	stub.applyRuntime = func(request dmsconfiguration.RuntimeRequest) (dmsconfiguration.Result, error) {
		result := dmsconfiguration.Result{}
		var failures []error
		for _, observed := range request.Devices {
			nic, err := find(context.Background(), observed.ID)
			if err != nil {
				return result, err
			}
			_, err = manager.ApplyRuntimeConfiguration(context.Background(), nic)
			row := dmsconfiguration.DeviceResult{DeviceID: observed.ID, Status: dmsconfiguration.Applied, Err: err}
			if err != nil {
				row.Status = dmsconfiguration.Failed
			}
			result.Devices = append(result.Devices, row)
			failures = append(failures, err)
		}
		return result, errors.Join(failures...)
	}
	return stub, inventory
}
