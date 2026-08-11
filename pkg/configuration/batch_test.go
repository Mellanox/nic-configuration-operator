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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/types"
)

var _ = Describe("configuration batches", func() {
	newDevice := func(name string) *v1alpha1.NicDevice {
		return &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: name}}
	}

	Describe("NV configuration", func() {
		It("preserves per-device results and input order when one device fails", func() {
			first := newDevice("first")
			second := newDevice("second")
			secondErr := errors.New("second failed")
			options := &types.ConfigurationOptions{SkipReset: true, WithDefault: false, Force: false}

			results, err := applyNVConfigurations(
				context.Background(),
				[]types.NVConfigurationRequest{
					{Device: first, Options: options},
					{Device: second, Options: options},
				},
				func(_ context.Context, device *v1alpha1.NicDevice, _ *types.ConfigurationOptions) (*types.ConfigurationApplyResult, error) {
					if device.Name == second.Name {
						return &types.ConfigurationApplyResult{Status: types.ApplyStatusFailed, RebootRequired: false}, secondErr
					}
					return &types.ConfigurationApplyResult{Status: types.ApplyStatusSuccess, RebootRequired: true}, nil
				},
			)

			Expect(err).To(MatchError(secondErr))
			Expect(results).To(HaveLen(2))
			Expect(results[0].Device).To(BeIdenticalTo(first))
			Expect(results[0].Result).To(Equal(&types.ConfigurationApplyResult{
				Status: types.ApplyStatusSuccess, RebootRequired: true,
			}))
			Expect(results[0].Err).NotTo(HaveOccurred())
			Expect(results[1].Device).To(BeIdenticalTo(second))
			Expect(results[1].Result).To(Equal(&types.ConfigurationApplyResult{
				Status: types.ApplyStatusFailed, RebootRequired: false,
			}))
			Expect(results[1].Err).To(MatchError(secondErr))
		})

		It("reports invalid requests without dropping their result entries", func() {
			device := newDevice("missing-options")
			applyCalled := false

			results, err := applyNVConfigurations(
				context.Background(),
				[]types.NVConfigurationRequest{{Device: nil, Options: &types.ConfigurationOptions{}}, {Device: device, Options: nil}},
				func(_ context.Context, _ *v1alpha1.NicDevice, _ *types.ConfigurationOptions) (*types.ConfigurationApplyResult, error) {
					applyCalled = true
					return nil, nil
				},
			)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("index 0 has a nil device"))
			Expect(err.Error()).To(ContainSubstring("options for device \"missing-options\" must not be nil"))
			Expect(results).To(HaveLen(2))
			Expect(results[0].Device).To(BeNil())
			Expect(results[0].Err).To(HaveOccurred())
			Expect(results[1].Device).To(BeIdenticalTo(device))
			Expect(results[1].Err).To(HaveOccurred())
			Expect(applyCalled).To(BeFalse())
		})

		It("retains skipped devices without invoking their per-device apply", func() {
			device := newDevice("skipped")
			applyCalled := false

			results, err := applyNVConfigurations(
				context.Background(),
				[]types.NVConfigurationRequest{{Device: device, Options: nil, Skip: true}},
				func(_ context.Context, _ *v1alpha1.NicDevice, _ *types.ConfigurationOptions) (*types.ConfigurationApplyResult, error) {
					applyCalled = true
					return nil, nil
				},
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(results).To(Equal([]types.DeviceNVConfigurationResult{{Device: device, Skipped: true}}))
			Expect(applyCalled).To(BeFalse())
		})

		It("rejects an empty per-device outcome", func() {
			device := newDevice("empty-result")

			results, err := applyNVConfigurations(
				context.Background(),
				[]types.NVConfigurationRequest{{Device: device, Options: &types.ConfigurationOptions{}}},
				func(_ context.Context, _ *v1alpha1.NicDevice, _ *types.ConfigurationOptions) (*types.ConfigurationApplyResult, error) {
					return nil, nil
				},
			)

			Expect(err).To(MatchError(`NV configuration for device "empty-result" returned neither a result nor an error`))
			Expect(results).To(HaveLen(1))
			Expect(errors.Is(err, results[0].Err)).To(BeTrue())
		})
	})

	Describe("runtime configuration", func() {
		It("preserves per-device results and input order when one device fails", func() {
			first := newDevice("first")
			second := newDevice("second")
			firstErr := errors.New("first failed")

			results, err := applyRuntimeConfigurations(
				context.Background(),
				[]types.RuntimeConfigurationRequest{{Device: first}, {Device: second}},
				func(_ context.Context, device *v1alpha1.NicDevice) (*types.RuntimeConfigurationApplyResult, error) {
					if device.Name == first.Name {
						return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}, firstErr
					}
					return &types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusSuccess}, nil
				},
			)

			Expect(err).To(MatchError(firstErr))
			Expect(results).To(HaveLen(2))
			Expect(results[0].Device).To(BeIdenticalTo(first))
			Expect(results[0].Result).To(Equal(&types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusFailed}))
			Expect(results[0].Err).To(MatchError(firstErr))
			Expect(results[1].Device).To(BeIdenticalTo(second))
			Expect(results[1].Result).To(Equal(&types.RuntimeConfigurationApplyResult{Status: types.ApplyStatusSuccess}))
			Expect(results[1].Err).NotTo(HaveOccurred())
		})

		It("reports cancellation for every device without invoking apply", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			applyCalled := false

			results, err := applyRuntimeConfigurations(
				ctx,
				[]types.RuntimeConfigurationRequest{{Device: newDevice("first")}, {Device: newDevice("second")}},
				func(_ context.Context, _ *v1alpha1.NicDevice) (*types.RuntimeConfigurationApplyResult, error) {
					applyCalled = true
					return nil, nil
				},
			)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring(context.Canceled.Error()))
			Expect(results).To(HaveLen(2))
			Expect(results[0].Err).To(MatchError(context.Canceled))
			Expect(results[1].Err).To(MatchError(context.Canceled))
			Expect(applyCalled).To(BeFalse())
		})

		It("retains skipped devices without invoking their per-device apply", func() {
			device := newDevice("skipped")
			applyCalled := false

			results, err := applyRuntimeConfigurations(
				context.Background(),
				[]types.RuntimeConfigurationRequest{{Device: device, Skip: true}},
				func(_ context.Context, _ *v1alpha1.NicDevice) (*types.RuntimeConfigurationApplyResult, error) {
					applyCalled = true
					return nil, nil
				},
			)

			Expect(err).NotTo(HaveOccurred())
			Expect(results).To(Equal([]types.DeviceRuntimeConfigurationResult{{Device: device, Skipped: true}}))
			Expect(applyCalled).To(BeFalse())
		})

		It("rejects an empty per-device outcome", func() {
			device := newDevice("empty-result")

			results, err := applyRuntimeConfigurations(
				context.Background(),
				[]types.RuntimeConfigurationRequest{{Device: device}},
				func(_ context.Context, _ *v1alpha1.NicDevice) (*types.RuntimeConfigurationApplyResult, error) {
					return nil, nil
				},
			)

			Expect(err).To(MatchError(`runtime configuration for device "empty-result" returned neither a result nor an error`))
			Expect(results).To(HaveLen(1))
			Expect(errors.Is(err, results[0].Err)).To(BeTrue())
		})
	})
})
