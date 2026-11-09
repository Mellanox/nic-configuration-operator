## Why

Plan-controller tests now execute the installed dms-cli/planner. The generic Go
runner does not contain that runtime and cannot exercise the existing assertions.

## What Changes

- Run the complete existing `make test` suite in an immutable official DOCA 3.6
  Linux/amd64 container with the repository's existing registry credentials.
- Use the existing private-module credential only for test dependency downloads.
- Preserve the reusable build, lint and generated-file validation jobs and the
  existing coverage upload. Route tests to the runtime job without skipping cases.

## Capabilities

### New Capabilities

- `stock-cli-controller-tests`: full hosted controller testing with real CLI.

### Modified Capabilities

None.

## Impact

One repository-local CI job. The independently pre-existing private-module
access failure in reusable jobs remains tracked by issue #571. No shared workflow
framework, runtime image overlay or operator production changes are included.
