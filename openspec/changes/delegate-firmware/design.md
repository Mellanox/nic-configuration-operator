## Context
Firmware host tooling and DMS transport remain in NCO after plan delegation.

## Goals / Non-Goals
Preserve behavior while moving generic operations to the shared library. Do not
introduce upgrade policies, device freshness conditions or additional services.

## Decisions
Keep source/cache and early burned-version idempotence in NCO; inject the library
manager for inspection, installation and reset. Reuse library runtime intent for
the remaining exported configuration entry point. Remove DMS process startup and
client registries once all consumers have moved.

## Risks / Trade-offs
BlueField installation must preserve lifecycle and selected-device scope. Active
flashing cannot be safely cancelled by rebinding PFs early. Prior traffic testing
does not establish firmware flashing parity; command and policy coverage must be
reported separately from hardware qualification.

## Migration Plan
Pin a published firmware library commit, validate affected and full non-deployment
suites, obtain council review, and target `dms-lib-integration`. External Go callers
require coordinated constructor updates.
