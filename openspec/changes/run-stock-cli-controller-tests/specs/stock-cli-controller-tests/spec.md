## ADDED Requirements

### Requirement: Exercise the full controller suite with the stock CLI

Hosted CI SHALL run the existing complete `make test` recipe with an installed
stock DOCA CLI/planner and envtest assets. It SHALL preserve coverage reporting
and SHALL NOT skip or mock real controller planning cases.

#### Scenario: Validate a consumer dependency update

- **WHEN** CI tests a consumer revision that plans through dms-cli
- **THEN** the official immutable Linux/amd64 DOCA runtime provides the CLI
- **AND** the existing controller assertions execute against that runtime

### Requirement: Preserve authentication boundaries

The test job SHALL use existing registry and private-module secrets without
printing tokens or persisting token values in URLs or Git configuration.

#### Scenario: Download the private library

- **WHEN** Go resolves the published private dependency
- **THEN** Git obtains the credential from the test step environment
- **AND** reusable build, lint and validation jobs retain their existing behavior
