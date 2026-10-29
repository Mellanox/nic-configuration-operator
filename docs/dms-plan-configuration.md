# Shared DMS plan configuration

Spectrum-X configuration uses `DMSPlanParams` and a structured ConfigMap containing `data["blueprint.yaml"]`. The Spectrum-X Operator resolves host target maps. NCO consumes the selected host's binding and applies hardware configuration to the ASICs selected by its existing NIC selector.

```yaml
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicConfigurationTemplate
metadata:
  name: spectrum-x
  namespace: network-operator
spec:
  nicSelector:
    nicType: "1023"
    pciAddresses: ["0000:64:00.0"]
  planRef:
    namespace: default
    name: spectrum-x-plan
  template:
    numVfs: 1
    linkType: Ethernet
    pciPerformanceOptimized:
      enabled: true
      maxReadRequest: 4096
```

The API addition is:

```go
type DMSPlanReference struct {
    Namespace string `json:"namespace"`
    Name      string `json:"name"`
}

// Both NicConfigurationTemplateSpec and NicDeviceConfigurationSpec contain:
// PlanRef *DMSPlanReference `json:"planRef,omitempty"`
```

`planRef` has required `namespace` and `name` fields and is copied unchanged into `NicDevice.spec.configuration.planRef`. The `template` field may be omitted for a plan-only request. An existing template's `numVfs: 0` remains intent to configure zero VFs. `force` retains its existing meaning. Reset-to-default cannot be combined with a plan reference and keeps its existing separate flow.

`spectrumXOptimized` (including `enabled: false`) and the old tarball ConfigMap input are obsolete. Use planRef and DMSPlanParams instead. NCO no longer runs the tarball data reconciler or its duplicate plan compilation path. Ordinary templates without a plan reference use the same library execution flow with no compiled plan. NCO preserves omitted-default translation and filters unsupported raw parameters separately on each observed PF before delegating writes. Reset-to-default and ordinary forced NV updates retain narrowly bounded compatibility paths; ordinary runtime still uses the library. The typed `SpectrumXOptimizedSpec`, NCO planner API and tarball installation API are removed. External Go consumers, including DPF, require coordinated migration to dms-lib; this change does not edit those repositories.

The daemon reads DMSPlanParams and the blueprint directly, without a mandatory watch on the optional CRD. Successful plan reconciliation uses the existing 20-minute configuration drift check; reference errors use the existing controller retry behavior. A binding must match the current node UID, plan generation, exact blueprint digest and own-host Resolved condition. Aggregate failure for another host does not block a valid own-host binding. No new device freshness condition is introduced.

Full inventory is used for compilation, while requests contain only selected ASICs. NCO translates its template into raw NV overrides and per-PCI runtime intent. Raw overrides win and retain indexed/future-PF names. Omitted optimization fields do not reset authored plan values; explicitly disabled fields preserve their default-restoration meaning. The library owns plan interpretation, Network Bay applicability and native operations. Invalid coupled subsets fail validation before writes.

Existing maintenance, reboot, uptime guards and status reporting remain in the controller. Partial apply errors preserve reboot requirements. A successful opt-in firmware reset ends the pass before runtime uses old bindings. Native prerequisite and unsupported runtime errors remain visible. In particular, generic TrafficClass does not invent a native control index for HWPLB; authored TC mappings retain their explicit indices.

NCO's existing CC process launcher and termination monitoring remain consumer-owned for release compatibility until the ARC ownership contract is implemented. The library invokes that launcher at the compiled CC group boundary. A missing process defers CC observation until ordered startup. With a running process, ordinary Apply queries current state and skips converged hardware writes; temporary CMA observation setup does not force replay. Actual observation or apply errors remain failures, and process termination still triggers reconciliation. Compiled configure readiness is checked before the existing runtime spec-change reboot. Hardware configuration conditions do not certify ARC readiness.

Install updated NCO CRDs and use matching manager and daemon images. The Spectrum-X Operator owns installation of DMSPlanParams; ordinary NCO configuration still starts without that CRD. Canonical Dockerfiles accept an optional BuildKit `netrc` secret for the private module download. Local qualification drivers are excluded from image contexts. The default DOCA base remains unchanged; testing with an explicit DOCA 3.6 host image does not establish compatibility with every older image.

Unit tests cover template translation, including zero/false and raw precedence. Controller envtest covers binding identity, selected-rail projection, result errors, option grouping and reset ordering. Source tests and image builds are separate from actual lab daemon qualification.

## Lab qualification: 2026-10-28–29

On mevo-carmel, matching Linux/amd64 manager and daemon images from NCO `473260a50f5a` with dms-lib `dcb65a51548e` ran against Network Operator `v26.10.0-rc.3`, using an explicit DOCA HOST 3.6.0078 override. The final controlled rollout completed runtime configuration at 22:54 UTC. The NCO canary selected worker01 rail0; worker02 retained the stock daemon and was not rebooted, preserving its unrelated pending BlueField activation.

