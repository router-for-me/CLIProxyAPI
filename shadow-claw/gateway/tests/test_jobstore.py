"""Unit tests for shadow-claw gateway jobstore module."""

import asyncio
import os
import sqlite3
import tempfile
import time
import unittest

import sys
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from jobstore import (
    JobStore,
    STATUS_COMPLETED,
    STATUS_FAILED,
    STATUS_LOST,
    STATUS_PENDING,
    STATUS_RUNNING,
    format_jobs_list,
    _relative_time,
    _truncate_prompt,
)


class TestJobStoreCreation(unittest.TestCase):
    """JobStore creates the DB file and tables on init."""

    def test_creates_db_file(self):
        with tempfile.TemporaryDirectory() as td:
            db_path = os.path.join(td, "test.db")
            store = JobStore(db_path=db_path)
            self.assertTrue(os.path.exists(db_path))
            store.close()

    def test_creates_jobs_table(self):
        with tempfile.TemporaryDirectory() as td:
            db_path = os.path.join(td, "test.db")
            store = JobStore(db_path=db_path)
            conn = sqlite3.connect(db_path)
            cursor = conn.execute(
                "SELECT name FROM sqlite_master WHERE type='table' AND name='jobs'"
            )
            self.assertIsNotNone(cursor.fetchone())
            conn.close()
            store.close()

    def test_creates_subdirectory_if_missing(self):
        with tempfile.TemporaryDirectory() as td:
            db_path = os.path.join(td, "sub", "dir", "test.db")
            store = JobStore(db_path=db_path)
            self.assertTrue(os.path.exists(db_path))
            store.close()


class TestCreateJob(unittest.TestCase):
    """create_job returns a uuid hex string and inserts a row."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.store = JobStore(db_path=os.path.join(self.td.name, "test.db"))

    def tearDown(self):
        self.store.close()
        self.td.cleanup()

    def test_create_job_returns_hex_string(self):
        job_id = asyncio.run(self.store.create_job("bash", prompt="echo hi"))
        self.assertIsInstance(job_id, str)
        self.assertEqual(len(job_id), 32)
        # Verify it is valid hex
        int(job_id, 16)

    def test_create_job_persists(self):
        job_id = asyncio.run(self.store.create_job("bash", prompt="echo hi"))
        job = asyncio.run(self.store.get_job(job_id))
        self.assertIsNotNone(job)
        self.assertEqual(job["tool_name"], "bash")
        self.assertEqual(job["prompt"], "echo hi")
        self.assertEqual(job["status"], STATUS_PENDING)

    def test_create_job_with_user_and_chat(self):
        job_id = asyncio.run(
            self.store.create_job("python", prompt="print(1)", user_id=42, chat_id=99)
        )
        job = asyncio.run(self.store.get_job(job_id))
        self.assertEqual(job["user_id"], 42)
        self.assertEqual(job["chat_id"], 99)


class TestUpdateStatus(unittest.TestCase):
    """update_status changes the status column."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.store = JobStore(db_path=os.path.join(self.td.name, "test.db"))

    def tearDown(self):
        self.store.close()
        self.td.cleanup()

    def test_update_to_running(self):
        job_id = asyncio.run(self.store.create_job("bash"))
        asyncio.run(self.store.update_status(job_id, STATUS_RUNNING))
        job = asyncio.run(self.store.get_job(job_id))
        self.assertEqual(job["status"], STATUS_RUNNING)

    def test_update_to_completed_with_summary(self):
        job_id = asyncio.run(self.store.create_job("bash"))
        asyncio.run(self.store.update_status(
            job_id, STATUS_COMPLETED, result_summary="done", exit_code=0
        ))
        job = asyncio.run(self.store.get_job(job_id))
        self.assertEqual(job["status"], STATUS_COMPLETED)
        self.assertEqual(job["result_summary"], "done")
        self.assertEqual(job["exit_code"], 0)
        self.assertIsNotNone(job["completed_at"])

    def test_update_to_failed_sets_completed_at(self):
        job_id = asyncio.run(self.store.create_job("bash"))
        asyncio.run(self.store.update_status(job_id, STATUS_FAILED, exit_code=1))
        job = asyncio.run(self.store.get_job(job_id))
        self.assertEqual(job["status"], STATUS_FAILED)
        self.assertIsNotNone(job["completed_at"])

    def test_invalid_status_raises(self):
        job_id = asyncio.run(self.store.create_job("bash"))
        with self.assertRaises(ValueError):
            asyncio.run(self.store.update_status(job_id, "bogus"))


