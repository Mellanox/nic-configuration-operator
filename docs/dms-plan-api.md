# Shared DMS plan API

The shared `DMSPlanParams` kind is namespaced (`dms.nvidia.com/v1alpha1`).
Its `blueprintRef` names a same-namespace ConfigMap containing structured YAML
in `data["blueprint.yaml"]`. Spectrum-X Operator installs its CRD and will own
host target-map reconciliation. The schema comes from GitLab dms-blueprints-lib
revision `4f00c0b5b6f9529c98c3cc1d65a52807d5a9bf47`, `lib/api`.

NCO references the plan with `spec.planRef: {namespace: ..., name: ...}` on
NicConfigurationTemplate; NicDevice.configuration carries the same reference.
SpectrumXRailPoolConfig references a plan in its own namespace with
`spec.dmsPlanParamsRef`.

This API preparation does not enable plan execution. Admission rejects nonempty
plan references until the dependent library integration is installed. Existing
configuration remains unchanged. The integration removes those temporary
restrictions and NCO's obsolete `template.spectrumXOptimized` section together
with its implementation, so this API-only change remains buildable and safe.
