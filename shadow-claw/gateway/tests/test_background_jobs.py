"""Tests for background_jobs execution-mode classification and dispatch."""

import os
import sys
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from background_jobs import ExecutionMode, DispatchResult, classify_execution_mode, dispatch


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


async def _ok() -> str:
    return "ok"


async def _fail() -> str:
    raise RuntimeError("tool exploded")


class _FakeJob:
    def __init__(self):
        self.created = []
        self.updates = []
        self._id_counter = 0

    async def create_job(self, tool_name, prompt=None, user_id=None, chat_id=None):
        self._id_counter += 1
        jid = f"job-{self._id_counter}"
        self.created.append({"id": jid, "tool_name": tool_name})
        return jid

    async def update_status(self, job_id, status, result_summary=None, exit_code=None):
        self.updates.append({"id": job_id, "status": status})


class _FakeApproval:
    def __init__(self):
        self.created = []
        self._id_counter = 0

    async def create(self, tool_name, payload, user_id=None, chat_id=None):
        self._id_counter += 1
        from dataclasses import dataclass

        @dataclass
        class _Req:
            id: str
            tool_name: str
            payload: dict
            status: str = "pending"

        req = _Req(id=f"appr-{self._id_counter}", tool_name=tool_name, payload=payload)
        self.created.append(req)
        return req


# ---------------------------------------------------------------------------
# Classification
# ---------------------------------------------------------------------------


class TestClassifyExecutionMode(unittest.TestCase):
    def test_memory_tools_are_sync(self):
        for name in ["memory_store", "memory_recall", "knowledge_lookup"]:
            self.assertEqual(classify_execution_mode(name), ExecutionMode.SYNC)

    def test_observe_tools_are_sync(self):
        for name in ["inbox_list_unread", "calendar_today", "browse_url"]:
            self.assertEqual(classify_execution_mode(name), ExecutionMode.SYNC)

    def test_act_tools_require_approval(self):
        for name in ["payment_send", "inbox_reply", "calendar_create_event", "desktop_command"]:
            self.assertEqual(classify_execution_mode(name), ExecutionMode.APPROVAL_REQUIRED)

    def test_research_topic_is_background(self):
        self.assertEqual(classify_execution_mode("research_topic"), ExecutionMode.BACKGROUND)

    def test_plan_execute_is_background(self):
        self.assertEqual(classify_execution_mode("plan_execute"), ExecutionMode.BACKGROUND)

    def test_unknown_tool_defaults_to_sync(self):
        self.assertEqual(classify_execution_mode("totally_unknown_tool_xyz"), ExecutionMode.SYNC)


# ---------------------------------------------------------------------------
# Dispatch — SYNC mode
# ---------------------------------------------------------------------------


class TestDispatchSync(unittest.IsolatedAsyncioTestCase):
    async def test_sync_returns_result(self):
        dr = await dispatch("memory_recall", _ok)
        self.assertEqual(dr.mode, ExecutionMode.SYNC)
        self.assertIsNone(dr.job_id)
        self.assertEqual(dr.result, "ok")

    async def test_sync_propagates_exception(self):
        with self.assertRaises(RuntimeError):
            await dispatch("memory_recall", _fail)


# ---------------------------------------------------------------------------
# Dispatch — BACKGROUND mode
# ---------------------------------------------------------------------------


class TestDispatchBackground(unittest.IsolatedAsyncioTestCase):
    async def test_background_creates_job_and_returns_result(self):
        js = _FakeJob()
        dr = await dispatch("research_topic", _ok, job_store=js)
        self.assertEqual(dr.mode, ExecutionMode.BACKGROUND)
        self.assertIsNotNone(dr.job_id)
        self.assertEqual(dr.result, "ok")
        statuses = [u["status"] for u in js.updates]
        self.assertIn("running", statuses)
        self.assertIn("completed", statuses)

    async def test_background_marks_failed_on_exception(self):
        js = _FakeJob()
        with self.assertRaises(RuntimeError):
            await dispatch("research_topic", _fail, job_store=js)
        statuses = [u["status"] for u in js.updates]
        self.assertIn("failed", statuses)

    async def test_background_falls_back_to_sync_without_job_store(self):
        dr = await dispatch("research_topic", _ok, job_store=None)
        self.assertEqual(dr.mode, ExecutionMode.SYNC)
        self.assertIsNone(dr.job_id)
        self.assertEqual(dr.result, "ok")


# ---------------------------------------------------------------------------
# Dispatch — APPROVAL_REQUIRED mode
# ---------------------------------------------------------------------------


class TestDispatchApproval(unittest.IsolatedAsyncioTestCase):
    async def test_approval_required_creates_approval_request(self):
        appr = _FakeApproval()
        dr = await dispatch(
            "payment_send",
            _ok,
            approval_store=appr,
            payload={"amount": "10", "recipient": "bob"},
        )
        self.assertEqual(dr.mode, ExecutionMode.APPROVAL_REQUIRED)
        self.assertIsNotNone(dr.job_id)
        self.assertIsNone(dr.result)
        self.assertEqual(len(appr.created), 1)
        self.assertEqual(appr.created[0].tool_name, "payment_send")

    async def test_approval_required_falls_back_to_sync_without_store(self):
        dr = await dispatch("payment_send", _ok, approval_store=None)
        self.assertEqual(dr.mode, ExecutionMode.SYNC)
        self.assertIsNone(dr.job_id)
        self.assertEqual(dr.result, "ok")

    async def test_approval_payload_stored_on_request(self):
        appr = _FakeApproval()
        payload = {"amount": "99", "recipient": "alice"}
        await dispatch("inbox_reply", _ok, approval_store=appr, payload=payload)
        self.assertEqual(appr.created[0].payload, payload)


if __name__ == "__main__":
    unittest.main()
