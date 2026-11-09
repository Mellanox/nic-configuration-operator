## Why

The shared library now plans through the installed dms-cli. NCO must consume a
compatible revision while preserving its existing firmware, naming, ordinary NV
and runtime, and controller ownership contracts.

## What Changes

- Pin the published NCO validation composite built from the existing library
  dependency and the reviewed CLI planner revision, preserving NCO feature APIs.
- Add the planning action to the controller test YANG fixture so tests exercise
  the actual local CLI with their authored blueprint.
- Validate the full Linux consumer suite and Linux/amd64 manager and daemon
  images with the existing packaged resolver workaround.

## Capabilities

### New Capabilities

- `cli-planner-consumer`: compatible dependency and real CLI test coverage.

### Modified Capabilities

None. Operator APIs and lifecycle ownership remain unchanged.

## Impact

Go module pins, controller test schema, and validation documentation. Requires an
installed compatible dms-cli/planner for plan-controller tests. No DPF, service
startup, host package, firmware/reset, or xplane delivery changes are included.
