// Code generated by mockery v2.53.4. DO NOT EDIT.

package mocks

import mock "github.com/stretchr/testify/mock"

// FirmwareProvisioner is an autogenerated mock type for the FirmwareProvisioner type
type FirmwareProvisioner struct {
	mock.Mock
}

// AddFirmwareBinariesToCacheByMetadata provides a mock function with given fields: cacheName
func (_m *FirmwareProvisioner) AddFirmwareBinariesToCacheByMetadata(cacheName string) error {
	ret := _m.Called(cacheName)

	if len(ret) == 0 {
		panic("no return value specified for AddFirmwareBinariesToCacheByMetadata")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string) error); ok {
		r0 = rf(cacheName)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// DeleteCache provides a mock function with given fields: cacheName
func (_m *FirmwareProvisioner) DeleteCache(cacheName string) error {
	ret := _m.Called(cacheName)

	if len(ret) == 0 {
		panic("no return value specified for DeleteCache")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string) error); ok {
		r0 = rf(cacheName)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// DownloadAndUnzipFirmwareArchives provides a mock function with given fields: cacheName, urls, cleanupArchives
func (_m *FirmwareProvisioner) DownloadAndUnzipFirmwareArchives(cacheName string, urls []string, cleanupArchives bool) error {
	ret := _m.Called(cacheName, urls, cleanupArchives)

	if len(ret) == 0 {
		panic("no return value specified for DownloadAndUnzipFirmwareArchives")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(string, []string, bool) error); ok {
		r0 = rf(cacheName, urls, cleanupArchives)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// DownloadBFB provides a mock function with given fields: cacheName, url
func (_m *FirmwareProvisioner) DownloadBFB(cacheName string, url string) (string, error) {
	ret := _m.Called(cacheName, url)

	if len(ret) == 0 {
		panic("no return value specified for DownloadBFB")
	}

	var r0 string
	var r1 error
	if rf, ok := ret.Get(0).(func(string, string) (string, error)); ok {
		return rf(cacheName, url)
	}
	if rf, ok := ret.Get(0).(func(string, string) string); ok {
		r0 = rf(cacheName, url)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func(string, string) error); ok {
		r1 = rf(cacheName, url)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// IsFWStorageAvailable provides a mock function with no fields
func (_m *FirmwareProvisioner) IsFWStorageAvailable() error {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for IsFWStorageAvailable")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func() error); ok {
		r0 = rf()
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// ValidateBFB provides a mock function with given fields: cacheName
func (_m *FirmwareProvisioner) ValidateBFB(cacheName string) (map[string]string, error) {
	ret := _m.Called(cacheName)

	if len(ret) == 0 {
		panic("no return value specified for ValidateBFB")
	}

	var r0 map[string]string
	var r1 error
	if rf, ok := ret.Get(0).(func(string) (map[string]string, error)); ok {
		return rf(cacheName)
	}
	if rf, ok := ret.Get(0).(func(string) map[string]string); ok {
		r0 = rf(cacheName)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(map[string]string)
		}
	}

	if rf, ok := ret.Get(1).(func(string) error); ok {
		r1 = rf(cacheName)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// ValidateCache provides a mock function with given fields: cacheName
func (_m *FirmwareProvisioner) ValidateCache(cacheName string) (map[string][]string, error) {
	ret := _m.Called(cacheName)

	if len(ret) == 0 {
		panic("no return value specified for ValidateCache")
	}

	var r0 map[string][]string
	var r1 error
	if rf, ok := ret.Get(0).(func(string) (map[string][]string, error)); ok {
		return rf(cacheName)
	}
	if rf, ok := ret.Get(0).(func(string) map[string][]string); ok {
		r0 = rf(cacheName)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(map[string][]string)
		}
	}

	if rf, ok := ret.Get(1).(func(string) error); ok {
		r1 = rf(cacheName)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// VerifyCachedBFB provides a mock function with given fields: cacheName, url
func (_m *FirmwareProvisioner) VerifyCachedBFB(cacheName string, url string) (bool, error) {
	ret := _m.Called(cacheName, url)

	if len(ret) == 0 {
		panic("no return value specified for VerifyCachedBFB")
	}

	var r0 bool
	var r1 error
	if rf, ok := ret.Get(0).(func(string, string) (bool, error)); ok {
		return rf(cacheName, url)
	}
	if rf, ok := ret.Get(0).(func(string, string) bool); ok {
		r0 = rf(cacheName, url)
	} else {
		r0 = ret.Get(0).(bool)
	}

	if rf, ok := ret.Get(1).(func(string, string) error); ok {
		r1 = rf(cacheName, url)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// VerifyCachedBinaries provides a mock function with given fields: cacheName, urls
func (_m *FirmwareProvisioner) VerifyCachedBinaries(cacheName string, urls []string) ([]string, error) {
	ret := _m.Called(cacheName, urls)

	if len(ret) == 0 {
		panic("no return value specified for VerifyCachedBinaries")
	}

	var r0 []string
	var r1 error
	if rf, ok := ret.Get(0).(func(string, []string) ([]string, error)); ok {
		return rf(cacheName, urls)
	}
	if rf, ok := ret.Get(0).(func(string, []string) []string); ok {
		r0 = rf(cacheName, urls)
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).([]string)
		}
	}

	if rf, ok := ret.Get(1).(func(string, []string) error); ok {
		r1 = rf(cacheName, urls)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// NewFirmwareProvisioner creates a new instance of FirmwareProvisioner. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewFirmwareProvisioner(t interface {
	mock.TestingT
	Cleanup(func())
}) *FirmwareProvisioner {
	mock := &FirmwareProvisioner{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
