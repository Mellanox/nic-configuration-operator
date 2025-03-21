// Code generated by mockery v2.46.3. DO NOT EDIT.

package mocks

import (
	context "context"

	mock "github.com/stretchr/testify/mock"

	v1alpha1 "github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

// FirmwareManager is an autogenerated mock type for the FirmwareManager type
type FirmwareManager struct {
	mock.Mock
}

// UpdateNICFirmware provides a mock function with given fields: ctx, device, version
func (_m *FirmwareManager) BurnNicFirmware(ctx context.Context, device *v1alpha1.NicDevice, version string) error {
	ret := _m.Called(ctx, device, version)

	if len(ret) == 0 {
		panic("no return value specified for BurnNicFirmware")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, *v1alpha1.NicDevice, string) error); ok {
		r0 = rf(ctx, device, version)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// ValidateRequestedFirmwareSource provides a mock function with given fields: ctx, device
func (_m *FirmwareManager) ValidateRequestedFirmwareSource(ctx context.Context, device *v1alpha1.NicDevice) (string, error) {
	ret := _m.Called(ctx, device)

	if len(ret) == 0 {
		panic("no return value specified for ValidateRequestedFirmwareSource")
	}

	var r0 string
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, *v1alpha1.NicDevice) (string, error)); ok {
		return rf(ctx, device)
	}
	if rf, ok := ret.Get(0).(func(context.Context, *v1alpha1.NicDevice) string); ok {
		r0 = rf(ctx, device)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func(context.Context, *v1alpha1.NicDevice) error); ok {
		r1 = rf(ctx, device)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// NewFirmwareManager creates a new instance of FirmwareManager. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewFirmwareManager(t interface {
	mock.TestingT
	Cleanup(func())
}) *FirmwareManager {
	mock := &FirmwareManager{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
