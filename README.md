[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](http://www.apache.org/licenses/LICENSE-2.0)
[![Go Report Card](https://goreportcard.com/badge/github.com/Mellanox/nic-configuration-operator)](https://goreportcard.com/report/github.com/Mellanox/nic-configuration-operator)
[![Coverage Status](https://coveralls.io/repos/github/Mellanox/nic-configuration-operator/badge.svg)](https://coveralls.io/github/Mellanox/nic-configuration-operator)
[![Build, Test, Lint](https://github.com/Mellanox/nic-configuration-operator/actions/workflows/build-test-lint.yml/badge.svg?event=push)](https://github.com/Mellanox/nic-configuration-operator/actions/workflows/build-test-lint.yml)
[![CodeQL](https://github.com/Mellanox/nic-configuration-operator/actions/workflows/codeql.yml/badge.svg)](https://github.com/Mellanox/nic-configuration-operator/actions/workflows/codeql.yml)
[![Image push](https://github.com/Mellanox/nic-configuration-operator/actions/workflows/image-push-main.yml/badge.svg?event=push)](https://github.com/Mellanox/nic-configuration-operator/actions/workflows/image-push-main.yml)

# NVIDIA NIC Configuration Operator

NVIDIA NIC Configuration Operator provides Kubernetes API(Custom Resource Definition) to allow FW configuration on Nvidia NICs
in a coordinated manner. It deploys a configuration daemon on each of the desired nodes to configure Nvidia NICs there. 
NVIDIA NIC Configuration Operator uses the [Maintenance Operator](https://github.com/Mellanox/maintenance-operator) to prepare a node for maintenance before the actual configuration.

## Deployment

### Prerequisites

* Kubernetes cluster
* [NVIDIA Network Operator](https://github.com/Mellanox/network-operator) deployed. It is recommended to deploy the [DOCA-OFED driver](https://github.com/Mellanox/network-operator?tab=readme-ov-file#driver-containers)
* [Maintenance Operator](https://github.com/Mellanox/maintenance-operator) deployed

NVIDIA NIC Configuration Operator can be deployed as part of the [NIC Cluster Policy CRD](https://github.com/Mellanox/network-operator?tab=readme-ov-file#nicclusterpolicy-spec).

## CRDs

### NICConfigurationTemplate

The NICConfigurationTemplate CRD is used to request FW configuration for a subset of devices.

NIC Configuration Operator will select NIC devices in the cluster that match the template's selectors and apply the configuration spec to them.

If more than one template matches a single device, none will be applied and the error will be reported in all of their statuses.

For more information refer to [api-reference](docs/api-reference.md).

#### [Example NICConfigurationTemplate](docs/examples/example-nicconfigurationtemplate-connectx6dx.yaml):

```yaml
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicConfigurationTemplate
metadata:
   name: connectx6-config
   namespace: network-operator
spec:
   nodeSelector:
      feature.node.kubernetes.io/network-sriov.capable: "true"
   nicSelector:
      # nicType selector is mandatory the rest are optional. Only a single type can be specified.
      nicType: 101b
      pciAddresses:
         - "0000:03:00.0"
         - “0000:04:00.0”
      serialNumbers:
         - "MT2116X09299"
   resetToDefault: false # if set, template is ignored, device configuration should reset
   template:
      numVfs: 2
      linkType: Ethernet
      pciPerformanceOptimized:
         enabled: true
         maxAccOutRead: 44
         maxReadRequest: 4096
      roceOptimized:
         enabled: true
         qos:
            trust: dscp
            pfc: "0,0,0,1,0,0,0,0"
            tos: 0
      gpuDirectOptimized:
         enabled: true
         env: Baremetal
```

#### Configuration details

* `numVFs`: if provided, configure SR-IOV VFs via nvconfig.
  * This is a mandatory parameter.
  * E.g: if `numVFs=2` then `SRIOV_EN=1` and `SRIOV_NUM_OF_VFS=2`.
  * If `numVFs=0` then `SRIOV_EN=0` and `SRIOV_NUM_OF_VFS=0`.
* `linkType`: if provided configure `linkType` for the NIC for all NIC ports.
  * This is a mandatory parameter.
  * E.g `linkType = Infiniband` then set `LINK_TYPE_P1=IB` and `LINK_TYPE_P2=IB` if second PCI function is present
* `pciPerformanceOptimized`: performs PCI performance optimizations. If enabled then by default the following will happen:
  * Set nvconfig `MAX_ACC_OUT_READ` nvconfig parameter to `0` (use device defaults)
  * Set PCI max read request size for each PF to `4096` (note: this is a runtime config and is not persistent)
  * Users can override values via `maxAccOutRead` and `maxReadRequest`
  * **IMPORTANT** :
    * According to the PRM, setting `MAX_ACC_OUT_READ` to zero enables the auto mode, which applies the best suitable optimizations. However, there is a bug in certain FW versions, where the zero value is not available.
    * In this case, until the fix is available, `MAX_ACC_OUT_READ` will not be set and a warning event will be emitted for this device's CR.
* `roceOptimized`: performs RoCE related optimizations. If enabled performs the following by default:
  * Nvconfig set for both ports (can be applied from PF0)
    * Conditionally applied for second port if present
      * `ROCE_CC_PRIO_MASK_P1=255`, `ROCE_CC_PRIO_MASK_P2=255`
      * `CNP_DSCP_P1=4`, `CNP_DSCP_P2=4`
      * `CNP_802P_PRIO_P1=6`, `CNP_802P_PRIO_P2=6`
  * Configure pfc (Priority Flow Control) for priority 3, set trust to dscp on each PF, set ToS (Type of Service) to 0.
    * Non-persistent (need to be applied after each boot)
    * Users can override values via `trust`, `pfc` and `tos` parameters
  * Can only be enabled with `linkType=Ethernet`
* `gpuDirectOptimized`: performs gpu direct optimizations. ATM only optimizations for Baremetal environment are supported. If enabled perform the following:
  * Set nvconfig `ATS_ENABLED=0`
  * Can only be enabled when `pciPerformanceOptimized` is enabled
  * Both the numeric values and their string aliases, supported by NVConfig, are allowed (e.g. `REAL_TIME_CLOCK_ENABLE=False`, `REAL_TIME_CLOCK_ENABLE=0`).
  * For per port parameters (suffix `_P1`, `_P2`) parameters with `_P2` suffix are ignored if the device is single port.
* If a configuration is not set in spec, its non-volatile configuration parameters (if any) should be set to device default.

### NicFirmwareSource

The [NICFirmwareSource CR](/api/v1alpha1/nicfirmwaresource_types.go) represents a list of url sources with NIC FW binaries archives.

#### Provisioning a storage class for NIC FW upgrade

To enable the NIC FW upgrade feature, `NicFirmwareStorage` section should be provided in the [NIC Cluster Policy](https://github.com/Mellanox/network-operator/blob/7c31e789835afdad114b1a68011e5307bc773bcf/api/v1alpha1/nicclusterpolicy_types.go#L321). There is an option to create a new PVC or use the existing one.
Firmware binaries will be provisioned by a provisioner controller which will watch for NICFirmwareSource obj and provision the binaries in a shared volume enabled by the given storage class.
Node agents will need to make sure that the reference NICFirmwareSource object is fully reconciled (status.state == Success) before proceeding with firmware update.

#### Example of the storage class deployment

To set up a persistent NFS storage in the cluster, the [example from the CSI NFS Driver repository](https://github.com/kubernetes-csi/csi-driver-nfs/blob/master/deploy/example/nfs-provisioner/README.md) might be used.
After deploying the NFS server and NFS CSI driver, the [storage class](https://github.com/kubernetes-csi/csi-driver-nfs/blob/master/deploy/example/storageclass-nfs.yaml) should be deployed. The name of the storage class can then be passed to the NIC Cluster Policy.

#### [Example NICFirmwareSource](docs/examples/example-nicfwsource-connectx6dx.yaml):

```yaml
# Change this template first as it only provides an example of configuration
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicFirmwareSource
metadata:
  name: connectx-6dx-firmware-22-44-1036
  namespace: network-operator
  finalizers:
    - configuration.net.nvidia.com/nic-configuration-operator
spec:
  # a list of firmware binaries from mlnx website if they are zipped try to unzip before placing
  binUrlSources:
    - https://www.mellanox.com/downloads/firmware/fw-ConnectX6Dx-rel-22_44_1036-MCX623106AC-CDA_Ax-UEFI-14.37.14-FlexBoot-3.7.500.signed.bin.zip
  bfbUrlSource: https://example.com/bluefield3-31.41.0.bfb
status:
  state: Success
  binaryVersions:
    22.44.1036:
    - mt_0000000436
  bfbVersions:
    a2dc: 34.41.0 # BF3 NIC FW
    a2d6: 25.21.0 # BF2 NIC FW
```

### NICFirmwareTemplate

The NICFirmwareTemplate CRD is used to request FW validation or update from a referenced NICFirmwareSource for a subset of devices.

NIC Configuration Operator will select NIC devices in the cluster that match the template's selectors and apply the configuration spec to them.

If more than one template matches a single device, none will be applied and the error will be reported in all of their statuses.

For more information refer to [api-reference](docs/api-reference.md).

#### [Example NICFirmwareTemplate](docs/examples/example-nicconfigurationtemplate-connectx6dx.yaml):

```yaml
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicFirmwareTemplate
metadata:
  name: connectx6dx-config
  namespace: network-operator
spec:
  nodeSelector:
    kubernetes.io/hostname: cloud-dev-41
  nicSelector:
    nicType: "101d"
  template:
    nicFirmwareSourceRef: connectx6dx-firmware-22-44-1036
    updatePolicy: Update
```

### NICDevice

The NICDevice CRD is created automatically by the configuration daemon and represents a specific NVIDIA NIC on a specific K8s node.
The name of the device combines the node name, device type and its serial number for easier tracking.

`FirmwareUpdateInProgress` status condition can be used for tracking the state of the FW validation/update on a specific device. If an error occurs during FW update, it will be reflected in this field.
`ConfigUpdateInProgress` status condition can be used for tracking the state of the FW configuration update on a specific device. If an error occurs during FW configuration update, it will be reflected in this field.

for more information refer to [api-reference](docs/api-reference.md).

#### Example NICDevice

```yaml
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicDevice
metadata:
   name: co-node-25-101b-mt2232t13210
   namespace: nic-configuration-operator
spec:
   firmware:
     nicFirmwareSourceRef: connectx6dx-firmware-22-44-1036
     updatePolicy: Update
   configuration:
      template:
         linkType: Ethernet
         numVfs: 8
         pciPerformanceOptimized:
            enabled: true
status:
   conditions:
      - reason: DeviceFirmwareConfigMatch
        type: FirmwareUpdateInProgress
        status: "False"
        message: Firmware matches the requested version
      - reason: UpdateSuccessful
        type: ConfigUpdateInProgress
        status: "False"
   firmwareVersion: 20.42.1000
   node: co-node-25
   partNumber: mcx632312a-hdat
   ports:
      - networkInterface: enp4s0f0np0
        pci: "0000:04:00.0"
        rdmaInterface: mlx5_0
      - networkInterface: enp4s0f1np1
        pci: "0000:04:00.1"
        rdmaInterface: mlx5_1
   psid: mt_0000000225
   serialNumber: mt2232t13210
   type: 101b
```

#### Implementation details:

The NicDevice CRD is created and reconciled by the configuration daemon. The reconciliation logic scheme can be found [here](docs/nic-configuration-reconcile-diagram.png).

## Order of operations

To include the NIC Configuration Operator as part of network configuration workflows, strict order of operations might need to be enforced. For example, [SR-IOV Network Configuration Daemon](https://github.com/k8snetworkplumbingwg/sriov-network-operator) pod should start AFTER the NIC Configuration Daemon has finished.
To indicate when NIC configuration is in progress to the pods that depend on it, the operator manages the `nvidia.com/operator.nic-configuration.wait` label, which has the value `false` when the requested NIC configuration has successfuly been applied, and the value `true` when the NIC configuration is in progress.
To use this mechanism, the next pods in the pipeline can add `nvidia.com/operator.nic-configuration.wait=false` to their node label selectors. That way, they will automatically be evicted from the node when the NICs are being configured.

The NIC Configuration Daemon itself relies on the `network.nvidia.com/operator.mofed.wait=false` label to be present on the node as it requires the DOCA-OFED driver to be running for some of the configurations.

## Feature flags
Feature flags can be enabled via environment variables in the helm chart or NVIDIA Network Operator's NicClusterPolicy.
Supported flags:
* `FW_RESET_AFTER_CONFIG_UPDATE`=`true`: explicitely reset the NIC's Firmware before the reboot and after updating its non-volatile configuration. Might be required on DGX servers where configuration update is not successfully applied after the warm reboot.