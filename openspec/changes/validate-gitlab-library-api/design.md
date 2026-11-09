## Context
The preserved upstream planner and the established Go API are published in one
GitLab revision. Hardware capabilities unavailable upstream are explicit stubs.
NCO's existing request-based runtime signatures use `NCOManager`, while upstream
`Manager` keeps its original signatures.

## Goals / Non-Goals
Build NCO and its planner from published immutable source, preserve existing
consumer assertions and expose missing capability errors. Do not restore hardware
implementations, alter preserved doSpcx sources, skip feature assertions, or add
production daemon flags.

## Decisions
Keep established GitHub import paths with real versioned GitLab replacements.
Derive the doSpcx module version from the root replacement, download it through
Go's module tooling and build `./planner` with `-mod=readonly`. Package the binary
at the library's fixed executable path in the daemon image. The manager does not
compile plans and does not need this additional binary. Private downloads use a
BuildKit secret; credentials are never copied or passed as build arguments.

## Risks / Trade-offs
Existing feature suites can fail because discovery, hardware application and
plan requirement projection are unsupported. Preserve those assertions and retain
full failures, distinguishing them from introduced compile or packaging errors.
Image construction is not hardware qualification. Carmel validation must remain
isolated from the production daemon and report unsupported capabilities explicitly.

## Migration Plan
Run Linux/amd64 build, existing unit/controller/envtest and race suites, pinned
lint and generation checks. Review the exact diff before committing or publishing
a PR. Build immutable manager/daemon images with the pinned DOCA base and retain
module, planner, architecture and digest identities.
