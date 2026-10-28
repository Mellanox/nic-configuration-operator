# Design

## Context

NCO owns Kubernetes records and naming intent. dms-lib owns native discovery, ASIC pairing, name validation and udev execution. The existing discovery controller already retains CRs reported as skipped or incomplete.

## Goals / Non-Goals

**Goals:** remove duplicate native operations, preserve existing controller flow and expose library errors through the current interfaces.

**Non-Goals:** fresh-inventory gates, new status conditions, maintenance/reset changes, NV/runtime migration, service ownership changes or deployment qualification.

## Decisions

1. Keep `DeviceDiscovery`, `SkippedDeviceReporter` and `IncompleteDeviceReporter`; adapt library inventory inside `pkg/devicediscovery`. This avoids controller-side inventory state and preserves physical-PCI CR identity. Process scoped issues before projecting Orchid peers.
2. Remove the constructor's obsolete NVConfigUtils argument rather than silently ignoring it. The known DPF caller requires coordinated migration; DPF is outside this PR.
3. Keep NCO naming expansion and future-PF policy, then pass explicit assignments to the library. Preserve changed-file-only activation and the controller's existing readback flow; return interface-name query errors without publishing empty names for failed reads.
4. Retain the existing firmware, configuration, maintenance and CC paths. A second reconciliation framework is unnecessary for this extraction.

## Risks / Trade-offs

- The constructor and utility API removal is a consumer breaking change, documented in `docs/dms-library-integration.md`.
- Public hosted CI and cold builds need access to the private library module (#571).
- Local Linux builds, unit/envtest tests, race checks and lint passed. Earlier read-only lab discovery used an older controller adapter; it does not qualify this final controller, naming activation or the unchanged daemon image.
- OpenSpec validation checks document structure only. These artifacts record the implemented PR contract and remain an active change until normal PR completion.
