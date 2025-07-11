// Code generated by mockery v2.53.4. DO NOT EDIT.

package mocks

import (
	context "context"

	types "github.com/Mellanox/nic-configuration-operator/pkg/types"
	mock "github.com/stretchr/testify/mock"

	v1alpha1 "github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

// ConfigurationUtils is an autogenerated mock type for the ConfigurationUtils type
type ConfigurationUtils struct {
	mock.Mock
}

// GetLinkType provides a mock function with given fields: name
func (_m *ConfigurationUtils) GetLinkType(name string) string {
	ret := _m.Called(name)

	if len(ret) == 0 {
		panic("no return value specified for GetLinkType")
	}

	var r0 string
	if rf, ok := ret.Get(0).(func(string) string); ok {
		r0 = rf(name)
	} else {
		r0 = ret.Get(0).(string)
	}

	return r0
}

// GetMaxReadRequestSize provides a mock function with given fields: pciAddr
func (_m *ConfigurationUtils) GetMaxReadRequestSize(pciAddr string) (int, error) {
	ret := _m.Called(pciAddr)

	if len(ret) == 0 {
		panic("no return value specified for GetMaxReadRequestSize")
	}

	var r0 int
	var r1 error
	if rf, ok := ret.Get(0).(func(string) (int, error)); ok {
		return rf(pciAddr)
	}
	if rf, ok := ret.Get(0).(func(string) int); ok {
		r0 = rf(pciAddr)
	} else {
		r0 = ret.Get(0).(int)
	}

	if rf, ok := ret.Get(1).(func(string) error); ok {
		r1 = rf(pciAddr)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// GetPCILinkSpeed provides a mock function with given fields: pciAddr
func (_m *ConfigurationUtils) GetPCILinkSpeed(pciAddr string) (int, error) {
	ret := _m.Called(pciAddr)

	if len(ret) == 0 {
		panic("no return value specified for GetPCILinkSpeed")
	}

	var r0 int
	var r1 error
	if rf, ok := ret.Get(0).(func(string) (int, error)); ok {
		return rf(pciAddr)
	}
	if rf, ok := ret.Get(0).(func(string) int); ok {
		r0 = rf(pciAddr)
	} else {
		r0 = ret.Get(0).(int)
	}

	if rf, ok := ret.Get(1).(func(string) error); ok {
		r1 = rf(pciAddr)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// GetTrustAndPFC provides a mock function with given fields: device, interfaceName
func (_m *ConfigurationUtils) GetTrustAndPFC(device *v1alpha1.NicDevice, interfaceName string) (string, string, error) {
	ret := _m.Called(device, interfaceName)

	if len(ret) == 0 {
		panic("no return value specified for GetTrustAndPFC")
	}

	var r0 string
	var r1 string
	var r2 error
	if rf, ok := ret.Get(0).(func(*v1alpha1.NicDevice, string) (string, string, error)); ok {
		return rf(device, interfaceName)
	}
	if rf, ok := ret.Get(0).(func(*v1alpha1.NicDevice, string) string); ok {
		r0 = rf(device, interfaceName)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func(*v1alpha1.NicDevice, string) string); ok {
		r1 = rf(device, interfaceName)
	} else {
		r1 = ret.Get(1).(string)
	}

	if rf, ok := ret.Get(2).(func(*v1alpha1.NicDevice, string) error); ok {
		r2 = rf(device, interfaceName)
	} else {
		r2 = ret.Error(2)
	}

	return r0, r1, r2
}

// QueryNvConfig provides a mock function with given fields: ctx, pciAddr
func (_m *ConfigurationUtils) QueryNvConfig(ctx context.Context, pciAddr string) (types.NvConfigQuery, error) {
	ret := _m.Called(ctx, pciAddr)

	if len(ret) == 0 {
		panic("no return value specified for QueryNvConfig")
	}

	var r0 types.NvConfigQuery
	var r1 error
	if rf, ok := ret.Get(0).(func(context.Context, string) (types.NvConfigQuery, error)); ok {
		return rf(ctx, pciAddr)
	}
	if rf, ok := ret.Get(0).(func(context.Context, string) types.NvConfigQuery); ok {
		r0 = rf(ctx, pciAddr)
	} else {
		r0 = ret.Get(0).(types.NvConfigQuery)
	}

	if rf, ok := ret.Get(1).(func(context.Context, string) error); ok {
		r1 = rf(ctx, pciAddr)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// ResetNicFirmware provides a mock function with given fields: ctx, pciAddr
func (_m *ConfigurationUtils) ResetNicFirmware(ctx context.Context, pciAddr string) error {
	ret := _m.Called(ctx, pciAddr)

	if len(ret) == 0 {
		panic("no return value specified for ResetNicFirmware")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, string) error); ok {
		r0 = rf(ctx, pciAddr)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// ResetNvConfig provides a mock function with given fields: pciAddr
func (_m *ConfigurationUtils) ResetNvConfig(pciAddr string) error {
	ret := _m.Called(pciAddr)

	if len(ret) == 0 {
		panic("no return value specified for ResetNvConfig")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string) error); ok {
		r0 = rf(pciAddr)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// SetMaxReadRequestSize provides a mock function with given fields: pciAddr, maxReadRequestSize
func (_m *ConfigurationUtils) SetMaxReadRequestSize(pciAddr string, maxReadRequestSize int) error {
	ret := _m.Called(pciAddr, maxReadRequestSize)

	if len(ret) == 0 {
		panic("no return value specified for SetMaxReadRequestSize")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string, int) error); ok {
		r0 = rf(pciAddr, maxReadRequestSize)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// SetNvConfigParameter provides a mock function with given fields: pciAddr, paramName, paramValue
func (_m *ConfigurationUtils) SetNvConfigParameter(pciAddr string, paramName string, paramValue string) error {
	ret := _m.Called(pciAddr, paramName, paramValue)

	if len(ret) == 0 {
		panic("no return value specified for SetNvConfigParameter")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string, string, string) error); ok {
		r0 = rf(pciAddr, paramName, paramValue)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// SetTrustAndPFC provides a mock function with given fields: device, trust, pfc
func (_m *ConfigurationUtils) SetTrustAndPFC(device *v1alpha1.NicDevice, trust string, pfc string) error {
	ret := _m.Called(device, trust, pfc)

	if len(ret) == 0 {
		panic("no return value specified for SetTrustAndPFC")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(*v1alpha1.NicDevice, string, string) error); ok {
		r0 = rf(device, trust, pfc)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// NewConfigurationUtils creates a new instance of ConfigurationUtils. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewConfigurationUtils(t interface {
	mock.TestingT
	Cleanup(func())
}) *ConfigurationUtils {
	mock := &ConfigurationUtils{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
