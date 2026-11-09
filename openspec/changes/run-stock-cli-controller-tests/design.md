## Decisions

Use a job container pinned to the immutable stock DOCA 3.6 image digest. Container registry
credentials reuse the existing NVCR secret names. Install only Go test prerequisites,
then use the existing `make test` recipe and its pinned envtest setup. Real planning
cases and coverage remain part of the full test run.

Private module authentication uses an environment-backed Git credential helper;
the token is neither embedded in URLs nor written into configuration. Checkout
must disable persisted credentials so its repository-scoped token cannot mask
that helper. Other reusable jobs retain their existing commands and behavior.

## Verification boundary

Local stock-runtime test results and hosted job completion are recorded separately.
The existing reusable build failure caused by private-module access remains an
independent repository issue; this test job does not silently disable those checks.
