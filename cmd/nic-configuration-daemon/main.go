package main

import (
	"flag"
	"os"

	maintenanceoperator "github.com/Mellanox/maintenance-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/internal/controller"
	"github.com/Mellanox/nic-configuration-operator/pkg/host"
	"github.com/Mellanox/nic-configuration-operator/pkg/maintenance"
	"github.com/Mellanox/nic-configuration-operator/pkg/ncolog"
)

var (
	scheme = runtime.NewScheme()
)

func main() {
	ncolog.BindFlags(flag.CommandLine)
	flag.Parse()
	ncolog.InitLog()

	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(maintenanceoperator.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	if err != nil {
		log.Log.Error(err, "unable to create manager")
		os.Exit(1)
	}

	// Get the pod name and namespace from the environment variables
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Log.Error(err, "NODE_NAME env var required but not set")
		os.Exit(1)
	}

	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		log.Log.Error(err, "NAMESPACE env var required but not set")
		os.Exit(1)
	}

	hostUtils := host.NewHostUtils()
	hostManager := host.NewHostManager(nodeName, hostUtils)
	maintenanceManager := maintenance.New(mgr.GetClient(), hostUtils, nodeName, namespace)

	deviceDiscovery := controller.NewDeviceRegistry(mgr.GetClient(), hostManager, nodeName, namespace)
	if err = mgr.Add(deviceDiscovery); err != nil {
		log.Log.Error(err, "unable to add device discovery runnable")
		os.Exit(1)
	}

	nicDeviceReconciler := controller.NicDeviceReconciler{
		Client:             mgr.GetClient(),
		Scheme:             mgr.GetScheme(),
		NodeName:           nodeName,
		NamespaceName:      namespace,
		HostManager:        hostManager,
		MaintenanceManager: maintenanceManager,
	}
	err = nicDeviceReconciler.SetupWithManager(mgr, true)
	if err != nil {
		log.Log.Error(err, "unable to create controller", "controller", "NicDeviceReconciler")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()

	err = mgr.GetCache().IndexField(ctx, &v1alpha1.NicDevice{}, "status.node", func(o client.Object) []string {
		return []string{o.(*v1alpha1.NicDevice).Status.Node}
	})
	if err != nil {
		log.Log.Error(err, "failed to index field for cache")
		os.Exit(1)
	}

	if err := mgr.Start(ctx); err != nil {
		log.Log.Error(err, "problem running manager")
		os.Exit(1)
	}
}
