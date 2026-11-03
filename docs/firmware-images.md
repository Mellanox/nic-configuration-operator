# Firmware from OCI images

`NicFirmwareTemplate.spec.template.firmwareImages` selects firmware from immutable
OCI images without a `NicFirmwareSource` or persistent volume. Each image contains
one ConnectX firmware binary. The legacy source and BlueField BFB paths remain available.

```yaml
apiVersion: configuration.net.nvidia.com/v1alpha1
kind: NicFirmwareTemplate
metadata:
  name: connectx-firmware
  namespace: nvidia-network-operator
spec:
  nodeSelector:
    firmware-group: canary
  nicSelector:
    nicType: "1017"
  template:
    updatePolicy: Validate
    firmwareImages:
      - registry.example.com/firmware/connectx5:connectx5-MT_0000000012-16.19.1200@sha256:<manifest-digest>
```

Replace the example digest with the image's 64 lowercase hexadecimal SHA-256
manifest digest. `Validate` checks the source and installed version; `Update`
uses the existing maintenance, firmware installation, and reset workflow.
Model/PSID/version tags help administrators identify images. Actual PSID,
firmware version, and bootability are checked from the binary, independently of
tags and image labels. Duplicate PSIDs in one inventory are rejected.

Exactly one of `firmwareImages` or `nicFirmwareSourceRef` must be set. An image
inventory contains at most 32 references. BlueField updates continue to use BFBs
through `nicFirmwareSourceRef`.

## Ownership and node selection

The existing template controller selects nodes and NICs and copies the firmware
specification onto matching `NicDevice` resources. Each daemon asynchronously
acquires only references selected for its own devices. Changing a template does
not mutate the DaemonSet or replace daemon Pods. Network Operator and standalone
Helm retain sole ownership of their workload specifications; ordinary updates
and recreation preserve the static cache and credential configuration.

The image is consumed as data. Its entrypoint is never executed, and no container
is created for each firmware image. This avoids restart coordination for firmware
selection changes and allows different nodes to select different inventories.

Two background workers acquire images with bounded attempts and retry backoff.
Pending and failed images appear through the existing firmware source conditions
on affected devices. A pending acquisition does not hold a firmware validation
worker while waiting for network I/O. The existing node-level configuration gate
still waits until the requested firmware is ready on all devices.

## Data image contract

Build a scratch image with one layer containing only `/firmware/firmware.bin`
and optional parent-directory entries. The payload must be a nonempty regular
file of at most 512 MiB. The archive must not contain links, devices, sparse
files, whiteouts, extended tar metadata, or other files. Plain tar and gzip layers
are supported. The entire compressed layer is bounded, downloaded, and checked
against its SHA-256 descriptor before decompression or archive parsing. Extraction
writes only the fixed payload filename and never applies archive paths, modes,
owners, or extended attributes to the filesystem.

References must pin a concrete OCI or Docker schema-2 image manifest with one
layer. Indexes, multi-platform lists, external layers, and multi-layer root
filesystems are not supported. Use `docker buildx build --platform=linux/amd64
--provenance=false` or select the concrete platform manifest digest. Do not pin
an index produced by default build provenance settings.

The cache is private ephemeral storage. `firmwareImageCacheSize` sets both the
process budget and the Kubernetes `emptyDir` limit, defaults to `4Gi`, and must
be at least `2Gi`. The budget includes compressed download and extracted payload
staging; leave at least 1033 MiB beyond the desired payload sizes for another
acquisition. Ensure node and Pod ephemeral-storage limits accommodate the budget. Up to 128 references are tracked per node. Already acquired desired
images remain usable when new references exceed capacity. Removing obsolete
references frees capacity; a firmware operation holds its selected file until
all synchronous tool calls finish. Container restart discards and reacquires
the owned cache rather than trusting partially written or stale files. Pod
replacement loses the cache. Neither lifecycle requires a PVC. Changing deployment settings such as the
cache size or mounted Secret names performs the ordinary DaemonSet rollout;
wait until firmware operations have finished before making those changes.

## Authentication and trust

Set `firmwareImagePullSecrets` explicitly in the NCO chart, or under
`spec.nicConfigurationOperator` in `NicClusterPolicy`. These are names of
`kubernetes.io/dockerconfigjson` or `kubernetes.io/dockercfg` Secrets in the
operator namespace. Public registries need no Secret. Component `imagePullSecrets`
are not reused implicitly: those credentials authorize kubelet image pulls and
may be broader than the firmware registry account.

Secrets are projected as optional read-only directories without Secret API
permissions. A missing or invalid configured credential causes acquisition to
fail and retry; it does not prevent daemon startup. Files are reopened for each
attempt, so projected credential rotation is picked up without a Pod rollout.
Docker credential helpers and host Docker configuration are never used.
Credential entries match an exact registry hostname/port and optional repository
prefix; the most specific prefix wins. Wildcard credential hosts are unsupported.

HTTPS is required. The existing proxy environment and a configured CA bundle
apply to firmware downloads. Use `certConfig.name` to reference a ConfigMap
containing `ca-bundle.crt`; this adds the referenced CA certificates to system
trust through `FIRMWARE_IMAGE_CA_FILE`. CA data is reloaded for acquisition
attempts. Explicit `SSL_CERT_FILE` and `SSL_CERT_DIR` overrides remain available.
Use explicit mirrored image references in disconnected environments.

Application downloads do not inherit kubelet credential providers, runtime
mirrors, preloaded runtime images, or container-runtime signature policy.
Digest verification authenticates the selected bytes; it does not verify a
publisher signature. Environments requiring signature policy must cover firmware
references as well as executable container images.

## Library compatibility

`NewFirmwareManager(client, dmsManager, namespace)` retains its existing direct-file
and `NicFirmwareSource` behavior and starts no network work. Applications opting
into image sources can use `NewFirmwareManagerWithImages` with an explicitly
owned resolver. An acquired file must remain immutable and available until its
release function is called. The built-in store provides this lifetime through
in-process references and must be run and closed by its owner.
