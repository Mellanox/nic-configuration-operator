// Copyright 2026 NVIDIA CORPORATION & AFFILIATES
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	dmsv1 "github.com/Mellanox/dms-lib/api/v1alpha1"
	dmsconfiguration "github.com/Mellanox/dms-lib/pkg/configuration"
	"github.com/Mellanox/dms-lib/pkg/device"
	"github.com/Mellanox/dms-lib/pkg/plan"
	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/configuration"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
	"github.com/Mellanox/nic-configuration-operator/pkg/utils"
	"github.com/vishvananda/netlink"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

type planConfigurationGroup struct {
	members map[string]*nicDeviceConfigurationStatus
	nv      dmsconfiguration.NVConfigRequest
	runtime dmsconfiguration.RuntimeRequest
	options dmsconfiguration.NVConfigOptions
}

// preparePlanConfigurations builds each reference once for this reconciliation.
// Only selected NicDevice objects enter execution requests; the complete host
// inventory remains compiler input, not authorization to configure other rails.
func (r *NicDeviceReconciler) preparePlanConfigurations(ctx context.Context, statuses nicDeviceConfigurationStatuses) (nicDeviceConfigurationStatuses, []*planConfigurationGroup, error) {
	legacy, selected, err := selectConfigurationStatuses(statuses)
	if err != nil {
		return nil, nil, err
	}
	if len(selected) == 0 {
		return legacy, nil, nil
	}
	if r.PlanConfiguration == nil || r.DiscoverPlanDevices == nil {
		return nil, nil, fmt.Errorf("DMS plan configuration is not initialized")
	}
	inventory, err := r.DiscoverPlanDevices(ctx)
	if err != nil {
		return nil, nil, err
	}
	if !inventory.Complete {
		return nil, nil, fmt.Errorf("device inventory is incomplete")
	}
	plans := map[types.NamespacedName]*plan.Plan{}
	groups := []*planConfigurationGroup{}
	byOptions := map[string]*planConfigurationGroup{}
	claimed := map[string]bool{}
	for _, status := range selected {
		spec := status.device.Spec.Configuration
		ref := types.NamespacedName{}
		var compiled *plan.Plan
		if spec.PlanRef != nil {
			if r.APIReader == nil {
				return nil, nil, fmt.Errorf("DMS plan API reader is not initialized")
			}
			ref = types.NamespacedName{Namespace: spec.PlanRef.Namespace, Name: spec.PlanRef.Name}
			compiled = plans[ref]
			if compiled == nil {
				compiled, err = r.resolveConfigurationPlan(ctx, ref, inventory)
				if err != nil {
					return nil, nil, err
				}
				plans[ref] = compiled
			}
		}
		observed, err := selectedPlanDevice(status.device, inventory)
		if err != nil {
			return nil, nil, err
		}
		if claimed[observed.ID] {
			return nil, nil, fmt.Errorf("multiple NicDevices select ASIC %s", observed.ID)
		}
		claimed[observed.ID] = true
		force, conf := false, ""
		if spec.Template != nil {
			force = spec.Template.Force
			if spec.Template.NetworkBay != nil {
				conf = spec.Template.NetworkBay.Conf
			}
		}
		key := fmt.Sprintf("%s/%s|%t|%s", ref.Namespace, ref.Name, force, conf)
		if compiled == nil && conf == "" {
			// Ordinary ASICs keep independent carrier and runtime failure handling.
			key += "|" + observed.ID
		}
		group := byOptions[key]
		if group == nil {
			group = &planConfigurationGroup{members: map[string]*nicDeviceConfigurationStatus{}, nv: dmsconfiguration.NVConfigRequest{Plan: compiled, Overrides: map[string][]dmsconfiguration.NVConfigParam{}}, runtime: dmsconfiguration.RuntimeRequest{Plan: compiled}, options: dmsconfiguration.NVConfigOptions{Force: force}}
			if conf != "" {
				group.nv.NetworkBay = &dmsconfiguration.NetworkBayConfig{Conf: conf}
			}
			byOptions[key] = group
			groups = append(groups, group)
		}
		overrides, err := r.translateConfigurationNV(ctx, spec.Template, observed, compiled != nil, force)
		if err != nil {
			return nil, nil, err
		}
		ports, err := configuration.TranslateRuntime(spec.Template, observed)
		if err != nil {
			return nil, nil, err
		}
		if compiled == nil {
			preserveUnboundPFPolicy(ports, observed)
		}
		group.members[observed.ID] = status
		group.nv.Devices = append(group.nv.Devices, observed)
		group.nv.Overrides[observed.ID] = overrides
		group.runtime.Devices = append(group.runtime.Devices, observed)
		group.runtime.Ports = append(group.runtime.Ports, ports...)
	}
	if err := validatePlanGroupRequirements(groups); err != nil {
		return nil, nil, err
	}

	return legacy, groups, nil
}

