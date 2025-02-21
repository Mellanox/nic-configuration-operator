# NVIDIA NIC Configuration Operator Design Reference

Full API definition can be found in [api-reference](api-reference.md).

NIC Configuration Operator provides the API in form of Kubernetes CRDs to allow the users to configure and manage FW of NVIDIA NICs in their clusters.

The NicConfigurationTemplate, NicFirmwareSource and NicFirmwareTemplate CRs are created by the user and declare the desired state for the NICs matching the selectors.

The NicDevice CRs are created by the operator itself. They represent the actual NICs installed on the nodes in the cluster.

The operator deployment consists of two entities: the operator pod and the configuration daemons. The operator pod is running on the control plane and is responsible for reconciling the user-created CRs mentioned above. The configuration daemons are deployed as a daemon set to the worker nodes (configurable with the node selector) and are responsible for creating and reconciling the NicDevice CRs as well as performing the actual FW configuration on the NICs.

### NicDevice CR

The NicDevice CRs are created by the operator itself. They represent the actual NICs installed on the nodes in the cluster.
When the configuration daemon pod has started, it spins up the [Device Discovery Controller](/internal/controller/devicediscovery_controller.go) that [looks up the NVIDIA NICs](/pkg/host/host.go) on the node where it runs, saving information such as NIC type, FW version, link type, ports with their PCI addresses and net devices, etc. It will then fetch the NicDevice CRs, already existing in the cluster for this node, compare them to the actual state and update/delete/create CRs as needed. Device Discovery runs periodically with a 5-minute interval.
The Device Discovery only populates the metadata portion of the CR in the Status section, reflecting the actual state. The actual NIC configuration is handled by the [NicDevice controller](/internal/controller/nicdevice_controller.go) which is designed more like a usual K8s controller. However, there are some notes on its configuration. First, since configuration daemons on each node each spawn their own respective instance of the NicDevice Controller, they are configured to only process the events from the NicDevice CRs on their own nodes. Second, upon receipt of the reconcile event, all the NicDevice CRs from this node are processed in a single batch. The controller performs the necessary checks and commands step by step, updating the NicDevice's status conditions along the way. The diagram for this controller's reconcile function can be found [here](nic-configuration-reconcile-diagram.png).

The section on the NIC FW upgrade is to be added with the implementation.

The NicDevice's spec is handled and updated by the NicConfigurationTemplate and NicFirmwareTemplate controllers in the operator pod.

### NicConfigurationTemplate and NicFirmwareTemplate controllers

[NicConfigurationTemplate Controller](/internal/controller/nicconfigurationtemplate_controller.go)

[NicFirmwareTemplate Controller](/internal/controller/nicfirmwaretemplate_controller.go) (update when implemented)

These two controllers are very similar in the way the function. They both reconcile their respective CRs and match the templates to the NicDevice CRs, based on the selectors provided in the CRs.

[These selectors](/api/v1alpha1/nicconfigurationtemplate_types.go) include node label, NIC type, serial numbers and PCI addresses. The NIC type selector is the only mandatory one and accepts a single value, as FW configurations for different NIC types should be provided separately.

Each controller processes their CRs in a single batch, listing all template CRs and all NicDevice CRs and then matching templates to devices. Each individual NicDevice can only be matched with a single NicConfigurationTemplate and a single NicFirmwareTemplate respectively. If more than one matching template of any kind exist, they will not be applied and the error event will be emitted. 

When a match is found, the controller updates the spec section of the NicDevice, copying the template spec there. From this point, the configuration of the device is handled by the configuration daemon. The template's status will include the list of devices to which this template was applied.

### NicFirmwareSource controller (to be implemented)

The [NicFirmwareSource CR](/api/v1alpha1/nicfirmwaresource_types.go) represents a list of url sources with NIC FW binaries archives.

Firmware binaries are provisioned by a [provisioner controller](/internal/controller/nicfirmwaresource_controller.go) which watches the NicFirmwareSource objects and provisions the binaries in a shared PV volume.

