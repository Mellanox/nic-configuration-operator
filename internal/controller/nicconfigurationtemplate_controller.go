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

type nicConfigurationTemplate struct {
	template v1alpha1.NicConfigurationTemplate
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

	templates := []nicTemplate{}
	for _, template := range templateList.Items {
		templates = append(templates, &nicConfigurationTemplate{template})
	}

	err = matchDevicesToTemplates(ctx, r.Client, r.EventRecorder, templates, func(device *v1alpha1.NicDevice) {
		device.Spec.Configuration = nil
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	// Try to update template's status with added / deleted devices
	for _, template := range templates {
		err = template.updateStatus(ctx, r.Client)
		if err != nil {
			log.Log.Error(err, "failed to update template status", "template", template.getName())
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (t *nicConfigurationTemplate) getName() string {
	return t.template.Name
}

func (t *nicConfigurationTemplate) getNodeSelector() map[string]string {
	return t.template.Spec.NodeSelector
}

func (t *nicConfigurationTemplate) getNicSelector() *v1alpha1.NicSelectorSpec {
	return t.template.Spec.NicSelector
}

func (t *nicConfigurationTemplate) getStatus() *v1alpha1.NicTemplateStatus {
	return &t.template.Status
}

func (t *nicConfigurationTemplate) applyToDevice(device *v1alpha1.NicDevice) bool {
	log.Log.V(2).Info(fmt.Sprintf("Applying template %s to device %s", t.template.Name, device.Name))

	updateSpec := false
	if device.Spec.Configuration == nil {
		updateSpec = true
		device.Spec.Configuration = &v1alpha1.NicDeviceConfigurationSpec{}
	}

	if device.Spec.Configuration.ResetToDefault != t.template.Spec.ResetToDefault {
		updateSpec = true
		device.Spec.Configuration.ResetToDefault = t.template.Spec.ResetToDefault
	}

	if !reflect.DeepEqual(device.Spec.Configuration.Template, t.template.Spec.Template) {
		updateSpec = true
		device.Spec.Configuration.Template = t.template.Spec.Template.DeepCopy()
	}

	return updateSpec
}

func (t *nicConfigurationTemplate) updateStatus(ctx context.Context, client client.Client) error {
	return client.Status().Update(ctx, &t.template)
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
