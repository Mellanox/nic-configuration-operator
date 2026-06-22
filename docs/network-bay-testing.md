## NIC discovery correctly identifies ASIC number and peer PCI:
```
amaslennikov@FTVX02CDJH nic-configuration-operator-chart % kubectl get nicdevices vr-nvl72-mec02-c14-1025-0001-03-00 -o yaml
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicDevice
metadata:
  creationTimestamp: "2026-06-12T10:00:28Z"
  generation: 1
  name: vr-nvl72-mec02-c14-1025-0001-03-00
  namespace: default
  ownerReferences:
  - apiVersion: v1
    kind: Node
    name: vr-nvl72-mec02-c14
    uid: ecd6789f-8bb9-4423-bfd4-f9899745ab42
  resourceVersion: "1404"
  uid: 7226b201-7833-4a6f-810f-fca92e42b790
spec: {}
status:
  conditions:
  - lastTransitionTime: "2026-06-12T10:00:28Z"
    message: Device configuration spec is empty, cannot update configuration
    reason: DeviceConfigSpecEmpty
    status: "False"
    type: ConfigUpdateInProgress
  - lastTransitionTime: "2026-06-12T10:00:28Z"
    message: Device firmware spec is empty, cannot update firmware
    reason: DeviceFirmwareSpecEmpty
    status: "False"
    type: FirmwareUpdateInProgress
  - lastTransitionTime: "2026-06-12T10:00:28Z"
    message: Can't get OFED version to check recommended firmware version
    reason: DeviceFirmwareConfigMatch
    status: Unknown
    type: FirmwareConfigMatch
  dpu: false
  firmwareVersion: 82.48.1304
  modelName: NVIDIA Quad ConnectX-9 SuperNIC C9480V Board for Vera Rubin NVL 144 systems
  networkBay:
    asic: 1
    peerPci: "0003:01:00.0"
  node: vr-nvl72-mec02-c14
  partNumber: 900-9X9BE-00PB-TSA
  ports:
  - networkInterface: ibP1p3s0
    pci: "0001:03:00.0"
    rdmaInterface: mlx5_0
  psid: mt_0000001659
  serialNumber: MT2550601FH0
  superNIC: true
  type: "1025"
amaslennikov@FTVX02CDJH nic-configuration-operator-chart % kubectl get nicdevices vr-nvl72-mec02-c14-1025-0003-01-00 -o yaml
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicDevice
metadata:
  creationTimestamp: "2026-06-12T10:00:28Z"
  generation: 1
  name: vr-nvl72-mec02-c14-1025-0003-01-00
  namespace: default
  ownerReferences:
  - apiVersion: v1
    kind: Node
    name: vr-nvl72-mec02-c14
    uid: ecd6789f-8bb9-4423-bfd4-f9899745ab42
  resourceVersion: "1414"
  uid: 73d3e037-0534-45de-8a1a-a0bd0faccf47
spec: {}
status:
  conditions:
  - lastTransitionTime: "2026-06-12T10:00:28Z"
    message: Device configuration spec is empty, cannot update configuration
    reason: DeviceConfigSpecEmpty
    status: "False"
    type: ConfigUpdateInProgress
  - lastTransitionTime: "2026-06-12T10:00:28Z"
    message: Device firmware spec is empty, cannot update firmware
    reason: DeviceFirmwareSpecEmpty
    status: "False"
    type: FirmwareUpdateInProgress
  - lastTransitionTime: "2026-06-12T10:00:28Z"
    message: Can't get OFED version to check recommended firmware version
    reason: DeviceFirmwareConfigMatch
    status: Unknown
    type: FirmwareConfigMatch
  dpu: false
  firmwareVersion: 82.48.1304
  modelName: NVIDIA Quad ConnectX-9 SuperNIC C9480V Board for Vera Rubin NVL 144 systems
  networkBay:
    asic: 0
    peerPci: "0001:03:00.0"
  node: vr-nvl72-mec02-c14
  partNumber: 900-9X9BE-00PB-TSA
  ports:
  - networkInterface: ibP3s6
    pci: "0003:01:00.0"
    rdmaInterface: mlx5_1
  psid: mt_0000001659
  serialNumber: MT2550601FH0
  superNIC: true
  type: "1025"
```
## OpenAPI validation
### Rejects config templates if target device is not CX9
```
kubectl apply -f ../../docs/examples/example-nicconfigurationtemplate-network-bay.yaml
The NicConfigurationTemplate "network-bay-config" is invalid: spec: Invalid value: networkBay can only be configured for ConnectX-9 (NicType 1025)
```
### Rejects linkType set together with NetworkBay
```
kubectl apply -f ../../docs/examples/example-nicconfigurationtemplate-network-bay.yaml
The NicConfigurationTemplate "network-bay-config" is invalid: spec.template: Invalid value: linkType must not be set when networkBay is configured (the Network Bay link type is governed by the system configuration)
```
## NicConfigurationTemplate returns an error if doesn't match both ASICs of an orchid
### Only one orchid ASIC was matched
```
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicConfigurationTemplate
metadata:
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: |
      {"apiVersion":"configuration.net.nvidia.com/v1alpha1","kind":"NicConfigurationTemplate","metadata":{"annotations":{},"name":"network-bay-config","namespace":"default"},"spec":{"nicSelector":{"nicType":"1025","pciAddresses":["0001:03:00.0"]},"resetToDefault":false,"template":{"linkType":"Ethernet","networkBay":{"conf":"conf6"},"numVfs":1}}}
  creationTimestamp: "2026-06-12T10:16:38Z"
  generation: 1
  name: network-bay-config
  namespace: default
  resourceVersion: "2692"
  uid: b4b8ecb3-7fe0-450f-bc29-d906814e1418
spec:
  nicSelector:
    nicType: "1025"
    pciAddresses:
    - "0001:03:00.0"
  resetToDefault: false
  template:
    force: false
    linkType: Ethernet
    networkBay:
      conf: conf6
    numVfs: 1
status:
  conditions:
  - lastTransitionTime: "2026-06-12T10:16:38Z"
    message: 'node vr-nvl72-mec02-c14: matched an odd number of devices (1); a Network
      Bay template must match whole bays (pairs)'
    reason: NetworkBayImbalance
    status: "False"
    type: NetworkBayPairing
```
### One device is not part of an orchid
```
kubectl get nicconfigurationtemplates.configuration.net.nvidia.com network-bay-config -o yaml
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicConfigurationTemplate
metadata:
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: |
      {"apiVersion":"configuration.net.nvidia.com/v1alpha1","kind":"NicConfigurationTemplate","metadata":{"annotations":{},"name":"network-bay-config","namespace":"default"},"spec":{"nicSelector":{"nicType":"1025","pciAddresses":["0001:03:00.0","0005:41:00.1"]},"resetToDefault":false,"template":{"linkType":"Ethernet","networkBay":{"conf":"conf6"},"numVfs":1}}}
  creationTimestamp: "2026-06-12T10:16:38Z"
  generation: 2
  name: network-bay-config
  namespace: default
  resourceVersion: "2962"
  uid: b4b8ecb3-7fe0-450f-bc29-d906814e1418
spec:
  nicSelector:
    nicType: "1025"
    pciAddresses:
    - "0001:03:00.0"
    - "0005:41:00.1"
  resetToDefault: false
  template:
    force: false
    linkType: Ethernet
    networkBay:
      conf: conf6
    numVfs: 1
status:
  conditions:
  - lastTransitionTime: "2026-06-12T10:16:38Z"
    message: 'node vr-nvl72-mec02-c14: device vr-nvl72-mec02-c14-1025-0005-41-00 is
      not part of a Network Bay card but was matched by a Network Bay template'
    reason: NetworkBayImbalance
    status: "False"
    type: NetworkBayPairing
```
## Template applies when matching full orchids
```
kubectl get nicconfigurationtemplates.configuration.net.nvidia.com network-bay-config -o yaml
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicConfigurationTemplate
metadata:
  annotations:
    kubectl.kubernetes.io/last-applied-configuration: |
      {"apiVersion":"configuration.net.nvidia.com/v1alpha1","kind":"NicConfigurationTemplate","metadata":{"annotations":{},"name":"network-bay-config","namespace":"default"},"spec":{"nicSelector":{"nicType":"1025","pciAddresses":["0001:03:00.0","0003:01:00.0"]},"resetToDefault":false,"template":{"linkType":"Ethernet","networkBay":{"conf":"conf6"},"numVfs":1}}}
  creationTimestamp: "2026-06-12T10:16:38Z"
  generation: 4
  name: network-bay-config
  namespace: default
  resourceVersion: "3239"
  uid: b4b8ecb3-7fe0-450f-bc29-d906814e1418
spec:
  nicSelector:
    nicType: "1025"
    pciAddresses:
    - "0001:03:00.0"
    - "0003:01:00.0"
  resetToDefault: false
  template:
    force: false
    linkType: Ethernet
    networkBay:
      conf: conf6
    numVfs: 1
status:
  conditions:
  - lastTransitionTime: "2026-06-12T10:23:26Z"
    message: all matched Network Bay cards are configured as whole bays
    reason: NetworkBayBalanced
    status: "True"
    type: NetworkBayPairing
  nicDevices:
  - vr-nvl72-mec02-c14-1025-0001-03-00
  - vr-nvl72-mec02-c14-1025-0003-01-00
```
## Validates system conf is already applied
```
kubectl get nicdevices vr-nvl72-mec02-c14-1025-0001-03-00 -o yaml
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicDevice
metadata:
  annotations:
    lastAppliedState: '{"configuration":{"template":{"numVfs":0,"linkType":"Infiniband","networkBay":{"conf":"conf6"}}}}'
  creationTimestamp: "2026-06-12T10:00:28Z"
  generation: 4
  name: vr-nvl72-mec02-c14-1025-0001-03-00
  namespace: default
  ownerReferences:
  - apiVersion: v1
    kind: Node
    name: vr-nvl72-mec02-c14
    uid: ecd6789f-8bb9-4423-bfd4-f9899745ab42
  resourceVersion: "3829"
  uid: 7226b201-7833-4a6f-810f-fca92e42b790
spec:
  configuration:
    template:
      force: false
      linkType: Infiniband
      networkBay:
        conf: conf6
      numVfs: 0
status:
  conditions:
  - lastTransitionTime: "2026-06-12T10:29:26Z"
    message: ""
    observedGeneration: 4
    reason: UpdateSuccessful
    status: "False"
    type: ConfigUpdateInProgress
  - lastTransitionTime: "2026-06-12T10:00:28Z"
    message: Device firmware spec is empty, cannot update firmware
    reason: DeviceFirmwareSpecEmpty
    status: "False"
    type: FirmwareUpdateInProgress
  - lastTransitionTime: lastTransitionTime: "2026-06-12T10:00:28Z"
    message: Can't get OFED version to check recommended firmware version
    reason: DeviceFirmwareConfigMatch
    status: Unknown
    type: FirmwareConfigMatch
  - lastTransitionTime: "2026-06-12T10:23:26Z"
    message: device is part of a complete Network Bay
    observedGeneration: 2
    reason: NetworkBayBalanced
    status: "True"
    type: NetworkBayPairing
  dpu: false
  firmwareVersion: 82.48.1304
  modelName: NVIDIA Quad ConnectX-9 SuperNIC C9480V Board for Vera Rubin NVL 144 systems
  networkBay:
    asic: 1
    peerPci: "0003:01:00.0"
  node: vr-nvl72-mec02-c14
  partNumber: 900-9X9BE-00PB-TSA
  ports:
  - networkInterface: ibP1p3s0
    pci: "0001:03:00.0"
    rdmaInterface: mlx5_0
  psid: mt_0000001659
  serialNumber: MT2550601FH0
  superNIC: true
  type: "1025"
  ```