Configuration daemons need to make sure that the reference NicFirmwareSource object is fully reconciled (status.state == Success) before proceeding with firmware update.

Deletion (user triggered) of the NicFirmwareSource obj should be blocked until it is no longer referenced by any NicFirmwareTemplate obj.

The PV will have a pre-defined layout to ease its consumption by configuration daemons:

```
pv-root 
  connectx-6dx-firmware-22-41-1000
    firmware-binaries
      22.41.1000
        MT_0000000771
          fw-ConnectX6Dx-rel-22_41_1000-MCX623436MN-CDA_Ax-UEFI-14.34.12-FlexBoot-3.7.400.bin
        MT_0000000773
          fw-ConnectX6Dx-rel-22_41_1000-MCX623436MS-CDA_Ax-UEFI-14.34.12-FlexBoot-3.7.400.signed.bin
        MT_0000000652
          fw-ConnectX6Dx-rel-22_41_1000-MCX623439MC-CDA_Ax-UEFI-14.34.12-FlexBoot-3.7.400.signed.bin
    metadata.json
```

The PV is mounted to the operator pod via the PVC claim and the user-provided storage class.

The controller uses the [FirmwareProvisioner interface](pkg/firmware/provisioning.go) to download, verify and store the FW binaries.

Each NicFirmwareSource object is reconciled separately.

The reconciliation pipeline follows these steps:

```go 
type FirmwareProvisioner interface {
    // IsFWStorageAvailable checks if the cache storage exists in the pod.
    IsFWStorageAvailable() error
    // VerifyCachedBinaries checks against the metadata.json for which urls have corresponding cached fw binary files
    // Returns a list of urls that need to be processed again
    VerifyCachedBinaries(cacheName string, urls []string) ([]string, error)
    // DownloadAndUnzipFirmwareArchives downloads and unzips fw archives from a list of urls
    // Stores a metadata file, mapping download url to file names
    // Returns binaries' filenames
    DownloadAndUnzipFirmwareArchives(cacheName string, urls []string, cleanupArchives bool) error
    // AddFirmwareBinariesToCacheByMetadata finds the newly downloaded firmware binary files and organizes them in the cache according to their metadata
    AddFirmwareBinariesToCacheByMetadata(cacheName string) error
    // ValidateCache traverses the cache directory and validates that
    // 1. There are no empty directories in the cache
    // 2. Each PSID has only one matching firmware binary in the cache
    // 3. Each non-empty PSID directory contains a firmware binary file (.bin)
    // Returns mapping between firmware version to PSIDs available in the cache, error if validation failed
    ValidateCache(cacheName string) (map[string][]string, error)
}
```

After the successful reconciliation, the NicFirmwareSource CR is updated with the `Success` status, allowing the config daemons to perform the FW upgrade on devices.

## Interaction with host

Configuration daemons perform operation and execute commands on the host node to configure the devices. Tools from the [MLNX_OFED repository](https://linux.mellanox.com/public/repo/mlnx_ofed/) are installed inside the [docker image](/Dockerfile.nic-configuration-daemon).

Specific commands and their usage can be found [here](/pkg/host/utils.go).

## Maintenance operator

NVIDIA NIC Configuration operator uses the [Maintenance operator](https://github.com/Mellanox/maintenance-operator) to prepare a node for maintenance before the actual configuration. If the Maintenance operator is not deployed, the NIC Configuration operator will not proceed with the NIC configurations.

The integration is done via the [MaintenanceManager interface](/pkg/maintenance/maintenancemanager.go) and has the following functionality:

```go
type MaintenanceManager interface {
	ScheduleMaintenance(ctx context.Context) error
	MaintenanceAllowed(ctx context.Context) (bool, error)
	ReleaseMaintenance(ctx context.Context) error
	Reboot() error
}
```

The maintenance manager uses the [NodeMaintenance CRs](https://github.com/Mellanox/maintenance-operator?tab=readme-ov-file#nodemaintenance) to schedule and release maintenance.