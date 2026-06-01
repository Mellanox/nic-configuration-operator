/*
2025 NVIDIA CORPORATION & AFFILIATES
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

package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/pkg/consts"
)

type clearDeviceSpecFunc = func(device *v1alpha1.NicDevice)

type nicTemplate interface {
	getName() string
	getNodeSelector() map[string]string
	getNicSelector() *v1alpha1.NicSelectorSpec
	getStatus() *v1alpha1.NicTemplateStatus
	// returns true if device's spec was updated
	applyToDevice(device *v1alpha1.NicDevice) bool
	updateStatus(ctx context.Context, client client.Client) error
	// networkBayRequested reports whether the template configures a ConnectX-9 Network Bay,
	// which triggers the whole-bay matching constraint. Only NicConfigurationTemplate can.
	networkBayRequested() bool
	// getObject returns the underlying template object, used to emit events against it.
	getObject() client.Object
}

func matchDevicesToTemplates(ctx context.Context, client client.Client, recorder record.EventRecorder, templates []nicTemplate, clearDeviceSpec clearDeviceSpecFunc) error {
	deviceList := &v1alpha1.NicDeviceList{}
	err := client.List(ctx, deviceList)
	if err != nil {
		log.Log.Error(err, "Failed to list NicDevices")
		return err
	}
	log.Log.V(2).Info("Listed devices", "count", len(deviceList.Items))

	nodeList := &v1.NodeList{}
	err = client.List(ctx, nodeList)
	if err != nil {
		log.Log.Error(err, "Failed to list cluster nodes")
		return err
	}
	log.Log.V(2).Info("Listed nodes", "count", len(nodeList.Items))

	nodeMap := map[string]*v1.Node{}
	for _, node := range nodeList.Items {
		nodeMap[node.Name] = &node
	}

	// Precompute, per Network Bay template, the set of nodes whose matched devices do not form
	// whole bays (see §7). Devices on a rejected node must not receive the template's spec.
	bayRejectedNodes := map[string]map[string]string{} // templateName -> nodeName -> reason
	for _, template := range templates {
		if template.networkBayRequested() {
			bayRejectedNodes[template.getName()] = computeNetworkBayRejectedNodes(template, templates, deviceList.Items, nodeMap)
		}
	}

	for _, device := range deviceList.Items {
		node, ok := nodeMap[device.Status.Node]
		if !ok {
			log.Log.Info("device doesn't match any node, skipping", "device", device.Name)
			continue
		}

		var matchingTemplates []nicTemplate

		for _, template := range templates {
			if !deviceMatchesConfigurationTemplateSelectors(&device, template, node) {
				dropDeviceFromStatus(device.Name, template)

				continue
			}

			matchingTemplates = append(matchingTemplates, template)
		}

		if len(matchingTemplates) == 0 {
			log.Log.V(2).Info("Device doesn't match any configuration template, resetting the spec", "device", device.Name)

			clearDeviceSpec(&device)

			err = client.Update(ctx, &device)
			if err != nil {
				log.Log.Error(err, "Failed to update device's spec", "device", device)
				return err
			}
		} else if len(matchingTemplates) > 1 {
			for _, template := range matchingTemplates {
				dropDeviceFromStatus(device.Name, template)
			}

			templateNames := []string{}
			for _, template := range matchingTemplates {
				templateNames = append(templateNames, template.getName())
			}
			joinedTemplateNames := strings.Join(templateNames, ",")
			err = fmt.Errorf("device matches several configuration templates: %s, %s", device.Name, joinedTemplateNames)
			log.Log.Error(err, "Device matches several configuration templates, deleting its spec and emitting an error event", "device", device.Name)
			err = handleErrorSeveralMatchingTemplates(ctx, client, recorder, &device, joinedTemplateNames, clearDeviceSpec)
			if err != nil {
				log.Log.Error(err, "Failed to emit warning about multiple templates matching one device", "templates", joinedTemplateNames)
				return err
			}
		} else {
			matchingTemplate := matchingTemplates[0]

			// Network Bay templates must match a whole bay on a node. If this device's node was
			// rejected, do not apply the spec — clear it and surface the imbalance instead.
			if rejected, ok := bayRejectedNodes[matchingTemplate.getName()]; ok {
				if reason, isRejected := rejected[device.Status.Node]; isRejected {
					log.Log.Info("Network Bay template does not match a whole bay on node, rejecting device",
						"device", device.Name, "node", device.Status.Node, "reason", reason)
					dropDeviceFromStatus(device.Name, matchingTemplate)
					if err := handleNetworkBayImbalance(ctx, client, &device, reason, clearDeviceSpec); err != nil {
						return err
					}
					continue
				}
			}

			status := matchingTemplate.getStatus()
			if !slices.Contains(status.NicDevices, device.Name) {
				status.NicDevices = append(status.NicDevices, device.Name)
			}

			updateRequired := matchingTemplate.applyToDevice(&device)
			if updateRequired {
				err := client.Update(ctx, &device)
				if err != nil {
					log.Log.Error(err, "Failed to update NicDevice spec", "device", device.Name)
					return err
				}
			}

			// If this device was previously rejected for a Network Bay imbalance that has since
			// resolved (e.g. the sibling ASIC finished discovery), clear the stale device condition.
			if matchingTemplate.networkBayRequested() {
				if err := clearNetworkBayImbalanceCondition(ctx, client, &device); err != nil {
					return err
				}
			}
		}
	}

	// Surface the aggregate Network Bay pairing status on each bay template (event + condition).
	for _, template := range templates {
		if !template.networkBayRequested() {
			continue
		}
		setNetworkBayTemplateCondition(recorder, template, bayRejectedNodes[template.getName()])
	}

	return nil
}

// computeNetworkBayRejectedNodes returns, for a Network Bay template, the nodes whose
// selector-matching devices do not form whole bays, mapped to a human-readable reason.
// allTemplates is the full set of configuration templates: a device that also matches another
// template will be cleared by the multi-template conflict handler, so its sibling must not be
// configured on its own — such a node is rejected to keep the bay atomic.
func computeNetworkBayRejectedNodes(template nicTemplate, allTemplates []nicTemplate, devices []v1alpha1.NicDevice, nodeMap map[string]*v1.Node) map[string]string {
	devicesByNode := map[string][]*v1alpha1.NicDevice{}
	conflictedNodes := map[string]bool{}
	for i := range devices {
		device := &devices[i]
		node, ok := nodeMap[device.Status.Node]
		if !ok {
			continue
		}
		if !deviceMatchesConfigurationTemplateSelectors(device, template, node) {
			continue
		}
		devicesByNode[device.Status.Node] = append(devicesByNode[device.Status.Node], device)
		// A device matching this bay template AND another template is a multi-template conflict:
		// the main matching loop will clear its spec, so it will never receive the bay config.
		if countMatchingTemplates(device, allTemplates, node) > 1 {
			conflictedNodes[device.Status.Node] = true
		}
	}

	rejected := map[string]string{}
	for nodeName, nodeDevices := range devicesByNode {
		if conflictedNodes[nodeName] {
			rejected[nodeName] = "one or more matched devices also match another configuration template; the Network Bay cannot be configured atomically"
			continue
		}
		if reason := networkBayImbalanceReason(nodeDevices); reason != "" {
			rejected[nodeName] = reason
		}
	}
	return rejected
}

// countMatchingTemplates returns how many of the given templates select the device on its node.
func countMatchingTemplates(device *v1alpha1.NicDevice, templates []nicTemplate, node *v1.Node) int {
	count := 0
	for _, template := range templates {
		if deviceMatchesConfigurationTemplateSelectors(device, template, node) {
			count++
		}
	}
	return count
}

// networkBayImbalanceReason validates that the matched devices on a node form whole Network Bays:
// an even count, every device detected as part of a bay, and serial numbers partitioning into
// pairs of exactly two. Returns "" when the set is valid, or a reason string otherwise.
func networkBayImbalanceReason(devices []*v1alpha1.NicDevice) string {
	if len(devices)%2 != 0 {
		return fmt.Sprintf("matched an odd number of devices (%d); a Network Bay template must match whole bays (pairs)", len(devices))
	}

	bySerial := map[string]int{}
	for _, device := range devices {
		if device.Status.NetworkBay == nil {
			return fmt.Sprintf("device %s is not part of a Network Bay card but was matched by a Network Bay template", device.Name)
		}
		bySerial[device.Status.SerialNumber]++
	}

	for serial, count := range bySerial {
		if count != 2 {
			return fmt.Sprintf("serial number %s matched %d devices, expected exactly 2 (one Network Bay pair)", serial, count)
		}
	}

	return ""
}

// handleNetworkBayImbalance clears the rejected device's spec and records a per-device condition
// describing why the Network Bay template was not applied.
func handleNetworkBayImbalance(ctx context.Context, client client.Client, device *v1alpha1.NicDevice, reason string, clearDeviceSpec clearDeviceSpecFunc) error {
	clearDeviceSpec(device)
	if err := client.Update(ctx, device); err != nil {
		log.Log.Error(err, "Failed to clear spec for Network Bay imbalanced device", "device", device.Name)
		return err
	}

	meta.SetStatusCondition(&device.Status.Conditions, metav1.Condition{
		Type:               consts.NetworkBayCondition,
		Status:             metav1.ConditionFalse,
		Reason:             consts.NetworkBayImbalanceReason,
		ObservedGeneration: device.Generation,
		Message:            reason,
	})
	if err := client.Status().Update(ctx, device); err != nil {
		log.Log.Error(err, "Failed to update Network Bay condition for device", "device", device.Name)
		return err
	}
	return nil
}

// clearNetworkBayImbalanceCondition transitions a device's NetworkBayPairing condition to True once
// a previously-flagged imbalance has resolved. It is a no-op for devices that never carried the
// condition, so healthy devices are not churned with status writes.
func clearNetworkBayImbalanceCondition(ctx context.Context, client client.Client, device *v1alpha1.NicDevice) error {
	existing := meta.FindStatusCondition(device.Status.Conditions, consts.NetworkBayCondition)
	if existing == nil || existing.Status == metav1.ConditionTrue {
		return nil
	}

	meta.SetStatusCondition(&device.Status.Conditions, metav1.Condition{
		Type:               consts.NetworkBayCondition,
		Status:             metav1.ConditionTrue,
		Reason:             consts.NetworkBayBalancedReason,
		ObservedGeneration: device.Generation,
		Message:            "device is part of a complete Network Bay",
	})
	if err := client.Status().Update(ctx, device); err != nil {
		log.Log.Error(err, "Failed to clear Network Bay condition for device", "device", device.Name)
		return err
	}
	return nil
}

// setNetworkBayTemplateCondition records the aggregate Network Bay pairing status on the template:
// a False condition (and Warning event on transition) listing offending nodes when any node was
// rejected, or a True condition when all matched bays are whole.
func setNetworkBayTemplateCondition(recorder record.EventRecorder, template nicTemplate, rejectedNodes map[string]string) {
	status := template.getStatus()

	var condition metav1.Condition
	if len(rejectedNodes) == 0 {
		condition = metav1.Condition{
			Type:    consts.NetworkBayCondition,
			Status:  metav1.ConditionTrue,
			Reason:  consts.NetworkBayBalancedReason,
			Message: "all matched Network Bay cards are configured as whole bays",
		}
	} else {
		messages := make([]string, 0, len(rejectedNodes))
		for node, reason := range rejectedNodes {
			messages = append(messages, fmt.Sprintf("node %s: %s", node, reason))
		}
		slices.Sort(messages)
		condition = metav1.Condition{
			Type:    consts.NetworkBayCondition,
			Status:  metav1.ConditionFalse,
			Reason:  consts.NetworkBayImbalanceReason,
			Message: strings.Join(messages, "; "),
		}
	}

	changed := meta.SetStatusCondition(&status.Conditions, condition)
	if changed && condition.Status == metav1.ConditionFalse {
		recorder.Event(template.getObject(), v1.EventTypeWarning, consts.NetworkBayImbalanceReason, condition.Message)
	}
}

func dropDeviceFromStatus(deviceName string, template nicTemplate) {
	status := template.getStatus()
	index := slices.Index(status.NicDevices, deviceName)
	if index != -1 {
		// Device no longer matches template, drop it from the template's status
		status.NicDevices = slices.Delete(status.NicDevices, index, index+1)
	}
}

func handleErrorSeveralMatchingTemplates(ctx context.Context, client client.Client, recorder record.EventRecorder, device *v1alpha1.NicDevice, matchingTemplates string, clearDeviceSpec clearDeviceSpecFunc) error {
	recorder.Event(device, v1.EventTypeWarning, "SpecError", fmt.Sprintf("Several templates matching this device: %s", matchingTemplates))

	clearDeviceSpec(device)

	return client.Update(ctx, device)
}

func deviceMatchesConfigurationTemplateSelectors(device *v1alpha1.NicDevice, template nicTemplate, node *v1.Node) bool {
	if !nodeMatchesNodeSelector(node, template.getNodeSelector()) {
		return false
	}

	nicSelector := template.getNicSelector()

	if nicSelector.NicType != device.Status.Type {
		return false
	}

	if !deviceMatchesPCISelector(device, nicSelector.PciAddresses) {
		return false
	}

	if !deviceMatchesSerialNumberSelector(device, nicSelector.SerialNumbers) {
		return false
	}

	if !deviceMatchesPartNumberSelector(device, nicSelector.PartNumbers) {
		return false
	}

	return true
}

func nodeMatchesNodeSelector(node *v1.Node, nodeSelector map[string]string) bool {
	for k, v := range nodeSelector {
		if nv, ok := node.Labels[k]; ok && nv == v {
			continue
		}
		return false
	}
	return true
}

func deviceMatchesPCISelector(device *v1alpha1.NicDevice, pciAddresses []string) bool {
	if len(pciAddresses) > 0 {
		matchesPCI := false
		for _, port := range device.Status.Ports {
			if slices.Contains(pciAddresses, port.PCI) {
				matchesPCI = true
			}
		}
		if !matchesPCI {
			return false
		}
	}

	return true
}

func deviceMatchesSerialNumberSelector(device *v1alpha1.NicDevice, serialNumbers []string) bool {
	if len(serialNumbers) > 0 {
		if !slices.Contains(serialNumbers, device.Status.SerialNumber) {
			return false
		}
	}

	return true
}

func deviceMatchesPartNumberSelector(device *v1alpha1.NicDevice, partNumbers []string) bool {
	if len(partNumbers) > 0 {
		if !slices.Contains(partNumbers, device.Status.PartNumber) {
			return false
		}
	}

	return true
}
