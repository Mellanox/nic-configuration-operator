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

# github tag e.g v1.2.3
GITHUB_TAG=${GITHUB_TAG:-}
# github repo owner e.g Mellanox
GITHUB_REPO_OWNER=${GITHUB_REPO_OWNER:-}

BASE=${PWD}
YQ_CMD="${BASE}/bin/yq"
HELM_VALUES=${BASE}/deployment/nic-configuration-operator-chart/values.yaml
HELM_CHART=${BASE}/deployment/nic-configuration-operator-chart/Chart.yaml


if [ -z "$GITHUB_TAG" ]; then
    echo "ERROR: GITHUB_TAG must be provided as env var"
    exit 1
fi

if [ -z "$GITHUB_REPO_OWNER" ]; then
    echo "ERROR: GITHUB_REPO_OWNER must be provided as env var"
    exit 1
fi

# tag provided via env var
OPERATOR_TAG=${GITHUB_TAG}

# patch values.yaml in-place

# maintenance-operator image:
OPERATOR_REPO=$(echo ${GITHUB_REPO_OWNER} | tr '[:upper:]' '[:lower:]') # this is used to allow to release maintenance-operator from forks
$YQ_CMD -i ".operator.image.repository = \"ghcr.io/${OPERATOR_REPO}\"" ${HELM_VALUES}
$YQ_CMD -i ".configDaemon.image.repository = \"ghcr.io/${OPERATOR_REPO}\"" ${HELM_VALUES}

$YQ_CMD -i ".operator.image.tag = \"${OPERATOR_TAG}\"" ${HELM_VALUES}
$YQ_CMD -i ".configDaemon.image.tag = \"${OPERATOR_TAG}\"" ${HELM_VALUES}

# patch Chart.yaml in-place
$YQ_CMD -i ".version = \"${OPERATOR_TAG#"v"}\"" ${HELM_CHART}
$YQ_CMD -i ".appVersion = \"${OPERATOR_TAG}\"" ${HELM_CHART}
