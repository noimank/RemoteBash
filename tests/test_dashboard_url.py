"""Tests for the dashboard base-URL helper used in MCP tool hints."""

import unittest

from remotebash.app import _dashboard_base_url


class DashboardBaseUrlTest(unittest.TestCase):
    def test_explicit_loopback(self):
        self.assertEqual(_dashboard_base_url("127.0.0.1", 24587),
                         "http://127.0.0.1:24587")

    def test_custom_host_and_port(self):
        self.assertEqual(_dashboard_base_url("dev.box", 8000),
                         "http://dev.box:8000")

    def test_wildcard_bind_normalised_to_localhost(self):
        for wildcard in ("0.0.0.0", "::", "", None):
            with self.subTest(host=wildcard):
                self.assertEqual(_dashboard_base_url(wildcard, 24587),
                                 "http://localhost:24587")


if __name__ == "__main__":
    unittest.main()
