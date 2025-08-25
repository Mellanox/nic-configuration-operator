// Copyright 2025 NVIDIA CORPORATION & AFFILIATES
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"maps"
	"os"
	"slices"

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
	"github.com/Mellanox/nic-configuration-operator/pkg/dms"
	"github.com/Mellanox/nic-configuration-operator/pkg/firmware"
	"github.com/Mellanox/nic-configuration-operator/pkg/helper"
	"github.com/Mellanox/nic-configuration-operator/pkg/host"
	"github.com/Mellanox/nic-configuration-operator/pkg/maintenance"
	"github.com/Mellanox/nic-configuration-operator/pkg/ncolog"
	"github.com/Mellanox/nic-configuration-operator/pkg/nvconfig"
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
	nvConfigUtils := nvconfig.NewNVConfigUtils()

	deviceDiscovery := devicediscovery.NewDeviceDiscovery(nodeName, nvConfigUtils)

	// Initialize DMS manager
	dmsManager := dms.NewDMSManager()

	// Start DMS instances for all discovered devices
	devices, err := deviceDiscovery.DiscoverNicDevices()
	if err != nil {
		log.Log.Error(err, "failed to discover NIC devices")
		os.Exit(1)
	}

	if err := dmsManager.StartDMSInstances(slices.Collect(maps.Values(devices))); err != nil {
		log.Log.Error(err, "failed to start DMS instances")
		os.Exit(1)
	}

	// Ensure DMS instances are stopped when the program exits
	defer func() {
		if err := dmsManager.StopAllDMSInstances(); err != nil {
			log.Log.Error(err, "failed to stop DMS instances")
		}
	}()

	configurationManager := configuration.NewConfigurationManager(eventRecorder, dmsManager, nvConfigUtils)
	maintenanceManager := maintenance.New(mgr.GetClient(), hostUtils, nodeName, namespace)
	firmwareManager := firmware.NewFirmwareManager(mgr.GetClient(), dmsManager, namespace)

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