The tested blueprint excludes only the PF-scoped CMA ToS operation that this switchdev RAW_PACKET representor cannot fulfill: the driver does not register that device with RDMA-CM. The same native query fails on the stock host; legacy DMS incorrectly reports a value through its global RDMA fallback. See dms-lib [issue 15](https://github.com/Mellanox/dms-lib/issues/15) and [issue 16](https://github.com/Mellanox/dms-lib/issues/16). Native TC[1], authored VF ToS intent, CC, all NV preparation and xplane content remain unchanged. This scoped result does not qualify the original PF-CMA intent; the library continues to report that failure.

The final images passed all four IPv6 paths (two rails in both directions), 10/10 packets each. Both 10-second, 64 KiB RDMA-CM probes passed at 455.91 and 452.60 Gb/s. These are functional smoke tests, not a performance comparison. Earlier settled direct library Apply with the same MRRS 4096 intent returned `Unchanged` with no errors; its backend trace contained 25 GET and zero SET operations. Complete NV JSON snapshots for selected rail0 and unselected rail1 were unchanged. No live NV write was needed or exercised.

Controlled daemon restart and final rollout preserved both node boot IDs and the original SR-IOV NodeState UID. The SR-IOV controller was temporarily scaled from one replica to zero to avoid independently confirmed baseline [issue 233](https://github.com/Mellanox/sriov-network-operator/issues/233), where an empty recreated NodeState causes an offload-drop-in change and another reboot. After each controlled operation it was restored to one available replica; SR-IOV daemon/device-plugin and xplane workloads recovered to two ready instances each, and the NodeState reported `Succeeded`. This operational condition is part of the qualification, not a product fix for that baseline issue.

The overnight window ran from 22:57 UTC on 28 October to the 07:00 UTC cutoff on 29 October. It collected 462 state samples through 06:59:11 UTC and 92 successful four-path ping rounds through 06:56:47 UTC. No workload failures, pod UID changes, additional container restarts, node boots or binding failures were observed. Captured NCO logs contained 22 successful reconciliations and no ERROR entries. One 02:26 sample had three API collection timeouts, and log streams reconnected; these are explicit observation gaps, so the evidence does not establish uninterrupted visibility or a sample exactly at the cutoff.

Morning network probes passed all four paths at 10/10 packets and both RDMA-CM probes at 456.98 and 454.07 Gb/s (functional smoke only). Morning full NV JSON for both the selected and unselected rails exactly matched the pre-canary snapshots, with zero changed parameters. These results qualify the stated profile, hardware and controlled operational conditions, not the omitted PF-CMA intent or other platforms.

## Council corrections after overnight qualification

The daemon rejects external plan requirements outside the exact supported host-k8s ownership contract before configuration writes. Host provisioning owns boot-policy/GPUDirect-ACS/LLDP prerequisites, Spectrum-X/SR-IOV own eswitch and xplane, and NCO retains CC startup at compiled CC endpoints. Recognizing these owners does not certify service readiness or add an xplane-before-NCO startup gate. Unknown services, artifacts, groups and dependencies fail explicitly; a CC service without compiled startup endpoints also fails.

NV intent translation is pure: the caller supplies observed fixed link types explicitly. Ordinary templates retain independent per-ASIC carrier failure handling and their existing skip policy for per-port settings on unbound PFs; PCI MRRS and basic QoS remain requested, and compiled plan intent is never filtered. They retain per-PF unsupported-parameter status; unsupported intent on one PF is never removed from a supported sibling. Force keeps its historical ordinary-NCO reapply behavior through the NV compatibility path, while plan Force remains the library dependency option. Existing library limitations for unsupported shared-RDMA/HWPLB runtime scope remain errors; unsafe legacy RDMA-device fallback is not copied.

These source corrections follow the overnight run described above. The earlier lab results do not qualify the newly migrated ordinary-template path. Fresh source tests and council review are recorded separately in the PR.

## Merged-library image retest (2026-10-29)

The manager and worker01 canary daemon built from `3911584d7b47` use both library
modules at `v0.0.0-20261029094238-9db732a9f4ce`. That merged PR22 tip has the same
source tree as reviewed `24c8986`; it is not the current main branch. Worker02
retained the stock rc.3 daemon. The images used the same digest-pinned DOCA 3.6
base and Linux/amd64 platform as the preceding qualification.

The canary completed fresh NV validation without updates or reboot, restarted CC
through its existing owner, completed runtime application and released its wait
gate. Full native NV JSON on selected rail0 and unselected rail1 exactly matched
the pre-rollout snapshots. Both node boot IDs and SR-IOV NodeState UIDs remained
unchanged. The SR-IOV controller was temporarily paused during startup to isolate
baseline issue233, then restored; both SR-IOV DaemonSets returned to two ready
pods. Four IPv6 paths passed with zero loss and both RDMA-CM smoke probes passed
(64 KiB, 10 seconds; 455.68/453.05 Gb/s). These are functional checks, not a
performance qualification.

This uses the existing explicitly adapted structured blueprint, including its
omitted unsupported PF-CMA ToS leaf. It qualifies the plan-backed canary path;
ordinary-template policy is covered by the Linux controller/configuration race
suite. Test-only commit `020aaaf` subsequently fixes asynchronous assertion
synchronization without changing image production code.

## Obsolete Spectrum-X API removal (2026-10-29)

The `spectrumXOptimized` field and type, their schema validation rules, the
tarball-data controller and the duplicate planner/configuration paths are removed.
`pkg/spectrumx` retains the existing CC process lifecycle.
`NewConfigurationManager` now takes only the event recorder, DMS manager and NV
utilities. This is a breaking API removal with no legacy marker or migration
checks; external Go consumers require coordinated updates.

The removal passed Linux build, lint, affected API/controller/configuration/CC
race suites, schema-copy comparison and strict OpenSpec validation, with fresh
council review. The complete race suite still reports the unchanged DMS test
fixture race tracked in [NCO issue 575](https://github.com/Mellanox/nic-configuration-operator/issues/575).
The images and traffic results above precede this removal; it has not been
deployed to the lab.
