#!/bin/bash
# Copyright 2025 NVIDIA CORPORATION & AFFILIATES
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# SPDX-License-Identifier: Apache-2.0

set -ex

# github repo owner: e.g mellanox
GITHUB_REPO_OWNER=${GITHUB_REPO_OWNER:-}
# github api token with package:write permissions
GITHUB_TOKEN=${GITHUB_TOKEN:-}
# github tag e.g v1.2.3
GITHUB_TAG=${GITHUB_TAG:-}

BASE=${PWD}
HELM_CMD="${BASE}/bin/helm"
HELM_CHART=${BASE}/deployment/nic-configuration-operator-chart
HELM_CHART_VERSION=${GITHUB_TAG#"v"}
HELM_CHART_TARBALL="nic-configuration-operator-chart-${HELM_CHART_VERSION}.tgz"

if [ -z "$GITHUB_REPO_OWNER" ]; then
    echo "ERROR: GITHUB_REPO_OWNER must be provided as env var"
    exit 1
fi

if [ -z "$GITHUB_TOKEN" ]; then
    echo "ERROR: GITHUB_TOKEN must be provided as env var"
    exit 1
fi

if [ -z "$GITHUB_TAG" ]; then
    echo "ERROR: GITHUB_TAG must be provided as env var"
    exit 1
fi

$HELM_CMD package ${HELM_CHART}
$HELM_CMD registry login ghcr.io -u ${GITHUB_REPO_OWNER} -p ${GITHUB_TOKEN}
# we set repo-owner to lowercase for oci registry.
$HELM_CMD push ${HELM_CHART_TARBALL} oci://ghcr.io/$(echo ${GITHUB_REPO_OWNER} | tr '[:upper:]' '[:lower:]')
