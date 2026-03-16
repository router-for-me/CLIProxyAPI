"""Golden workflow and hostile failure-path tests for the Personal Agent milestone.

Covers:
- Inbox capability: golden list/search/reply workflows + hostile failure paths
- Calendar capability: golden today/list/create/free-time + hostile failure paths
- Agent loop integration with inbox and calendar tools
- Injectable clock strategy for time-sensitive calendar tests
- ToolRegistry isolation between test cases via setUp/tearDown reset

Test pyramid positioning:
  - Unit-level: individual tool invocations via ToolRegistry.invoke()
  - Integration: full AgentLoop driving tool calls end-to-end
  - Hostile: connector errors, bad inputs, boundary conditions
"""

import asyncio
import json
import os
import sys
import types
import unittest
from datetime import datetime
from unittest.mock import AsyncMock

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

# ---------------------------------------------------------------------------
# Stub telegram + telegram.ext so imports don't blow up in CI
# ---------------------------------------------------------------------------

if "telegram" not in sys.modules:
    _tg = types.ModuleType("telegram")

    class _IKB:
        def __init__(self, text, callback_data=None):
            self.text = text
            self.callback_data = callback_data

    class _IKM:
        def __init__(self, kb):
            self.inline_keyboard = kb

    _tg.InlineKeyboardButton = _IKB
    _tg.InlineKeyboardMarkup = _IKM

    class _Update:
        ALL_TYPES = object()

    _tg.Update = _Update
    sys.modules["telegram"] = _tg

if "telegram.ext" not in sys.modules:
    _tgext = types.ModuleType("telegram.ext")

    class _CtxTypes:
        DEFAULT_TYPE = object

    _tgext.ContextTypes = _CtxTypes
    sys.modules["telegram.ext"] = _tgext


def _run(coro):
    return asyncio.run(coro)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


class _FailingInboxConnector:
    """Connector that always raises — for hostile-path tests."""

    async def list_unread(self, max_results=10):
        raise RuntimeError("inbox backend unavailable")

    async def search(self, query, max_results=10):
        raise RuntimeError("search backend unavailable")

    async def reply(self, thread_id, body):
        raise RuntimeError("reply backend unavailable")


class _FailingCalendarConnector:
    """Connector that always raises — for hostile-path tests."""

    async def list_events(self, start_date, end_date):
        raise RuntimeError("calendar backend unavailable")

    async def create_event(self, title, start_time, end_time, description=""):
        raise RuntimeError("create backend unavailable")

    async def find_free_slots(self, date, duration_minutes):
        raise RuntimeError("free-slots backend unavailable")


class _EmptyCalendarConnector:
    """Connector that always returns empty collections."""

    async def list_events(self, start_date, end_date):
        return []

    async def create_event(self, title, start_time, end_time, description=""):
        raise AssertionError("create_event should not be called by list/free-time tools")

    async def find_free_slots(self, date, duration_minutes):
        return []


# ---------------------------------------------------------------------------
# Inbox capability — golden workflows
# ---------------------------------------------------------------------------


