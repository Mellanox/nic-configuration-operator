## ADDED Requirements

### Requirement: Published shared Go dependency
The operator SHALL use exact published GitLab library, API and doSPCX module
versions without a sibling checkout or separately packaged planner executable.

#### Scenario: Build an operator image
- **WHEN** the builder resolves the pinned modules with authorized GitLab access
- **THEN** the existing operator imports compile against the library API
- **AND** the shared Go planner is linked into the operator binary
- **AND** no planner executable is installed in the runtime image

### Requirement: Preserve reconciliation behavior
The dependency migration SHALL retain existing controller semantics and SHALL
report unsupported service payloads honestly without inventing configuration.

#### Scenario: Resolve host planning intent
- **WHEN** the operator uses DMSPlanParams and a structured blueprint
- **THEN** it calls the established library planning API in process
- **AND** no new rollout coordination or service lifecycle behavior is introduced
