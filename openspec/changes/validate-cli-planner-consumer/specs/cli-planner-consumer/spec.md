## ADDED Requirements

### Requirement: Consume a compatible published CLI planner revision

NCO SHALL use a published library revision that preserves its existing firmware,
naming, ordinary configuration, runtime, and plan ownership APIs. Product Go
module files SHALL NOT depend on sibling-workspace replacements.

#### Scenario: Build the consumer independently

- **WHEN** the canonical manager and daemon Dockerfiles build from NCO alone
- **THEN** the published dependency resolves without sibling directories
- **AND** both images target Linux/amd64

### Requirement: Test the real planning boundary

Controller plan tests SHALL include the planning action in their local YANG
fixture and execute against a compatible installed CLI/planner. Validation SHALL
separate offline test results from hardware runtime qualification.

#### Scenario: Compile a fixture-backed controller plan

- **WHEN** a controller test compiles its authored blueprint through dms-cli
- **THEN** the local schema resolves the blueprints planning action
- **AND** the existing selection, ordering, and ownership assertions run
