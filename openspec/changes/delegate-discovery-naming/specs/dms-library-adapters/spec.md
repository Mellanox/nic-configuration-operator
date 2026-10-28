# DMS Library Adapters

## Purpose

Define NCO's discovery and naming adaptation to dms-lib while preserving existing controller lifecycle and consumer policy.

## ADDED Requirements

### Requirement: Discovery adaptation preserves NCO record ownership

NCO SHALL obtain native inventory from dms-lib and project metadata, PF endpoints and explicit peer identities into the existing records keyed by physical PCI address. It SHALL retain the existing discovery/reporting interfaces and expose `NewDeviceDiscovery(nodeName)` without the obsolete NVConfigUtils dependency.

#### Scenario: Complete inventory

- **WHEN** library discovery returns complete inventory with usable ASIC records
- **THEN** NCO projects their metadata and ports without using serial numbers as CR identity or independently inferring Orchid peers

#### Scenario: Scoped issue with skipping enabled

- **WHEN** `SKIP_DEVICE_ON_DISCOVERY_ERROR=true` and discovery reports an ASIC-scoped issue, including an unresolved Orchid peer
- **THEN** the adapter omits that ASIC, reports its physical PCI key through the partial-discovery reporters and returns healthy records so the existing controller retains the affected CR

#### Scenario: Global or required discovery failure

- **WHEN** discovery fails, inventory is incomplete, an issue cannot be scoped, or required discovery fails without the skip switch
- **THEN** the adapter returns an error and the existing discovery controller does not treat that scan as evidence of device disappearance

### Requirement: Naming delegation retains consumer policy

NCO SHALL retain template selection, placeholder expansion, shared-RDMA naming and future-PF policy while delegating physical-name validation and udev operations to dms-lib. The existing controller readback flow SHALL propagate library query errors without replacing failed reads with empty names.

#### Scenario: Explicit naming assignments

- **WHEN** NCO resolves a naming template
- **THEN** it supplies explicit PCI/netdev/RDMA assignments to the library, preserving the first-PF policy for repeated shared RDMA names

#### Scenario: Readback failure

- **WHEN** the library cannot read interface names for a PF
- **THEN** NCO returns the error and retains that PF's previously recorded names

### Requirement: Extraction preserves controller lifecycle

The migration SHALL preserve the existing discovery controller and firmware, NV/runtime, reset, maintenance and CC reconciliation paths; it SHALL NOT add discovery freshness conditions or a new lifecycle gate.

#### Scenario: Normal reconciliation after migration

- **WHEN** the daemon reconciles its existing intents
- **THEN** discovery and naming use the library adapters while the existing controller lifecycle and configuration implementations remain in effect
