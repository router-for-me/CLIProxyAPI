"""Unit tests for shadow-claw gateway audit module."""

import json
import os
import sqlite3
import tempfile
import time
import unittest
from unittest.mock import MagicMock

import sys
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from audit import AuditLog, install_audit, _map_fields


class TestAuditLogCreation(unittest.TestCase):
    """AuditLog creates the DB and tables in a temp directory."""

    def test_creates_db_file(self):
        with tempfile.TemporaryDirectory() as td:
            db_path = os.path.join(td, "audit.db")
            al = AuditLog(db_path=db_path)
            self.assertTrue(os.path.exists(db_path))
            al.close()

    def test_creates_audit_events_table(self):
        with tempfile.TemporaryDirectory() as td:
            db_path = os.path.join(td, "audit.db")
            al = AuditLog(db_path=db_path)
            conn = sqlite3.connect(db_path)
            cursor = conn.execute(
                "SELECT name FROM sqlite_master WHERE type='table' AND name='audit_events'"
            )
            self.assertIsNotNone(cursor.fetchone())
            conn.close()
            al.close()

    def test_creates_subdirectory(self):
        with tempfile.TemporaryDirectory() as td:
            db_path = os.path.join(td, "sub", "audit.db")
            al = AuditLog(db_path=db_path)
            self.assertTrue(os.path.exists(db_path))
            al.close()


class TestRecordSync(unittest.TestCase):
    """record_sync stores an event and returns an integer ID."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.al = AuditLog(db_path=os.path.join(self.td.name, "audit.db"))

    def tearDown(self):
        self.al.close()
        self.td.cleanup()

    def test_returns_integer_id(self):
        row_id = self.al.record_sync(
            "chat.request.success", route="openai", model="gpt-4", duration_ms=100
        )
        self.assertIsInstance(row_id, int)
        self.assertGreater(row_id, 0)

    def test_stores_event_type(self):
        self.al.record_sync("chat.request.error", error="timeout")
        rows = self.al.search_sync(event_type="chat.request.error")
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["event_type"], "chat.request.error")

    def test_stores_user_id_via_telegram_mapping(self):
        self.al.record_sync("chat.request.success", telegram_user_id=42)
        rows = self.al.search_sync(user_id=42)
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["user_id"], 42)

    def test_stores_tool_name(self):
        self.al.record_sync("tool.job.completed", tool_name="bash", ok=True)
        rows = self.al.search_sync(tool_name="bash")
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["tool_name"], "bash")

    def test_success_inferred_from_ok_field(self):
        self.al.record_sync("tool.job.completed", ok=True)
        rows = self.al.search_sync()
        self.assertEqual(rows[0]["success"], 1)

    def test_success_inferred_from_event_suffix(self):
        self.al.record_sync("chat.request.success")
        rows = self.al.search_sync()
        self.assertEqual(rows[0]["success"], 1)

    def test_failure_inferred_from_error_suffix(self):
        self.al.record_sync("chat.request.error")
        rows = self.al.search_sync()
        self.assertEqual(rows[0]["success"], 0)

    def test_extras_stored_in_details(self):
        self.al.record_sync("chat.request.success", custom_field="hello")
        rows = self.al.search_sync()
        self.assertIsInstance(rows[0]["details"], dict)
        self.assertEqual(rows[0]["details"]["custom_field"], "hello")


class TestSearchSync(unittest.TestCase):
    """search_sync with various filters."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.al = AuditLog(db_path=os.path.join(self.td.name, "audit.db"))

    def tearDown(self):
        self.al.close()
        self.td.cleanup()

    def test_search_by_event_type(self):
        self.al.record_sync("chat.request.success")
        self.al.record_sync("tool.job.completed", ok=True)
        rows = self.al.search_sync(event_type="chat.request.success")
        self.assertEqual(len(rows), 1)

    def test_search_by_user_id(self):
        self.al.record_sync("chat.request.success", telegram_user_id=1)
        self.al.record_sync("chat.request.success", telegram_user_id=2)
        rows = self.al.search_sync(user_id=1)
        self.assertEqual(len(rows), 1)

    def test_search_by_tool_name(self):
        self.al.record_sync("tool.job.completed", tool_name="bash", ok=True)
        self.al.record_sync("tool.job.completed", tool_name="python", ok=True)
        rows = self.al.search_sync(tool_name="python")
        self.assertEqual(len(rows), 1)

    def test_search_with_limit(self):
        for _ in range(10):
            self.al.record_sync("chat.request.success")
        rows = self.al.search_sync(limit=3)
        self.assertEqual(len(rows), 3)

    def test_search_since_hours(self):
        self.al.record_sync("chat.request.success")
        # All events are "now", so since_hours=1 should include them
        rows = self.al.search_sync(since_hours=1)
        self.assertEqual(len(rows), 1)

    def test_search_empty(self):
        rows = self.al.search_sync()
        self.assertEqual(rows, [])