class TestInboxCapabilityGoldenWorkflows(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        from agent import ToolRegistry
        ToolRegistry.reset()
        from capabilities.inbox_capability import StubInboxConnector, register_inbox_tools
        self.connector = StubInboxConnector()
        register_inbox_tools(self.connector)
        from agent import ToolRegistry as TR
        self.TR = TR

    def tearDown(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    async def test_inbox_list_unread_returns_formatted_messages(self):
        result = await self.TR.invoke("inbox_list_unread", {})
        self.assertIn("t1", result)
        self.assertIn("alice@example.com", result)
        self.assertIn("Project update", result)
        self.assertIn("t2", result)
        self.assertIn("bob@example.com", result)

    async def test_inbox_list_unread_respects_max_results(self):
        result = await self.TR.invoke("inbox_list_unread", {"max_results": 1})
        # Only one message in result (first one)
        self.assertIn("t1", result)
        self.assertNotIn("t2", result)

    async def test_inbox_list_unread_clamps_max_results_above_50(self):
        result = await self.TR.invoke("inbox_list_unread", {"max_results": 999})
        # Should not error — just apply internal cap
        self.assertNotIn("Failed", result)

    async def test_inbox_search_by_sender(self):
        result = await self.TR.invoke("inbox_search", {"query": "alice"})
        self.assertIn("alice@example.com", result)
        self.assertNotIn("bob@example.com", result)

    async def test_inbox_search_by_subject_keyword(self):
        result = await self.TR.invoke("inbox_search", {"query": "meeting"})
        self.assertIn("Meeting tomorrow", result)

    async def test_inbox_search_no_results_returns_no_messages_found(self):
        result = await self.TR.invoke("inbox_search", {"query": "zzz_no_match_xyz"})
        self.assertIn("No messages found", result)

    async def test_inbox_reply_sends_and_confirms(self):
        result = await self.TR.invoke(
            "inbox_reply", {"thread_id": "t1", "body": "Thanks for the update!"}
        )
        self.assertIn("t1", result)
        self.assertEqual(len(self.connector.sent), 1)
        self.assertEqual(self.connector.sent[0]["thread_id"], "t1")
        self.assertEqual(self.connector.sent[0]["body"], "Thanks for the update!")



# ---------------------------------------------------------------------------
# Inbox capability — hostile failure paths
# ---------------------------------------------------------------------------


class TestInboxCapabilityHostileFailures(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        from agent import ToolRegistry
        ToolRegistry.reset()
        from capabilities.inbox_capability import register_inbox_tools
        register_inbox_tools(_FailingInboxConnector())
        from agent import ToolRegistry as TR
        self.TR = TR

    def tearDown(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    async def test_list_unread_connector_error_returns_error_string_not_exception(self):
        result = await self.TR.invoke("inbox_list_unread", {})
        self.assertIn("Failed to fetch inbox", result)
        self.assertIn("inbox backend unavailable", result)

    async def test_search_connector_error_returns_error_string(self):
        result = await self.TR.invoke("inbox_search", {"query": "hello"})
        self.assertIn("Search failed", result)
        self.assertIn("search backend unavailable", result)

    async def test_reply_connector_error_returns_error_string(self):
        result = await self.TR.invoke(
            "inbox_reply", {"thread_id": "t1", "body": "Hi"}
        )
        self.assertIn("Failed to send reply", result)
        self.assertIn("reply backend unavailable", result)

    async def test_search_empty_query_returns_validation_error(self):
        result = await self.TR.invoke("inbox_search", {"query": ""})
        self.assertIn("Error", result)
        self.assertIn("empty", result)

    async def test_reply_empty_thread_id_returns_validation_error(self):
        result = await self.TR.invoke("inbox_reply", {"thread_id": "", "body": "hello"})
        self.assertIn("Error", result)
        self.assertIn("empty", result)

    async def test_reply_empty_body_returns_validation_error(self):
        result = await self.TR.invoke("inbox_reply", {"thread_id": "t1", "body": ""})
        self.assertIn("Error", result)
        self.assertIn("empty", result)


# ---------------------------------------------------------------------------
# Calendar capability — golden workflows (with injectable clock)
# ---------------------------------------------------------------------------


class TestCalendarCapabilityGoldenWorkflows(unittest.IsolatedAsyncioTestCase):
    """Uses a fake clock pinned to 2024-01-15 for all time-sensitive operations."""

    FIXED_DATE = "2024-01-15"
    FIXED_DT = datetime(2024, 1, 15, 8, 30, 0)

    def setUp(self):
        from agent import ToolRegistry
        ToolRegistry.reset()
        from capabilities.calendar_capability import StubCalendarConnector, register_calendar_tools
        self.connector = StubCalendarConnector()
        register_calendar_tools(self.connector, clock=lambda: self.FIXED_DT)
        from agent import ToolRegistry as TR
        self.TR = TR

    def tearDown(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    async def test_calendar_today_returns_agenda_for_fake_clock_date(self):
        result = await self.TR.invoke("calendar_today", {})
        self.assertIn(self.FIXED_DATE, result)
        self.assertIn("Team standup", result)
        self.assertIn("Lunch with Sarah", result)

    async def test_calendar_today_uses_clock_not_real_now(self):
        """Prove the clock is injectable: different fake dates appear in the header."""
        from agent import ToolRegistry
        ToolRegistry.reset()
        from capabilities.calendar_capability import StubCalendarConnector, register_calendar_tools

        dt_2025 = datetime(2025, 6, 21, 9, 0, 0)
        register_calendar_tools(StubCalendarConnector(), clock=lambda: dt_2025)
        result = await ToolRegistry.invoke("calendar_today", {})
        # The header line must contain the injected date
        self.assertIn("2025-06-21", result)
        # And NOT the date from the setUp fixture clock
        self.assertNotIn(f"({self.FIXED_DATE})", result)

    async def test_calendar_list_events_returns_events_for_range(self):
        result = await self.TR.invoke(
            "calendar_list_events",
            {"start_date": "2024-01-15", "end_date": "2024-01-17"},
        )
        self.assertIn("Team standup", result)
        self.assertIn("2024-01-15", result)

    async def test_calendar_list_events_defaults_end_date_to_start_date(self):
        result = await self.TR.invoke(
            "calendar_list_events",
            {"start_date": "2024-01-15"},
        )
        self.assertIn("Team standup", result)

    async def test_calendar_create_event_succeeds_with_valid_times(self):
        result = await self.TR.invoke(
            "calendar_create_event",
            {
                "title": "Sprint review",
                "start_time": "2024-01-15 14:00",
                "end_time": "2024-01-15 15:00",
            },
        )
        self.assertIn("Sprint review", result)
        self.assertIn("new_evt_1", result)
        self.assertEqual(len(self.connector.created), 1)
        self.assertEqual(self.connector.created[0]["title"], "Sprint review")

    async def test_calendar_create_event_stores_description(self):
        await self.TR.invoke(
            "calendar_create_event",
            {
                "title": "1:1",
                "start_time": "2024-01-15 10:00",
                "end_time": "2024-01-15 10:30",
                "description": "Quarterly check-in",
            },
        )
        self.assertEqual(self.connector.created[0]["description"], "Quarterly check-in")

    async def test_calendar_find_free_time_returns_slots(self):
        result = await self.TR.invoke(
            "calendar_find_free_time",
            {"date": "2024-01-15", "duration_minutes": 30},
        )
        self.assertIn("30-minute slots", result)
        self.assertIn("2024-01-15 14:00", result)


# ---------------------------------------------------------------------------
# Calendar capability — hostile failure paths
# ---------------------------------------------------------------------------


class TestCalendarCapabilityHostileFailures(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        from agent import ToolRegistry
        ToolRegistry.reset()
        from capabilities.calendar_capability import register_calendar_tools
        self.fixed_dt = datetime(2024, 1, 15, 9, 0)
        register_calendar_tools(
            _FailingCalendarConnector(), clock=lambda: self.fixed_dt
        )
        from agent import ToolRegistry as TR
        self.TR = TR

    def tearDown(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    async def test_create_event_end_before_start_returns_validation_error(self):
        result = await self.TR.invoke(
            "calendar_create_event",
            {
                "title": "Bad event",
                "start_time": "2024-01-15 15:00",
                "end_time": "2024-01-15 14:00",
            },
        )
        self.assertIn("end_time must be after start_time", result)

    async def test_create_event_end_equal_to_start_returns_validation_error(self):
        result = await self.TR.invoke(
            "calendar_create_event",
            {
                "title": "Zero duration",
                "start_time": "2024-01-15 10:00",
                "end_time": "2024-01-15 10:00",
            },
        )
        self.assertIn("end_time must be after start_time", result)

    async def test_create_event_invalid_start_format_returns_error(self):
        result = await self.TR.invoke(
            "calendar_create_event",
            {
                "title": "Bad start",
                "start_time": "not-a-date",
                "end_time": "2024-01-15 11:00",
            },
        )
        self.assertIn("Invalid start_time", result)
        self.assertIn("not-a-date", result)

    async def test_create_event_invalid_end_format_returns_error(self):
        result = await self.TR.invoke(
            "calendar_create_event",
            {
                "title": "Bad end",
                "start_time": "2024-01-15 10:00",
                "end_time": "tomorrow afternoon",
            },
        )
        self.assertIn("Invalid end_time", result)

    async def test_create_event_empty_title_returns_validation_error(self):
        result = await self.TR.invoke(
            "calendar_create_event",
            {
                "title": "",
                "start_time": "2024-01-15 10:00",
                "end_time": "2024-01-15 11:00",
            },
        )
        self.assertIn("Error", result)
        self.assertIn("title", result)

    async def test_calendar_today_connector_error_returns_error_string(self):
        result = await self.TR.invoke("calendar_today", {})
        self.assertIn("Failed to fetch today's agenda", result)
        self.assertIn("calendar backend unavailable", result)

    async def test_calendar_list_connector_error_returns_error_string(self):
        result = await self.TR.invoke(
            "calendar_list_events", {"start_date": "2024-01-15"}
        )
        self.assertIn("Failed to fetch events", result)

    async def test_calendar_find_free_time_connector_error_returns_error_string(self):
        result = await self.TR.invoke(
            "calendar_find_free_time",
            {"date": "2024-01-15", "duration_minutes": 30},
        )
        self.assertIn("Failed to query free time", result)

    async def test_find_free_time_empty_calendar_returns_no_slots_message(self):
        from agent import ToolRegistry
        ToolRegistry.reset()
        from capabilities.calendar_capability import register_calendar_tools
        register_calendar_tools(_EmptyCalendarConnector(), clock=lambda: self.fixed_dt)
        result = await ToolRegistry.invoke(
            "calendar_find_free_time",
            {"date": "2024-01-15", "duration_minutes": 60},
        )
        self.assertIn("No free", result)
        self.assertIn("60-minute", result)

    async def test_find_free_time_zero_duration_returns_validation_error(self):
        result = await self.TR.invoke(
            "calendar_find_free_time",
            {"date": "2024-01-15", "duration_minutes": 0},
        )
        self.assertIn("Error", result)
        self.assertIn("positive", result)

    async def test_calendar_list_events_empty_start_returns_validation_error(self):
        result = await self.TR.invoke(
            "calendar_list_events",
            {"start_date": ""},
        )
        self.assertIn("Error", result)


# ---------------------------------------------------------------------------
# Agent loop integration — inbox workflow
# ---------------------------------------------------------------------------


class TestAgentLoopInboxWorkflow(unittest.IsolatedAsyncioTestCase):
    """Full AgentLoop driving inbox tools end-to-end."""

    def setUp(self):
        from agent import ToolRegistry
        ToolRegistry.reset()
        from capabilities.inbox_capability import StubInboxConnector, register_inbox_tools
        self.connector = StubInboxConnector()
        register_inbox_tools(self.connector)

    def tearDown(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    async def test_loop_calls_inbox_list_unread_and_returns_text(self):
        """Simulate: user asks for inbox, LLM calls inbox_list_unread, returns summary."""
        from agent import AgentLoop

        call_count = 0

        async def mock_send(messages, tools):
            nonlocal call_count
            call_count += 1
            if call_count == 1:
                return {
                    "content": "",
                    "tool_calls": [
                        {
                            "id": "tc_inbox_1",
                            "function": {
                                "name": "inbox_list_unread",
                                "arguments": json.dumps({"max_results": 5}),
                            },
                        }
                    ],
                    "finish_reason": "tool_calls",
                }
            # Second turn: LLM sees the inbox listing and responds
            last_tool_msg = next(m for m in reversed(messages) if m["role"] == "tool")
            self.assertIn("alice@example.com", last_tool_msg["content"])
            return {
                "content": "You have 2 unread messages from alice and bob.",
                "tool_calls": [],
                "finish_reason": "stop",
            }

        loop = AgentLoop(send_fn=mock_send)
        result = await loop.run([{"role": "user", "content": "Check my inbox"}])
        self.assertIn("2 unread messages", result)
        self.assertEqual(call_count, 2)

    async def test_loop_inbox_search_then_reply_two_tool_calls(self):
        """Multi-step: search for message, then reply to it."""
        from agent import AgentLoop

        call_count = 0

        async def mock_send(messages, tools):
            nonlocal call_count
            call_count += 1
            if call_count == 1:
                # First: search
                return {
                    "content": "",
                    "tool_calls": [
                        {
                            "id": "tc_search",
                            "function": {
                                "name": "inbox_search",
                                "arguments": json.dumps({"query": "meeting"}),
                            },
                        }
                    ],
                    "finish_reason": "tool_calls",
                }
            if call_count == 2:
                # Second: reply
                return {
                    "content": "",
                    "tool_calls": [
                        {
                            "id": "tc_reply",
                            "function": {
                                "name": "inbox_reply",
                                "arguments": json.dumps(
                                    {"thread_id": "t2", "body": "Sure, 10am works!"}
                                ),
                            },
                        }
                    ],
                    "finish_reason": "tool_calls",
                }
            return {
                "content": "I replied to Bob's meeting message.",
                "tool_calls": [],
                "finish_reason": "stop",
            }

        loop = AgentLoop(send_fn=mock_send, max_iterations=5)
        result = await loop.run(
            [{"role": "user", "content": "Find the meeting message and reply: sure works"}]
        )
        self.assertIn("replied to Bob", result)
        self.assertEqual(len(self.connector.sent), 1)
        self.assertEqual(self.connector.sent[0]["thread_id"], "t2")


# ---------------------------------------------------------------------------
# Agent loop integration — calendar workflow
# ---------------------------------------------------------------------------


class TestAgentLoopCalendarWorkflow(unittest.IsolatedAsyncioTestCase):
    """Full AgentLoop driving calendar tools end-to-end with fake clock."""

    FIXED_DT = datetime(2024, 1, 15, 9, 0, 0)

    def setUp(self):
        from agent import ToolRegistry
        ToolRegistry.reset()
        from capabilities.calendar_capability import StubCalendarConnector, register_calendar_tools
        self.connector = StubCalendarConnector()
        register_calendar_tools(self.connector, clock=lambda: self.FIXED_DT)

    def tearDown(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    async def test_loop_gets_today_agenda(self):
        from agent import AgentLoop

        call_count = 0

        async def mock_send(messages, tools):
            nonlocal call_count
            call_count += 1
            if call_count == 1:
                return {
                    "content": "",
                    "tool_calls": [
                        {
                            "id": "tc_today",
                            "function": {
                                "name": "calendar_today",
                                "arguments": "{}",
                            },
                        }
                    ],
                    "finish_reason": "tool_calls",
                }
            tool_result = next(m for m in reversed(messages) if m["role"] == "tool")
            self.assertIn("Team standup", tool_result["content"])
            self.assertIn("2024-01-15", tool_result["content"])
            return {
                "content": "Today you have a standup at 09:00 and lunch at 12:00.",
                "tool_calls": [],
                "finish_reason": "stop",
            }

        loop = AgentLoop(send_fn=mock_send)
        result = await loop.run([{"role": "user", "content": "What's on my calendar today?"}])
        self.assertIn("standup", result)

    async def test_loop_creates_event_after_validation_rejected_once(self):
        """The LLM fixes bad times after seeing a validation error from the capability."""
        from agent import AgentLoop

        call_count = 0

        async def mock_send(messages, tools):
            nonlocal call_count
            call_count += 1
            if call_count == 1:
                # LLM makes a mistake: end before start
                return {
                    "content": "",
                    "tool_calls": [
                        {
                            "id": "tc_bad",
                            "function": {
                                "name": "calendar_create_event",
                                "arguments": json.dumps(
                                    {
                                        "title": "Focus block",
                                        "start_time": "2024-01-15 15:00",
                                        "end_time": "2024-01-15 14:00",
                                    }
                                ),
                            },
                        }
                    ],
                    "finish_reason": "tool_calls",
                }
            if call_count == 2:
                # LLM self-corrects after seeing the validation error
                error_msg = next(m for m in reversed(messages) if m["role"] == "tool")
                self.assertIn("end_time must be after start_time", error_msg["content"])
                return {
                    "content": "",
                    "tool_calls": [
                        {
                            "id": "tc_fix",
                            "function": {
                                "name": "calendar_create_event",
                                "arguments": json.dumps(
                                    {
                                        "title": "Focus block",
                                        "start_time": "2024-01-15 14:00",
                                        "end_time": "2024-01-15 15:00",
                                    }
                                ),
                            },
                        }
                    ],
                    "finish_reason": "tool_calls",
                }
            return {
                "content": "Focus block created from 14:00 to 15:00.",
                "tool_calls": [],
                "finish_reason": "stop",
            }

        loop = AgentLoop(send_fn=mock_send, max_iterations=5)
        result = await loop.run(
            [{"role": "user", "content": "Block 14:00-15:00 as focus time"}]
        )
        self.assertIn("Focus block", result)
        # Connector should have exactly one created event (the corrected one)
        self.assertEqual(len(self.connector.created), 1)
        self.assertEqual(self.connector.created[0]["start"], "2024-01-15 14:00")


# ---------------------------------------------------------------------------
# tools_panel param parsers for new capabilities
# ---------------------------------------------------------------------------


class TestToolsPanelCapabilityParsers(unittest.TestCase):
    """Unit-test the new _parse_tool_input cases added for inbox and calendar."""

    def _parse(self, param_type, text):
        import tools_panel
        return tools_panel._parse_tool_input("dummy_tool", param_type, text)

    def test_thread_reply_splits_thread_and_body(self):
        result = self._parse("thread_reply", "t1 | Thanks for the heads-up!")
        self.assertEqual(result["thread_id"], "t1")
        self.assertEqual(result["body"], "Thanks for the heads-up!")

    def test_date_range_parses_start_and_end(self):
        result = self._parse("date_range", "2024-01-15 | 2024-01-20")
        self.assertEqual(result["start_date"], "2024-01-15")
        self.assertEqual(result["end_date"], "2024-01-20")

    def test_date_range_defaults_end_to_start_when_single_date(self):
        result = self._parse("date_range", "2024-01-15")
        self.assertEqual(result["start_date"], "2024-01-15")
        self.assertEqual(result["end_date"], "2024-01-15")

    def test_event_create_parses_title_start_end(self):
        result = self._parse(
            "event_create",
            "Sprint review | 2024-01-15 14:00 | 2024-01-15 15:00",
        )
        self.assertEqual(result["title"], "Sprint review")
        self.assertEqual(result["start_time"], "2024-01-15 14:00")
        self.assertEqual(result["end_time"], "2024-01-15 15:00")
        self.assertEqual(result["description"], "")

    def test_event_create_parses_description_when_present(self):
        result = self._parse(
            "event_create",
            "1:1 | 2024-01-15 10:00 | 2024-01-15 10:30 | Quarterly review",
        )
        self.assertEqual(result["description"], "Quarterly review")

    def test_free_time_parses_date_and_duration(self):
        result = self._parse("free_time", "2024-01-15 | 30")
        self.assertEqual(result["date"], "2024-01-15")
        self.assertEqual(result["duration_minutes"], 30)

    def test_free_time_defaults_duration_to_30_when_missing(self):
        result = self._parse("free_time", "2024-01-15")
        self.assertEqual(result["duration_minutes"], 30)

    def test_free_time_defaults_duration_to_30_when_non_numeric(self):
        result = self._parse("free_time", "2024-01-15 | half hour")
        self.assertEqual(result["duration_minutes"], 30)


class TestInboxCapabilityEdgeCases(unittest.IsolatedAsyncioTestCase):
    """Separate class so setUp always starts from a clean registry."""

    def setUp(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    def tearDown(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    async def test_inbox_empty_inbox_returns_no_messages_found(self):
        from agent import ToolRegistry
        from capabilities.inbox_capability import StubInboxConnector, register_inbox_tools
        register_inbox_tools(StubInboxConnector(messages=[]))
        result = await ToolRegistry.invoke("inbox_list_unread", {})
        self.assertIn("No messages found", result)


# ---------------------------------------------------------------------------
# ToolRegistry isolation — sanity check
# ---------------------------------------------------------------------------


class TestToolRegistryIsolation(unittest.TestCase):
    """Verify that setUp/tearDown ToolRegistry.reset() prevents cross-test pollution."""

    def setUp(self):
        from agent import ToolRegistry
        ToolRegistry.reset()
        self.TR = ToolRegistry

    def tearDown(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    def test_inbox_tools_not_present_before_registration(self):
        self.assertNotIn("inbox_list_unread", self.TR.list_tools())
        self.assertNotIn("calendar_today", self.TR.list_tools())

    def test_inbox_tools_present_after_registration(self):
        from capabilities.inbox_capability import StubInboxConnector, register_inbox_tools
        register_inbox_tools(StubInboxConnector())
        self.assertIn("inbox_list_unread", self.TR.list_tools())
        self.assertIn("inbox_search", self.TR.list_tools())
        self.assertIn("inbox_reply", self.TR.list_tools())

    def test_calendar_tools_present_after_registration(self):
        from capabilities.calendar_capability import StubCalendarConnector, register_calendar_tools
        register_calendar_tools(StubCalendarConnector())
        self.assertIn("calendar_today", self.TR.list_tools())
        self.assertIn("calendar_list_events", self.TR.list_tools())
        self.assertIn("calendar_create_event", self.TR.list_tools())
        self.assertIn("calendar_find_free_time", self.TR.list_tools())

    def test_reset_clears_capability_tools(self):
        from capabilities.inbox_capability import StubInboxConnector, register_inbox_tools
        register_inbox_tools(StubInboxConnector())
        self.assertIn("inbox_list_unread", self.TR.list_tools())
        self.TR.reset()
        self.assertNotIn("inbox_list_unread", self.TR.list_tools())


if __name__ == "__main__":
    unittest.main()
