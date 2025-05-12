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

# This script ad-hoc fixes references in api doc

DOC=$(realpath $1)

awk '
{
    gsub(/\(#configuration.net.nvidia.com%2fv1alpha1)/, "(#configurationnetnvidiacomv1alpha1)");
    gsub(/#configuration.net.nvidia.com\/v1alpha1./, "#");
}1
' ${DOC} > ${DOC}.tmp
mv -f ${DOC}.tmp ${DOC}