class TestSummarySync(unittest.TestCase):
    """summary_sync produces a formatted activity summary."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.al = AuditLog(db_path=os.path.join(self.td.name, "audit.db"))

    def tearDown(self):
        self.al.close()
        self.td.cleanup()

    def test_summary_empty_db(self):
        result = self.al.summary_sync(hours=1)
        self.assertIn("Activity Summary", result)
        self.assertIn("0 chat requests", result)
        self.assertIn("0 tool executions", result)
        self.assertIn("0 auth denials", result)

    def test_summary_with_chat_events(self):
        self.al.record_sync("chat.request.success", model="gpt-4", duration_ms=100)
        self.al.record_sync("chat.request.error", model="gpt-4", duration_ms=50)
        result = self.al.summary_sync(hours=1)
        self.assertIn("2 chat requests", result)
        self.assertIn("1 ok", result)
        self.assertIn("1 failed", result)
        self.assertIn("gpt-4", result)

    def test_summary_with_tool_events(self):
        self.al.record_sync("tool.job.completed", ok=True, duration_ms=200)
        result = self.al.summary_sync(hours=1)
        self.assertIn("1 tool executions", result)
        self.assertIn("1 ok", result)

    def test_summary_with_auth_denials(self):
        self.al.record_sync("gateway.auth.denied")
        result = self.al.summary_sync(hours=1)
        self.assertIn("1 auth denials", result)

    def test_summary_avg_latency(self):
        self.al.record_sync("chat.request.success", model="m", duration_ms=100)
        self.al.record_sync("chat.request.success", model="m", duration_ms=200)
        result = self.al.summary_sync(hours=1)
        self.assertIn("150ms", result)

    def test_summary_label_for_hours(self):
        result = self.al.summary_sync(hours=24)
        self.assertIn("last 24h", result)


class TestExportJsonSync(unittest.TestCase):
    """export_json_sync returns valid JSON."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.al = AuditLog(db_path=os.path.join(self.td.name, "audit.db"))

    def tearDown(self):
        self.al.close()
        self.td.cleanup()

    def test_returns_valid_json(self):
        self.al.record_sync("chat.request.success", model="gpt-4")
        result = self.al.export_json_sync()
        parsed = json.loads(result)
        self.assertIsInstance(parsed, list)
        self.assertEqual(len(parsed), 1)

    def test_empty_export(self):
        result = self.al.export_json_sync()
        parsed = json.loads(result)
        self.assertEqual(parsed, [])

    def test_export_filter_by_event_type(self):
        self.al.record_sync("chat.request.success")
        self.al.record_sync("tool.job.completed", ok=True)
        result = self.al.export_json_sync(event_type="chat.request.success")
        parsed = json.loads(result)
        self.assertEqual(len(parsed), 1)
        self.assertEqual(parsed[0]["event_type"], "chat.request.success")


