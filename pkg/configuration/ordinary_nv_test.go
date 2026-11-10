// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package configuration

import (
	"context"
	"errors"
	"slices"

	dmsconfiguration "github.com/Mellanox/dms-lib/pkg/configuration"
	"github.com/Mellanox/dms-lib/pkg/device"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type ordinaryNVManager struct {
	dmsconfiguration.Manager
	validate func(dmsconfiguration.NVConfigRequest) (dmsconfiguration.Validation, error)
	apply    func(dmsconfiguration.NVConfigRequest) (dmsconfiguration.Result, error)
}

func (m ordinaryNVManager) ValidateNVConfiguration(_ context.Context, request dmsconfiguration.NVConfigRequest, _ dmsconfiguration.NVConfigOptions) (dmsconfiguration.Validation, error) {
	return m.validate(request)
}
func (m ordinaryNVManager) ApplyNVConfiguration(_ context.Context, request dmsconfiguration.NVConfigRequest, _ dmsconfiguration.NVConfigOptions) (dmsconfiguration.Result, error) {
	return m.apply(request)
}

var _ = Describe("ordinary per-PF NV policy", func() {
	var request dmsconfiguration.NVConfigRequest
	BeforeEach(func() {
		request = dmsconfiguration.NVConfigRequest{
			Devices:   []device.Device{{ID: "asic", Ports: []device.Port{{PCI: "0000:03:00.0"}, {PCI: "0000:03:00.1"}}}},
			Overrides: map[string][]dmsconfiguration.NVConfigParam{"asic": {{Name: "COMMON", Value: "1"}, {Name: "ASYMMETRIC", Value: "2"}}},
		}
	})
	It("filters a hidden parameter only on its rejecting PF and preflights every PF before writes", func() {
		validated := map[string]bool{}
		writes := map[string][]dmsconfiguration.NVConfigParam{}
		manager := ordinaryNVManager{
			validate: func(r dmsconfiguration.NVConfigRequest) (dmsconfiguration.Validation, error) {
				Expect(r.Devices).To(HaveLen(1))
				Expect(r.Devices[0].Ports).To(HaveLen(1))
				pci := r.Devices[0].Ports[0].PCI
				row := dmsconfiguration.DeviceValidation{DeviceID: "asic", UpdateNeeded: true, RebootNeeded: true}
				if pci == "0000:03:00.1" && len(r.Overrides["asic"]) == 2 {
					row.UnsupportedParameters = []string{"ASYMMETRIC"}
					row.Blockers = []dmsconfiguration.Blocker{{Reason: dmsconfiguration.UnsupportedConfiguration, Message: "unknown parameter"}}
				} else {
					validated[pci] = true
				}
				return dmsconfiguration.Validation{Devices: []dmsconfiguration.DeviceValidation{row}}, nil
			},
			apply: func(r dmsconfiguration.NVConfigRequest) (dmsconfiguration.Result, error) {
				Expect(validated).To(HaveLen(2))
				writes[r.Devices[0].Ports[0].PCI] = slices.Clone(r.Overrides["asic"])
				return dmsconfiguration.Result{Devices: []dmsconfiguration.DeviceResult{{DeviceID: "asic", Status: dmsconfiguration.Applied, RebootNeeded: true}}}, nil
			},
		}
		result, err := ApplyOrdinaryNV(context.Background(), manager, request, dmsconfiguration.NVConfigOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Devices).To(HaveLen(1))
		Expect(result.Devices[0].Status).To(Equal(dmsconfiguration.Applied))
		Expect(result.Devices[0].UnsupportedParameters).To(Equal([]string{"ASYMMETRIC"}))
		Expect(result.Devices[0].RebootNeeded).To(BeTrue())
		Expect(writes["0000:03:00.0"]).To(Equal(request.Overrides["asic"]))
		Expect(writes["0000:03:00.1"]).To(Equal([]dmsconfiguration.NVConfigParam{{Name: "COMMON", Value: "1"}}))
		Expect(request.Overrides["asic"]).To(HaveLen(2), "input must remain unchanged")
	})
	It("does not write an ASIC when any PF preflight fails", func() {
		manager := ordinaryNVManager{
			validate: func(r dmsconfiguration.NVConfigRequest) (dmsconfiguration.Validation, error) {
				row := dmsconfiguration.DeviceValidation{DeviceID: "asic", UpdateNeeded: true}
				if r.Devices[0].Ports[0].PCI == "0000:03:00.1" {
					row.Err = errors.New("query failed")
				}
				return dmsconfiguration.Validation{Devices: []dmsconfiguration.DeviceValidation{row}}, errors.Join(row.Err)
			},
			apply: func(dmsconfiguration.NVConfigRequest) (dmsconfiguration.Result, error) {
				Fail("mutation after failed preflight")
				return dmsconfiguration.Result{}, nil
			},
		}
		result, err := ApplyOrdinaryNV(context.Background(), manager, request, dmsconfiguration.NVConfigOptions{})
		Expect(err).To(MatchError("query failed"))
		Expect(result.Devices[0].Err).To(MatchError("query failed"))
		Expect(result.Devices[0].Status).To(Equal(dmsconfiguration.Failed))
	})
	It("retains partial effects when a later PF fails", func() {
		manager := ordinaryNVManager{
			validate: func(dmsconfiguration.NVConfigRequest) (dmsconfiguration.Validation, error) {
				return dmsconfiguration.Validation{Devices: []dmsconfiguration.DeviceValidation{{DeviceID: "asic", UpdateNeeded: true}}}, nil
			},
			apply: func(r dmsconfiguration.NVConfigRequest) (dmsconfiguration.Result, error) {
				row := dmsconfiguration.DeviceResult{DeviceID: "asic", Status: dmsconfiguration.Applied}
				if r.Devices[0].Ports[0].PCI == "0000:03:00.1" {
					row.Status, row.Err = dmsconfiguration.Failed, errors.New("second PF failed")
				}
				return dmsconfiguration.Result{Devices: []dmsconfiguration.DeviceResult{row}}, row.Err
			},
		}
		result, err := ApplyOrdinaryNV(context.Background(), manager, request, dmsconfiguration.NVConfigOptions{})
		Expect(err).To(HaveOccurred())
		Expect(result.Devices[0].Status).To(Equal(dmsconfiguration.Partial))
	})
	DescribeTable("does not discard a real blocker while skipping hidden parameters", func(force bool, blocker dmsconfiguration.BlockingReason) {
		request.Overrides["asic"] = []dmsconfiguration.NVConfigParam{{Name: "HIDDEN", Value: "1"}}
		manager := ordinaryNVManager{validate: func(dmsconfiguration.NVConfigRequest) (dmsconfiguration.Validation, error) {
			return dmsconfiguration.Validation{Devices: []dmsconfiguration.DeviceValidation{{DeviceID: "asic", UnsupportedParameters: []string{"HIDDEN"}, Blockers: []dmsconfiguration.Blocker{{Reason: blocker}}}}}, nil
		}}
		validation, err := ValidateOrdinaryNV(context.Background(), manager, request, dmsconfiguration.NVConfigOptions{Force: force})
		Expect(err).NotTo(HaveOccurred())
		Expect(validation.Devices[0].Blockers).NotTo(BeEmpty())
	}, Entry("force preserves unsupported intent", true, dmsconfiguration.UnsupportedConfiguration),
		Entry("missing prerequisite is not hidden parameter policy", false, dmsconfiguration.MissingPrerequisite))
	It("reports all-hidden intent without manufacturing a library write", func() {
		manager := ordinaryNVManager{validate: func(dmsconfiguration.NVConfigRequest) (dmsconfiguration.Validation, error) {
			return dmsconfiguration.Validation{Devices: []dmsconfiguration.DeviceValidation{{DeviceID: "asic", UnsupportedParameters: []string{"COMMON", "ASYMMETRIC"}, Blockers: []dmsconfiguration.Blocker{{Reason: dmsconfiguration.UnsupportedConfiguration}}}}}, nil
		}, apply: func(dmsconfiguration.NVConfigRequest) (dmsconfiguration.Result, error) {
			Fail("no supported intent")
			return dmsconfiguration.Result{}, nil
		}}
		result, err := ApplyOrdinaryNV(context.Background(), manager, request, dmsconfiguration.NVConfigOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Devices[0].Status).To(Equal(dmsconfiguration.Unchanged))
		Expect(result.Devices[0].UnsupportedParameters).To(ConsistOf("COMMON", "ASYMMETRIC"))
	})
	It("retains partial effects when a delegated write returns an error", func() {
		writes := 0
		manager := ordinaryNVManager{
			validate: func(dmsconfiguration.NVConfigRequest) (dmsconfiguration.Validation, error) {
				return dmsconfiguration.Validation{Devices: []dmsconfiguration.DeviceValidation{{DeviceID: "asic", UpdateNeeded: true}}}, nil
			},
			apply: func(dmsconfiguration.NVConfigRequest) (dmsconfiguration.Result, error) {
				writes++
				return dmsconfiguration.Result{Devices: []dmsconfiguration.DeviceResult{{DeviceID: "asic", Status: dmsconfiguration.Applied}}}, errors.New("write completed but request failed")
			},
		}
		result, err := ApplyOrdinaryNV(context.Background(), manager, request, dmsconfiguration.NVConfigOptions{})
		Expect(err).To(HaveOccurred())
		Expect(result.Devices[0].Status).To(Equal(dmsconfiguration.Partial))
		Expect(writes).To(Equal(1))
	})
	It("keeps unmatched canonical unsupported aliases blocked", func() {
		request.Overrides["asic"] = []dmsconfiguration.NVConfigParam{{Name: "ARRAY[01]", Value: "1"}}
		manager := ordinaryNVManager{validate: func(dmsconfiguration.NVConfigRequest) (dmsconfiguration.Validation, error) {
			return dmsconfiguration.Validation{Devices: []dmsconfiguration.DeviceValidation{{DeviceID: "asic", UnsupportedParameters: []string{"ARRAY[1]"}, Blockers: []dmsconfiguration.Blocker{{Reason: dmsconfiguration.UnsupportedConfiguration}}}}}, nil
		}, apply: func(dmsconfiguration.NVConfigRequest) (dmsconfiguration.Result, error) {
			Fail("unresolved unsupported alias must not be applied")
			return dmsconfiguration.Result{}, nil
		}}
		result, err := ApplyOrdinaryNV(context.Background(), manager, request, dmsconfiguration.NVConfigOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Devices[0].Status).To(Equal(dmsconfiguration.Blocked))
	})
	It("cannot delegate empty raw intent to the real library", func() {
		manager, err := dmsconfiguration.NewManager(dmsconfiguration.Options{})
		Expect(err).NotTo(HaveOccurred())
		request.Overrides = nil
		_, err = manager.ValidateNVConfiguration(context.Background(), request, dmsconfiguration.NVConfigOptions{})
		Expect(err).To(MatchError(ContainSubstring("requires plan, NetworkBay or overrides")))
	})

	It("refuses splitting default-reset scope", func() {
		_, err := ValidateOrdinaryNV(context.Background(), ordinaryNVManager{}, request, dmsconfiguration.NVConfigOptions{WithDefault: true})
		Expect(err).To(MatchError(ContainSubstring("without default reset")))
	})
})