class TestGetJob(unittest.TestCase):
    """get_job returns dict or None."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.store = JobStore(db_path=os.path.join(self.td.name, "test.db"))

    def tearDown(self):
        self.store.close()
        self.td.cleanup()

    def test_get_existing_job(self):
        job_id = asyncio.run(self.store.create_job("bash"))
        job = asyncio.run(self.store.get_job(job_id))
        self.assertIsInstance(job, dict)
        self.assertEqual(job["id"], job_id)

    def test_get_nonexistent_job_returns_none(self):
        job = asyncio.run(self.store.get_job("nonexistent_id"))
        self.assertIsNone(job)


class TestListRecent(unittest.TestCase):
    """list_recent with optional filters."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.store = JobStore(db_path=os.path.join(self.td.name, "test.db"))

    def tearDown(self):
        self.store.close()
        self.td.cleanup()

    def test_list_recent_returns_all(self):
        asyncio.run(self.store.create_job("bash"))
        asyncio.run(self.store.create_job("python"))
        jobs = asyncio.run(self.store.list_recent(limit=10))
        self.assertEqual(len(jobs), 2)

    def test_list_recent_respects_limit(self):
        for _ in range(5):
            asyncio.run(self.store.create_job("bash"))
        jobs = asyncio.run(self.store.list_recent(limit=3))
        self.assertEqual(len(jobs), 3)

    def test_list_recent_filter_by_tool(self):
        asyncio.run(self.store.create_job("bash"))
        asyncio.run(self.store.create_job("python"))
        jobs = asyncio.run(self.store.list_recent(tool_name="python"))
        self.assertEqual(len(jobs), 1)
        self.assertEqual(jobs[0]["tool_name"], "python")

    def test_list_recent_filter_by_status(self):
        jid = asyncio.run(self.store.create_job("bash"))
        asyncio.run(self.store.update_status(jid, STATUS_COMPLETED))
        asyncio.run(self.store.create_job("bash"))  # still pending
        jobs = asyncio.run(self.store.list_recent(status=STATUS_COMPLETED))
        self.assertEqual(len(jobs), 1)
        self.assertEqual(jobs[0]["status"], STATUS_COMPLETED)

    def test_list_recent_invalid_status_raises(self):
        with self.assertRaises(ValueError):
            asyncio.run(self.store.list_recent(status="bogus"))

    def test_list_recent_empty(self):
        jobs = asyncio.run(self.store.list_recent())
        self.assertEqual(jobs, [])


