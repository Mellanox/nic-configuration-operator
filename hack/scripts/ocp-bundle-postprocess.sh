#!/bin/bash

#  2024 NVIDIA CORPORATION & AFFILIATES
#
#  Licensed under the Apache License, Version 2.0 (the License);
#  you may not use this file except in compliance with the License.
#  You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
#  Unless required by applicable law or agreed to in writing, software
#  distributed under the License is distributed on an AS IS BASIS,
#  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#  See the License for the specific language governing permissions and
#  limitations under the License.

set -o nounset
set -o pipefail
set -o errexit

if [[ "${TRACE-0}" == "1" ]]; then
    set -o xtrace
fi

printf "  relatedImages:\n    - name: nvidia-nic-configuration-operator\n      image: %s\n    - name: nvidia-nic-configuration-daemon\n      image: %s" "$OPERATOR_IMAGE_TAG" "$CONFIG_DAEMON_IMAGE_TAG" >> bundle/manifests/nvidia-nic-configuration-operator.clusterserviceversion.yaml

# Add containerImage annotation
# Escape the tag annotation value for sed
ESCAPED_TAG=$(printf '%s\n' "$OPERATOR_IMAGE_TAG" | sed -e 's/[]\/$*.^[]/\\&/g')
sed -i "0,/annotations:/s/annotations:/annotations:\n    containerImage: $ESCAPED_TAG/" bundle/manifests/nvidia-nic-configuration-operator.clusterserviceversion.yaml

# Add OpenShift versions in metadata/annotations.yaml
echo "  com.redhat.openshift.versions: $BUNDLE_OCP_VERSIONS" >> bundle/metadata/annotations.yaml