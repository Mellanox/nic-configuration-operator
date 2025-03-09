# nic-configuration-operator-chart

![Version: 0.0.1](https://img.shields.io/badge/Version-0.0.1-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: latest](https://img.shields.io/badge/AppVersion-latest-informational?style=flat-square)

A Helm chart for NIC Configuration Operator

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| configDaemon.image.name | string | `"nic-configuration-operator-daemon"` |  |
| configDaemon.image.repository | string | `"ghcr.io/mellanox"` | repository to use for the config daemon image |
| configDaemon.image.tag | string | `"latest"` | image tag to use for the config daemon image |
| configDaemon.nodeSelector | object | `{}` | node selector for the config daemon |
| configDaemon.resources | object | `{"limits":{"cpu":"500m","memory":"128Mi"},"requests":{"cpu":"10m","memory":"64Mi"}}` | resources and limits for the config daemon |
| imagePullSecrets | list | `[]` | image pull secrets for both the operator and the config daemon |
| logLevel | string | `"info"` | log level configuration (debug|info) |
| nicFirmwareStorage | object | `{"availableStorageSize":"1Gi","create":true,"pvcName":"nic-fw-storage-pvc","storageClassName":""}` | settings to enable the NIC Firmware Storage |
| nicFirmwareStorage.availableStorageSize | string | `"1Gi"` | storage size for the NIC Configuration Operator to request. 1Gi is the default value when not provided |
| nicFirmwareStorage.create | bool | `true` | create a new pvc or use an existing one |
| nicFirmwareStorage.pvcName | string | `"nic-fw-storage-pvc"` | name of the PVC to mount as NIC Firmware storage. If not provided, the NIC FW upgrade feature will be disabled. |
| nicFirmwareStorage.storageClassName | string | `""` | storage class name to be used to store NIC FW binaries during NIC FW upgrade. If not provided, the cluster-default storage class will be used |
| operator.affinity | object | `{"nodeAffinity":{"preferredDuringSchedulingIgnoredDuringExecution":[{"preference":{"matchExpressions":[{"key":"node-role.kubernetes.io/master","operator":"Exists"}]},"weight":1},{"preference":{"matchExpressions":[{"key":"node-role.kubernetes.io/control-plane","operator":"Exists"}]},"weight":1}]}}` | node affinity for the operator |
| operator.image.name | string | `"nic-configuration-operator"` |  |
| operator.image.repository | string | `"ghcr.io/mellanox"` | repository to use for the operator image |
| operator.image.tag | string | `"latest"` | image tag to use for the operator image |
| operator.nodeSelector | object | `{}` | node selector for the operator |
| operator.replicas | int | `1` | operator deployment number of replicas |
| operator.resources | object | `{"limits":{"cpu":"500m","memory":"128Mi"},"requests":{"cpu":"10m","memory":"64Mi"}}` | specify resource requests and limits for the operator |
| operator.serviceAccount.annotations | object | `{}` | set annotations for the operator service account |
| operator.tolerations | list | `[{"effect":"NoSchedule","key":"node-role.kubernetes.io/master","operator":"Exists"},{"effect":"NoSchedule","key":"node-role.kubernetes.io/control-plane","operator":"Exists"}]` | tolerations for the operator |

