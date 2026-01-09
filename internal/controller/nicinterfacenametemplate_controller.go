/*
2026 NVIDIA CORPORATION & AFFILIATES
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
	"reflect"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	v1alpha1 "github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
)

const nicInterfaceNameTemplateSyncEventName = "nic-interface-name-template-sync-event"

// NicInterfaceNameTemplateReconciler reconciles a NicInterfaceNameTemplate object
type NicInterfaceNameTemplateReconciler struct {
	client.Client
	EventRecorder record.EventRecorder
	Scheme        *runtime.Scheme

	NodeName string
}

// Reconcile reconciles the NicInterfaceNameTemplate object
func (r *NicInterfaceNameTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Name != nicInterfaceNameTemplateSyncEventName || req.Namespace != "" {
		return reconcile.Result{}, nil
	}
	reqLog := log.FromContext(ctx)
	reqLog.Info("Reconciling NicInterfaceNameTemplates")

	// Get the node object for this controller
	node := &v1.Node{}
	err := r.Get(ctx, types.NamespacedName{Name: r.NodeName}, node)
	if err != nil {
		reqLog.Error(err, "Failed to get node", "nodeName", r.NodeName)
		return ctrl.Result{}, err
	}

	// List NicInterfaceNameTemplate objects and filter them by the node's labels
	templateList := &v1alpha1.NicInterfaceNameTemplateList{}
	err = r.List(ctx, templateList)
	if err != nil {
		reqLog.Error(err, "Failed to list NicInterfaceNameTemplates")
		return ctrl.Result{}, err
	}
	reqLog.V(2).Info("Listed NicInterfaceNameTemplates", "count", len(templateList.Items))

	var matchingTemplates []v1alpha1.NicInterfaceNameTemplate
	for _, template := range templateList.Items {
		if nodeMatchesNodeSelector(node, template.Spec.NodeSelector) {
			matchingTemplates = append(matchingTemplates, template)
		}
	}
	reqLog.V(2).Info("Found matching templates", "count", len(matchingTemplates))

	selectorFields := fields.OneTermEqualSelector("status.node", r.NodeName)

	// List NicDevice objects from this node
	deviceList := &v1alpha1.NicDeviceList{}
	err = r.List(ctx, deviceList, &client.ListOptions{FieldSelector: selectorFields})
	if err != nil {
		reqLog.Error(err, "Failed to list NicDevices")
		return ctrl.Result{}, err
	}

	nodeDevices := deviceList.Items
	reqLog.V(2).Info("Found devices on this node", "count", len(nodeDevices))

	if len(matchingTemplates) > 1 {
		// Error out if there are more than one matching template
		templateNames := make([]string, len(matchingTemplates))
		for i, t := range matchingTemplates {
			templateNames[i] = t.Name
		}
		err = fmt.Errorf("multiple NicInterfaceNameTemplates match node %s: %v", r.NodeName, templateNames)
		reqLog.Error(err, "Multiple templates matching this node")

		// Clear the InterfaceNameTemplate spec from all devices on this node
		for i := range nodeDevices {
			device := &nodeDevices[i]
			if device.Spec.InterfaceNameTemplate != nil {
				r.EventRecorder.Event(device, v1.EventTypeWarning, "SpecError",
					fmt.Sprintf("Multiple NicInterfaceNameTemplates match this node: %v", templateNames))
				device.Spec.InterfaceNameTemplate = nil
				if updateErr := r.Update(ctx, device); updateErr != nil {
					reqLog.Error(updateErr, "Failed to clear device InterfaceNameTemplate spec", "device", device.Name)
					return ctrl.Result{}, updateErr
				}
			}
		}
		return ctrl.Result{}, err
	}

	if len(matchingTemplates) == 0 {
		// No matching templates, clear the InterfaceNameTemplate spec from all devices on this node
		reqLog.V(2).Info("No matching templates, clearing InterfaceNameTemplate spec from devices")
		for i := range nodeDevices {
			device := &nodeDevices[i]
			if device.Spec.InterfaceNameTemplate != nil {
				device.Spec.InterfaceNameTemplate = nil
				if err := r.Update(ctx, device); err != nil {
					reqLog.Error(err, "Failed to clear device InterfaceNameTemplate spec", "device", device.Name)
					return ctrl.Result{}, err
				}
			}
		}
		return ctrl.Result{}, nil
	}

	// Apply the matching template to devices
	matchingTemplate := &matchingTemplates[0]
	reqLog.V(2).Info("Applying template to devices", "template", matchingTemplate.Name)

	for i := range nodeDevices {
		device := &nodeDevices[i]

		// Calculate NicIndex, RailIndex, and PlaneIndices based on the device's PCI address
		nicIndex, railIndex, planeIndices, found := calculateNicRailAndPlaneIndices(device, matchingTemplate.Spec.RailPciAddresses, matchingTemplate.Spec.PfsPerNic)
		if !found {
			reqLog.V(2).Info("Device PCI address not found in template's RailPciAddresses, skipping",
				"device", device.Name, "ports", device.Status.Ports)
			// Clear the InterfaceNameTemplate spec if device doesn't match the template's PCI addresses
			if device.Spec.InterfaceNameTemplate != nil {
				device.Spec.InterfaceNameTemplate = nil
				if err := r.Update(ctx, device); err != nil {
					reqLog.Error(err, "Failed to clear device InterfaceNameTemplate spec", "device", device.Name)
					return ctrl.Result{}, err
				}
			}
			continue
		}

		// Build the new spec
		newSpec := &v1alpha1.NicDeviceInterfaceNameSpec{
			NicIndex:         nicIndex,
			RailIndex:        railIndex,
			PlaneIndices:     planeIndices,
			RdmaDevicePrefix: matchingTemplate.Spec.RdmaDevicePrefix,
			NetDevicePrefix:  matchingTemplate.Spec.NetDevicePrefix,
			RailPciAddresses: matchingTemplate.Spec.RailPciAddresses,
		}

		// Check if update is needed
		if !reflect.DeepEqual(device.Spec.InterfaceNameTemplate, newSpec) {
			reqLog.V(2).Info("Updating device InterfaceNameTemplate spec",
				"device", device.Name, "nicIndex", nicIndex, "railIndex", railIndex)
			device.Spec.InterfaceNameTemplate = newSpec
			if err := r.Update(ctx, device); err != nil {
				reqLog.Error(err, "Failed to update device InterfaceNameTemplate spec", "device", device.Name)
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

// calculateNicRailAndPlaneIndices finds the NIC index (flattened position), rail index, and plane indices
// for a device based on its PCI addresses, the template's RailPciAddresses, and pfsPerNic.
// Plane indices are sequential within a rail - for each NIC in a rail, planes are numbered consecutively.
// Example with 2 NICs per rail and pfsPerNic=2:
//   - Rail 1: NIC1 planes [1,2], NIC2 planes [3,4]
//   - Rail 2: NIC3 planes [1,2], NIC4 planes [3,4]
//
// Returns nicIndex, railIndex, planeIndices, and whether the device was found in the mapping.
func calculateNicRailAndPlaneIndices(device *v1alpha1.NicDevice, railPciAddresses [][]string, pfsPerNic int) (int, int, []int, bool) {
	nicIndex := 0
	for railIndex, pciAddrs := range railPciAddresses {
		for nicPositionInRail, pciAddr := range pciAddrs {
			// Check if any of the device's ports match this PCI address
			for _, port := range device.Status.Ports {
				if port.PCI == pciAddr {
					// Indices start with 1
					// Calculate plane indices based on position within the rail
					planeIndices := make([]int, pfsPerNic)
					firstPlaneIndex := nicPositionInRail*pfsPerNic + 1
					for i := 0; i < pfsPerNic; i++ {
						planeIndices[i] = firstPlaneIndex + i
					}
					return nicIndex + 1, railIndex + 1, planeIndices, true
				}
			}
			nicIndex++
		}
	}
	return 0, 0, nil, false
}

// SetupWithManager sets up the controller with the Manager.
func (r *NicInterfaceNameTemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.EventRecorder = mgr.GetEventRecorderFor("NicInterfaceNameTemplateReconciler")

	qHandler := func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: "",
			Name:      nicInterfaceNameTemplateSyncEventName,
		}})
	}

	eventHandler := handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Log.Info("Enqueuing sync for create event", "resource", e.Object.GetName())
			qHandler(q)
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Log.Info("Enqueuing sync for update event", "resource", e.ObjectNew.GetName())
			qHandler(q)
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Log.Info("Enqueuing sync for delete event", "resource", e.Object.GetName())
			qHandler(q)
		},
		GenericFunc: func(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Log.Info("Enqueuing sync for generic event", "resource", e.Object.GetName())
			qHandler(q)
		},
	}

	// Trigger also on update of NicDevice from this node
	nicDeviceEventHandler := handler.Funcs{
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Log.Info("Enqueuing sync for NicDevice update event", "resource", e.ObjectNew.GetName())
			qHandler(q)
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		Watches(&v1alpha1.NicInterfaceNameTemplate{}, eventHandler).
		Watches(&v1alpha1.NicDevice{}, nicDeviceEventHandler).
		Named("nicInterfaceNameTemplateReconciler").
		Complete(r)
}