// selectConfigurationStatuses keeps reset-to-default on its existing boundary.
func selectConfigurationStatuses(statuses nicDeviceConfigurationStatuses) (nicDeviceConfigurationStatuses, nicDeviceConfigurationStatuses, error) {
	legacy := nicDeviceConfigurationStatuses{}
	selected := nicDeviceConfigurationStatuses{}
	for _, status := range statuses {
		spec := status.device.Spec.Configuration
		if spec != nil && spec.ResetToDefault && spec.PlanRef != nil {
			return nil, nil, fmt.Errorf("device %s: planRef cannot reset to default", status.device.Name)
		}
		if spec == nil || spec.ResetToDefault {
			legacy = append(legacy, status)
			continue
		}

		if spec.PlanRef != nil && (spec.PlanRef.Name == "" || spec.PlanRef.Namespace == "") {
			return nil, nil, fmt.Errorf("device %s: planRef requires namespace/name and cannot reset to default", status.device.Name)
		}
		selected = append(selected, status)
	}
	return legacy, selected, nil
}

func (r *NicDeviceReconciler) translateConfigurationNV(ctx context.Context, template *v1alpha1.ConfigurationTemplateSpec, observed device.Device, planned, force bool) ([]dmsconfiguration.NVConfigParam, error) {
	// Ordinary Force keeps its own native queries and validation.
	if !planned && force {
		return nil, nil
	}
	var defaults map[string]string
	var err error
	if template != nil {
		defaults, err = r.PlanConfiguration.QueryNVDefaults(ctx, observed.Ports[0])
		if err != nil {
			return nil, err
		}
	}
	linkTypes, err := observeFixedLinkTypes(ctx, template, observed, defaults)
	if err != nil {
		return nil, err
	}
	return configuration.TranslateNVOverrides(template, observed, defaults, linkTypes, !planned)
}

func validatePlanGroupRequirements(groups []*planConfigurationGroup) error {
	for _, group := range groups {
		if group.nv.Plan == nil {
			continue
		}
		requirements, err := group.nv.Plan.Requirements(group.nv.Devices)
		if err != nil {
			return err
		}
		if err := validateConfigurationRequirements(requirements); err != nil {
			return err
		}
		for _, requirement := range requirements {
			if requirement.Stage == "configure" && requirement.Group == "cc" && requirement.Kind == "service" {
				ports, err := group.nv.Plan.RuntimeGroupPorts("cc", group.nv.Devices)
				if err != nil {
					return err
				}
				if len(ports) == 0 {
					return fmt.Errorf("CC service requirement %s has no eligible compiled CC endpoints", requirement.Reference)
				}
			}
		}
	}
	return nil
}

