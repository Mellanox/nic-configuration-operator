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

package configuration

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

// ApplyNVConfigurations applies NV configuration to all requests in parallel.
// Results preserve input order and include one entry for every request, even when
// one or more devices fail. The returned error joins all per-device failures.
func (h configurationManager) ApplyNVConfigurations(
	ctx context.Context,
	nodeName string,
	requests []types.NVConfigurationRequest,
) ([]types.DeviceNVConfigurationResult, error) {
	log.FromContext(ctx).V(2).Info("applying NV configuration batch", "node", nodeName, "devices", len(requests))
	return applyNVConfigurations(ctx, requests, h.ApplyNVConfiguration)
}

func applyNVConfigurations(
	ctx context.Context,
	requests []types.NVConfigurationRequest,
	apply func(context.Context, *v1alpha1.NicDevice, *types.ConfigurationOptions) (*types.ConfigurationApplyResult, error),
) ([]types.DeviceNVConfigurationResult, error) {
	results := make([]types.DeviceNVConfigurationResult, len(requests))
	errs := make([]error, len(requests))

	var waitGroup sync.WaitGroup
	for index := range requests {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()

			request := requests[index]
			result := types.DeviceNVConfigurationResult{Device: request.Device}
			switch {
			case request.Device == nil:
				result.Err = fmt.Errorf("NV configuration request at index %d has a nil device", index)
			case request.Skip:
				result.Skipped = true
			case request.Options == nil:
				result.Err = fmt.Errorf("NV configuration options for device %q must not be nil", request.Device.Name)
			case ctx.Err() != nil:
				result.Err = ctx.Err()
			default:
				result.Result, result.Err = apply(ctx, request.Device, request.Options)
				if result.Result == nil && result.Err == nil {
					result.Err = fmt.Errorf("NV configuration for device %q returned neither a result nor an error", request.Device.Name)
				}
			}

			results[index] = result
			errs[index] = result.Err
		}()
	}
	waitGroup.Wait()

	return results, errors.Join(errs...)
}

// ApplyRuntimeConfigurations applies runtime configuration to all devices in parallel.
// Results preserve input order and include one entry for every device, even when
// one or more devices fail. The returned error joins all per-device failures.
func (h configurationManager) ApplyRuntimeConfigurations(
	ctx context.Context,
	nodeName string,
	requests []types.RuntimeConfigurationRequest,
) ([]types.DeviceRuntimeConfigurationResult, error) {
	log.FromContext(ctx).V(2).Info("applying runtime configuration batch", "node", nodeName, "devices", len(requests))
	return applyRuntimeConfigurations(ctx, requests, h.ApplyRuntimeConfiguration)
}

func applyRuntimeConfigurations(
	ctx context.Context,
	requests []types.RuntimeConfigurationRequest,
	apply func(context.Context, *v1alpha1.NicDevice) (*types.RuntimeConfigurationApplyResult, error),
) ([]types.DeviceRuntimeConfigurationResult, error) {
	results := make([]types.DeviceRuntimeConfigurationResult, len(requests))
	errs := make([]error, len(requests))

	var waitGroup sync.WaitGroup
	for index := range requests {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()

			request := requests[index]
			result := types.DeviceRuntimeConfigurationResult{Device: request.Device}
			switch {
			case request.Device == nil:
				result.Err = fmt.Errorf("runtime configuration request at index %d has a nil device", index)
			case request.Skip:
				result.Skipped = true
			case ctx.Err() != nil:
				result.Err = ctx.Err()
			default:
				result.Result, result.Err = apply(ctx, request.Device)
				if result.Result == nil && result.Err == nil {
					result.Err = fmt.Errorf("runtime configuration for device %q returned neither a result nor an error", request.Device.Name)
				}
			}

			results[index] = result
			errs[index] = result.Err
		}()
	}
	waitGroup.Wait()

	return results, errors.Join(errs...)
}
