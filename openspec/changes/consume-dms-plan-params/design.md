## Context

The existing daemon compiles doSPCX tarball data and performs plan execution through its own manager. Spectrum-X Operator now publishes per-host bindings for a structured blueprint. The migration must preserve selected rails and NCO's maintenance/reboot behavior without adding a discovery freshness framework or a new ARC readiness protocol.

## Decisions

`DMSPlanReference` contains required `Namespace` and `Name` strings. The same value is copied from `NicConfigurationTemplate.spec.planRef` to `NicDevice.spec.configuration.planRef`. Template policy is optional with a reference. An existing template with zero VFs still expresses zero VFs; an absent template contributes no overrides.

The controller uses its APIReader to read DMSPlanParams, the current Node and the referenced ConfigMap's exact `blueprint.yaml` bytes. It requires a matching own-host Node UID, observed generation, digest, inventory stage and Resolved condition. An unresolved unrelated host does not block this host. It does not install a mandatory informer for the optional CRD; the existing 20-minute successful reconciliation interval refreshes reference inputs, including a CRD installed after daemon startup. Error retries and CC termination events retain their existing behavior.

One full inventory and one compiled plan per reference are local to a reconciliation. Requests contain only selected ASICs. NV requests are partitioned by actual apply options and Network Bay intent; all partitions validate before any writes. The library checks projection and complete pairing. A per-device compiler alternative would duplicate work and obscure shared group ownership.

NCO reuses its template policy to produce raw NV overrides and explicit per-PCI runtime intent. Raw values have highest precedence, including future/indexed parameters. Omitted optimization blocks do not restore defaults over authored plan intent; explicitly disabled blocks preserve their existing default-restoration meaning. Ordinary unreferenced templates now use the same library group flow with no plan and restore omitted optimization defaults. Only reset-to-default and ordinary forced NV application retain explicit compatibility paths. Per-PF ordinary raw requests preserve unsupported-parameter policy independently on each observed endpoint, without dropping a supported sibling assignment. Existing external public manager consumers remain unchanged.

Existing condition, maintenance, uptime and reboot helpers consume library rows. A partial apply preserves each row's reboot flag before returning aggregate errors. A successful in-process reset ends the pass before runtime uses captured bindings. Runtime Apply owns supported native setup. When CC is already running, ordinary Apply performs observation and convergence without forcing replay. Only an absent CC process requests ordered replay with the startup hook. Apply errors remain errors, including a process exit during observation; existing termination notifications trigger another reconciliation. Compiled configure readiness is checked before the existing spec-change reboot. Existing consumer-owned CC startup and termination monitoring remain in NCO, ordered at the applicable runtime group; library code does not acquire process ownership.

## Risks and boundaries

This is a breaking Spectrum-X input migration, not a claim that the old tarball input remains supported by the daemon. The typed Spectrum-X configuration, tarball installer and duplicate planner APIs are removed. External callers require migration. Firmware and reset behavior are unchanged. Runtime limitations and native prerequisite failures are reported rather than silently skipped. The library's typed TrafficClass may reject unsupported HWPLB layouts; authored native TC mappings retain their exact selector semantics.

Canonical image builds can use an optional BuildKit netrc secret for the private Go dependency; credentials are not copied into layers. Default DOCA image versions remain unchanged. Lab image validation must state the explicit DOCA 3.6 override and distinguish source tests, image builds and actual daemon qualification.

## Council corrections

Before any writes, reject external plan requirements outside the exact host-k8s phase/group/kind/reference ownership contract. This is ownership validation, not a new service readiness protocol. Known downstream eswitch/xplane setup must not be made a prerequisite for NCO releasing its existing gate. CC service requirements need actual compiler-selected CC endpoints. Translation receives observed link types as explicit input instead of performing netlink reads internally.
