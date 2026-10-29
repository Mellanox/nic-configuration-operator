## Why
NCO's remaining DMS package owns firmware transport and duplicate runtime QoS
operations. Delegate host operations to dms-lib while preserving consumer policy.

## What Changes
- Delegate firmware inspection, installation and reset to dms-lib.
- Delegate compatibility runtime configuration through the existing library API.
- Remove DMS client/server code and daemon lifecycle wiring.
- Preserve source/cache, maintenance, update policy and reboot behavior.

## Capabilities
### New Capabilities
- `firmware-delegation`: NCO firmware policy over the shared library.

## Impact
NCO Go constructors and dependency pin change. No CRD migration, new feature or
DPF source change.
