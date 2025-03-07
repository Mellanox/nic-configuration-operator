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
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
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
			continue
		}

		if len(matchingTemplates) > 1 {
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
		}

		matchingTemplate := matchingTemplates[0]

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
	}

	return nil
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
