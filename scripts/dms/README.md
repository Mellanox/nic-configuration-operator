# Temporary DMS netdev resolver workaround

The daemon image builds replace `iface_from_bdf()` in the installed DMS
`tools/configuration/_lib.sh` to exclude switchdev PF/VF/SF representors.
The remainder of the packaged library and its permissions are preserved.
The normal, Ubuntu STIG-FIPS and RHEL STIG-FIPS daemon images use the same fix.

Remove this directory, its Dockerfile steps and its CI test invocation once all
supported daemon base images/packages contain the upstream fix
(DMS Redmine #5352343, Gerrit 1534069).

Run the regression test with `bash scripts/dms/test-netdev-resolver.sh`.
