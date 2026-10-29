## ADDED Requirements

### Requirement: Preserve NCO firmware selection policy
NCO SHALL retain source readiness, cache selection, early burned-version no-op
policy and the existing fallback when version observation fails.

#### Scenario: Requested version is already burned
- **WHEN** the burned version matches the requested version
- **THEN** installation returns before resolving a cached artifact
- **AND** running-version activation decisions remain with the controller

#### Scenario: Current version query fails
- **WHEN** firmware version observation fails during an installation request
- **THEN** NCO continues with artifact resolution and library validation before installation

### Requirement: Preserve activation ownership
NCO SHALL retain maintenance, conditions, update policy and host reboot ownership
while delegating host operations to the firmware library.

#### Scenario: Firmware installation succeeds
- **WHEN** the controller installs firmware with reset skipped
- **THEN** BlueField retains host reboot handling and ConnectX retains firmware reset with reboot fallback

### Requirement: Remove DMS transport dependency
NCO SHALL use library firmware and runtime operations without starting dmsd or
using the removed pkg/dms client/server interfaces.

#### Scenario: Compatibility runtime request
- **WHEN** an existing caller applies ordinary runtime configuration
- **THEN** NCO translates intent using observed device relationships and delegates execution to the library
- **AND** existing carrier and unbound-PF policy remains in effect
