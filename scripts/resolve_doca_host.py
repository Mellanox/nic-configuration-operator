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

"""Resolve DOCA-Host local installer URLs from NVIDIA package repository metadata.

Usage: python3 scripts/resolve_doca_host.py 3.5

The input is a major.minor series. The highest published patch in the probed
range is selected. Print JSON and optionally write GitLab dotenv variables.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import gzip
import json
import re
import sys
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen
import xml.etree.ElementTree as ET


REPO_ROOT = "https://linux.mellanox.com/public/repo/doca"
DOWNLOAD_ROOT = "https://www.mellanox.com/downloads/DOCA"
RPM_NS = {"repo": "http://linux.duke.edu/metadata/repo", "pkg": "http://linux.duke.edu/metadata/common"}


def request(url, *, method="GET", timeout=15):
    with urlopen(Request(url, method=method), timeout=timeout) as response:
        if response.status != 200:
            raise ValueError(f"{url}: HTTP {response.status}")
        if method == "HEAD":
            if url.startswith(DOWNLOAD_ROOT) and response.url.split("/", 3)[2] not in (
                "www.mellanox.com", "content.mellanox.com"
            ):
                raise ValueError(f"unexpected download redirect: {response.url}")
            return None
        return response.read()


def published_patches(series, max_patch, timeout):
    """Probe APT Release files; 404 means that patch is not published."""
    def probe(patch):
        version = f"{series}.{patch}"
        url = f"{REPO_ROOT}/{version}/ubuntu24.04/x86_64/Release"
        try:
            request(url, method="HEAD", timeout=timeout)
        except HTTPError as error:
            if error.code == 404:
                error.close()
                return None
            raise
        return version

    with ThreadPoolExecutor(max_workers=8) as pool:
        versions = [version for version in pool.map(probe, range(max_patch + 1)) if version]
    if not versions:
        raise ValueError(f"no published Ubuntu 24.04 x86_64 DOCA repository for {series}.0–{max_patch}")
    return versions


def unique_package_versions(packages_text, names):
    versions = {name: set() for name in names}
    for stanza in packages_text.split("\n\n"):
        fields = dict(line.split(": ", 1) for line in stanza.splitlines() if ": " in line)
        if fields.get("Package") in versions:
            versions[fields["Package"]].add(fields["Version"])
    if any(len(found) != 1 for found in versions.values()):
        raise ValueError(f"missing or ambiguous APT package versions: {versions}")
    return {name: next(iter(found)) for name, found in versions.items()}


def apt_versions(version, timeout):
    base = f"{REPO_ROOT}/{version}/ubuntu24.04/x86_64"
    release = request(f"{base}/Release", timeout=timeout).decode("utf-8")
    packages = request(f"{base}/Packages", timeout=timeout).decode("utf-8")
    found = unique_package_versions(packages, ("doca-all", "ofed-scripts"))
    match = re.fullmatch(r"(\d+\.\d+)\.OFED\.\1\..+", found["ofed-scripts"])
    if not match or not re.fullmatch(re.escape(version) + r"-\d+", found["doca-all"]):
        raise ValueError(f"unexpected APT package versions: {found}")
    ofed = match.group(1)
    expected_release = f"{found['doca-all']}-{ofed}-ubuntu2404"
    release_version = next((line[9:] for line in release.splitlines() if line.startswith("Version: ")), None)
    if release_version is not None and release_version != expected_release:
        raise ValueError(f"APT Release version {release_version} differs from {expected_release}")
    return found["doca-all"], ofed


def rpm_versions(version, timeout):
    base = f"{REPO_ROOT}/{version}/rhel9/x86_64"
    repomd = ET.fromstring(request(f"{base}/repodata/repomd.xml", timeout=timeout))
    location = repomd.find("repo:data[@type='primary']/repo:location", RPM_NS)
    if location is None:
        raise ValueError("RHEL repomd.xml has no primary package index")
    index = request(f"{base}/{location.attrib['href']}", timeout=timeout)
    if location.attrib["href"].endswith(".gz"):
        index = gzip.decompress(index)
    packages = ET.fromstring(index)
    found = {"doca-all": set(), "ofed-scripts": set()}
    for package in packages.findall("pkg:package", RPM_NS):
        name = package.findtext("pkg:name", namespaces=RPM_NS)
        if name in found:
            rpm_version = package.find("pkg:version", RPM_NS)
            found[name].add((rpm_version.attrib["ver"], rpm_version.attrib["rel"]))
    if any(len(values) != 1 for values in found.values()):
        raise ValueError(f"missing or ambiguous RPM package versions: {found}")
    doca_ver, doca_rel = next(iter(found["doca-all"]))
    ofed_ver, ofed_rel = next(iter(found["ofed-scripts"]))
    if doca_ver != version or not doca_rel.isdecimal() or not ofed_rel.startswith(f"OFED.{ofed_ver}."):
        raise ValueError(f"unexpected RPM package versions: {found}")
    return f"{doca_ver}-{doca_rel}", ofed_ver


def resolve(series, max_patch=20, timeout=15):
    versions = published_patches(series, max_patch, timeout)
    version = versions[-1]
    apt_build, apt_ofed = apt_versions(version, timeout)
    rpm_build, rpm_ofed = rpm_versions(version, timeout)
    if (apt_build, apt_ofed) != (rpm_build, rpm_ofed):
        raise ValueError(f"APT and RPM metadata disagree: {(apt_build, apt_ofed)} vs {(rpm_build, rpm_ofed)}")

    base = f"{DOWNLOAD_ROOT}/DOCA_v{version}/host"
    ubuntu = f"{base}/doca-host_{apt_build}-{apt_ofed}-ubuntu2404_amd64.deb"
    rhel = f"{base}/doca-host-{rpm_build}_{rpm_ofed}_rhel9.x86_64.rpm"
    for url in (ubuntu, rhel):
        request(url, method="HEAD", timeout=timeout)
    return {"doca_version": version, "ubuntu_2404_amd64_url": ubuntu, "rhel_9_x86_64_url": rhel}


def dotenv(result):
    return "\n".join((
        f"DOCA_HOST_VERSION={result['doca_version']}",
        f"DOCA_HOST_UBUNTU_URL={result['ubuntu_2404_amd64_url']}",
        f"DOCA_HOST_RHEL_URL={result['rhel_9_x86_64_url']}",
        "",
    ))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("series", help="DOCA major.minor release, for example 3.5")
    parser.add_argument("--max-patch", type=int, default=20, help="highest patch number to probe (default: 20)")
    parser.add_argument("--timeout", type=int, default=15, help="HTTP timeout in seconds (default: 15)")
    parser.add_argument("--dotenv", help="write resolved GitLab CI variables to this file")
    args = parser.parse_args()
    if not re.fullmatch(r"\d+\.\d+", args.series) or args.max_patch < 0 or args.timeout < 1:
        parser.error("series must be major.minor; max-patch must be nonnegative; timeout must be positive")
    try:
        result = resolve(args.series, args.max_patch, args.timeout)
        if args.dotenv:
            with open(args.dotenv, "w", encoding="utf-8") as output:
                output.write(dotenv(result))
        print(json.dumps(result, indent=2))
    except (HTTPError, URLError, ValueError, ET.ParseError, OSError) as error:
        print(f"DOCA URL resolution failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