class TestCleanupSync(unittest.TestCase):
    """cleanup_sync removes events older than the specified number of days."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.al = AuditLog(db_path=os.path.join(self.td.name, "audit.db"))

    def tearDown(self):
        self.al.close()
        self.td.cleanup()

    def test_cleanup_removes_old_events(self):
        self.al.record_sync("chat.request.success")
        # Backdate the event
        old_ts = time.time() - (40 * 86400)  # 40 days ago
        self.al._conn.execute(
            "UPDATE audit_events SET timestamp = ?", (old_ts,)
        )
        self.al._conn.commit()
        removed = self.al.cleanup_sync(older_than_days=30)
        self.assertEqual(removed, 1)

    def test_cleanup_keeps_recent_events(self):
        self.al.record_sync("chat.request.success")
        removed = self.al.cleanup_sync(older_than_days=30)
        self.assertEqual(removed, 0)
        rows = self.al.search_sync()
        self.assertEqual(len(rows), 1)


class TestInstallAudit(unittest.TestCase):
    """install_audit wraps log_event and records to audit DB."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.al = AuditLog(db_path=os.path.join(self.td.name, "audit.db"))

    def tearDown(self):
        self.al.close()
        self.td.cleanup()

    def test_original_is_called(self):
        original = MagicMock()
        wrapped = install_audit(original, self.al)
        wrapped("chat.request.success", route="openai")
        original.assert_called_once_with("chat.request.success", route="openai")

    def test_audit_db_receives_event(self):
        original = MagicMock()
        wrapped = install_audit(original, self.al)
        wrapped("chat.request.success", route="openai", model="gpt-4")
        rows = self.al.search_sync(event_type="chat.request.success")
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["model"], "gpt-4")

    def test_audit_failure_does_not_propagate(self):
        """If audit recording fails, the original call should still succeed."""
        original = MagicMock()
        wrapped = install_audit(original, self.al)
        # Close the DB to force an error in record_sync
        self.al.close()
        # Should not raise
        wrapped("chat.request.success", route="r")
        original.assert_called_once()

    def test_multiple_events(self):
        original = MagicMock()
        wrapped = install_audit(original, self.al)
        wrapped("chat.request.success", route="a")
        wrapped("chat.request.error", route="b")
        wrapped("tool.job.completed", ok=True, tool_name="bash")
        self.assertEqual(original.call_count, 3)
        rows = self.al.search_sync()
        self.assertEqual(len(rows), 3)


class TestMapFields(unittest.TestCase):
    """_map_fields translates log_event field names to audit schema columns."""

    def test_telegram_user_id_mapped(self):
        result = _map_fields("test", {"telegram_user_id": 42})
        self.assertEqual(result["user_id"], 42)
        self.assertNotIn("telegram_user_id", result)

    def test_error_mapped_to_error_text(self):
        result = _map_fields("test", {"error": "something broke"})
        self.assertEqual(result["error_text"], "something broke")

    def test_ok_true_mapped_to_success_1(self):
        result = _map_fields("test", {"ok": True})
        self.assertEqual(result["success"], 1)

    def test_ok_false_mapped_to_success_0(self):
        result = _map_fields("test", {"ok": False})
        self.assertEqual(result["success"], 0)

    def test_success_inferred_from_suffix_success(self):
        result = _map_fields("chat.request.success", {})
        self.assertEqual(result["success"], 1)

    def test_success_inferred_from_suffix_error(self):
        result = _map_fields("chat.request.error", {})
        self.assertEqual(result["success"], 0)

    def test_success_inferred_from_suffix_timeout(self):
        result = _map_fields("tool.exec.timeout", {})
        self.assertEqual(result["success"], 0)

    def test_success_inferred_from_suffix_denied(self):
        result = _map_fields("gateway.auth.denied", {})
        self.assertEqual(result["success"], 0)

    def test_success_inferred_from_suffix_completed(self):
        result = _map_fields("tool.job.completed", {})
        self.assertEqual(result["success"], 1)

    def test_direct_columns_pass_through(self):
        result = _map_fields("test", {
            "tool_name": "bash", "model": "gpt-4", "route": "openai",
            "duration_ms": 100, "chat_id": 5
        })
        self.assertEqual(result["tool_name"], "bash")
        self.assertEqual(result["model"], "gpt-4")
        self.assertEqual(result["route"], "openai")
        self.assertEqual(result["duration_ms"], 100)
        self.assertEqual(result["chat_id"], 5)

    def test_extras_go_to_details(self):
        result = _map_fields("test", {"custom_key": "custom_val"})
        self.assertIn("details", result)
        details = json.loads(result["details"])
        self.assertEqual(details["custom_key"], "custom_val")

    def test_suppressed_fields_not_in_details(self):
        result = _map_fields("test", {
            "telegram_user_id": 1, "ok": True, "event": "x"
        })
        if "details" in result:
            details = json.loads(result["details"])
            self.assertNotIn("telegram_user_id", details)
            self.assertNotIn("ok", details)
            self.assertNotIn("event", details)


if __name__ == "__main__":
    unittest.main()
