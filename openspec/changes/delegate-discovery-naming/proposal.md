# Proposal

## Why

NCO duplicates the hardware discovery and naming operations now provided by dms-lib. Replacing those implementations reduces maintenance while retaining NCO's existing controller lifecycle and template policy.

## What Changes

- Replace native discovery with a thin library-inventory adapter and retain the existing partial-discovery reporters.
- Delegate naming validation and udev operations to the library; retain NCO's placeholder, shared-RDMA and future-PF policy.
- **BREAKING:** change `NewDeviceDiscovery(nodeName, nvConfigUtils)` to `NewDeviceDiscovery(nodeName)` and remove the unused `DeviceDiscoveryUtils` API.

## Capabilities

### New Capabilities

- `dms-library-adapters`: records the discovery/naming integration contract, including retained controller behavior and the constructor migration.

### Modified Capabilities

None; this repository had no existing OpenSpec specifications.

## Impact

PR [#572](https://github.com/Mellanox/nic-configuration-operator/pull/572) changes `pkg/devicediscovery`, `pkg/udev`, daemon wiring and interface-name readback. DPF must update its constructor call when adopting this NCO version. No configuration CRD or controller lifecycle changes are included. Private module access remains tracked in [#571](https://github.com/Mellanox/nic-configuration-operator/issues/571).