class TestCleanupExpired(unittest.TestCase):
    """cleanup_expired removes old jobs beyond TTL."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.store = JobStore(db_path=os.path.join(self.td.name, "test.db"))

    def tearDown(self):
        self.store.close()
        self.td.cleanup()

    def test_cleanup_removes_old_jobs(self):
        job_id = asyncio.run(self.store.create_job("bash"))
        # Manually backdate the created_at to be very old
        conn = self.store._get_conn()
        old_ts = time.time() - 200000  # ~55 hours ago
        conn.execute("UPDATE jobs SET created_at = ? WHERE id = ?", (old_ts, job_id))
        conn.commit()

        removed = asyncio.run(self.store.cleanup_expired(ttl_hours=24))
        self.assertEqual(removed, 1)

        job = asyncio.run(self.store.get_job(job_id))
        self.assertIsNone(job)

    def test_cleanup_keeps_recent_jobs(self):
        asyncio.run(self.store.create_job("bash"))
        removed = asyncio.run(self.store.cleanup_expired(ttl_hours=24))
        self.assertEqual(removed, 0)
        jobs = asyncio.run(self.store.list_recent())
        self.assertEqual(len(jobs), 1)


class TestMarkLostOnRestart(unittest.TestCase):
    """mark_lost_on_restart flips running/pending to lost."""

    def setUp(self):
        self.td = tempfile.TemporaryDirectory()
        self.store = JobStore(db_path=os.path.join(self.td.name, "test.db"))

    def tearDown(self):
        self.store.close()
        self.td.cleanup()

    def test_pending_marked_lost(self):
        asyncio.run(self.store.create_job("bash"))
        count = asyncio.run(self.store.mark_lost_on_restart())
        self.assertEqual(count, 1)
        jobs = asyncio.run(self.store.list_recent(status=STATUS_LOST))
        self.assertEqual(len(jobs), 1)

    def test_running_marked_lost(self):
        jid = asyncio.run(self.store.create_job("bash"))
        asyncio.run(self.store.update_status(jid, STATUS_RUNNING))
        count = asyncio.run(self.store.mark_lost_on_restart())
        self.assertEqual(count, 1)
        job = asyncio.run(self.store.get_job(jid))
        self.assertEqual(job["status"], STATUS_LOST)

    def test_completed_not_marked_lost(self):
        jid = asyncio.run(self.store.create_job("bash"))
        asyncio.run(self.store.update_status(jid, STATUS_COMPLETED))
        count = asyncio.run(self.store.mark_lost_on_restart())
        self.assertEqual(count, 0)
        job = asyncio.run(self.store.get_job(jid))
        self.assertEqual(job["status"], STATUS_COMPLETED)

    def test_multiple_jobs_mixed(self):
        j1 = asyncio.run(self.store.create_job("bash"))  # pending
        j2 = asyncio.run(self.store.create_job("bash"))
        asyncio.run(self.store.update_status(j2, STATUS_RUNNING))
        j3 = asyncio.run(self.store.create_job("bash"))
        asyncio.run(self.store.update_status(j3, STATUS_COMPLETED))
        count = asyncio.run(self.store.mark_lost_on_restart())
        self.assertEqual(count, 2)


class TestFormatJobsList(unittest.TestCase):
    """format_jobs_list produces readable output."""

    def test_empty_list(self):
        result = format_jobs_list([])
        self.assertEqual(result, "No jobs found.")

    def test_single_job(self):
        jobs = [{
            "status": "completed",
            "tool_name": "bash",
            "created_at": time.time() - 120,
            "prompt": "echo hello",
        }]
        result = format_jobs_list(jobs)
        self.assertIn("Recent Jobs", result)
        self.assertIn("[completed]", result)
        self.assertIn("bash", result)
        self.assertIn("echo hello", result)

    def test_truncated_prompt(self):
        long_prompt = "A" * 100
        result = _truncate_prompt(long_prompt, max_len=40)
        self.assertLessEqual(len(result), 40)
        self.assertTrue(result.endswith("..."))

    def test_none_prompt(self):
        result = _truncate_prompt(None)
        self.assertEqual(result, "")

    def test_relative_time_seconds(self):
        self.assertIn("s ago", _relative_time(time.time() - 30))

    def test_relative_time_minutes(self):
        self.assertIn("m ago", _relative_time(time.time() - 300))

    def test_relative_time_hours(self):
        self.assertIn("h ago", _relative_time(time.time() - 7200))

    def test_relative_time_days(self):
        self.assertIn("d ago", _relative_time(time.time() - 200000))

    def test_relative_time_future(self):
        self.assertEqual(_relative_time(time.time() + 100), "just now")


if __name__ == "__main__":
    unittest.main()
