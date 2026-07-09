Packages:

- [configuration.net.nvidia.com/v1alpha1](#configurationnetnvidiacomv1alpha1)

## configuration.net.nvidia.com/v1alpha1

Package v1alpha1 contains API Schema definitions for the configuration.net v1alpha1 API group

Resource Types:

### ConfigurationTemplateSpec

(*Appears on:*[NicConfigurationTemplateSpec](#NicConfigurationTemplateSpec),
[NicDeviceConfigurationSpec](#NicDeviceConfigurationSpec))

ConfigurationTemplateSpec is a set of configurations for the NICs

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>numVfs</code><br />
<em>int</em></td>
<td><p>Number of VFs to be configured</p></td>
</tr>
<tr>
<td><code>linkType</code><br />
<em><a href="#LinkTypeEnum">LinkTypeEnum</a></em></td>
<td><em>(Optional)</em>
<p>LinkType to be configured, Ethernet|Infiniband. Required unless networkBay is configured; for Network Bay the link type is governed by the system configuration and must not be set.</p></td>
</tr>
<tr>
<td><code>pciPerformanceOptimized</code><br />
<em><a href="#PciPerformanceOptimizedSpec">PciPerformanceOptimizedSpec</a></em></td>
<td><p>PCI performance optimization settings</p></td>
</tr>
<tr>
<td><code>roceOptimized</code><br />
<em><a href="#RoceOptimizedSpec">RoceOptimizedSpec</a></em></td>
<td><p>RoCE optimization settings</p></td>
</tr>
<tr>
<td><code>gpuDirectOptimized</code><br />
<em><a href="#GpuDirectOptimizedSpec">GpuDirectOptimizedSpec</a></em></td>
<td><p>GPU Direct optimization settings</p></td>
</tr>
<tr>
<td><code>runtimePerformanceOptimized</code><br />
<em><a href="#RuntimePerformanceOptimizedSpec">RuntimePerformanceOptimizedSpec</a></em></td>
<td><p>Runtime NIC performance tuning (ring buffers, channels, LRO) applied via ethtool</p></td>
</tr>
<tr>
<td><code>spectrumXOptimized</code><br />
<em><a href="#SpectrumXOptimizedSpec">SpectrumXOptimizedSpec</a></em></td>
<td><p>Spectrum-X optimization settings. Works only with linkType==Ethernet &amp;&amp; numVfs==1. RawNvConfig parameters, if provided, are merged as overrides on top of Spectrum-X calculated
params.</p></td>
</tr>
<tr>
<td><code>networkBay</code><br />
<em><a href="#NetworkBaySpec">NetworkBaySpec</a></em></td>
<td><em>(Optional)</em>
<p>NetworkBay configures a ConnectX-9 Network Bay card (per-ASIC set_system_conf). Allowed only for ConnectX-9 (nicType 1025).</p></td>
</tr>
<tr>
<td><code>rawNvConfig</code><br />
<em><a href="#NvConfigParam">[]NvConfigParam</a></em></td>
<td><p>List of arbitrary nv config parameters</p></td>
</tr>
<tr>
<td><code>force</code><br />
<em>bool</em></td>
<td><em>(Optional)</em>
<p>Force passes <code>--force</code> to mlxconfig set commands. When set, the daemon applies the nv config batch and set_system_conf with –force, letting mlxconfig accept a batch it would otherwise
refuse due to implicit parameter dependencies.</p></td>
</tr>
</tbody>
</table>

### ECNSpec

(*Appears on:*[QosSpec](#QosSpec))

ECNSpec specifies Explicit Congestion Notification settings

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>enabled</code><br />
<em>bool</em></td>
<td><p>Enable ECN on the specified priority</p></td>
</tr>
<tr>
<td><code>priority</code><br />
<em>int</em></td>
<td><p>Traffic class / priority to enable ECN on (0-7)</p></td>
</tr>
</tbody>
</table>

### FirmwareTemplateSpec

(*Appears on:*[NicDeviceSpec](#NicDeviceSpec), [NicFirmwareTemplateSpec](#NicFirmwareTemplateSpec))

FirmwareTemplateSpec specifies a FW update policy for a given FW source ref

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>nicFirmwareSourceRef</code><br />
<em>string</em></td>
<td><p>NicFirmwareSourceRef refers to existing NicFirmwareSource CR on where to get the FW from</p></td>
</tr>
<tr>
<td><code>updatePolicy</code><br />
<em>string</em></td>
<td><p>UpdatePolicy indicates whether the operator needs to validate installed FW or upgrade it</p></td>
</tr>
</tbody>
</table>

### GpuDirectOptimizedSpec

(*Appears on:*[ConfigurationTemplateSpec](#ConfigurationTemplateSpec))

GpuDirectOptimizedSpec specifies GPU Direct optimization settings

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>enabled</code><br />
<em>bool</em></td>
<td><p>Optimize GPU Direct</p></td>
</tr>
<tr>
<td><code>env</code><br />
<em>string</em></td>
<td><p>GPU direct environment, e.g. Baremetal</p></td>
</tr>
</tbody>
</table>

### LinkTypeEnum (`string` alias)

(*Appears on:*[ConfigurationTemplateSpec](#ConfigurationTemplateSpec))

LinkTypeEnum described the link type (Ethernet / Infiniband)

### NetworkBaySpec

(*Appears on:*[ConfigurationTemplateSpec](#ConfigurationTemplateSpec))

NetworkBaySpec configures a ConnectX-9 Network Bay (“orchid”) card. A Network Bay card exposes two CX9 ASICs as two PCI endpoints that share a single OSFP cage and must be configured as a pair.
Allowed only when nicSelector.nicType == “1025” (ConnectX-9), enforced by CEL on NicConfigurationTemplateSpec.

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>conf</code><br />
<em>string</em></td>
<td><p>Conf is the argument passed to <code>mlxconfig set_system_conf</code>. The per-ASIC index is appended automatically by the daemon based on the device’s detected Network Bay ASIC index, e.g.
set_system_conf [0].</p></td>
</tr>
</tbody>
</table>

### NicConfigurationTemplate

NicConfigurationTemplate is the Schema for the nicconfigurationtemplates API

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>metadata</code><br />
<em><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta">Kubernetes meta/v1.ObjectMeta</a></em></td>
<td>Refer to the Kubernetes API documentation for the fields of the <code>metadata</code> field.</td>
</tr>
<tr>
<td><code>spec</code><br />
<em><a href="#NicConfigurationTemplateSpec">NicConfigurationTemplateSpec</a></em></td>
<td><p>Defines the desired state of NICs</p>
<br />
<br />
&#10;<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<tbody>
<tr>
<td><code>nodeSelector</code><br />
<em>map[string]string</em></td>
<td><p>NodeSelector contains labels required on the node. When empty, the template will be applied to matching devices on all nodes.</p></td>
</tr>
<tr>
<td><code>nicSelector</code><br />
<em><a href="#NicSelectorSpec">NicSelectorSpec</a></em></td>
<td><p>NIC selector configuration</p></td>
</tr>
<tr>
<td><code>resetToDefault</code><br />
<em>bool</em></td>
<td><em>(Optional)</em>
<p>ResetToDefault specifies whether node agent needs to perform a reset flow The following operations will be performed: * Nvconfig reset of all non-volatile configurations - Mstconfig -d reset for
each PF - Mstconfig -d set ADVANCED_PCI_SETTINGS=1 * Node reboot - Applies new NIC NV config - Will undo any runtime configuration previously performed for the device/driver</p></td>
</tr>
<tr>
<td><code>template</code><br />
<em><a href="#ConfigurationTemplateSpec">ConfigurationTemplateSpec</a></em></td>
<td><p>Configuration template to be applied to matching devices</p></td>
</tr>
</tbody>
</table></td>
</tr>
<tr>
<td><code>status</code><br />
<em><a href="#NicTemplateStatus">NicTemplateStatus</a></em></td>
<td><p>Defines the observed state of NicConfigurationTemplate</p></td>
</tr>
</tbody>
</table>

### NicConfigurationTemplateSpec

(*Appears on:*[NicConfigurationTemplate](#NicConfigurationTemplate))

NicConfigurationTemplateSpec defines the desired state of NicConfigurationTemplate

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>nodeSelector</code><br />
<em>map[string]string</em></td>
<td><p>NodeSelector contains labels required on the node. When empty, the template will be applied to matching devices on all nodes.</p></td>
</tr>
<tr>
<td><code>nicSelector</code><br />
<em><a href="#NicSelectorSpec">NicSelectorSpec</a></em></td>
<td><p>NIC selector configuration</p></td>
</tr>
<tr>
<td><code>resetToDefault</code><br />
<em>bool</em></td>
<td><em>(Optional)</em>
<p>ResetToDefault specifies whether node agent needs to perform a reset flow The following operations will be performed: * Nvconfig reset of all non-volatile configurations - Mstconfig -d reset for
each PF - Mstconfig -d set ADVANCED_PCI_SETTINGS=1 * Node reboot - Applies new NIC NV config - Will undo any runtime configuration previously performed for the device/driver</p></td>
</tr>
<tr>
<td><code>template</code><br />
<em><a href="#ConfigurationTemplateSpec">ConfigurationTemplateSpec</a></em></td>
<td><p>Configuration template to be applied to matching devices</p></td>
</tr>
</tbody>
</table>

### NicDevice

NicDevice is the Schema for the nicdevices API

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>metadata</code><br />
<em><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta">Kubernetes meta/v1.ObjectMeta</a></em></td>
<td>Refer to the Kubernetes API documentation for the fields of the <code>metadata</code> field.</td>
</tr>
<tr>
<td><code>spec</code><br />
<em><a href="#NicDeviceSpec">NicDeviceSpec</a></em></td>
<td><br />
<br />
&#10;<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<tbody>
<tr>
<td><code>configuration</code><br />
<em><a href="#NicDeviceConfigurationSpec">NicDeviceConfigurationSpec</a></em></td>
<td><p>Configuration specifies the configuration requested by NicConfigurationTemplate</p></td>
</tr>
<tr>
<td><code>firmware</code><br />
<em><a href="#FirmwareTemplateSpec">FirmwareTemplateSpec</a></em></td>
<td><p>Firmware specifies the fw upgrade policy requested by NicFirmwareTemplate</p></td>
</tr>
<tr>
<td><code>interfaceNameTemplate</code><br />
<em><a href="#NicDeviceInterfaceNameSpec">NicDeviceInterfaceNameSpec</a></em></td>
<td><p>InterfaceNameTemplate specifies the interface name template to be applied to the NIC</p></td>
</tr>
</tbody>
</table></td>
</tr>
<tr>
<td><code>status</code><br />
<em><a href="#NicDeviceStatus">NicDeviceStatus</a></em></td>
<td></td>
</tr>
</tbody>
</table>

### NicDeviceConfigurationSpec

(*Appears on:*[NicDeviceSpec](#NicDeviceSpec))

NicDeviceConfigurationSpec contains desired configuration of the NIC

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>resetToDefault</code><br />
<em>bool</em></td>
<td><p>ResetToDefault specifies whether node agent needs to perform a reset flow. The following operations will be performed: * Nvconfig reset of all non-volatile configurations - Mstconfig -d reset
for each PF - Mstconfig -d set ADVANCED_PCI_SETTINGS=1 * Node reboot - Applies new NIC NV config - Will undo any runtime configuration previously performed for the device/driver</p></td>
</tr>
<tr>
<td><code>template</code><br />
<em><a href="#ConfigurationTemplateSpec">ConfigurationTemplateSpec</a></em></td>
<td><p>Configuration template applied from the NicConfigurationTemplate CR</p></td>
</tr>
</tbody>
</table>

### NicDeviceInterfaceNameSpec

(*Appears on:*[NicDeviceSpec](#NicDeviceSpec))

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>nicIndex</code><br />
<em>int</em></td>
<td><p>NicIndex is the index of the NIC in the flattened list of NICs based on the Template</p></td>
</tr>
<tr>
<td><code>railIndex</code><br />
<em>int</em></td>
<td><p>RailIndex is the index of the rail where the given NIC belongs to based on the Template</p></td>
</tr>
<tr>
<td><code>planeIndices</code><br />
<em>[]int</em></td>
<td><p>PlaneIndices is the indices of the planes for the given NIC based on the Template</p></td>
</tr>
<tr>
<td><code>rdmaDevicePrefix</code><br />
<em>string</em></td>
<td><p>— Parameters from the NicInterfaceNameTemplate CR — RdmaDevicePrefix specifies the prefix for the rdma device name. Empty means RDMA naming is skipped.</p></td>
</tr>
<tr>
<td><code>netDevicePrefix</code><br />
<em>string</em></td>
<td><p>NetDevicePrefix specifies the prefix for the net device name</p></td>
</tr>
</tbody>
</table>

### NicDeviceNetworkBayStatus

(*Appears on:*[NicDeviceStatus](#NicDeviceStatus))

NicDeviceNetworkBayStatus holds the ConnectX-9 Network Bay identity of a device.

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>asic</code><br />
<em>int</em></td>
<td><p>Asic is the orchid ASIC index (0 or 1) inferred from the MGIR.ga register field.</p></td>
</tr>
<tr>
<td><code>peerPci</code><br />
<em>string</em></td>
<td><em>(Optional)</em>
<p>PeerPCI is the PCI address of the sibling ASIC in the same Network Bay card (the other device sharing this device’s serial number). Empty if the peer could not be resolved.</p></td>
</tr>
</tbody>
</table>

### NicDevicePortSpec

(*Appears on:*[NicDeviceStatus](#NicDeviceStatus))

NicDevicePortSpec describes the ports of the NIC

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>pci</code><br />
<em>string</em></td>
<td><p>PCI is a PCI address of the port, e.g. 0000:3b:00.0</p></td>
</tr>
<tr>
<td><code>fwctlDevice</code><br />
<em>string</em></td>
<td><em>(Optional)</em>
<p>FwctlDevice is the fwctl character device path for this port, e.g. /dev/fwctl/fwctl0. Empty when the host does not expose a fwctl device for this PCI function.</p></td>
</tr>
<tr>
<td><code>networkInterface</code><br />
<em>string</em></td>
<td><p>NetworkInterface is the name of the network interface for this port, e.g. eth1</p></td>
</tr>
<tr>
<td><code>rdmaInterface</code><br />
<em>string</em></td>
<td><p>RdmaInterface is the name of the rdma interface for this port, e.g. mlx5_1</p></td>
</tr>
</tbody>
</table>

### NicDeviceSpec

(*Appears on:*[NicDevice](#NicDevice))

NicDeviceSpec defines the desired state of NicDevice

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>configuration</code><br />
<em><a href="#NicDeviceConfigurationSpec">NicDeviceConfigurationSpec</a></em></td>
<td><p>Configuration specifies the configuration requested by NicConfigurationTemplate</p></td>
</tr>
<tr>
<td><code>firmware</code><br />
<em><a href="#FirmwareTemplateSpec">FirmwareTemplateSpec</a></em></td>
<td><p>Firmware specifies the fw upgrade policy requested by NicFirmwareTemplate</p></td>
</tr>
<tr>
<td><code>interfaceNameTemplate</code><br />
<em><a href="#NicDeviceInterfaceNameSpec">NicDeviceInterfaceNameSpec</a></em></td>
<td><p>InterfaceNameTemplate specifies the interface name template to be applied to the NIC</p></td>
</tr>
</tbody>
</table>

### NicDeviceStatus

(*Appears on:*[NicDevice](#NicDevice))

NicDeviceStatus defines the observed state of NicDevice

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>node</code><br />
<em>string</em></td>
<td><p>Node where the device is located</p></td>
</tr>
<tr>
<td><code>type</code><br />
<em>string</em></td>
<td><p>Type of device, e.g. ConnectX7</p></td>
</tr>
<tr>
<td><code>serialNumber</code><br />
<em>string</em></td>
<td><p>SerialNumber of the device, e.g. MT2116X09299. Informational only — not guaranteed unique across all cards on a host: on systems with embedded NICs sharing a flashed VPD image (e.g. HGX B300)
multiple cards will report the same serial number. The operator identifies NICs uniquely by their PCI device address (the <code>pci</code> field on the first entry in <code>ports</code>, with the
function digit stripped).</p></td>
</tr>
<tr>
<td><code>partNumber</code><br />
<em>string</em></td>
<td><p>Part number of the device, e.g. MCX713106AEHEA_QP1</p></td>
</tr>
<tr>
<td><code>psid</code><br />
<em>string</em></td>
<td><p>Product Serial ID of the device, e.g. MT_0000000221</p></td>
</tr>
<tr>
<td><code>firmwareVersion</code><br />
<em>string</em></td>
<td><p>Firmware version currently installed on the device, e.g. 22.31.1014</p></td>
</tr>
<tr>
<td><code>dpu</code><br />
<em>bool</em></td>
<td><p>DPU indicates if the device is a BlueField in DPU mode</p></td>
</tr>
<tr>
<td><code>modelName</code><br />
<em>string</em></td>
<td><p>ModelName is the model name of the device, e.g. ConnectX-6 or BlueField-3</p></td>
</tr>
<tr>
<td><code>superNIC</code><br />
<em>bool</em></td>
<td><p>SuperNIC indicates if the device is a SuperNIC</p></td>
</tr>
<tr>
<td><code>ports</code><br />
<em><a href="#NicDevicePortSpec">[]NicDevicePortSpec</a></em></td>
<td><p>List of ports for the device</p></td>
</tr>
<tr>
<td><code>networkBay</code><br />
<em><a href="#NicDeviceNetworkBayStatus">NicDeviceNetworkBayStatus</a></em></td>
<td><em>(Optional)</em>
<p>NetworkBay holds ConnectX-9 Network Bay (“orchid”) identity for the device. Set only when the device is detected as part of a Network Bay card.</p></td>
</tr>
<tr>
<td><code>conditions</code><br />
<em><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta">[]Kubernetes meta/v1.Condition</a></em></td>
<td><p>List of conditions observed for the device</p></td>
</tr>
</tbody>
</table>

### NicFirmwareSource

NicFirmwareSource is the Schema for the nicfirmwaresources API

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>metadata</code><br />
<em><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta">Kubernetes meta/v1.ObjectMeta</a></em></td>
<td>Refer to the Kubernetes API documentation for the fields of the <code>metadata</code> field.</td>
</tr>
<tr>
<td><code>spec</code><br />
<em><a href="#NicFirmwareSourceSpec">NicFirmwareSourceSpec</a></em></td>
<td><br />
<br />
&#10;<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<tbody>
<tr>
<td><code>binUrlSources</code><br />
<em>[]string</em></td>
<td><em>(Optional)</em>
<p>BinUrlSources represents a list of url sources for ConnectX Firmware</p></td>
</tr>
<tr>
<td><code>bfbUrlSource</code><br />
<em>string</em></td>
<td><em>(Optional)</em>
<p>BFBUrlSource represents a url source for BlueField Bundle</p></td>
</tr>
<tr>
<td><code>docaSpcXCCUrlSource</code><br />
<em>string</em></td>
<td><em>(Optional)</em>
<p>DocaSpcXCCUrlSource represents a url source for DOCA SPC-X CC .deb package for ubuntu 22.04 Will be removed in the future, once Doca SPC-X CC algorithm will be publicly available</p></td>
</tr>
</tbody>
</table></td>
</tr>
<tr>
<td><code>status</code><br />
<em><a href="#NicFirmwareSourceStatus">NicFirmwareSourceStatus</a></em></td>
<td></td>
</tr>
</tbody>
</table>

### NicFirmwareSourceSpec

(*Appears on:*[NicFirmwareSource](#NicFirmwareSource))

NicFirmwareSourceSpec represents a list of url sources for FW

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>binUrlSources</code><br />
<em>[]string</em></td>
<td><em>(Optional)</em>
<p>BinUrlSources represents a list of url sources for ConnectX Firmware</p></td>
</tr>
<tr>
<td><code>bfbUrlSource</code><br />
<em>string</em></td>
<td><em>(Optional)</em>
<p>BFBUrlSource represents a url source for BlueField Bundle</p></td>
</tr>
<tr>
<td><code>docaSpcXCCUrlSource</code><br />
<em>string</em></td>
<td><em>(Optional)</em>
<p>DocaSpcXCCUrlSource represents a url source for DOCA SPC-X CC .deb package for ubuntu 22.04 Will be removed in the future, once Doca SPC-X CC algorithm will be publicly available</p></td>
</tr>
</tbody>
</table>

### NicFirmwareSourceStatus

(*Appears on:*[NicFirmwareSource](#NicFirmwareSource))

NicFirmwareSourceStatus represents the status of the FW from given sources, e.g. version available for PSIDs

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>state</code><br />
<em>string</em></td>
<td><p>State represents the firmware processing state</p></td>
</tr>
<tr>
<td><code>reason</code><br />
<em>string</em></td>
<td><p>Reason shows an error message if occurred</p></td>
</tr>
<tr>
<td><code>binaryVersions</code><br />
<em>map[string][]string</em></td>
<td><p>Versions is a map of available FW binaries versions to PSIDs a PSID should have only a single FW version available for it</p></td>
</tr>
<tr>
<td><code>bfbVersions</code><br />
<em>map[string]string</em></td>
<td><p>BFBVersions represents the FW versions available in the provided BFB bundle</p></td>
</tr>
<tr>
<td><code>docaSpcXCCVersion</code><br />
<em>string</em></td>
<td><p>DocaSpcXCCVersion represents the FW versions available in the provided DOCA SPC-X CC .deb package for ubuntu 22.04</p></td>
</tr>
</tbody>
</table>

### NicFirmwareTemplate

NicFirmwareTemplate is the Schema for the nicfirmwaretemplates API

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>metadata</code><br />
<em><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta">Kubernetes meta/v1.ObjectMeta</a></em></td>
<td>Refer to the Kubernetes API documentation for the fields of the <code>metadata</code> field.</td>
</tr>
<tr>
<td><code>spec</code><br />
<em><a href="#NicFirmwareTemplateSpec">NicFirmwareTemplateSpec</a></em></td>
<td><br />
<br />
&#10;<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<tbody>
<tr>
<td><code>nodeSelector</code><br />
<em>map[string]string</em></td>
<td><p>NodeSelector contains labels required on the node. When empty, the template will be applied to matching devices on all nodes.</p></td>
</tr>
<tr>
<td><code>nicSelector</code><br />
<em><a href="#NicSelectorSpec">NicSelectorSpec</a></em></td>
<td><p>NIC selector configuration</p></td>
</tr>
<tr>
<td><code>template</code><br />
<em><a href="#FirmwareTemplateSpec">FirmwareTemplateSpec</a></em></td>
<td><p>Firmware update template</p></td>
</tr>
</tbody>
</table></td>
</tr>
<tr>
<td><code>status</code><br />
<em><a href="#NicTemplateStatus">NicTemplateStatus</a></em></td>
<td></td>
</tr>
</tbody>
</table>

### NicFirmwareTemplateSpec

(*Appears on:*[NicFirmwareTemplate](#NicFirmwareTemplate))

NicFirmwareTemplateSpec defines the FW templates and node/nic selectors for it

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>nodeSelector</code><br />
<em>map[string]string</em></td>
<td><p>NodeSelector contains labels required on the node. When empty, the template will be applied to matching devices on all nodes.</p></td>
</tr>
<tr>
<td><code>nicSelector</code><br />
<em><a href="#NicSelectorSpec">NicSelectorSpec</a></em></td>
<td><p>NIC selector configuration</p></td>
</tr>
<tr>
<td><code>template</code><br />
<em><a href="#FirmwareTemplateSpec">FirmwareTemplateSpec</a></em></td>
<td><p>Firmware update template</p></td>
</tr>
</tbody>
</table>

### NicInterfaceNameTemplate

NicInterfaceNameTemplate is the Schema for the nicinterfacenametemplates API

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>metadata</code><br />
<em><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta">Kubernetes meta/v1.ObjectMeta</a></em></td>
<td>Refer to the Kubernetes API documentation for the fields of the <code>metadata</code> field.</td>
</tr>
<tr>
<td><code>spec</code><br />
<em><a href="#NicInterfaceNameTemplateSpec">NicInterfaceNameTemplateSpec</a></em></td>
<td><br />
<br />
&#10;<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<tbody>
<tr>
<td><code>nodeSelector</code><br />
<em>map[string]string</em></td>
<td><p>NodeSelector contains labels required on the node. When empty, the template will be applied to matching devices on all nodes.</p></td>
</tr>
<tr>
<td><code>pfsPerNic</code><br />
<em>int</em></td>
<td><p>PfsPerNic specifies the number of PFs per NIC Used to calculate the number of planes per NIC</p></td>
</tr>
<tr>
<td><code>rdmaDevicePrefix</code><br />
<em>string</em></td>
<td><em>(Optional)</em>
<p>RdmaDevicePrefix specifies the prefix for the rdma device name. When empty, no RDMA udev rules are generated and RDMA device naming is skipped. %nic_id%, %plane_id% and %rail_id% placeholders can
be used to construct the device name %nic_id% is the index of the NIC in the flattened list of NICs %plane_id% is the index of the plane of the specific NIC %rail_id% is the index of the rail where
the given NIC belongs to</p></td>
</tr>
<tr>
<td><code>netDevicePrefix</code><br />
<em>string</em></td>
<td><p>NetDevicePrefix specifies the prefix for the net device name %nic_id%, %plane_id% and %rail_id% placeholders can be used to construct the device name %nic_id% is the index of the NIC in the
flattened list of NICs %plane_id% is the index of the plane of the specific NIC %rail_id% is the index of the rail where the given NIC belongs to</p></td>
</tr>
<tr>
<td><code>railPciAddresses</code><br />
<em>[][]string</em></td>
<td><p>RailPciAddresses defines the PCI address to rail mapping and order The first dimension is the rail index, the second dimension is the PCI addresses of the NICs in the rail. The PCI addresses
must be sorted in the order of the rails. Example: [[“0000:1a:00.0”, “0000:2a:00.0”], [“0000:3a:00.0”, “0000:4a:00.0”]] specifies 2 rails with 2 NICs each.</p></td>
</tr>
</tbody>
</table></td>
</tr>
<tr>
<td><code>status</code><br />
<em><a href="#NicInterfaceNameTemplateStatus">NicInterfaceNameTemplateStatus</a></em></td>
<td></td>
</tr>
</tbody>
</table>

### NicInterfaceNameTemplateSpec

(*Appears on:*[NicInterfaceNameTemplate](#NicInterfaceNameTemplate))

NicInterfaceNameTemplateSpec defines the desired state of NicInterfaceNameTemplate

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>nodeSelector</code><br />
<em>map[string]string</em></td>
<td><p>NodeSelector contains labels required on the node. When empty, the template will be applied to matching devices on all nodes.</p></td>
</tr>
<tr>
<td><code>pfsPerNic</code><br />
<em>int</em></td>
<td><p>PfsPerNic specifies the number of PFs per NIC Used to calculate the number of planes per NIC</p></td>
</tr>
<tr>
<td><code>rdmaDevicePrefix</code><br />
<em>string</em></td>
<td><em>(Optional)</em>
<p>RdmaDevicePrefix specifies the prefix for the rdma device name. When empty, no RDMA udev rules are generated and RDMA device naming is skipped. %nic_id%, %plane_id% and %rail_id% placeholders can
be used to construct the device name %nic_id% is the index of the NIC in the flattened list of NICs %plane_id% is the index of the plane of the specific NIC %rail_id% is the index of the rail where
the given NIC belongs to</p></td>
</tr>
<tr>
<td><code>netDevicePrefix</code><br />
<em>string</em></td>
<td><p>NetDevicePrefix specifies the prefix for the net device name %nic_id%, %plane_id% and %rail_id% placeholders can be used to construct the device name %nic_id% is the index of the NIC in the
flattened list of NICs %plane_id% is the index of the plane of the specific NIC %rail_id% is the index of the rail where the given NIC belongs to</p></td>
</tr>
<tr>
<td><code>railPciAddresses</code><br />
<em>[][]string</em></td>
<td><p>RailPciAddresses defines the PCI address to rail mapping and order The first dimension is the rail index, the second dimension is the PCI addresses of the NICs in the rail. The PCI addresses
must be sorted in the order of the rails. Example: [[“0000:1a:00.0”, “0000:2a:00.0”], [“0000:3a:00.0”, “0000:4a:00.0”]] specifies 2 rails with 2 NICs each.</p></td>
</tr>
</tbody>
</table>

### NicInterfaceNameTemplateStatus

(*Appears on:*[NicInterfaceNameTemplate](#NicInterfaceNameTemplate))

NicInterfaceNameTemplateStatus defines the observed state of NicInterfaceNameTemplate

### NicSelectorSpec

(*Appears on:*[NicConfigurationTemplateSpec](#NicConfigurationTemplateSpec),
[NicFirmwareTemplateSpec](#NicFirmwareTemplateSpec))

NicSelectorSpec is a desired configuration for NICs

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>nicType</code><br />
<em>string</em></td>
<td><p>Type of the NIC to be selected, e.g. 101d,1015,a2d6 etc.</p></td>
</tr>
<tr>
<td><code>pciAddresses</code><br />
<em>[]string</em></td>
<td><p>Array of PCI addresses to be selected, e.g. “0000:03:00.0”</p></td>
</tr>
<tr>
<td><code>serialNumbers</code><br />
<em>[]string</em></td>
<td><p>Serial numbers of the NICs to be selected, e.g. MT2116X09299. Note: serial numbers are not guaranteed unique — on systems with embedded NICs that share a flashed VPD image (e.g. HGX B300),
multiple physical cards report the same serial, and this selector will match all of them. Use <code>pciAddresses</code> for precise per-card selection on such systems.</p></td>
</tr>
<tr>
<td><code>partNumbers</code><br />
<em>[]string</em></td>
<td><p>Part numbers of the NICs to be selected, e.g. MCX713106AEHEA_QP1</p></td>
</tr>
</tbody>
</table>

### NicTemplateStatus

(*Appears on:*[NicConfigurationTemplate](#NicConfigurationTemplate), [NicFirmwareTemplate](#NicFirmwareTemplate))

NicTemplateStatus defines the observed state of NicConfigurationTemplate and NicFirmwareTemplate

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>nicDevices</code><br />
<em>[]string</em></td>
<td><p>NicDevice CRs matching this configuration / firmware template</p></td>
</tr>
<tr>
<td><code>conditions</code><br />
<em><a href="https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta">[]Kubernetes meta/v1.Condition</a></em></td>
<td><p>Conditions observed for this template, e.g. a Network Bay pairing imbalance on a node</p></td>
</tr>
</tbody>
</table>

### NvConfigParam

(*Appears on:*[ConfigurationTemplateSpec](#ConfigurationTemplateSpec))

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>name</code><br />
<em>string</em></td>
<td><p>Name of the arbitrary nvconfig parameter</p></td>
</tr>
<tr>
<td><code>value</code><br />
<em>string</em></td>
<td><p>Value of the arbitrary nvconfig parameter</p></td>
</tr>
</tbody>
</table>

### PauseFramesSpec

(*Appears on:*[QosSpec](#QosSpec))

PauseFramesSpec specifies global pause frame settings

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>enabled</code><br />
<em>bool</em></td>
<td><p>Enable global pause frames (autoneg, rx, tx). Set to false to disable all pause frames (recommended when PFC is used).</p></td>
</tr>
</tbody>
</table>

### PciPerformanceOptimizedSpec

(*Appears on:*[ConfigurationTemplateSpec](#ConfigurationTemplateSpec))

PciPerformanceOptimizedSpec specifies PCI performance optimization settings

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>enabled</code><br />
<em>bool</em></td>
<td><p>Specifies whether to enable PCI performance optimization</p></td>
</tr>
<tr>
<td><code>maxAccOutRead</code><br />
<em>int</em></td>
<td><p>Deprecated: this field is ignored and no longer maps to MAX_ACC_OUT_READ.</p></td>
</tr>
<tr>
<td><code>maxReadRequest</code><br />
<em>int</em></td>
<td><p>Specifies the size of a single PCI read request in bytes</p></td>
</tr>
</tbody>
</table>

### QosSpec

(*Appears on:*[RoceOptimizedSpec](#RoceOptimizedSpec))

QosSpec specifies Quality of Service settings

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>trust</code><br />
<em>string</em></td>
<td><p>Trust mode for QoS settings, e.g. dscp</p></td>
</tr>
<tr>
<td><code>pfc</code><br />
<em>string</em></td>
<td><p>Priority-based Flow Control configuration, e.g. “0,0,0,1,0,0,0,0”</p></td>
</tr>
<tr>
<td><code>tos</code><br />
<em>int</em></td>
<td><p>8-bit value for type of service</p></td>
</tr>
<tr>
<td><code>cableLen</code><br />
<em>int</em></td>
<td><p>Cable length in meters, used for ECN buffer threshold calculation</p></td>
</tr>
<tr>
<td><code>ecn</code><br />
<em><a href="#ECNSpec">ECNSpec</a></em></td>
<td><p>ECN (Explicit Congestion Notification) settings</p></td>
</tr>
<tr>
<td><code>pauseFrames</code><br />
<em><a href="#PauseFramesSpec">PauseFramesSpec</a></em></td>
<td><p>Global pause frame settings (disable when using PFC)</p></td>
</tr>
</tbody>
</table>

### RoceOptimizedSpec

(*Appears on:*[ConfigurationTemplateSpec](#ConfigurationTemplateSpec))

RoceOptimizedSpec specifies RoCE optimization settings

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>enabled</code><br />
<em>bool</em></td>
<td><p>Optimize RoCE</p></td>
</tr>
<tr>
<td><code>qos</code><br />
<em><a href="#QosSpec">QosSpec</a></em></td>
<td><p>Quality of Service settings</p></td>
</tr>
<tr>
<td><code>roceMode</code><br />
<em>int</em></td>
<td><p>RoCE mode: 1 for RoCE v1, 2 for RoCE v2. Only effective when roceOptimized.enabled is true.</p></td>
</tr>
</tbody>
</table>

### RuntimePerformanceOptimizedSpec

(*Appears on:*[ConfigurationTemplateSpec](#ConfigurationTemplateSpec))

RuntimePerformanceOptimizedSpec specifies runtime NIC performance tuning applied via ethtool

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>enabled</code><br />
<em>bool</em></td>
<td><p>Enable runtime performance optimization</p></td>
</tr>
<tr>
<td><code>rxRingSize</code><br />
<em>int</em></td>
<td><p>RX ring buffer size (ethtool -G rx)</p></td>
</tr>
<tr>
<td><code>txRingSize</code><br />
<em>int</em></td>
<td><p>TX ring buffer size (ethtool -G tx)</p></td>
</tr>
<tr>
<td><code>combinedChannels</code><br />
<em>int</em></td>
<td><p>Number of combined channels (ethtool -L combined)</p></td>
</tr>
<tr>
<td><code>lro</code><br />
<em>bool</em></td>
<td><p>Enable Large Receive Offload (ethtool -K lro)</p></td>
</tr>
</tbody>
</table>

### SpectrumXOptimizedSpec

(*Appears on:*[ConfigurationTemplateSpec](#ConfigurationTemplateSpec))

SpectrumXOptimizedSpec enables Spectrum-X specific optimizations

<table>
<colgroup>
<col style="width: 50%" />
<col style="width: 50%" />
</colgroup>
<thead>
<tr>
<th>Field</th>
<th>Description</th>
</tr>
</thead>
<tbody>
<tr>
<td><code>enabled</code><br />
<em>bool</em></td>
<td><p>Optimize Spectrum X</p></td>
</tr>
<tr>
<td><code>version</code><br />
<em>string</em></td>
<td><p>Version of the Spectrum-X architecture to optimize for. Should match the name of the config map with Spectrum-X profile</p></td>
</tr>
<tr>
<td><code>overlay</code><br />
<em>string</em></td>
<td><em>(Optional)</em>
<p>Overlay mode to be configured Can be “l3” or “none”</p></td>
</tr>
<tr>
<td><code>multiplaneMode</code><br />
<em>string</em></td>
<td><em>(Optional)</em>
<p>Multiplane mode to be configured Can be “none”, “swplb”, “hwplb”, or “uniplane”</p></td>
</tr>
<tr>
<td><code>numberOfPlanes</code><br />
<em>int</em></td>
<td><em>(Optional)</em>
<p>Number of planes to be configured</p></td>
</tr>
</tbody>
</table>

--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

*Generated with `gen-crd-api-reference-docs` on git commit `85f5e0d`.*
