## Why

Spectrum-X configuration must consume the shared DMSPlanParams binding and structured blueprint instead of compiling a second tarball-based plan in NCO. The library owns plan interpretation and device operations; NCO retains template policy and its existing maintenance, activation and process lifecycle.

## What Changes

- Add an explicit namespaced `planRef` to configuration templates and propagate it to selected NicDevices. A plan-only template is valid.
- Resolve the current own-host DMSPlanParams binding and compile once per reference from full inventory, while executing only NCO-selected ASICs.
- Translate existing NCO template policy into library NV overrides and per-PCI runtime intent; preserve raw override precedence and result/error/reboot information.
- **BREAKING:** `spectrumXOptimized` and tarball blueprint input must migrate to PlanRef and structured ConfigMap input. Ordinary unreferenced templates delegate execution through the same library flow. Reset-to-default and ordinary Force NV semantics retain explicit compatibility paths; the obsolete typed Spectrum-X API and duplicate planner are removed, so external consumers require coordinated migration to dms-lib.
- Preserve the existing NCO CC process launcher for baseline compatibility until the ARC service contract replaces it.

## Capabilities

### New Capabilities

- `dms-plan-configuration`: explicit plan input, selected-device execution and existing lifecycle integration.

### Modified Capabilities

None. The discovery/naming extraction contract is unchanged.

## Impact

NCO API types, generated CRDs, template propagation, daemon configuration wiring, translators and canonical manager/daemon Dockerfiles change. Matching manager/daemon images and updated NCO CRDs are required. DMSPlanParams is owned by Spectrum-X Operator and remains optional for ordinary NCO operation. No DPF or DOCA source changes are included.
