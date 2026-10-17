#!/usr/bin/env python3
"""Verify the smoke HTTP helper without launching a runtime or making requests."""
import io
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

import smoke_runtime


class SmokeRequestTests(unittest.TestCase):
    def setUp(self):
        patcher = patch("smoke_runtime.urllib.request.build_opener")
        self.opener = patcher.start()
        self.addCleanup(patcher.stop)
        self.client = self.opener.return_value
        response = self.client.open.return_value.__enter__.return_value
        response.status = 200
        response.read.return_value = b'{"ok":true}'

    def request_sent(self):
        args, kwargs = self.client.open.call_args
        self.assertEqual(kwargs, {"timeout": 2})
        self.assertEqual(self.opener.call_args.args[0].proxies, {})
        return args[0]

    def test_get_has_no_body_or_default_authentication(self):
        self.assertEqual(smoke_runtime.request(12345, "/v1/models"), (200, b'{"ok":true}'))
        request = self.request_sent()
        self.assertEqual(request.full_url, "http://127.0.0.1:12345/v1/models")
        self.assertEqual(request.get_method(), "GET")
        self.assertIsNone(request.data)
        self.assertIsNone(request.get_header("Authorization"))
        self.assertIsNone(request.get_header("Content-type"))

    def test_post_preserves_empty_shutdown_body(self):
        smoke_runtime.request(12345, "/v0/management/runtime-shutdown", "test-key", "POST")
        request = self.request_sent()
        self.assertEqual(request.get_method(), "POST")
        self.assertEqual(request.data, b"")
        self.assertEqual(request.get_header("Authorization"), "Bearer test-key")
        self.assertIsNone(request.get_header("Content-type"))

    def test_put_sends_exact_v8_json_body_and_content_type(self):
        payload = b'["rotated-test-key"]'
        smoke_runtime.request(12345, "/v8/management/config/access/api-keys", "test-key", "PUT", payload)
        request = self.request_sent()
        self.assertEqual(request.get_method(), "PUT")
        self.assertEqual(request.data, payload)
        self.assertEqual(request.get_header("Content-type"), "application/json")
        self.assertEqual(request.get_header("Authorization"), "Bearer test-key")

    def test_http_errors_remain_available_to_negative_auth_checks(self):
        self.client.open.side_effect = HTTPError("http://127.0.0.1/", 401, "Unauthorized", {}, io.BytesIO(b"denied"))
        self.assertEqual(smoke_runtime.request(12345, "/v8/management/config", "wrong"), (401, b"denied"))


if __name__ == "__main__":
    unittest.main()
