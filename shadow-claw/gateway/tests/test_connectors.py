"""Focused tests for Personal Agent read-only connectors."""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from connectors import ConnectorState
from connectors.calendar_connector import CalendarConnector, CalendarConnectorStatus
from connectors.gmail_connector import GmailConnector, GmailConnectorStatus
from errors import AuthExpiredError, ConnectorUnavailableError


class TestConnectorStateEnum(unittest.TestCase):
    def test_all_states_present(self):
        self.assertEqual(ConnectorState.CONNECTED, "connected")
        self.assertEqual(ConnectorState.DEGRADED, "degraded")
        self.assertEqual(ConnectorState.EXPIRED, "expired")
        self.assertEqual(ConnectorState.DISABLED, "disabled")


class TestGmailConnector(unittest.IsolatedAsyncioTestCase):
    async def test_status_disabled_without_tokens(self):
        connector = GmailConnector(access_token="", refresh_token="")
        status = connector.status()
        self.assertEqual(status.state, ConnectorState.DISABLED)
        self.assertIn("not configured", status.detail)

    async def test_status_expired_with_only_refresh_token(self):
        connector = GmailConnector(access_token="", refresh_token="refresh")
        status = connector.status()
        self.assertEqual(status.state, ConnectorState.EXPIRED)
        self.assertIn("expired", status.detail)

    async def test_status_connected_with_access_token(self):
        connector = GmailConnector(access_token="token", refresh_token="refresh")
        status = connector.status()
        self.assertEqual(status.state, ConnectorState.CONNECTED)

    async def test_status_returns_frozen_dataclass(self):
        connector = GmailConnector(access_token="", refresh_token="")
        status = connector.status()
        self.assertIsInstance(status, GmailConnectorStatus)

    async def test_list_unread_disabled_raises(self):
        connector = GmailConnector(access_token="", refresh_token="")
        with self.assertRaises(ConnectorUnavailableError):
            await connector.list_unread()

    async def test_list_unread_expired_raises(self):
        connector = GmailConnector(access_token="", refresh_token="refresh")
        with self.assertRaises(AuthExpiredError):
            await connector.list_unread()

    async def test_list_unread_connected_returns_placeholder(self):
        connector = GmailConnector(access_token="token", refresh_token="refresh")
        messages = await connector.list_unread()
        self.assertEqual(len(messages), 1)
        self.assertEqual(messages[0]["thread_id"], "gmail-demo-1")

    async def test_list_unread_max_results_respected(self):
        connector = GmailConnector(access_token="token", refresh_token="refresh")
        messages = await connector.list_unread(max_results=0)
        self.assertEqual(messages, [])

    async def test_search_hit_returns_matching_message(self):
        connector = GmailConnector(access_token="token", refresh_token="refresh")
        results = await connector.search("welcome")
        self.assertEqual(len(results), 1)

    async def test_search_miss_returns_empty(self):
        connector = GmailConnector(access_token="token", refresh_token="refresh")
        results = await connector.search("zzz-no-match-zzz")
        self.assertEqual(results, [])

    async def test_search_disabled_raises(self):
        connector = GmailConnector(access_token="", refresh_token="")
        with self.assertRaises(ConnectorUnavailableError):
            await connector.search("anything")

    async def test_search_expired_raises(self):
        connector = GmailConnector(access_token="", refresh_token="refresh")
        with self.assertRaises(AuthExpiredError):
            await connector.search("anything")

    def test_no_reply_method(self):
        """reply() is a write operation and must not exist on read-only connector."""
        connector = GmailConnector(access_token="token", refresh_token="refresh")
        self.assertFalse(hasattr(connector, "reply"))


class TestCalendarConnector(unittest.IsolatedAsyncioTestCase):
    async def test_status_disabled_without_tokens(self):
        connector = CalendarConnector(access_token="", refresh_token="")
        status = connector.status()
        self.assertEqual(status.state, ConnectorState.DISABLED)
        self.assertIn("not configured", status.detail)

    async def test_status_expired_with_only_refresh_token(self):
        connector = CalendarConnector(access_token="", refresh_token="refresh")
        status = connector.status()
        self.assertEqual(status.state, ConnectorState.EXPIRED)
        self.assertIn("expired", status.detail)

    async def test_status_connected_with_access_token(self):
        connector = CalendarConnector(access_token="token", refresh_token="refresh")
        status = connector.status()
        self.assertEqual(status.state, ConnectorState.CONNECTED)

    async def test_status_returns_frozen_dataclass(self):
        connector = CalendarConnector(access_token="", refresh_token="")
        status = connector.status()
        self.assertIsInstance(status, CalendarConnectorStatus)

    async def test_list_events_disabled_raises(self):
        connector = CalendarConnector(access_token="", refresh_token="")
        with self.assertRaises(ConnectorUnavailableError):
            await connector.list_events("2024-01-15", "2024-01-15")

    async def test_list_events_expired_raises(self):
        connector = CalendarConnector(access_token="", refresh_token="refresh")
        with self.assertRaises(AuthExpiredError):
            await connector.list_events("2024-01-15", "2024-01-15")

    async def test_list_events_connected_returns_placeholder(self):
        connector = CalendarConnector(access_token="token", refresh_token="refresh")
        events = await connector.list_events("2024-01-15", "2024-01-15")
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0]["id"], "calendar-demo-1")

    async def test_list_events_start_date_used_in_event(self):
        connector = CalendarConnector(access_token="token", refresh_token="refresh")
        events = await connector.list_events("2025-06-01", "2025-06-01")
        self.assertIn("2025-06-01", events[0]["start"])

    async def test_find_free_slots_connected_returns_slots(self):
        connector = CalendarConnector(access_token="token", refresh_token="refresh")
        slots = await connector.find_free_slots("2024-01-15", 30)
        self.assertEqual(len(slots), 2)
        self.assertIn("start", slots[0])
        self.assertIn("end", slots[0])

    async def test_find_free_slots_disabled_raises(self):
        connector = CalendarConnector(access_token="", refresh_token="")
        with self.assertRaises(ConnectorUnavailableError):
            await connector.find_free_slots("2024-01-15", 30)

    async def test_find_free_slots_expired_raises(self):
        connector = CalendarConnector(access_token="", refresh_token="refresh")
        with self.assertRaises(AuthExpiredError):
            await connector.find_free_slots("2024-01-15", 30)

    def test_no_create_event_method(self):
        """create_event() is a write operation and must not exist on read-only connector."""
        connector = CalendarConnector(access_token="token", refresh_token="refresh")
        self.assertFalse(hasattr(connector, "create_event"))


if __name__ == "__main__":
    unittest.main()
