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
	"sort"

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
	"github.com/Mellanox/nic-configuration-operator/pkg/udev"
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

	sort.Slice(matchingTemplates, func(i, j int) bool {
		if matchingTemplates[i].Namespace == matchingTemplates[j].Namespace {
			return matchingTemplates[i].Name < matchingTemplates[j].Name
		}
		return matchingTemplates[i].Namespace < matchingTemplates[j].Namespace
	})

	assignments, err := buildInterfaceNameAssignments(r.NodeName, nodeDevices, matchingTemplates)
	if err != nil {
		r.recordTemplateError(matchingTemplates, err)
		reqLog.Error(err, "Invalid interface name template assignment")
		return ctrl.Result{}, err
	}

	proposedDevices := make([]*v1alpha1.NicDevice, 0, len(assignments))
	for i := range assignments {
		proposed := assignments[i].device.DeepCopy()
		proposed.Spec.InterfaceNameTemplate = assignments[i].spec
		proposedDevices = append(proposedDevices, proposed)
	}
	if err := udev.ValidateInterfaceNames(proposedDevices); err != nil {
		err = fmt.Errorf("invalid interface names for node %s: %w", r.NodeName, err)
		r.recordTemplateError(matchingTemplates, err)
		reqLog.Error(err, "Invalid generated interface names")
		return ctrl.Result{}, err
	}

	for i := range assignments {
		assignment := &assignments[i]
		if reflect.DeepEqual(assignment.device.Spec.InterfaceNameTemplate, assignment.spec) {
			continue
		}

		updated := assignment.device.DeepCopy()
		updated.Spec.InterfaceNameTemplate = assignment.spec
		if err := r.Patch(ctx, updated, client.MergeFrom(assignment.device.DeepCopy())); err != nil {
			reqLog.Error(err, "Failed to update device InterfaceNameTemplate spec", "device", assignment.device.Name)
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

type interfaceNameAssignment struct {
	device *v1alpha1.NicDevice
	spec   *v1alpha1.NicDeviceInterfaceNameSpec
}

type interfaceNameTemplateMatch struct {
	template     *v1alpha1.NicInterfaceNameTemplate
	nicIndex     int
	railIndex    int
	planeIndices []int
}

func buildInterfaceNameAssignments(nodeName string, devices []v1alpha1.NicDevice, templates []v1alpha1.NicInterfaceNameTemplate) ([]interfaceNameAssignment, error) {
	for i := range templates {
		template := &templates[i]
		if template.Spec.PfsPerNic <= 0 {
			return nil, fmt.Errorf("NicInterfaceNameTemplate %s must set pfsPerNic greater than zero", templateObjectKey(template))
		}
		if template.Spec.NetDevicePrefix == "" && template.Spec.RdmaDevicePrefix == "" {
			return nil, fmt.Errorf("NicInterfaceNameTemplate %s must set at least one device prefix", templateObjectKey(template))
		}
	}

	assignments := make([]interfaceNameAssignment, 0, len(devices))
	for i := range devices {
		device := &devices[i]
		matches := make([]interfaceNameTemplateMatch, 0, 1)
		for j := range templates {
			template := &templates[j]
			nicIndex, railIndex, planeIndices, found := calculateNicRailAndPlaneIndices(
				device, template.Spec.RailPciAddresses, template.Spec.PfsPerNic)
			if found {
				matches = append(matches, interfaceNameTemplateMatch{
					template:     template,
					nicIndex:     nicIndex,
					railIndex:    railIndex,
					planeIndices: planeIndices,
				})
			}
		}

		if len(matches) > 1 {
			templateNames := make([]string, len(matches))
			for j := range matches {
				templateNames[j] = templateObjectKey(matches[j].template)
			}
			return nil, fmt.Errorf("NicDevice %s on node %s is selected by multiple NicInterfaceNameTemplates: %v",
				deviceObjectKey(device), nodeName, templateNames)
		}

		assignment := interfaceNameAssignment{device: device}
		if len(matches) == 1 {
			match := matches[0]
			assignment.spec = &v1alpha1.NicDeviceInterfaceNameSpec{
				NicIndex:         match.nicIndex,
				RailIndex:        match.railIndex,
				PlaneIndices:     match.planeIndices,
				RdmaDevicePrefix: match.template.Spec.RdmaDevicePrefix,
				NetDevicePrefix:  match.template.Spec.NetDevicePrefix,
			}
		}
		assignments = append(assignments, assignment)
	}

	return assignments, nil
}

func (r *NicInterfaceNameTemplateReconciler) recordTemplateError(templates []v1alpha1.NicInterfaceNameTemplate, err error) {
	for i := range templates {
		r.EventRecorder.Event(&templates[i], v1.EventTypeWarning, "SpecError", err.Error())
	}
}

func templateObjectKey(template *v1alpha1.NicInterfaceNameTemplate) string {
	return types.NamespacedName{Namespace: template.Namespace, Name: template.Name}.String()
}

func deviceObjectKey(device *v1alpha1.NicDevice) string {
	return types.NamespacedName{Namespace: device.Namespace, Name: device.Name}.String()
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
					// Indices start with 0
					// Calculate plane indices based on position within the rail
					planeIndices := make([]int, pfsPerNic)
					firstPlaneIndex := nicPositionInRail * pfsPerNic
					for i := 0; i < pfsPerNic; i++ {
						planeIndices[i] = firstPlaneIndex + i
					}
					return nicIndex, railIndex, planeIndices, true
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

	// Trigger when device discovery changes which devices or PCI addresses can
	// match a template, or when the resolved naming spec changes.
	nicDeviceEventHandler := handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			device, ok := e.Object.(*v1alpha1.NicDevice)
			if ok && device.Status.Node == r.NodeName {
				qHandler(q)
			}
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			oldDevice, oldOK := e.ObjectOld.(*v1alpha1.NicDevice)
			newDevice, newOK := e.ObjectNew.(*v1alpha1.NicDevice)
			if !oldOK || !newOK || (oldDevice.Status.Node != r.NodeName && newDevice.Status.Node != r.NodeName) {
				return
			}
			if !deviceInterfaceNameSelectionChanged(oldDevice, newDevice) {
				return
			}
			log.Log.Info("Enqueuing sync for NicDevice update event", "resource", e.ObjectNew.GetName())
			qHandler(q)
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			device, ok := e.Object.(*v1alpha1.NicDevice)
			if ok && device.Status.Node == r.NodeName {
				qHandler(q)
			}
		},
		GenericFunc: func(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			device, ok := e.Object.(*v1alpha1.NicDevice)
			if ok && device.Status.Node == r.NodeName {
				qHandler(q)
			}
		},
	}

	nodeEventHandler := handler.Funcs{
		CreateFunc: func(ctx context.Context, e event.CreateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if e.Object.GetName() == r.NodeName {
				qHandler(q)
			}
		},
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			oldNode, oldOK := e.ObjectOld.(*v1.Node)
			newNode, newOK := e.ObjectNew.(*v1.Node)
			if oldOK && newOK && newNode.Name == r.NodeName && !reflect.DeepEqual(oldNode.Labels, newNode.Labels) {
				qHandler(q)
			}
		},
		DeleteFunc: func(ctx context.Context, e event.DeleteEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if e.Object.GetName() == r.NodeName {
				qHandler(q)
			}
		},
		GenericFunc: func(ctx context.Context, e event.GenericEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			if e.Object.GetName() == r.NodeName {
				qHandler(q)
			}
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		Watches(&v1alpha1.NicInterfaceNameTemplate{}, eventHandler).
		Watches(&v1alpha1.NicDevice{}, nicDeviceEventHandler).
		Watches(&v1.Node{}, nodeEventHandler).
		Named("nicInterfaceNameTemplateReconciler").
		Complete(r)
}

func deviceInterfaceNameSelectionChanged(oldDevice, newDevice *v1alpha1.NicDevice) bool {
	if oldDevice.Status.Node != newDevice.Status.Node ||
		!reflect.DeepEqual(oldDevice.Spec.InterfaceNameTemplate, newDevice.Spec.InterfaceNameTemplate) ||
		len(oldDevice.Status.Ports) != len(newDevice.Status.Ports) {
		return true
	}

	for i := range oldDevice.Status.Ports {
		if oldDevice.Status.Ports[i].PCI != newDevice.Status.Ports[i].PCI {
			return true
		}
	}
	return false
}
