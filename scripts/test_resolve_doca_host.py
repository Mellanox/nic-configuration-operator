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

"""Focused checks for DOCA release selection and installer consistency."""

import io
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

import resolve_doca_host as resolver

ROOT = "https://example.test/doca-host-repo"
UBUNTU = "doca-host_3.6.0-059000-26.10-ubuntu2404_amd64.deb"
RHEL = "doca-host-3.6.0-059000_26.10_rhel9.x86_64.rpm"


def fake_links(listings):
    def links(url, timeout):
        if url not in listings:
            raise HTTPError(url, 404, "Not Found", {}, io.BytesIO())
        return listings[url]
    return links


class ResolveDocaHostTests(unittest.TestCase):
    def setUp(self):
        repo_root = patch.object(resolver, "REPO_ROOT", ROOT)
        repo_root.start()
        self.addCleanup(repo_root.stop)

    def test_gitlab_dotenv_contains_resolved_urls(self):
        result = {
            "doca_version": "3.6.0",
            "doca_build": "3.6.0-059000",
            "ubuntu_2404_amd64_url": "https://example.test/host.deb",
            "rhel_9_x86_64_url": "https://example.test/host.rpm",
        }
        self.assertEqual(
            resolver.dotenv(result),
            "DOCA_HOST_VERSION=3.6.0\n"
            "DOCA_HOST_BUILD=3.6.0-059000\n"
            "DOCA_HOST_UBUNTU_URL=https://example.test/host.deb\n"
            "DOCA_HOST_RHEL_URL=https://example.test/host.rpm\n",
        )

    def test_patch_versions_are_sorted_numerically_and_skip_suffixed_trees(self):
        root = ["doca-repo-3.4.2/", "doca-repo-3.4.10/", "doca-repo-3.4.1_anth/", "doca-repo-3.40.0/", "doca-repo-3.5.0/"]
        with patch.object(resolver, "links", side_effect=fake_links({f"{ROOT}/": root})):
            self.assertEqual(resolver.patch_versions("3.4", 1), ["3.4.10", "3.4.2"])

    def test_urls_point_at_fixed_build_directory(self):
        listings = {
            f"{ROOT}/": ["doca-repo-3.6.0/", "doca-repo-3.6.0_riscv/"],
            f"{ROOT}/doca-repo-3.6.0/last_stable/": [UBUNTU, RHEL, "doca-host_3.6.0-059000-26.10-ubuntu2404_arm64.deb"],
        }
        with patch.object(resolver, "links", side_effect=fake_links(listings)), \
             patch.object(resolver, "request") as request:
            result = resolver.resolve("3.6")
        base = f"{ROOT}/doca-repo-3.6.0/doca-repo-3.6.0-059000"
        self.assertEqual(result["doca_build"], "3.6.0-059000")
        self.assertEqual(result["ubuntu_2404_amd64_url"], f"{base}/{UBUNTU}")
        self.assertEqual(result["rhel_9_x86_64_url"], f"{base}/{RHEL}")
        self.assertEqual(request.call_count, 2)

    def test_patch_without_last_stable_falls_back_to_previous_patch(self):
        listings = {
            f"{ROOT}/": ["doca-repo-3.6.0/", "doca-repo-3.6.1/"],
            f"{ROOT}/doca-repo-3.6.0/last_stable/": [UBUNTU, RHEL],
        }
        with patch.object(resolver, "links", side_effect=fake_links(listings)), \
             patch.object(resolver, "request"):
            self.assertEqual(resolver.resolve("3.6")["doca_version"], "3.6.0")

    def test_mismatched_installers_prevent_url_use(self):
        listings = {
            f"{ROOT}/": ["doca-repo-3.6.0/"],
            f"{ROOT}/doca-repo-3.6.0/last_stable/": [UBUNTU, RHEL.replace("059000", "058000")],
        }
        with patch.object(resolver, "links", side_effect=fake_links(listings)), \
             patch.object(resolver, "request") as request:
            with self.assertRaisesRegex(ValueError, "disagree"):
                resolver.resolve("3.6")
            request.assert_not_called()

    def test_missing_installer_is_rejected(self):
        listings = {
            f"{ROOT}/": ["doca-repo-3.6.0/"],
            f"{ROOT}/doca-repo-3.6.0/last_stable/": [UBUNTU],
        }
        with patch.object(resolver, "links", side_effect=fake_links(listings)), \
             patch.object(resolver, "request"):
            with self.assertRaisesRegex(ValueError, "expected one"):
                resolver.resolve("3.6")


if __name__ == "__main__":
    unittest.main()
