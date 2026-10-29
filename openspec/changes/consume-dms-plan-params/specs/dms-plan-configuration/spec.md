## ADDED Requirements

### Requirement: Consume explicit shared plan input
The daemon SHALL resolve an explicit namespaced PlanRef using the structured blueprint and the current own-host DMSPlanParams binding. Ordinary configuration SHALL remain usable without the DMSPlanParams CRD.

#### Scenario: Current host with unrelated unresolved hosts
- **WHEN** the selected host has matching UID, generation, blueprint digest and Resolved status
- **THEN** its plan can compile even when aggregate resolution is false for another host

#### Scenario: Changed reference input
- **WHEN** the binding generation, UID or digest is stale, or the reference is missing
- **THEN** the daemon reports an input error before configuration writes

### Requirement: Keep execution within selected devices
The controller SHALL compile full inventory once per reference and SHALL pass only NCO-selected devices to execution. All NV option partitions SHALL validate before any write.

#### Scenario: One selected rail in a two-rail plan
- **WHEN** NCO selects one ASIC and the shared plan binds both rails with a per-device step barrier
- **THEN** only the selected ASIC appears in NV and runtime requests

#### Scenario: Incomplete coupled selection
- **WHEN** the library rejects a selected subset or Network Bay pair
- **THEN** the controller does not apply any NV partition

### Requirement: Preserve template and activation semantics
The adapter SHALL retain raw override precedence, explicit zero/false intent and all per-device apply errors and reboot requirements. A missing template SHALL contribute no template intent. Existing maintenance and reboot handling SHALL remain authoritative.

#### Scenario: Partial apply requires activation
- **WHEN** a device row requires reboot and the operation also returns an error
- **THEN** the device's reboot requirement remains recorded while the failure is returned

#### Scenario: Reset invalidates compiled runtime bindings
- **WHEN** the existing opt-in firmware reset succeeds after NV apply
- **THEN** the reconciliation ends before using the captured plan for runtime

### Requirement: Preserve consumer process ownership
NCO SHALL retain its existing CC process startup and monitoring at the applicable runtime group. Hardware configuration status SHALL NOT assert readiness of an undefined ARC service contract.

#### Scenario: Existing CC process is lost
- **WHEN** the existing termination notification triggers reconciliation
- **THEN** the applicable CC runtime group can invoke the existing tracked launcher again

#### Scenario: Existing CC process is running
- **WHEN** the tracked CC process is already running
- **THEN** ordinary library Apply performs observation and convergence without a forced startup replay, and actual errors remain failures

#### Scenario: Successful configuration drift check
- **WHEN** plan configuration completes successfully
- **THEN** the daemon retains the existing 20-minute reconciliation interval

### Requirement: Explicit external ownership
The daemon SHALL reject unknown required services, artifacts, package-runtime groups and external dependencies before configuration writes. Known host-k8s ownership SHALL reuse the existing provisioning, SPX/SR-IOV and CC boundaries without introducing a downstream readiness cycle.

#### Scenario: Unknown package-owned group
- **WHEN** a plan contains a required external group outside the supported ownership contract
- **THEN** reconciliation fails before NV or runtime application and does not record successful configuration

#### Scenario: CC service without operation endpoints
- **WHEN** a plan requires the existing CC service but provides no eligible compiled CC operation endpoints
- **THEN** reconciliation reports the missing startup scope instead of claiming unchanged configuration

### Requirement: Ordinary template delegation
Ordinary templates SHALL use library NV/runtime execution with NCO translation and status policy. Reset-to-default and ordinary forced NV application SHALL preserve their bounded compatibility paths.

#### Scenario: Asymmetric native parameter support
- **WHEN** an ordinary raw parameter is supported by one PF and absent on its sibling
- **THEN** only the rejecting PF omits that assignment and the device retains its unsupported-parameter status

#### Scenario: Pure fixed-link translation
- **WHEN** template intent is translated for a fixed-link device
- **THEN** translation consumes explicit observed link-type input and performs no host read

#### Scenario: Independent ordinary runtime and unbound PFs
- **WHEN** an ordinary ASIC fails carrier checks while another is eligible
- **THEN** the eligible ASIC still receives runtime configuration
- **AND** ordinary unbound PFs retain the existing per-port skip policy while PCI MRRS and basic QoS remain requested
- **AND** compiled plan runtime intent is never filtered by this ordinary-template policy

#### Scenario: Ordinary fixed-link PF is unbound
- **WHEN** an ordinary fixed-link NIC has a PF without a netdev
- **THEN** NV translation skips that PF's link-type observation and preserves its NV intent
- **AND** bound PFs still require valid observed link types

#### Scenario: Network Bay peer fails carrier validation
- **WHEN** ordinary Network Bay ASICs share an NV configuration group and one fails carrier validation
- **THEN** the healthy ASIC still receives its own runtime configuration
- **AND** NV validation and application retain the paired group

### Requirement: Shared plan replaces obsolete Spectrum-X configuration
The operator API SHALL use planRef and DMSPlanParams for Spectrum-X configuration. The obsolete spectrumXOptimized field, typed options and duplicate NCO planning APIs SHALL be removed.

#### Scenario: Strict client sends removed configuration
- **WHEN** a template or device containing spectrumXOptimized is submitted with strict field validation
- **THEN** the API rejects the unknown field
