/*
Copyright 2024.

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
	"slices"
	"strings"

	v1 "k8s.io/api/core/v1"
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

const nicConfigurationTemplateSyncEventName = "nic-configuration-template-sync-event"

// NicConfigurationTemplateReconciler reconciles a NicConfigurationTemplate object
type NicConfigurationTemplateReconciler struct {
	client.Client
	EventRecorder record.EventRecorder
	Scheme        *runtime.Scheme
}

//+kubebuilder:rbac:groups=configuration.net.nvidia.com,resources=nicconfigurationtemplates,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=configuration.net.nvidia.com,resources=nicconfigurationtemplates/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=configuration.net.nvidia.com,resources=nicconfigurationtemplates/finalizers,verbs=update
//+kubebuilder:rbac:groups=configuration.net.nvidia.com,resources=nicdevices/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=configuration.net.nvidia.com,resources=nicdevices,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=configuration.net.nvidia.com,resources=nicdevices/finalizers,verbs=update
//+kubebuilder:rbac:groups=configuration.net.nvidia.com,resources=nicfirmwaretemplates,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=configuration.net.nvidia.com,resources=nicfirmwaretemplates/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=configuration.net.nvidia.com,resources=nicfirmwaretemplates/finalizers,verbs=update
//+kubebuilder:rbac:groups=configuration.net.nvidia.com,resources=nicfirmwaresources/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=configuration.net.nvidia.com,resources=nicfirmwaresources,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=configuration.net.nvidia.com,resources=nicfirmwaresources/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get
//+kubebuilder:rbac:groups="",resources=pods,verbs=list
//+kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create;delete;get;list;patch;update;watch
//+kubebuilder:rbac:groups=maintenance.nvidia.com,resources=nodemaintenances,verbs=get;list;watch;create;update;patch;delete

// Reconcile reconciles the NicConfigurationTemplate object
func (r *NicConfigurationTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Name != nicConfigurationTemplateSyncEventName || req.Namespace != "" {
		return reconcile.Result{}, nil
	}
	log.Log.Info("Reconciling NicConfigurationTemplates")

	templateList := &v1alpha1.NicConfigurationTemplateList{}
	err := r.List(ctx, templateList)
	if err != nil {
		return reconcile.Result{}, err
	}
	log.Log.V(2).Info("Listed templates", "templates", templateList.Items)

	deviceList := &v1alpha1.NicDeviceList{}
	err = r.List(ctx, deviceList)
	if err != nil {
		log.Log.Error(err, "Failed to list NicDevices")
		return ctrl.Result{}, err
	}
	log.Log.V(2).Info("Listed devices", "devices", deviceList.Items)

	nodeList := &v1.NodeList{}
	err = r.List(ctx, nodeList)
	if err != nil {
		log.Log.Error(err, "Failed to list cluster nodes")
		return ctrl.Result{}, err
	}
	log.Log.V(2).Info("Listed nodes", "nodes", nodeList.Items)

	nodeMap := map[string]*v1.Node{}
	for _, node := range nodeList.Items {
		nodeMap[node.Name] = &node
	}

	templates := []*v1alpha1.NicConfigurationTemplate{}
	for _, template := range templateList.Items {
		templates = append(templates, &template)
	}

	for _, device := range deviceList.Items {
		node, ok := nodeMap[device.Status.Node]
		if !ok {
			log.Log.Info("device doesn't match any node, skipping", "device", device.Name)
			continue
		}

		var matchingTemplates []*v1alpha1.NicConfigurationTemplate

		for _, template := range templates {
			if !deviceMatchesSelectors(&device, template, node) {
				r.dropDeviceFromStatus(device.Name, template)

				continue
			}

			matchingTemplates = append(matchingTemplates, template)
		}

		if len(matchingTemplates) == 0 {
			log.Log.V(2).Info("Device doesn't match any configuration template, resetting the spec", "device", device.Name)
			device.Spec.Configuration = nil
			err = r.Update(ctx, &device)
			if err != nil {
				log.Log.Error(err, "Failed to update device's spec", "device", device)
				return ctrl.Result{}, err
			}
			continue
		}

		if len(matchingTemplates) > 1 {
			for _, template := range matchingTemplates {
				r.dropDeviceFromStatus(device.Name, template)
			}

			templateNames := []string{}
			for _, template := range matchingTemplates {
				templateNames = append(templateNames, template.Name)
			}
			joinedTemplateNames := strings.Join(templateNames, ",")
			err = fmt.Errorf("device matches several configuration templates: %s, %s", device.Name, joinedTemplateNames)
			log.Log.Error(err, "Device matches several configuration templates, deleting its spec and emitting an error event", "device", device.Name)
			err = r.handleErrorSeveralMatchingTemplates(ctx, &device, joinedTemplateNames)
			if err != nil {
				log.Log.Error(err, "Failed to emit warning about multiple templates matching one device", "templates", joinedTemplateNames)
				return ctrl.Result{}, err
			}
		}

		matchingTemplate := matchingTemplates[0]

		if !slices.Contains(matchingTemplate.Status.NicDevices, device.Name) {
			matchingTemplate.Status.NicDevices = append(matchingTemplate.Status.NicDevices, device.Name)
		}

		err = r.applyTemplateToDevice(ctx, &device, matchingTemplate)
		if err != nil {
			log.Log.Error(err, "failed to apply template to device", "template", matchingTemplate.Name, "device", device.Name)
			return ctrl.Result{}, err
		}
	}

	// Try to update template's status with added / deleted devices
	for _, template := range templates {
		err = r.Status().Update(ctx, template)
		if err != nil {
			log.Log.Error(err, "failed to update template status", "template", template.Name)
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *NicConfigurationTemplateReconciler) dropDeviceFromStatus(deviceName string, template *v1alpha1.NicConfigurationTemplate) {
	index := slices.Index(template.Status.NicDevices, deviceName)
	if index != -1 {
		// Device no longer matches template, drop it from the template's status
		template.Status.NicDevices = slices.Delete(template.Status.NicDevices, index, index+1)
	}
}

func (r *NicConfigurationTemplateReconciler) applyTemplateToDevice(ctx context.Context, device *v1alpha1.NicDevice, template *v1alpha1.NicConfigurationTemplate) error {
	log.Log.V(2).Info(fmt.Sprintf("Applying template %s to device %s", template.Name, device.Name))

	updateSpec := false
	if device.Spec.Configuration == nil {
		updateSpec = true
		device.Spec.Configuration = &v1alpha1.NicDeviceConfigurationSpec{}
	}

	if device.Spec.Configuration.ResetToDefault != template.Spec.ResetToDefault {
		updateSpec = true
		device.Spec.Configuration.ResetToDefault = template.Spec.ResetToDefault
	}

	if !reflect.DeepEqual(device.Spec.Configuration.Template, template.Spec.Template) {
		updateSpec = true
		device.Spec.Configuration.Template = template.Spec.Template.DeepCopy()
	}

	if updateSpec {
		err := r.Update(ctx, device)
		if err != nil {
			log.Log.Error(err, "Failed to update NicDevice spec", "device", device.Name)
			return err
		}
	}

	return nil
}

func (r *NicConfigurationTemplateReconciler) handleErrorSeveralMatchingTemplates(ctx context.Context, device *v1alpha1.NicDevice, matchingTemplates string) error {
	r.EventRecorder.Event(device, v1.EventTypeWarning, "SpecError", fmt.Sprintf("Several templates matching this device: %s", matchingTemplates))
	device.Spec.Configuration = nil
	return r.Update(ctx, device)
}

func nodeMatchesTemplate(node *v1.Node, template *v1alpha1.NicConfigurationTemplate) bool {
	for k, v := range template.Spec.NodeSelector {
		if nv, ok := node.Labels[k]; ok && nv == v {
			continue
		}
		return false
	}
	return true
}

func deviceMatchesPCISelector(device *v1alpha1.NicDevice, template *v1alpha1.NicConfigurationTemplate) bool {
	if len(template.Spec.NicSelector.PciAddresses) > 0 {
		matchesPCI := false
		for _, port := range device.Status.Ports {
			if slices.Contains(template.Spec.NicSelector.PciAddresses, port.PCI) {
				matchesPCI = true
			}
		}
		if !matchesPCI {
			return false
		}
	}

	return true
}

func deviceMatchesSerialNumberSelector(device *v1alpha1.NicDevice, template *v1alpha1.NicConfigurationTemplate) bool {
	if len(template.Spec.NicSelector.SerialNumbers) > 0 {
		if !slices.Contains(template.Spec.NicSelector.SerialNumbers, device.Status.SerialNumber) {
			return false
		}
	}

	return true
}

func deviceMatchesSelectors(device *v1alpha1.NicDevice, template *v1alpha1.NicConfigurationTemplate, node *v1.Node) bool {
	if !nodeMatchesTemplate(node, template) {
		return false
	}

	if template.Spec.NicSelector.NicType != device.Status.Type {
		return false
	}

	if !deviceMatchesPCISelector(device, template) {
		return false
	}

	if !deviceMatchesSerialNumberSelector(device, template) {
		return false
	}

	return true
}

// SetupWithManager sets up the controller with the Manager.
func (r *NicConfigurationTemplateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.EventRecorder = mgr.GetEventRecorderFor("NicConfigurationTemplateReconciler")

	qHandler := func(q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
		q.Add(reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: "",
			Name:      nicConfigurationTemplateSyncEventName,
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

	nicDeviceEventHandler := handler.Funcs{
		UpdateFunc: func(ctx context.Context, e event.UpdateEvent, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			log.Log.Info("Enqueuing sync for update event", "resource", e.ObjectNew.GetName())
			qHandler(q)
		},
	}

	return ctrl.NewControllerManagedBy(mgr).
		Watches(&v1alpha1.NicConfigurationTemplate{}, eventHandler).
		Watches(&v1alpha1.NicDevice{}, nicDeviceEventHandler).
		Named("nicConfigurationTemplateReconciler").
		Complete(r)
}
