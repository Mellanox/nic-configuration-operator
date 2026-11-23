#!/bin/sh
# Copyright 2026 NVIDIA CORPORATION & AFFILIATES
# SPDX-License-Identifier: Apache-2.0

set -eu

# Accept a Go VCS repository path. Credentials belong in netrc, outside the checkout.
repository=${DMS_LIB_REPOSITORY:?Set DMS_LIB_REPOSITORY to the DMS library repository path ending in .git}
case "$repository" in
    *[!a-zA-Z0-9._/-]*)
        echo 'DMS_LIB_REPOSITORY contains characters outside host/path.git' >&2
        exit 1
        ;;
esac
if ! printf '%s\n' "$repository" | grep -Eq '^[a-zA-Z0-9.-]+(/[a-zA-Z0-9_.-]+)+[.]git$'; then
    echo 'DMS_LIB_REPOSITORY must be a host/path.git without a URL scheme or credentials' >&2
    exit 1
fi

# go.sum uses the replacement module paths as checksum keys.
sed -i.bak "s|dms-lib-repository[.]invalid/dms-blueprints-lib[.]git|$repository|g" go.mod go.sum
rm -f go.mod.bak go.sum.bak
