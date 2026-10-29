# Firmware delegation to dms-lib

NCO uses the shared firmware package for querying burned/running versions,
inspecting BIN/BFB metadata, verifying and installing firmware, and resetting a
device. The daemon no longer starts `dmsd`, maintains DMS clients, or invokes
`dmsc` for firmware. The remaining compatibility runtime entry point uses the
existing library configuration API, allowing `pkg/dms` to be removed entirely.

NCO retains `NicFirmwareSource` readiness, download/cache layout, image selection,
maintenance, update policy, conditions and host reboot coordination. Its early
burned-version match still skips installation before resolving a cached path,
even when running firmware differs. Failure to query versions still permits an
installation attempt. Direct-file requests without a version inspect the image
before this comparison. ConnectX image version/PSID/bootability checks remain
mandatory in the library.

The controller still installs without an automatic firmware reset, then requests
a host reboot for BlueField or attempts its existing ConnectX reset with reboot
fallback. Direct firmware-manager calls retain their existing `SkipReset` return
behavior. The firmware utility's `SKIP_VM_CHECK` interpretation remains explicit;
the configuration reset path continues to omit that option.

BlueField retains full BFB provisioning. The library explicitly selects the
device's verified rshim, preserves installer preference and fallback, and restores
managed PF bindings after failures. Active flashing completes before cancellation
is returned and before PFs are rebound. Firmware mutations require the existing
NCO maintenance authorization; this refactor introduces no upgrade policy.

The NCO Go API changes constructors to accept library managers, and removes the
old DMS server/client interfaces. External Go consumers need coordinated updates.
No DPF source is modified.

Validation must distinguish mocked command/lifecycle tests and controller envtest
from real firmware flashing. Prior mevo-carmel traffic qualification predates this
firmware extraction and does not qualify the new firmware backend.