// preserveUnboundPFPolicy retains the ordinary-template baseline: a PF without
// a netdev does not receive per-port RoCE, extended QoS or performance settings.
// PCI MRRS and basic QoS remain requested. Compiled plan intent never uses this.
func preserveUnboundPFPolicy(ports []dmsconfiguration.PortRuntimeConfig, observed device.Device) {
	for i := range ports {
		if observed.Ports[i].Netdev != "" {
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

// validateConfigurationRequirements recognizes ownership already established by
// the host-k8s deployment. These are not readiness certificates: in particular,
// SPX/SR-IOV setup follows NCO's existing completion gate. Unknown required work
// must not disappear merely because the library does not own its execution.
func validateConfigurationRequirements(requirements []plan.Requirement) error {
	for _, requirement := range requirements {
		key := requirement.Stage + "/" + requirement.Group + "/" + requirement.Kind + "/" + requirement.Reference
		switch key {
		// Host provisioning owns boot policy, GPUDirect ACS and LLDP.
		case "prepare/post-breakout/service/spcx-boot-policy", "prepare/post-breakout/service/spcx-gpudirect-acs", "configure/preflight/service/lldpd":
		// Spectrum-X and SR-IOV operators own eSwitch and xplane setup.
		case "configure/eswitch/package-runtime/eswitch", "configure/xplane/package-runtime/xplane", "configure/xplane/service/spcx-doca-xplane-boot":
		// NCO retains its existing ordered CC startup and process monitor.
		case "configure/cc/service/spcx-cc-late", "configure/cc/service/doca_spcx_cc":
		default:
			return fmt.Errorf("unsupported external plan requirement %s", key)
		}
	}
	return nil
}

func selectedPlanDevice(nic *v1alpha1.NicDevice, inventory device.Inventory) (device.Device, error) {
	if len(nic.Status.Ports) == 0 {
		return device.Device{}, fmt.Errorf("device %s has no observed PF", nic.Name)
	}
	key := utils.PCIDeviceAddress(nic.Status.Ports[0].PCI)
	for _, issue := range inventory.Issues {
		if issue.PCI == "" && issue.DeviceID == "" || issue.PCI != "" && utils.PCIDeviceAddress(issue.PCI) == key {
			return device.Device{}, fmt.Errorf("selected device %s has a discovery issue: %v", nic.Name, issue.Err)
		}
	}
	for _, observed := range inventory.Devices {
		if len(observed.Ports) > 0 && utils.PCIDeviceAddress(observed.Ports[0].PCI) == key {
			for _, issue := range inventory.Issues {
				if issue.DeviceID == observed.ID {
					return device.Device{}, fmt.Errorf("selected device %s has a discovery issue: %v", nic.Name, issue.Err)
				}
			}
			return observed, nil
		}
	}
	return device.Device{}, fmt.Errorf("selected device %s is absent from inventory", nic.Name)
}

// resolveConfigurationPlan checks the referenced Kubernetes input identity, not
// a new device readiness condition. The library owns inventory interpretation.
func (r *NicDeviceReconciler) resolveConfigurationPlan(ctx context.Context, ref types.NamespacedName, inventory device.Inventory) (*plan.Plan, error) {
	params := &dmsv1.DMSPlanParams{}
	if err := r.APIReader.Get(ctx, ref, params); err != nil {
		return nil, fmt.Errorf("read DMSPlanParams %s: %w", ref, err)
	}
	node := &corev1.Node{}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Name: r.NodeName}, node); err != nil {
		return nil, err
	}
	if !node.DeletionTimestamp.IsZero() || !labels.SelectorFromSet(params.Spec.NodeSelector).Matches(labels.Set(node.Labels)) {
		return nil, fmt.Errorf("DMSPlanParams %s does not select node %s", ref, r.NodeName)
	}
	blueprint := &corev1.ConfigMap{}
	if err := r.APIReader.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: params.Spec.BlueprintRef}, blueprint); err != nil {
		return nil, err
	}
	data := []byte(blueprint.Data["blueprint.yaml"])
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	host, found := params.Status.TargetMapByHost[r.NodeName]
	condition := meta.FindStatusCondition(host.Conditions, "Resolved")
	if !found || host.NodeUID != node.UID || host.ObservedGeneration != params.Generation || host.BlueprintDigest != digest || host.InventoryStage == "" || condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != params.Generation {
		return nil, fmt.Errorf("DMSPlanParams %s has no current resolved binding for node %s", ref, r.NodeName)
	}
	loaded, err := plan.Load(data)
	if err != nil {
		return nil, err
	}
	options, err := configurationPlanOptions(params.Spec)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(host.TargetMap)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&options.TargetMap); err != nil {
		return nil, fmt.Errorf("decode host target map: %w", err)
	}
	return plan.BuildFromInventory(ctx, loaded, inventory, options)
}

func configurationPlanOptions(spec dmsv1.DMSPlanSpec) (plan.BuildOptions, error) {
	parameters := make(map[string]any, len(spec.Parameters)+3)
	for key, value := range spec.Parameters {
		switch key {
		case "planes", "overlay", "SP_XGS":
			return plan.BuildOptions{}, fmt.Errorf("parameter %q must use its explicit field", key)
		}
		parameters[key] = value
	}
	if spec.Planes != nil {
		parameters["planes"] = int(*spec.Planes)
	}
	if spec.Overlay != "" {
		parameters["overlay"] = spec.Overlay
	}
	if spec.SPXGS != nil {
		parameters["SP_XGS"] = *spec.SPXGS
	}
	return plan.BuildOptions{Profile: spec.Profile, Environment: "host-k8s", PlatformType: spec.PlatformType, Parameters: parameters}, nil
}

// +kubebuilder:rbac:groups=dms.nvidia.com,resources=dmsplanparams,verbs=get
// Keep the CRD optional: no informer is registered. Existing periodic reconcile
// reads external plan changes even if the CRD is installed after daemon startup.

// observeFixedLinkTypes gathers the existing NCO link-type observation explicitly
// before template translation. It is needed only when firmware exposes no link
// type parameter; it does not add configuration or infer a firmware capability.
func observeFixedLinkTypes(ctx context.Context, template *v1alpha1.ConfigurationTemplateSpec, observed device.Device, defaults map[string]string) (map[string]string, error) {
	if template == nil || template.LinkType == "" {
		return nil, nil
	}
	if _, configurable := defaults[consts.LinkTypeP1Param]; configurable {
		return nil, nil
	}
	result := make(map[string]string, len(observed.Ports))
	for _, port := range observed.Ports {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if port.Netdev == "" {
			// Translation decides whether missing evidence is allowed for this intent.
			continue
		}
		link, err := netlink.LinkByName(port.Netdev)
		if err != nil {
			return nil, fmt.Errorf("read link type for %s: %w", port.Netdev, err)
		}
		switch link.Attrs().EncapType {
		case "ether":
			result[port.Netdev] = consts.Ethernet
		case "infiniband":
			result[port.Netdev] = consts.Infiniband
		default:
			return nil, fmt.Errorf("unsupported link type %q for %s", link.Attrs().EncapType, port.Netdev)
		}
	}
	return result, nil
}
