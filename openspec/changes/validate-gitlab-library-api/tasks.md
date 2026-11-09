## 1. Adapt and package
- [x] 1.1 Pin root/API to the published GitLab revision with versioned replacements.
- [x] 1.2 Select NCOManager at existing type, constructor and test-double sites; refresh the controller-test CRD from the same published API.
- [x] 1.3 Package the preserved same-version doSpcx planner without copying its source.
- [x] 1.4 Document unsupported hardware behavior and retain existing feature assertions.

## 2. Verify and deliver
- [x] 2.1 Run Linux build, full existing non-E2E tests and race checks; retain all unsupported failures.
- [x] 2.2 Run pinned lint, generation cleanliness and strict OpenSpec validation.
- [ ] 2.3 Obtain exact-diff review before commit or PR publication.
- [ ] 2.4 Build/push Linux/amd64 images and record immutable identities and planner smoke results.
