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

"""Focused checks for DOCA release selection and metadata consistency."""

import io
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

import resolve_doca_host as resolver


class ResolveDocaHostTests(unittest.TestCase):
    def test_gitlab_dotenv_contains_resolved_urls(self):
        result = {
            "doca_version": "3.5.0",
            "ubuntu_2404_amd64_url": "https://example.test/host.deb",
            "rhel_9_x86_64_url": "https://example.test/host.rpm",
        }
        self.assertEqual(
            resolver.dotenv(result),
            "DOCA_HOST_VERSION=3.5.0\n"
            "DOCA_HOST_UBUNTU_URL=https://example.test/host.deb\n"
            "DOCA_HOST_RHEL_URL=https://example.test/host.rpm\n",
        )

    def test_published_patches_skip_only_missing_versions(self):
        def fake_request(url, **kwargs):
            if "/3.2.1/" in url or "/3.2.3/" in url:
                return None
            raise HTTPError(url, 404, "Not Found", {}, io.BytesIO())

        with patch.object(resolver, "request", side_effect=fake_request):
            self.assertEqual(resolver.published_patches("3.2", 3, 1), ["3.2.1", "3.2.3"])

    def test_highest_published_patch_is_selected(self):
        with patch.object(resolver, "published_patches", return_value=["3.4.0", "3.4.1"]), \
             patch.object(resolver, "apt_versions", return_value=("3.4.1-011000", "26.04")) as apt, \
             patch.object(resolver, "rpm_versions", return_value=("3.4.1-011000", "26.04")), \
             patch.object(resolver, "request"):
            result = resolver.resolve("3.4")
        apt.assert_called_once_with("3.4.1", 15)
        self.assertEqual(result["doca_version"], "3.4.1")
        self.assertIn("DOCA_v3.4.1", result["ubuntu_2404_amd64_url"])

    def test_duplicate_apt_package_versions_are_rejected(self):
        packages = "\n\n".join((
            "Package: doca-all\nVersion: 3.5.0-082000",
            "Package: doca-all\nVersion: 3.5.0-083000",
            "Package: ofed-scripts\nVersion: 26.07.OFED.26.07.0.7.7-1",
        ))
        with self.assertRaisesRegex(ValueError, "ambiguous"):
            resolver.unique_package_versions(packages, ("doca-all", "ofed-scripts"))

    def test_apt_and_rpm_disagreement_prevents_url_use(self):
        with patch.object(resolver, "published_patches", return_value=["3.5.0"]), \
             patch.object(resolver, "apt_versions", return_value=("3.5.0-082000", "26.07")), \
             patch.object(resolver, "rpm_versions", return_value=("3.5.0-083000", "26.07")), \
             patch.object(resolver, "request") as request:
            with self.assertRaisesRegex(ValueError, "disagree"):
                resolver.resolve("3.5")
            request.assert_not_called()


if __name__ == "__main__":
    unittest.main()
