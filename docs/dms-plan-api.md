<!-- Copyright 2026 NVIDIA CORPORATION & AFFILIATES -->

# Shared DMS plan API

The shared `DMSPlanParams` kind is namespaced (`dms.nvidia.com/v1alpha1`).
Its `blueprintRef` names a same-namespace ConfigMap containing structured YAML
in `data["blueprint.yaml"]`. Spectrum-X Operator installs its CRD and will own
host target-map reconciliation. The schema comes from GitLab dms-blueprints-lib
revision `4f00c0b5b6f9529c98c3cc1d65a52807d5a9bf47`, `lib/api`.

NCO references a plan by name with `spec.planRef: spectrum-x-plan` on
NicConfigurationTemplate. The template, NicDevice and DMSPlanParams must share
a namespace. NicDevice carries the name in `spec.configuration.planRef`.
SpectrumXRailPoolConfig references a plan in its own namespace with
`spec.dmsPlanParamsRef`.

Plan references are enabled by the DMS library integration. NCO accepts a
plan-only template or ordinary template settings as explicit overrides, and
rejects combining a plan reference with resetToDefault. The obsolete
`template.spectrumXOptimized` section is removed. Spectrum-X Operator obtains
xplane service configuration from the selected plan while preserving its
existing rail-networking inputs and readiness gates.
