# DMS library discovery and interface naming

The daemon uses `github.com/Mellanox/dms-lib/pkg/device` for physical ASIC
inventory and current PF interface names. The existing public NCO
`pkg/devicediscovery` API remains available for consumers migrating separately.
Firmware management, NV configuration, doSPCX plan generation, runtime
configuration and CC process ownership retain their existing implementations in
this migration.

Builds require access to the pinned `Mellanox/dms-lib` Go module.

## Discovery and retained status

Discovery reconciles only records in its configured namespace, matching its
creation ownership. One library `Inventory` is projected into the existing `NicDevice` status. CR names
remain based on node, device type and physical PCI address; serial numbers and
opaque library device IDs do not become Kubernetes object identity. Optional VPD
metadata can be empty. Network Bay peers come only from resolved library pairing.

`DiscoveryReady=True` means the latest pass observed usable device data. A scoped
issue retains the previous status and marks that device `DiscoveryReady=False`.
An incomplete scan or global error retains all existing devices and marks them
unready; it never treats a failed read as disappearance. Discovery retries failed
passes after five seconds, rather than spinning. Successful scans retain the
existing five-minute period.

`SKIP_DEVICE_ON_DISCOVERY_ERROR=true` permits independent healthy ASICs to
continue. Without the switch, required discovery failures keep the existing
fail-whole-scan behavior. Every configuration reconciliation obtains a new
inventory and checks identity metadata, firmware/mode and the complete PF set;
a condition from an earlier pass is not authority to mutate a retained device.
Shared Network Bay pairs and the existing node-scoped Spectrum-X plan wait for
all their participants. A pending discovery prevents host reboot and maintenance
release. Firmware activation and the optional post-NV firmware reset end the
current pass so later configuration must obtain fresh inventory.

## Naming ownership

NCO resolves template selection, NIC/rail/plane placeholders, shared RDMA naming,
and names for future PFs. The generic `naming.Validate` and `naming.Apply` APIs own
physical-name validation, rule rendering, files, udev reload/trigger and settle.
The owned rule filenames are unchanged. The boolean returned by Apply describes
file replacement; it does not establish that interface activation succeeded.

After every successful Apply, including unchanged file contents, NCO reads current
netdev/RDMA names using the library's sysfs-only `ReadInterfaceNames`. Failed reads
retain the last-known names and block dependent configuration. An absent optional
endpoint is represented by an empty name only when its read succeeds.

Naming files and activation are host-wide. If any known device is stale, NCO
leaves the complete naming domain alone rather than writing rules from a filtered
set. A healthy named ASIC can continue only when fresh name reads match its
resolved intent. A pending template removal waits for the complete naming domain. Its condition
is cleared only after the complete rule set is activated and interface-name
readback succeeds, including when no other configuration intent remains. Shared
configuration groups are checked again after naming eligibility, so a failed
name check cannot produce a partial node plan or Network Bay pair.

## Validation scope

Controller tests use envtest with a real Kubernetes API server. They cover
partial discovery, failed-status retention and recovery, stale endpoint and
replacement identity rejection, shared-plan deferral, maintenance/reboot gates,
and failed interface-name reads. Naming bridge tests cover NCO policy and delegate
host filesystem/udev behavior to the library's tests.

Hardware qualification uses isolated discovery-only controllers and read-only
interface-name queries on hosts with DOCA 3.6. It does not deploy the full daemon,
rename host interfaces, change NIC configuration, reset hardware or start services.
The daemon image's existing DOCA version is not changed by this patch; compatibility
with that image remains a separate deployment qualification.

The discovery-only manager was exercised on both `mevo-carmel-1` workers with
DOCA DMS 3.6.0078-1 and MFT 4.38.0-134. All nine ASIC records retained their
original UIDs across scoped metadata failure, incomplete inventory and recovery.
Only the affected ASIC became unready for a scoped failure; incomplete inventory
marked all records on that host unready while the other host remained ready.
Existing operators were temporarily suspended for isolation and restored, and
test resources were removed with boot IDs and node scheduling unchanged.

The live binary preceded the deterministic error ordering, namespace ownership
and missing-record freshness follow-ups; those changes are covered by local
controller tests. Its chroot execution could not resolve the existing `/host`
OFED-version lookup, so recommended-firmware conditions were not qualified.
