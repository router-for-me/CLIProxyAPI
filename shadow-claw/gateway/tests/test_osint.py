"""Tests for tools/osint.py — OSINT tools wrapping maigret, PhoneInfoga, theHarvester."""

import asyncio
import json
import os
import unittest
from unittest.mock import AsyncMock, MagicMock, patch

from tools.osint import (
    _validate_username,
    _validate_phone,
    _validate_domain,
    osint_username,
    osint_phone,
    osint_domain,
)


class TestInputValidation(unittest.TestCase):
    """Input sanitisation prevents command injection."""

    def test_valid_usernames(self):
        self.assertEqual(_validate_username("johndoe"), "johndoe")
        self.assertEqual(_validate_username("john.doe"), "john.doe")
        self.assertEqual(_validate_username("john_doe-99"), "john_doe-99")
        self.assertEqual(_validate_username("@johndoe"), "johndoe")

    def test_invalid_usernames(self):
        self.assertIsNone(_validate_username(""))
        self.assertIsNone(_validate_username("john doe"))  # spaces
        self.assertIsNone(_validate_username("john;rm -rf /"))  # injection
        self.assertIsNone(_validate_username("a" * 65))  # too long
        self.assertIsNone(_validate_username("user$(whoami)"))  # injection

    def test_valid_phones(self):
        self.assertEqual(_validate_phone("+5511999999999"), "+5511999999999")
        self.assertEqual(_validate_phone("5511999999999"), "5511999999999")
        self.assertEqual(_validate_phone("+1 555-123-4567"), "+15551234567")

    def test_invalid_phones(self):
        self.assertIsNone(_validate_phone(""))
        self.assertIsNone(_validate_phone("abc"))
        self.assertIsNone(_validate_phone("123"))  # too short
        self.assertIsNone(_validate_phone("+1;cat /etc/passwd"))  # injection

    def test_valid_domains(self):
        self.assertEqual(_validate_domain("example.com"), "example.com")
        self.assertEqual(_validate_domain("sub.example.com"), "sub.example.com")
        self.assertEqual(_validate_domain("https://example.com/path"), "example.com")

    def test_invalid_domains(self):
        self.assertIsNone(_validate_domain(""))
        self.assertIsNone(_validate_domain("not a domain"))
        self.assertIsNone(_validate_domain("-invalid.com"))
        self.assertIsNone(_validate_domain("example;rm -rf /"))


class TestOsintUsername(unittest.IsolatedAsyncioTestCase):
    """Tests for osint_username tool."""

    async def test_rejects_invalid_username(self):
        result = await osint_username("invalid user!")
        self.assertIn("Invalid username", result)

    @patch("tools.osint.shutil.which", return_value=None)
    async def test_missing_maigret_binary(self, _):
        result = await osint_username("testuser")
        self.assertIn("maigret not installed", result)

    @patch("tools.osint.shutil.which", return_value="/usr/bin/maigret")
    @patch("tools.osint._run_cmd")
    async def test_successful_scan(self, mock_run, mock_which):
        mock_run.return_value = (
            0,
            "[+] GitHub: https://github.com/testuser\n[+] Twitter: https://twitter.com/testuser\n",
            "",
        )
        result = await osint_username("testuser")
        self.assertIn("Found 2 profiles", result)
        self.assertIn("github.com", result)

    @patch("tools.osint.shutil.which", return_value="/usr/bin/maigret")
    @patch("tools.osint._run_cmd")
    async def test_timeout(self, mock_run, _):
        mock_run.return_value = (-1, "", "Command timed out after 120s")
        result = await osint_username("testuser")
        self.assertIn("timed out", result)


class TestOsintPhone(unittest.IsolatedAsyncioTestCase):
    """Tests for osint_phone tool."""

    async def test_rejects_invalid_phone(self):
        result = await osint_phone("not-a-phone")
        self.assertIn("Invalid phone", result)

    @patch("tools.osint.shutil.which", return_value=None)
    async def test_missing_phoneinfoga(self, _):
        result = await osint_phone("+5511999999999")
        self.assertIn("phoneinfoga not installed", result)

    @patch("tools.osint.shutil.which", return_value="/usr/bin/phoneinfoga")
    @patch("tools.osint._run_cmd")
    async def test_successful_scan(self, mock_run, _):
        mock_run.return_value = (
            0,
            "Country: Brazil\nCarrier: Vivo\nLine type: Mobile\n",
            "",
        )
        result = await osint_phone("+5511999999999")
        self.assertIn("Brazil", result)
        self.assertIn("Vivo", result)


class TestOsintDomain(unittest.IsolatedAsyncioTestCase):
    """Tests for osint_domain tool."""

    async def test_rejects_invalid_domain(self):
        result = await osint_domain("not a domain!")
        self.assertIn("Invalid domain", result)

    @patch("tools.osint.shutil.which", return_value=None)
    async def test_missing_theharvester(self, _):
        result = await osint_domain("example.com")
        self.assertIn("theHarvester not installed", result)

    @patch("tools.osint.shutil.which", return_value="/usr/bin/theHarvester")
    @patch("tools.osint._run_cmd")
    @patch("tools.osint._ensure_dir", return_value="/tmp/shadow_claw_osint/harvester_example.com")
    @patch("tools.osint.os.path.exists", return_value=True)
    @patch(
        "builtins.open",
        unittest.mock.mock_open(
            read_data=json.dumps(
                {"emails": ["admin@example.com", "info@example.com"], "hosts": ["mail.example.com"], "ips": ["1.2.3.4"]}
            )
        ),
    )
    async def test_successful_scan_with_json(self, mock_exists, mock_ensure, mock_run, _):
        mock_run.return_value = (0, "done", "")
        result = await osint_domain("example.com")
        self.assertIn("admin@example.com", result)
        self.assertIn("mail.example.com", result)
        self.assertIn("Emails (2)", result)


if __name__ == "__main__":
    unittest.main()
