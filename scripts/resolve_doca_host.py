#!/usr/bin/env python3
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

"""Resolve DOCA-Host local installer URLs from the NVIDIA-internal build repository.

Usage: python3 scripts/resolve_doca_host.py 3.6

The input is a major.minor series. The highest patch whose last_stable build is
populated is selected, and the URLs point at that build's own directory so they
do not move if last_stable is updated mid-pipeline. The repository root URL is
read from DOCA_HOST_REPO_ROOT, which is defined as a GitLab CI variable. Print
JSON and optionally write GitLab dotenv variables.
"""

import argparse
import json
import os
import re
import sys
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


REPO_ROOT = os.environ.get("DOCA_HOST_REPO_ROOT", "").rstrip("/")


def request(url, *, method="GET", timeout=15):
    with urlopen(Request(url, method=method), timeout=timeout) as response:
        if response.status != 200:
            raise ValueError(f"{url}: HTTP {response.status}")
        return response.read()


def links(url, timeout):
    return re.findall(r'href="([^"]+)"', request(url, timeout=timeout).decode("utf-8"))


def patch_versions(series, timeout):
    """Return the series' patch releases, highest first, ignoring suffixed trees like _riscv."""
    pattern = re.compile(rf"doca-repo-({re.escape(series)}\.(\d+))/")
    found = {}
    for name in links(f"{REPO_ROOT}/", timeout):
        match = pattern.fullmatch(name)
        if match:
            found[int(match.group(2))] = match.group(1)
    if not found:
        raise ValueError(f"no internal DOCA repository for {series}.x under {REPO_ROOT}/")
    return [found[patch] for patch in sorted(found, reverse=True)]


def stable_installers(version, timeout):
    """Return (build, ubuntu file, rhel file) of last_stable, or None if it is not published."""
    try:
        names = links(f"{REPO_ROOT}/doca-repo-{version}/last_stable/", timeout)
    except HTTPError as error:
        if error.code == 404:
            error.close()
            return None
        raise
    v = re.escape(version)
    ubuntu = [m for n in names if (m := re.fullmatch(rf"doca-host_({v}-\d+)-(\d+\.\d+)-ubuntu2404_amd64\.deb", n))]
    rhel = [m for n in names if (m := re.fullmatch(rf"doca-host-({v}-\d+)_(\d+\.\d+)_rhel9\.x86_64\.rpm", n))]
    if len(ubuntu) != 1 or len(rhel) != 1:
        raise ValueError(f"{version} last_stable: expected one Ubuntu 24.04 and one RHEL 9 installer, "
                         f"found {[m.group(0) for m in ubuntu + rhel]}")
    if ubuntu[0].groups() != rhel[0].groups():
        raise ValueError(f"{version} last_stable: Ubuntu and RHEL installers disagree: "
                         f"{ubuntu[0].group(0)} vs {rhel[0].group(0)}")
    return ubuntu[0].group(1), ubuntu[0].group(0), rhel[0].group(0)


def resolve(series, timeout=15):
    for version in patch_versions(series, timeout):
        installers = stable_installers(version, timeout)
        if installers is not None:
            break
    else:
        raise ValueError(f"no last_stable build published for any {series}.x release")

    build, ubuntu_file, rhel_file = installers
    base = f"{REPO_ROOT}/doca-repo-{version}/doca-repo-{build}"
    ubuntu, rhel = f"{base}/{ubuntu_file}", f"{base}/{rhel_file}"
    for url in (ubuntu, rhel):
        request(url, method="HEAD", timeout=timeout)
    return {"doca_version": version, "doca_build": build, "ubuntu_2404_amd64_url": ubuntu, "rhel_9_x86_64_url": rhel}


def dotenv(result):
    return "\n".join((
        f"DOCA_HOST_VERSION={result['doca_version']}",
        f"DOCA_HOST_BUILD={result['doca_build']}",
        f"DOCA_HOST_UBUNTU_URL={result['ubuntu_2404_amd64_url']}",
        f"DOCA_HOST_RHEL_URL={result['rhel_9_x86_64_url']}",
        "",
    ))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("series", help="DOCA major.minor release, for example 3.6")
    parser.add_argument("--timeout", type=int, default=15, help="HTTP timeout in seconds (default: 15)")
    parser.add_argument("--dotenv", help="write resolved GitLab CI variables to this file")
    args = parser.parse_args()
    if not re.fullmatch(r"\d+\.\d+", args.series) or args.timeout < 1:
        parser.error("series must be major.minor; timeout must be positive")
    if not REPO_ROOT:
        parser.error("DOCA_HOST_REPO_ROOT must be set to the DOCA-Host repository root URL")
    try:
        result = resolve(args.series, args.timeout)
        if args.dotenv:
            with open(args.dotenv, "w", encoding="utf-8") as output:
                output.write(dotenv(result))
        print(json.dumps(result, indent=2))
    except (HTTPError, URLError, ValueError, OSError) as error:
        print(f"DOCA URL resolution failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
