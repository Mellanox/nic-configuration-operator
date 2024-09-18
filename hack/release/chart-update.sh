#!/bin/bash
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
$YQ_CMD -i ".operator.image.repository = \"ghcr.io/${OPERATOR_REPO}/nic-configuration-operator\"" ${HELM_VALUES}

# patch Chart.yaml in-place
$YQ_CMD -i ".version = \"${OPERATOR_TAG#"v"}\"" ${HELM_CHART}
$YQ_CMD -i ".appVersion = \"${OPERATOR_TAG}\"" ${HELM_CHART}
