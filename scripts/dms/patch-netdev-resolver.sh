#!/bin/bash
# Copyright 2026 NVIDIA CORPORATION & AFFILIATES
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


# Replace only the resolver so other helpers match the installed DMS version.
set -euo pipefail

lib="${1:-/opt/mellanox/doca/services/dms/tools/configuration/_lib.sh}"
replacement="$(dirname "$0")/iface-from-bdf.sh"
test -r "$replacement"
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT

awk -v replacement="$replacement" '
    /^iface_from_bdf\(\) *\{/ {
        count++
        replacing = 1
        in_function = 0
        while ((getline line < replacement) > 0) {
            if (line ~ /^iface_from_bdf\(\)/) in_function = 1
            if (in_function) print line
        }
        close(replacement)
        next
    }
    replacing && /^}/ { replacing = 0; next }
    !replacing { print }
    END { if (count != 1 || replacing || !in_function) exit 1 }
' "$lib" > "$tmp" || {
    echo "Cannot locate exactly one complete iface_from_bdf function in $lib" >&2
    exit 1
}
bash -n "$tmp"
# Preserve the packaged file's ownership and permissions.
cat "$tmp" > "$lib"
