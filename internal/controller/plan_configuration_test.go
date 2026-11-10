// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	dmsv1 "github.com/Mellanox/dms-lib/api/v1alpha1"
	dmsconfiguration "github.com/Mellanox/dms-lib/pkg/configuration"
	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/dms-lib/pkg/plan"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	configurationMocks "github.com/Mellanox/nic-configuration-operator/pkg/configuration/mocks"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	maintenanceMocks "github.com/Mellanox/nic-configuration-operator/pkg/maintenance/mocks"
	spectrumxMocks "github.com/Mellanox/nic-configuration-operator/pkg/spectrumx/mocks"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type planManagerStub struct {
	dmsconfiguration.Manager
	defaults     func(device.Port) (map[string]string, error)
	validateNV   func(dmsconfiguration.NVConfigRequest, dmsconfiguration.NVConfigOptions) (dmsconfiguration.Validation, error)
	applyNV      func(dmsconfiguration.NVConfigRequest, dmsconfiguration.NVConfigOptions) (dmsconfiguration.Result, error)
	applyRuntime func(dmsconfiguration.RuntimeRequest) (dmsconfiguration.Result, error)
}

func (m *planManagerStub) QueryNVDefaults(_ context.Context, p device.Port) (map[string]string, error) {
	return m.defaults(p)
}
func (m *planManagerStub) ValidateNVConfiguration(_ context.Context, r dmsconfiguration.NVConfigRequest, o dmsconfiguration.NVConfigOptions) (dmsconfiguration.Validation, error) {
	return m.validateNV(r, o)
}
func (m *planManagerStub) ApplyNVConfiguration(_ context.Context, r dmsconfiguration.NVConfigRequest, o dmsconfiguration.NVConfigOptions) (dmsconfiguration.Result, error) {
	return m.applyNV(r, o)
}
func (m *planManagerStub) ApplyRuntimeConfiguration(_ context.Context, r dmsconfiguration.RuntimeRequest) (dmsconfiguration.Result, error) {
	return m.applyRuntime(r)
}

