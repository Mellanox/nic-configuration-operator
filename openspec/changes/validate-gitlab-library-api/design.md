# Design

Keep existing library imports with exact versioned GitLab replacements. Go does
not inherit dependency replacements, so the consumer also pins the canonical
`nvidia.com/doca/dms/doSpcx` module to the library's published source revision.
The core planner is linked into the operator. No runtime planner executable or
build-dms-planner helper is needed. Existing DMS commands still use dms-cli.

NCO uses the established configuration.Manager/NewManager API; no separate
NCOManager interface is introduced. Discovery, configuration, naming and firmware
remain the existing library implementations. Reconciliation behavior is not
expanded. Unknown service contracts and unavailable host-k8s payloads remain
explicit errors; Xplane generation is excluded from hardware validation.

Authenticated private GitLab access is required. Clean-container anonymous
repository and module retrieval fails; public CI needs supported credentials or
an authorized access change. This change does not alter project visibility.

Validate build, existing tests, race tests, lint and generation separately from
image publication and hardware qualification. Previous subprocess/stub-based
image results do not qualify this version.
