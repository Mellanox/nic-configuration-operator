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


set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# Surround the original resolver with unrelated helpers to catch over-replacement.
cat > "$fixture/lib.sh" <<'EOF'
before() { echo before; }
iface_from_bdf() {
    local sysdir="/sys/bus/pci/devices/${1}/net"
    ls "$sysdir" 2>/dev/null | head -1
}
after() { echo after; }
EOF
bash "$here/patch-netdev-resolver.sh" "$fixture/lib.sh"
cp "$fixture/lib.sh" "$fixture/once.sh"
bash "$here/patch-netdev-resolver.sh" "$fixture/lib.sh"
cmp "$fixture/once.sh" "$fixture/lib.sh" || fail "patch is not idempotent"
sed "s|/sys/bus/pci/devices|$fixture/sys|g" "$fixture/lib.sh" > "$fixture/test.sh"
source "$fixture/test.sh"
[[ $(before) == before && $(after) == after ]] || fail "unrelated helpers changed"
net="$fixture/sys/0000:15:00.0/net"
mkdir -p "$net/eth0" "$net/eth_r0_p0"
echo p0 > "$net/eth_r0_p0/phys_port_name"
for representor in pf0vf0 pf0sf0 c0pf0vf0 c1pf0sf2 pf0 c0pf0; do
    echo "$representor" > "$net/eth0/phys_port_name"
    [[ $(iface_from_bdf 0000:15:00.0) == eth_r0_p0 ]] || fail "$representor selected"
done
rm -rf "$net/eth_r0_p0"
[[ -z $(iface_from_bdf 0000:15:00.0) ]] || fail "representor-only target selected"
rm "$net/eth0/phys_port_name"
[[ $(iface_from_bdf 0000:15:00.0) == eth0 ]] || fail "netdev without metadata rejected"
rm -rf "$net"
[[ -z $(iface_from_bdf 0000:15:00.0) ]] || fail "absent PCI directory resolved"

# Fail without modifying the library if an upstream layout changes.
for invalid in missing duplicate unterminated; do
    case "$invalid" in
        missing) echo 'before() { echo before; }' > "$fixture/bad.sh" ;;
        duplicate) cat "$fixture/once.sh" "$fixture/once.sh" > "$fixture/bad.sh" ;;
        unterminated) printf 'iface_from_bdf() {\n    echo broken\n' > "$fixture/bad.sh" ;;
    esac
    cp "$fixture/bad.sh" "$fixture/original.sh"
    if bash "$here/patch-netdev-resolver.sh" "$fixture/bad.sh" 2> "$fixture/error"; then
        fail "$invalid resolver accepted"
    fi
    cmp "$fixture/original.sh" "$fixture/bad.sh" || fail "$invalid library modified"
done
echo 'PASS: DMS resolver workaround'