var _ = Describe("DMS plan configuration", func() {
	var ctx context.Context
	var c client.Client
	var r *NicDeviceReconciler
	var library *planManagerStub
	var inventory device.Inventory
	var namespace *corev1.Namespace
	var node *corev1.Node
	var params *dmsv1.DMSPlanParams
	var blueprint *corev1.ConfigMap
	var status *nicDeviceConfigurationStatus
	BeforeEach(func() {
		ctx = context.Background()
		GinkgoT().Setenv("DMS_SCHEMA_PATH", "testdata/dms/yang")
		var err error
		c, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		namespace = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "plan-"}}
		Expect(c.Create(ctx, namespace)).To(Succeed())
		node = &corev1.Node{ObjectMeta: metav1.ObjectMeta{GenerateName: "plan-node-"}}
		Expect(c.Create(ctx, node)).To(Succeed())
		inventory = device.Inventory{Complete: true, Devices: []device.Device{{ID: "rail0", Type: "1023", Ports: []device.Port{{PCI: "0000:03:00.0", PFIndex: ptr.To(0), PhysicalPortIndex: ptr.To(0)}}}, {ID: "rail1", Type: "1023", Ports: []device.Port{{PCI: "0000:04:00.0", PFIndex: ptr.To(0), PhysicalPortIndex: ptr.To(0)}}}}}
		data, err := os.ReadFile("testdata/dms/blueprint.yaml")
		Expect(err).NotTo(HaveOccurred())
		blueprint = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "blueprint", Namespace: namespace.Name}, Data: map[string]string{"blueprint.yaml": string(data)}}
		Expect(c.Create(ctx, blueprint)).To(Succeed())
		params = &dmsv1.DMSPlanParams{ObjectMeta: metav1.ObjectMeta{Name: "params", Namespace: namespace.Name}, Spec: dmsv1.DMSPlanSpec{NodeSelector: map[string]string{}, BlueprintRef: blueprint.Name}}
		Expect(c.Create(ctx, params)).To(Succeed())
		loaded, err := plan.Load(data)
		Expect(err).NotTo(HaveOccurred())
		compiled, err := plan.BuildFromInventory(ctx, loaded, inventory, plan.BuildOptions{Environment: "host-k8s"})
		Expect(err).NotTo(HaveOccurred())
		bound, err := compiled.TargetMap()
		Expect(err).NotTo(HaveOccurred())
		encoded, err := json.Marshal(bound)
		Expect(err).NotTo(HaveOccurred())
		host := dmsv1.HostTargetMap{NodeUID: node.UID, ObservedGeneration: params.Generation, BlueprintDigest: fmt.Sprintf("%x", sha256.Sum256(data)), Conditions: []metav1.Condition{{Type: "Resolved", Status: metav1.ConditionTrue, Reason: "Resolved", ObservedGeneration: params.Generation, LastTransitionTime: metav1.Now()}}}
		Expect(json.Unmarshal(encoded, &host.TargetMap)).To(Succeed())
		params.Status.TargetMapByHost = map[string]dmsv1.HostTargetMap{node.Name: host}
		// Another host can be unresolved without blocking this host's binding.
		params.Status.Conditions = []metav1.Condition{{Type: "Resolved", Status: metav1.ConditionFalse, Reason: "OtherHost", LastTransitionTime: metav1.Now()}}
		Expect(c.Status().Update(ctx, params)).To(Succeed())
		nic := &v1alpha1.NicDevice{ObjectMeta: metav1.ObjectMeta{Name: "selected", Namespace: namespace.Name}, Spec: v1alpha1.NicDeviceSpec{Configuration: &v1alpha1.NicDeviceConfigurationSpec{PlanRef: &v1alpha1.DMSPlanReference{Name: params.Name, Namespace: namespace.Name}}}}
		Expect(c.Create(ctx, nic)).To(Succeed())
		nic.Status.Ports = []v1alpha1.NicDevicePortSpec{{PCI: "0000:03:00.0"}}
		Expect(c.Status().Update(ctx, nic)).To(Succeed())
		status = &nicDeviceConfigurationStatus{device: nic, firmwareValidationSuccessful: true}
		library = &planManagerStub{}
		r = &NicDeviceReconciler{Client: c, APIReader: c, NodeName: node.Name, PlanConfiguration: library, DiscoverPlanDevices: func(context.Context) (device.Inventory, error) { return inventory, nil }}
	})
	AfterEach(func() { Expect(c.Delete(ctx, node)).To(Succeed()); Expect(c.Delete(ctx, namespace)).To(Succeed()) })
	It("compiles the host once and projects execution to only the selected rail", func() {
		calls := 0
		r.DiscoverPlanDevices = func(context.Context) (device.Inventory, error) { calls++; return inventory, nil }
		legacy, groups, err := r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).NotTo(HaveOccurred())
		Expect(legacy).To(BeEmpty())
		Expect(calls).To(Equal(1))
		Expect(groups).To(HaveLen(1))
		Expect(groups[0].nv.Plan.Info().DeviceIDs).To(ConsistOf("rail0", "rail1"))
		Expect(groups[0].nv.Devices).To(Equal(inventory.Devices[:1]))
		Expect(groups[0].runtime.Devices).To(Equal(inventory.Devices[:1]))
		Expect(groups[0].runtime.Ports).To(BeEmpty())
		_, err = groups[0].runtime.Plan.Requirements(groups[0].runtime.Devices)
		Expect(err).NotTo(HaveOccurred())
	})
	It("rejects an unknown externally owned group before configuration validation or writes", func() {
		data := strings.Replace(blueprint.Data["blueprint.yaml"], "link-runtime: {stage: configure,", "link-runtime: {owner: package-owned-runtime, stage: configure,", 1)
		data = strings.ReplaceAll(data, "link-runtime", "external-required")
		blueprint.Data["blueprint.yaml"] = data
		Expect(c.Update(ctx, blueprint)).To(Succeed())
		host := params.Status.TargetMapByHost[node.Name]
		host.BlueprintDigest = fmt.Sprintf("%x", sha256.Sum256([]byte(data)))
		params.Status.TargetMapByHost[node.Name] = host
		Expect(c.Status().Update(ctx, params)).To(Succeed())
		// The unset library methods panic if validation/application is reached.
		_, err := r.reconcileConfiguration(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).To(MatchError(ContainSubstring("unsupported external plan requirement configure/external-required/package-runtime/external-required")))
		Expect(status.device.Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
	})
	It("rejects a CC service with no compiled startup endpoints", func() {
		data := strings.ReplaceAll(blueprint.Data["blueprint.yaml"], "link-runtime", "cc")
		data = strings.Replace(data, "- type: knob\n        path: /nvidia/cc/slot/[0]\n        values: {enabled: true}", "- type: service\n        service_name: spcx-cc-late\n        template: cc.service.j2", 1)
		data = strings.Replace(data, "templates: {}", "templates: {cc.service.j2: '[Service]'}", 1)
		blueprint.Data["blueprint.yaml"] = data
		Expect(c.Update(ctx, blueprint)).To(Succeed())
		host := params.Status.TargetMapByHost[node.Name]
		host.BlueprintDigest = fmt.Sprintf("%x", sha256.Sum256([]byte(data)))
		params.Status.TargetMapByHost[node.Name] = host
		Expect(c.Status().Update(ctx, params)).To(Succeed())
		_, err := r.reconcileConfiguration(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).To(MatchError(ContainSubstring("has no eligible compiled CC endpoints")))
	})
	DescribeTable("limits external requirements to the established host-k8s ownership contract", func(requirement plan.Requirement, supported bool) {
		err := validateConfigurationRequirements([]plan.Requirement{requirement})
		if supported {
			Expect(err).NotTo(HaveOccurred())
		} else {
			Expect(err).To(HaveOccurred())
		}
	},
		Entry("delegated eswitch", plan.Requirement{Stage: "configure", Group: "eswitch", Kind: "package-runtime", Reference: "eswitch"}, true),
		Entry("delegated xplane", plan.Requirement{Stage: "configure", Group: "xplane", Kind: "service", Reference: "spcx-doca-xplane-boot"}, true),
		Entry("existing CC owner", plan.Requirement{Stage: "configure", Group: "cc", Kind: "service", Reference: "spcx-cc-late"}, true),
		Entry("unknown service in a known group", plan.Requirement{Stage: "configure", Group: "cc", Kind: "service", Reference: "unknown"}, false),
		Entry("known name in the wrong phase", plan.Requirement{Stage: "prepare", Group: "cc", Kind: "service", Reference: "spcx-cc-late"}, false),
		Entry("unknown artifact", plan.Requirement{Stage: "prepare", Group: "breakout", Kind: "artifact", Reference: "unknown"}, false),
		Entry("unknown external dependency", plan.Requirement{Stage: "configure", Group: "cc", Kind: "dependency", Reference: "unknown"}, false),
	)
	It("copies plan-only intent without retaining a legacy template", func() {
		status.device.Spec.Configuration.Template = &v1alpha1.ConfigurationTemplateSpec{NumVfs: 8}
		template := &nicConfigurationTemplate{template: v1alpha1.NicConfigurationTemplate{Spec: v1alpha1.NicConfigurationTemplateSpec{PlanRef: &v1alpha1.DMSPlanReference{Namespace: namespace.Name, Name: "different"}}}}
		Expect(template.applyToDevice(status.device)).To(BeTrue())
		Expect(status.device.Spec.Configuration.Template).To(BeNil())
		Expect(status.device.Spec.Configuration.PlanRef).To(Equal(template.template.Spec.PlanRef))
		template.template.Spec.PlanRef.Name = "mutated"
		Expect(status.device.Spec.Configuration.PlanRef.Name).To(Equal("different"))
	})

	It("leaves ordinary configuration independent of optional plan APIs", func() {
		status.device.Spec.Configuration.PlanRef = nil
		status.device.Spec.Configuration.Template = &v1alpha1.ConfigurationTemplateSpec{}
		r.APIReader = nil
		library.defaults = func(device.Port) (map[string]string, error) { return nil, nil }
		legacy, groups, err := r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).NotTo(HaveOccurred())
		Expect(legacy).To(BeEmpty())
		Expect(groups).To(HaveLen(1))
		Expect(groups[0].nv.Plan).To(BeNil())
		Expect(groups[0].runtime.Plan).To(BeNil())
	})
	It("preserves ordinary unbound PF policy without changing required plan intent", func() {
		status.device.Spec.Configuration.Template = &v1alpha1.ConfigurationTemplateSpec{
			LinkType:                    "Ethernet",
			PciPerformanceOptimized:     &v1alpha1.PciPerformanceOptimizedSpec{Enabled: true},
			RoceOptimized:               &v1alpha1.RoceOptimizedSpec{Enabled: true, RoceMode: 2, Qos: &v1alpha1.QosSpec{Trust: "dscp", CableLen: 7}},
			RuntimePerformanceOptimized: &v1alpha1.RuntimePerformanceOptimizedSpec{Enabled: true, RxRingSize: 1024},
		}
		library.defaults = func(device.Port) (map[string]string, error) {
			return map[string]string{consts.LinkTypeP1Param: "2"}, nil
		}
		_, planned, err := r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).NotTo(HaveOccurred())
		Expect(planned[0].runtime.Ports[0].RoCEMode).NotTo(BeNil())
		Expect(planned[0].runtime.Ports[0].CableLengthMeters).NotTo(BeNil())
		Expect(planned[0].runtime.Ports[0].RXRingSize).NotTo(BeNil())
		status.device.Spec.Configuration.PlanRef = nil
		_, ordinary, err := r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).NotTo(HaveOccurred())
		port := ordinary[0].runtime.Ports[0]
		Expect(port.RoCEMode).To(BeNil())
		Expect(port.CableLengthMeters).To(BeNil())
		Expect(port.RXRingSize).To(BeNil())
		Expect(port.MaxReadRequestBytes).To(Equal(ptr.To(uint32(4096))))
		Expect(port.Trust).To(Equal(ptr.To("dscp")))
	})
	It("keeps ordinary Force NV queries in the compatibility path", func() {
		status.device.Spec.Configuration.PlanRef = nil
		status.device.Spec.Configuration.Template = &v1alpha1.ConfigurationTemplateSpec{Force: true}
		library.defaults = func(device.Port) (map[string]string, error) {
			Fail("ordinary Force must not query library defaults")
			return nil, errors.New("unavailable")
		}
		_, groups, err := r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).NotTo(HaveOccurred())
		Expect(groups).To(HaveLen(1))
		Expect(groups[0].options.Force).To(BeTrue())
		Expect(groups[0].nv.Overrides["rail0"]).To(BeEmpty())
	})
	DescribeTable("keeps ordinary ASIC runtime independent of paired NV grouping", func(networkBay bool, failure string) {
		status.device.Spec.Configuration.PlanRef = nil
		status.device.Spec.Configuration.Template = &v1alpha1.ConfigurationTemplateSpec{LinkType: "Ethernet", PciPerformanceOptimized: &v1alpha1.PciPerformanceOptimizedSpec{Enabled: true}}
		if networkBay {
			status.device.Spec.Configuration.Template.LinkType = ""
			status.device.Spec.Configuration.Template.NetworkBay = &v1alpha1.NetworkBaySpec{Conf: "conf3"}
		}
		peer := status.device.DeepCopy()
		peer.ResourceVersion, peer.UID = "", ""
		peer.Name = "peer"
		Expect(c.Create(ctx, peer)).To(Succeed())
		peer.Status.Ports = []v1alpha1.NicDevicePortSpec{{PCI: "0000:04:00.0"}}
		Expect(c.Status().Update(ctx, peer)).To(Succeed())
		library.defaults = func(device.Port) (map[string]string, error) {
			return map[string]string{consts.LinkTypeP1Param: "2"}, nil
		}
		_, groups, err := r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status, {device: peer}})
		Expect(err).NotTo(HaveOccurred())
		if networkBay {
			Expect(groups).To(HaveLen(1))
			Expect(groups[0].nv.Devices).To(HaveLen(2))
		} else {
			Expect(groups).To(HaveLen(2))
		}
		status.rebootRequired = failure == "reboot"
		r.CheckRuntimePrerequisites = func(nic *v1alpha1.NicDevice) error {
			if nic.Name == status.device.Name && failure == "carrier" {
				return errors.New("NO-CARRIER")
			}
			return nil
		}
		var applied []string
		library.applyRuntime = func(request dmsconfiguration.RuntimeRequest) (dmsconfiguration.Result, error) {
			Expect(request.Devices).To(HaveLen(1))
			Expect(request.Ports).To(HaveLen(1))
			Expect(request.Ports[0].PCI).To(Equal(request.Devices[0].Ports[0].PCI))
			id := request.Devices[0].ID
			applied = append(applied, id)
			row := dmsconfiguration.DeviceResult{DeviceID: id, Status: dmsconfiguration.Unchanged}
			if id == "rail0" && failure == "apply" {
				row.Status, row.Err = dmsconfiguration.Failed, errors.New("runtime write failed")
			}
			return dmsconfiguration.Result{Devices: []dmsconfiguration.DeviceResult{row}}, row.Err
		}
		err = r.applyPlanRuntime(ctx, groups)
		switch failure {
		case "carrier":
			Expect(err).To(MatchError(ContainSubstring("NO-CARRIER")))
			Expect(applied).To(Equal([]string{"rail1"}))
		case "reboot":
			Expect(err).NotTo(HaveOccurred())
			Expect(applied).To(Equal([]string{"rail1"}))
		default:
			if failure == "apply" {
				Expect(err).To(MatchError(ContainSubstring("runtime write failed")))
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
			Expect(applied).To(Equal([]string{"rail0", "rail1"}))
		}
		if networkBay {
			Expect(groups[0].nv.Devices).To(HaveLen(2))
			Expect(groups[0].runtime.Devices).To(HaveLen(2))
			Expect(groups[0].runtime.Ports).To(HaveLen(2))
		}
	}, Entry("ordinary carrier failure", false, "carrier"),
		Entry("NetworkBay carrier failure", true, "carrier"),
		Entry("NetworkBay pending reboot", true, "reboot"),
		Entry("NetworkBay runtime apply failure", true, "apply"),
		Entry("healthy NetworkBay pair", true, ""))

	DescribeTable("rejects outdated reference input", func(change func(*dmsv1.HostTargetMap)) {
		host := params.Status.TargetMapByHost[node.Name]
		change(&host)
		params.Status.TargetMapByHost[node.Name] = host
		Expect(c.Status().Update(ctx, params)).To(Succeed())
		_, _, err := r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).To(MatchError(ContainSubstring("no current resolved binding")))
	}, Entry("node incarnation", func(h *dmsv1.HostTargetMap) { h.NodeUID = types.UID("old") }), Entry("generation", func(h *dmsv1.HostTargetMap) { h.ObservedGeneration-- }), Entry("digest", func(h *dmsv1.HostTargetMap) { h.BlueprintDigest = "old" }), Entry("condition generation", func(h *dmsv1.HostTargetMap) { h.Conditions[0].ObservedGeneration-- }), Entry("failed resolution", func(h *dmsv1.HostTargetMap) { h.Conditions[0].Status = metav1.ConditionFalse }), Entry("missing stage", func(h *dmsv1.HostTargetMap) { h.InventoryStage = "" }))
	It("rejects changed blueprint bytes and deleted references", func() {
		blueprint.Data["blueprint.yaml"] += "\n# changed\n"
		Expect(c.Update(ctx, blueprint)).To(Succeed())
		_, _, err := r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).To(MatchError(ContainSubstring("no current resolved binding")))
		Expect(c.Delete(ctx, params)).To(Succeed())
		_, _, err = r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).To(HaveOccurred())
	})
	It("preserves explicit false and string coercion input, and rejects reserved collisions", func() {
		options, err := configurationPlanOptions(dmsv1.DMSPlanSpec{SPXGS: ptr.To(false), Planes: ptr.To(int32(0)), Parameters: map[string]string{"value": "8"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(options.Parameters).To(Equal(map[string]any{"SP_XGS": false, "planes": 0, "value": "8"}))
		_, err = configurationPlanOptions(dmsv1.DMSPlanSpec{Parameters: map[string]string{"planes": "2"}})
		Expect(err).To(HaveOccurred())
	})
	It("groups only matching apply options and keeps complete selected pairs together", func() {
		peer := status.device.DeepCopy()
		peer.Name = "peer"
		peer.ResourceVersion = ""
		peer.UID = ""
		peer.Status = v1alpha1.NicDeviceStatus{}
		Expect(c.Create(ctx, peer)).To(Succeed())
		peer.Status.Ports = []v1alpha1.NicDevicePortSpec{{PCI: "0000:04:00.0"}}
		Expect(c.Status().Update(ctx, peer)).To(Succeed())
		statuses := nicDeviceConfigurationStatuses{status, {device: peer}}
		library.defaults = func(device.Port) (map[string]string, error) { return nil, nil }
		status.device.Spec.Configuration.Template = &v1alpha1.ConfigurationTemplateSpec{Force: true, NetworkBay: &v1alpha1.NetworkBaySpec{Conf: "conf3"}}
		peer.Spec.Configuration.Template = status.device.Spec.Configuration.Template.DeepCopy()
		_, groups, err := r.preparePlanConfigurations(ctx, statuses)
		Expect(err).NotTo(HaveOccurred())
		Expect(groups).To(HaveLen(1))
		Expect(groups[0].nv.Devices).To(HaveLen(2))
		Expect(groups[0].options.Force).To(BeTrue())
		Expect(groups[0].options.WithDefault).To(BeFalse())
		peer.Spec.Configuration.Template.Force = false
		_, groups, err = r.preparePlanConfigurations(ctx, statuses)
		Expect(err).NotTo(HaveOccurred())
		Expect(groups).To(HaveLen(2))
		Expect(groups[0].nv.Devices).To(HaveLen(1))
		Expect(groups[1].nv.Devices).To(HaveLen(1))
	})
	It("preserves the baseline successful configuration polling interval for plan references", func() {
		maintenance := &maintenanceMocks.MaintenanceManager{}
		maintenance.On("ReleaseMaintenance", mock.Anything).Return(nil).Once()
		r.MaintenanceManager = maintenance
		library.validateNV = func(dmsconfiguration.NVConfigRequest, dmsconfiguration.NVConfigOptions) (dmsconfiguration.Validation, error) {
			return dmsconfiguration.Validation{Devices: []dmsconfiguration.DeviceValidation{{DeviceID: "rail0"}}}, nil
		}
		library.applyRuntime = func(request dmsconfiguration.RuntimeRequest) (dmsconfiguration.Result, error) {
			Expect(request.Devices).To(Equal(inventory.Devices[:1]))
			return dmsconfiguration.Result{Devices: []dmsconfiguration.DeviceResult{{DeviceID: "rail0", Status: dmsconfiguration.Unchanged}}}, nil
		}
		result, err := r.reconcileConfiguration(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(20 * time.Minute))
		Expect(status.rebootRequired).To(BeFalse())
		maintenance.AssertExpectations(GinkgoT())
	})

	It("returns to reconciliation after successful in-process reset before using runtime bindings", func() {
		GinkgoT().Setenv(consts.FEATURE_GATE_FW_RESET_AFTER_CONFIG_UPDATE, consts.LabelValueTrue)
		maintenance := &maintenanceMocks.MaintenanceManager{}
		maintenance.On("ScheduleMaintenance", mock.Anything).Return(nil).Once()
		maintenance.On("MaintenanceAllowed", mock.Anything).Return(true, nil).Once()
		manager := &configurationMocks.ConfigurationManager{}
		manager.On("ResetNicFirmware", mock.Anything, status.device).Return(nil).Once()
		r.MaintenanceManager = maintenance
		r.ConfigurationManager = manager
		library.validateNV = func(dmsconfiguration.NVConfigRequest, dmsconfiguration.NVConfigOptions) (dmsconfiguration.Validation, error) {
			return dmsconfiguration.Validation{Devices: []dmsconfiguration.DeviceValidation{{DeviceID: "rail0", UpdateNeeded: true}}}, nil
		}
		library.applyNV = func(dmsconfiguration.NVConfigRequest, dmsconfiguration.NVConfigOptions) (dmsconfiguration.Result, error) {
			return dmsconfiguration.Result{Devices: []dmsconfiguration.DeviceResult{{DeviceID: "rail0", Status: dmsconfiguration.Applied}}}, nil
		}
		result, err := r.reconcileConfiguration(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(shortRequeueTime))
		maintenance.AssertExpectations(GinkgoT())
		manager.AssertExpectations(GinkgoT())
	})

	It("validates every options partition before any write", func() {
		_, groups, err := r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).NotTo(HaveOccurred())
		calls := 0
		library.validateNV = func(dmsconfiguration.NVConfigRequest, dmsconfiguration.NVConfigOptions) (dmsconfiguration.Validation, error) {
			calls++
			return dmsconfiguration.Validation{Devices: []dmsconfiguration.DeviceValidation{{DeviceID: "rail0", Err: errors.New("projection rejected")}}}, errors.New("invalid group")
		}
		err = r.validatePlanNV(ctx, append(groups, groups...))
		Expect(err).To(HaveOccurred())
		Expect(calls).To(Equal(2))
	})
	It("retains reboot and all row errors from a partial NV apply", func() {
		_, groups, err := r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).NotTo(HaveOccurred())
		status.nvConfigUpdateRequired = true
		library.applyNV = func(dmsconfiguration.NVConfigRequest, dmsconfiguration.NVConfigOptions) (dmsconfiguration.Result, error) {
			return dmsconfiguration.Result{Devices: []dmsconfiguration.DeviceResult{{DeviceID: "rail0", Status: dmsconfiguration.Partial, RebootNeeded: true, Err: errors.New("write failed")}}}, errors.New("aggregate failure")
		}
		_, err = r.applyPlanNV(ctx, groups)
		Expect(err).To(MatchError(And(ContainSubstring("aggregate failure"), ContainSubstring("write failed"))))
		Expect(status.rebootRequired).To(BeTrue())
	})
	It("rejects missing and foreign result rows", func() {
		_, groups, err := r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).NotTo(HaveOccurred())
		err = r.recordPlanResults(ctx, groups[0], dmsconfiguration.Result{Devices: []dmsconfiguration.DeviceResult{{DeviceID: "unselected", Status: dmsconfiguration.Applied}}}, nil, false)
		Expect(err).To(MatchError(And(ContainSubstring("invalid configuration result identity"), ContainSubstring("missing configuration result"))))
	})
	It("checks compiled configure readiness before the existing spec-change reboot", func() {
		data, err := os.ReadFile("testdata/dms/blueprint.yaml")
		Expect(err).NotTo(HaveOccurred())
		data = []byte(strings.ReplaceAll(string(data), "bdf: '0000:03:00.0', device_id: '0x1023', role: ew, rail: 0, plane: 0", "bdf: '0000:03:00.1', device_id: '0x1023', role: ew, rail: 0, plane: 0"))
		loaded, err := plan.Load(data)
		Expect(err).NotTo(HaveOccurred())
		compiled, err := plan.BuildFromInventory(ctx, loaded, inventory, plan.BuildOptions{Environment: "host-k8s"})
		Expect(err).NotTo(HaveOccurred())
		Expect(compiled.Info().RuntimeReady).To(BeFalse())
		status.device.Annotations = map[string]string{consts.LastAppliedStateAnnotation: "previous-spec"}
		Expect(c.Update(ctx, status.device)).To(Succeed())

		bound, err := compiled.TargetMap()
		Expect(err).NotTo(HaveOccurred())
		encoded, err := json.Marshal(bound)
		Expect(err).NotTo(HaveOccurred())
		host := params.Status.TargetMapByHost[node.Name]
		Expect(json.Unmarshal(encoded, &host.TargetMap)).To(Succeed())
		host.BlueprintDigest = fmt.Sprintf("%x", sha256.Sum256(data))
		params.Status.TargetMapByHost[node.Name] = host
		blueprint.Data["blueprint.yaml"] = string(data)
		Expect(c.Update(ctx, blueprint)).To(Succeed())
		Expect(c.Status().Update(ctx, params)).To(Succeed())
		library.validateNV = func(dmsconfiguration.NVConfigRequest, dmsconfiguration.NVConfigOptions) (dmsconfiguration.Validation, error) {
			return dmsconfiguration.Validation{Devices: []dmsconfiguration.DeviceValidation{{DeviceID: "rail0"}}}, nil
		}
		_, err = r.reconcileConfiguration(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).To(MatchError(ContainSubstring("configure plan is not ready")))
		Expect(status.rebootRequired).To(BeFalse())
		Expect(status.device.Annotations[consts.LastAppliedStateAnnotation]).To(Equal("previous-spec"))
	})
	It("does not launch another CC endpoint after callback cancellation", func() {
		callbackCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		process := &spectrumxMocks.SpectrumXManager{}
		process.On("RunDocaSpcXCC", v1alpha1.NicDevicePortSpec{PCI: "0000:03:00.0", RdmaInterface: "roce0"}).Run(func(mock.Arguments) { cancel() }).Return(nil).Once()
		r.SpectrumXManager = process
		err := r.startPlanCC(callbackCtx, "rail0", []device.Port{{PCI: "0000:03:00.0", RDMA: "roce0"}, {PCI: "0000:03:00.1", RDMA: "roce1"}})
		Expect(err).To(MatchError(context.Canceled))
		process.AssertExpectations(GinkgoT())
	})

	DescribeTable("preserves existing CC ownership and delegates convergence to Apply", func(running bool, outcome dmsconfiguration.ApplyStatus, failure bool) {
		inventory.Devices[0].Ports[0].RDMA = "roce0"
		data, err := os.ReadFile("testdata/dms/blueprint.yaml")
		Expect(err).NotTo(HaveOccurred())
		loaded, err := plan.Load([]byte(strings.ReplaceAll(string(data), "link-runtime", "cc")))
		Expect(err).NotTo(HaveOccurred())
		compiled, err := plan.BuildFromInventory(ctx, loaded, inventory, plan.BuildOptions{Environment: "host-k8s"})
		Expect(err).NotTo(HaveOccurred())
		group := &planConfigurationGroup{members: map[string]*nicDeviceConfigurationStatus{"rail0": status}, runtime: dmsconfiguration.RuntimeRequest{Plan: compiled, Devices: inventory.Devices[:1]}}
		process := &spectrumxMocks.SpectrumXManager{}
		process.On("IsDocaSpcXCCRunning", "roce0").Return(running).Once()
		r.SpectrumXManager = process
		applyCalls := 0
		applyError := errors.New("CC process exited during runtime observation")
		library.applyRuntime = func(request dmsconfiguration.RuntimeRequest) (dmsconfiguration.Result, error) {
			applyCalls++
			Expect(request.Devices).To(Equal(inventory.Devices[:1]))
			if running {
				Expect(request.BeforeGroup).To(BeEmpty())
			} else {
				Expect(request.BeforeGroup).To(HaveKey("cc"))
				Expect(request.BeforeGroup["cc"](ctx, "rail0", request.Devices[0].Ports)).To(Succeed())
			}
			result := dmsconfiguration.Result{Devices: []dmsconfiguration.DeviceResult{{DeviceID: "rail0", Status: outcome}}}
			if failure {
				result.Devices[0].Err = applyError
				return result, applyError
			}
			return result, nil
		}
		if !running {
			process.On("RunDocaSpcXCC", v1alpha1.NicDevicePortSpec{PCI: "0000:03:00.0", RdmaInterface: "roce0"}).Return(nil).Once()
		}
		err = r.applyPlanRuntime(ctx, []*planConfigurationGroup{group})
		if failure {
			Expect(err).To(MatchError(ContainSubstring(applyError.Error())))
			Expect(status.device.Annotations).NotTo(HaveKey(consts.LastAppliedStateAnnotation))
		} else {
			Expect(err).NotTo(HaveOccurred())
			Expect(status.device.Annotations).To(HaveKey(consts.LastAppliedStateAnnotation))
		}
		Expect(applyCalls).To(Equal(1))
		process.AssertExpectations(GinkgoT())
	}, Entry("restart starts CC at its group boundary", false, dmsconfiguration.Applied, false),
		Entry("running converged process uses ordinary Apply", true, dmsconfiguration.Unchanged, false),
		Entry("running process with drift uses ordinary Apply", true, dmsconfiguration.Applied, false),
		Entry("running process observation failure remains an error", true, dmsconfiguration.Partial, true))

	It("uses runtime Apply directly and never treats a separate read-only prerequisite check as a gate", func() {
		_, groups, err := r.preparePlanConfigurations(ctx, nicDeviceConfigurationStatuses{status})
		Expect(err).NotTo(HaveOccurred())
		calls := 0
		library.applyRuntime = func(request dmsconfiguration.RuntimeRequest) (dmsconfiguration.Result, error) {
			calls++
			Expect(request.Devices).To(Equal(inventory.Devices[:1]))
			return dmsconfiguration.Result{Devices: []dmsconfiguration.DeviceResult{{DeviceID: "rail0", Status: dmsconfiguration.Unchanged}}}, nil
		}
		Expect(r.applyPlanRuntime(ctx, groups)).To(Succeed())
		Expect(calls).To(Equal(1))
		Expect(status.device.Annotations).To(HaveKey(consts.LastAppliedStateAnnotation))
	})
})
