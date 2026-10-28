# DMS library discovery and interface naming

`pkg/devicediscovery` now delegates hardware discovery to `dms-lib/pkg/device`
and converts its inventory into the existing NCO records. `pkg/udev` delegates
name validation, rule rendering, files and udev activation to `dms-lib/pkg/naming`.
NCO retains template selection, NIC/rail/plane placeholders, shared RDMA naming
and future-PF naming policy. The controller reconciliation lifecycle is unchanged.

The discovery adapter keeps physical PCI addresses as CR keys; serial numbers
remain metadata. It consumes the library's explicit Orchid peer identity.
Incomplete enumeration fails the scan without deleting retained CRs. With
`SKIP_DEVICE_ON_DISCOVERY_ERROR=true`, scoped issues omit the affected ASIC and
populate the existing partial-discovery reporters so its CR is retained while
healthy records update. Without the switch, required discovery failures fail the
scan. Optional missing VPD metadata does not omit an otherwise usable device.

The controller reads expected interface names through `device.ReadInterfaceNames`
and refreshes other reconciled devices after every successful rule application.
This read-only refresh also retries failed reads or status updates when rule files
are already unchanged; it does not reactivate udev. Read errors are returned
without replacing stored names with invented empty strings.

## Consumer compatibility

`NewDeviceDiscovery` now takes only `nodeName`; its former `NVConfigUtils`
argument is unnecessary because native discovery belongs to dms-lib. The unused
`DeviceDiscoveryUtils` API and its native implementation are removed. The
`DeviceDiscovery`, `SkippedDeviceReporter` and `IncompleteDeviceReporter`
interfaces remain unchanged. DPF's constructor call requires a coordinated update;
this PR does not modify DPF. Firmware, NV configuration, doSPCX compilation,
runtime settings and CC ownership retain their existing implementations here.

Builds require access to the pinned private `Mellanox/dms-lib` module; hosted CI
access is tracked in [issue #571](https://github.com/Mellanox/nic-configuration-operator/issues/571).

## Validation boundary

Adapter tests cover metadata/ports, explicit peer mapping, optional VPD and
partial-failure reporting. Existing controller envtest suites cover CR retention
and naming lifecycle. Naming tests retain NCO template policy; generic filesystem
and udev behavior is tested in dms-lib.

Earlier read-only qualification observed all nine ASICs on both mevo-carmel-1
workers with DOCA DMS 3.6.0078-1. That driver used the same library revision but an
older controller adapter, so it is discovery evidence rather than qualification
of this final controller revision. No interface rename, NIC configuration, reset,
service startup or full daemon-image deployment was performed. The daemon image's
existing DOCA version is unchanged; its compatibility is not established by the
host's DOCA 3.6 results.
