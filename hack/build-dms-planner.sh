#!/bin/sh
# Copyright 2026 NVIDIA CORPORATION & AFFILIATES
# SPDX-License-Identifier: Apache-2.0

# Build the preserved upstream planner from the library's exact GitLab revision.
set -eu

output=${1:?absolute output path is required}
case "$output" in
    /*) ;;
    *) echo "planner output path must be absolute" >&2; exit 1 ;;
esac
library_module=gitlab-master.nvidia.com/cloud-orchestration/dms-blueprints-lib.git/lib
library=$(go list -m -f '{{if .Replace}}{{.Replace.Path}}@{{.Replace.Version}}{{end}}' github.com/Mellanox/dms-lib)
api=$(go list -m -f '{{if .Replace}}{{.Replace.Path}}@{{.Replace.Version}}{{end}}' github.com/Mellanox/dms-lib/api)
case "$library" in
    "$library_module"@v*) version=${library#*@} ;;
    *) echo "dms-lib must have a versioned GitLab replacement" >&2; exit 1 ;;
esac
if [ "$api" != "$library_module/api@$version" ]; then
    echo "dms-lib API must use the same GitLab revision as the library" >&2
    exit 1
fi
planner_module=gitlab-master.nvidia.com/cloud-orchestration/dms-blueprints-lib.git/doSpcx
go mod download -json "$planner_module@$version"
planner_dir=$(go list -m -f '{{.Dir}}' "$planner_module@$version")
CGO_ENABLED=0 go -C "$planner_dir" build -mod=readonly -trimpath -o "$output" ./planner
