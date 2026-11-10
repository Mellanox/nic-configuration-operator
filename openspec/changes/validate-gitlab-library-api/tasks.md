## Implementation
- [x] Remove planner executable build helper and Docker packaging.
- [x] Retain established library consumer interfaces.
- [x] Pin library/API f89d1f2937ff and shared planner 54bb3fa91b4c.

## Validation
- [x] Build and run existing operator tests and race tests on Linux/amd64.
- [x] Run lint and generated-file checks (both pass; generation unchanged).
- [ ] Publish updated draft PR with exact results and remaining limits.
- [ ] Build exact-pin images and perform separately attributed hardware validation.
