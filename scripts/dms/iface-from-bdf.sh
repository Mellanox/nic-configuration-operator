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


# Temporary workaround for DMS PCI netdev resolution in switchdev mode.
# Remove when the bundled DMS includes the representor-selection fix.
iface_from_bdf() {
    local bdf="$1"
    local sysdir="/sys/bus/pci/devices/${bdf}/net"
    local netdev port_name
    for netdev in "$sysdir"/*; do
        [[ -d "$netdev" ]] || continue
        port_name=$(cat "$netdev/phys_port_name" 2>/dev/null) || port_name=""
        # Switchdev representors share the PF's PCI directory but cannot
        # service the physical port's ip/dcb operations.
        [[ "$port_name" =~ ^(c[0-9]+)?pf[0-9]+((vf|sf)[0-9]+)?$ ]] && continue
        echo "${netdev##*/}"
        return 0
    done
    return 0
}
