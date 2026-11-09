## Context

The CLI planner candidate follows upstream library main, while NCO's existing
library pin includes additional firmware, naming, and runtime APIs. A direct pin
would not compile. The validation composite combines those exact histories;
its identity and compatibility limits must remain explicit.

## Decisions

Keep consumer production code unchanged. Preserve its public dependency APIs on
the separate composite and select a real published pseudo-version for standalone
Docker builds. The API submodule pin stays unchanged.

Use the installed stock dms-cli/planner for controller integration tests, with a
minimal planning-action schema in the existing test fixture. Record preliminary
local-source checks separately from final published-pin validation.

Keep canonical Dockerfiles and the existing PF netdev resolver workaround. Build
both images for Linux/amd64 against one immutable DOCA runtime base and record
source, platform, digest, CLI/planner, and helper identities.

## Risks and Limits

The public CLI omits implicit generated rename artifacts; the composite documents
that limit and preserves selected services and explicit group requirements.
Local checks and offline CLI fixtures do not establish hardware qualification.
The coordinator owns any separately authorized runtime deployment.
