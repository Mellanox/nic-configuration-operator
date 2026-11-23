<!-- Copyright 2026 NVIDIA CORPORATION & AFFILIATES -->

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
this PR does not modify DPF. Firmware operations and generic NV/runtime execution
use the library; NCO retains Kubernetes selection, ordinary template policy,
maintenance and existing CC process ownership. Spectrum-X configuration selects
DMSPlanParams through spec.planRef; template.spectrumXOptimized is removed.

The established `github.com/Mellanox/dms-lib` and `/api` import paths are retained
through versioned replacements to the GitLab `dms-blueprints-lib.git/lib`,
`lib/api` and `doSpcx` modules. All three replacements pin revision
`79e474e6d794807e53f5ca72baf1dbe8000bb26b`. Planning imports the Go planner
packages directly; configuration uses the installed `dms-cli`. No separately
packaged planner process or local module replacement is required.

Set the GitLab project's CI/CD variable `DMS_LIB_REPOSITORY` to the private
Git repository path in `host/group/project.git` form. The STIG build jobs
substitute it into the Go module and checksum files in the job checkout.
The existing `GITLAB_USERNAME` and `GITLAB_TOKEN` need read access to that
repository. BuildKit receives those credentials through the `netrc` secret.

For a local build, set the same variable and run
`sh hack/resolve-dms-lib-repository.sh` in a disposable checkout before running
Go or Docker. Set `GOPRIVATE` to the repository host for Go, or pass
`--build-arg GOPRIVATE` and `--secret id=netrc,src=/path/to/netrc` to Docker.
The repository placeholder belongs in the committed files.

## Validation boundary

Adapter tests cover metadata/ports, explicit peer mapping, optional VPD and
partial-failure reporting. Existing controller envtest suites cover CR retention
and naming lifecycle. Naming tests retain NCO template policy; generic filesystem
and udev behavior is tested in dms-lib.

Earlier read-only qualification observed all nine ASICs on both mevo-carmel-1
workers with DOCA DMS 3.6.0078-1. That driver used an earlier library and controller adapter, so it is historical
discovery evidence rather than qualification of this GitLab pin or final controller revision. No interface rename, NIC configuration, reset,
service startup or full daemon-image deployment was performed. The daemon image's
existing DOCA version is unchanged; its compatibility is not established by the
host's DOCA 3.6 results.

The pinned library includes the native-only PCI selection fix in GitLab MR !13.
NCO forwards `MLXCONFIG_USE_PCI_ADDRESS` to that option while preserving DMS
NV/runtime routing and discovery identity.
