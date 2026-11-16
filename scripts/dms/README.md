# Temporary DMS netdev resolver workaround

The daemon image builds replace `iface_from_bdf()` in the installed DMS
`tools/configuration/_lib.sh` to exclude switchdev PF/VF/SF representors.
The remainder of the packaged library and its permissions are preserved.
The normal, Ubuntu STIG-FIPS and RHEL STIG-FIPS daemon images use the same fix.

Remove this directory, its Dockerfile steps and its CI test invocation once all
supported daemon base images/packages contain the upstream fix
(DMS Redmine #5352343, Gerrit 1534069).

Run the regression test with `bash scripts/dms/test-netdev-resolver.sh`.

## Custom DMS packages in GitLab CI

The 26.10 GitLab builds install custom DMS packages into the regular operator
and daemon images and the Ubuntu/RHEL STIG daemon images. STIG operator images
do not contain DMS.

Configure `DOCA_DMS_URL_26_10` and `DOCA_DMS_RPM_URL_26_10` as GitLab CI variables.
The DEB URL uses `{arch}` for `amd` or `arm`; the RPM URL points to the amd64
package. Pin `DOCA_DMS_VERSION` and the checksums `DOCA_DMS_SHA256_AMD64`,
`DOCA_DMS_SHA256_ARM64`, and `DOCA_DMS_SHA256_RHEL_AMD64` in `.gitlab-ci.yml`.
Downloads are checksum-verified and installed through temporary BuildKit mounts.
The custom package is installed before applying the netdev resolver workaround.

The NCO packages built from DOCA 3.6 LTS require SDK 3.6.0084 or newer.
Without a custom package URL, local Docker builds retain the base image's DMS.
