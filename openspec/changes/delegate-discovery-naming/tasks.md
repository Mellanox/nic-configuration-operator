# Tasks

## 1. Implement the extraction

- [x] 1.1 Replace native discovery with the library adapter and retain partial-discovery reporting.
- [x] 1.2 Delegate naming operations, retain NCO naming policy and wire library readback.
- [x] 1.3 Remove obsolete native utilities/tests and document the constructor migration for DPF.

## 2. Verify the implemented scope

- [x] 2.1 Test metadata/ports, explicit peers, optional VPD, scoped Orchid errors and partial reporting.
- [x] 2.2 Run the full Linux non-deployment suite before the naming-only dependency update, then verify the final published pin with affected race tests, static build and lint.
- [x] 2.3 Obtain council review of the reduced implementation at `671b5de`; retain the documented deployment/hardware limitations.
- [x] 2.4 Record proposal, design and scenarios for the implemented PR and validate their OpenSpec structure.

The test and qualification boundary is documented in `docs/dms-library-integration.md`; these checked tasks do not assert hosted CI success or deployment qualification.
