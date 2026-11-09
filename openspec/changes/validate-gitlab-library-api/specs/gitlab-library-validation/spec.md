## ADDED Requirements

### Requirement: Reproduce published upstream source
NCO SHALL resolve the library root, API and preserved doSpcx planner from the same
published GitLab revision using versioned module dependencies without sibling paths.

#### Scenario: Build an image in an isolated context
- **WHEN** the canonical Dockerfile builds with authorized private-module access
- **THEN** the planner is downloaded at the root replacement version and installed at the library's fixed executable path
- **AND** credentials are supplied only through an ephemeral BuildKit secret

### Requirement: Select the NCO request interface explicitly
NCO SHALL use NCOManager for its established request-based runtime methods while
retaining the existing feature assertions and consumer policy.

#### Scenario: An upstream hardware capability is unavailable
- **WHEN** discovery or a hardware configuration adapter reports unsupported
- **THEN** validation retains that failure and does not report successful hardware qualification
- **AND** existing feature assertions are neither rewritten nor skipped
