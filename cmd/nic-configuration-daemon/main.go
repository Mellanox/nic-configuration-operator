package main

import (
	"flag"
	"os"

	maintenanceoperator "github.com/Mellanox/maintenance-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/Mellanox/nic-configuration-operator/api/v1alpha1"
	"github.com/Mellanox/nic-configuration-operator/internal/controller"
	"github.com/Mellanox/nic-configuration-operator/pkg/configuration"
	"github.com/Mellanox/nic-configuration-operator/pkg/devicediscovery"
	"github.com/Mellanox/nic-configuration-operator/pkg/firmware"
	"github.com/Mellanox/nic-configuration-operator/pkg/helper"
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
		// Setting bind address to 0 disables the health probe / metrics server
		HealthProbeBindAddress: "0",
		Metrics:                metricsserver.Options{BindAddress: "0"},
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

	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel != "" {
		err = ncolog.SetLogLevel(logLevel)
		if err != nil {
			log.Log.Error(err, "failed to set log level")
			os.Exit(1)
		}
	}

	eventRecorder := mgr.GetEventRecorderFor("NicDeviceReconciler")

	hostUtils := host.NewHostUtils()
	deviceDiscovery := devicediscovery.NewDeviceDiscovery(nodeName)

	configurationManager := configuration.NewConfigurationManager(eventRecorder)
	maintenanceManager := maintenance.New(mgr.GetClient(), hostUtils, nodeName, namespace)
	firmwareManager := firmware.NewFirmwareManager(mgr.GetClient(), namespace)

	if err := initNicFwMap(namespace); err != nil {
		log.Log.Error(err, "unable to init NicFwMap")
		os.Exit(1)
	}

	deviceDiscoveryController := controller.NewDeviceDiscoveryController(
		mgr.GetClient(), deviceDiscovery, hostUtils, nodeName, namespace)
	if err = mgr.Add(deviceDiscoveryController); err != nil {
		log.Log.Error(err, "unable to add device discovery runnable")
		os.Exit(1)
	}

	nicDeviceReconciler := controller.NicDeviceReconciler{
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		NodeName:             nodeName,
		NamespaceName:        namespace,
		ConfigurationManager: configurationManager,
		MaintenanceManager:   maintenanceManager,
		FirmwareManager:      firmwareManager,
		EventRecorder:        eventRecorder,
		HostUtils:            hostUtils,
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

func initNicFwMap(namespace string) error {
	kubeclient := kubernetes.NewForConfigOrDie(ctrl.GetConfigOrDie())
	if err := helper.InitNicFwMapFromConfigMap(kubeclient, namespace); err != nil {
		return err
	}

	return nil
}