## Allows to add other params to config template
```
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicDevice
metadata:
  annotations:
    lastAppliedState: '{"configuration":{"template":{"numVfs":0,"networkBay":{"conf":"conf6"},"rawNvConfig":[{"name":"ROCE_CC_STEERING_EXT","value":"2"}]}}}'
  creationTimestamp: "2026-06-12T10:43:54Z"
  generation: 3
  name: vr-nvl72-mec02-c14-1025-0001-03-00
  namespace: default
  ownerReferences:
  - apiVersion: v1
    kind: Node
    name: vr-nvl72-mec02-c14
    uid: ecd6789f-8bb9-4423-bfd4-f9899745ab42
  resourceVersion: "6723"
  uid: d021d439-b688-4954-8b50-f23d112819d4
spec:
  configuration:
    template:
      force: false
      networkBay:
        conf: conf6
      numVfs: 0
      rawNvConfig:
      - name: ROCE_CC_STEERING_EXT
        value: "2"
status:
  conditions:
  - lastTransitionTime: "2026-06-12T11:09:19Z"
    message: ""
    observedGeneration: 3
    reason: UpdateSuccessful
    status: "False"
    type: ConfigUpdateInProgress
```

## Allows to change system_conf type

```
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicDevice
metadata:
  creationTimestamp: "2026-06-12T10:43:54Z"
  generation: 4
  name: vr-nvl72-mec02-c14-1025-0001-03-00
  namespace: default
  ownerReferences:
  - apiVersion: v1
    kind: Node
    name: vr-nvl72-mec02-c14
    uid: ecd6789f-8bb9-4423-bfd4-f9899745ab42
  resourceVersion: "8046"
  uid: d021d439-b688-4954-8b50-f23d112819d4
spec:
  configuration:
    template:
      force: false
      networkBay:
        conf: conf9
      numVfs: 0
      rawNvConfig:
      - name: ROCE_CC_STEERING_EXT
        value: "2"
status:
  conditions:
  - lastTransitionTime: "2026-06-12T11:21:00Z"
    message: ""
    observedGeneration: 4
    reason: PendingReboot
    status: "True"
    type: ConfigUpdateInProgress
  - lastTransitionTime: "2026-06-12T10:43:54Z"
    message: Device firmware spec is empty, cannot update firmware
    reason: DeviceFirmwareSpecEmpty
    status: "False"
    type: FirmwareUpdateInProgress
  - lastTransitionTime: "2026-06-12T10:43:54Z"
    message: Can't get OFED version to check recommended firmware version
    reason: DeviceFirmwareConfigMatch
    status: Unknown
    type: FirmwareConfigMatch
  dpu: false
  firmwareVersion: 82.48.1304
  modelName: NVIDIA Quad ConnectX-9 SuperNIC C9480V Board for Vera Rubin NVL 144 systems
  networkBay:
    asic: 1
    peerPci: "0003:01:00.0"
  node: vr-nvl72-mec02-c14
  partNumber: 900-9X9BE-00PB-TSA
  ports:
  - networkInterface: enP1p3s0f0np0
    pci: "0001:03:00.0"
    rdmaInterface: mlx5_0
  - networkInterface: enP1p3s0f1np1
    pci: "0001:03:00.1"
    rdmaInterface: mlx5_1
  - networkInterface: enP1p3s0f2np2
    pci: "0001:03:00.2"
    rdmaInterface: mlx5_2
  - networkInterface: enP1p3s0f3np3
    pci: "0001:03:00.3"
    rdmaInterface: mlx5_3
  psid: mt_0000001659
  serialNumber: MT2550601FH0
  superNIC: true
  type: "1025"
```