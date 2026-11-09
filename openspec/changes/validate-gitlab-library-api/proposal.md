## Why
Validate NCO against the preserved upstream GitLab planner and established library
API without importing downstream hardware implementations or hiding missing capabilities.

## What Changes
- Pin root and API imports to published GitLab revision `954b6bd3f5aa` using versioned replacements.
- Select the request-based NCO configuration adapter explicitly.
- Build and package the preserved planner from that same published revision.
- Retain existing feature assertions and report unsupported behavior honestly.

## Capabilities
### New Capabilities
- `gitlab-library-validation`: reproducible GitLab API and planner packaging validation.

## Impact
Dependency resolution, configuration interface selection and image packaging change.
Refresh the controller-test DMSPlanParams CRD fixture from that same API revision. No new hardware
implementation, production daemon mode or cluster rollout is introduced.
