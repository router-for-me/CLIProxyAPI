"""Tests for KnowledgeVault and MemoryCapability.

Covers:
- KnowledgeItem ingestion / upsert semantics
- Source-aware recall and source-scoped listing
- Exact-key lookup via get_by_ref
- Snapshot context builder (empty and non-empty)
- Delete (snapshot removal)
- ConversationManager extensions: source tag, get_memory, list_by_source, delete_memory
- MemoryCapability orchestration (programmatic API)
- Tool registration and invocation via ToolRegistry
"""

import asyncio
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))


def _run(coro):
    return asyncio.run(coro)


# ---------------------------------------------------------------------------
# ConversationManager extensions
# ---------------------------------------------------------------------------


class TestConversationManagerExtensions(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.db_path = os.path.join(self.tmp, "test.db")
        from memory_store import ConversationManager
        self.cm = ConversationManager(db_path=self.db_path)

    def tearDown(self):
        self.cm.close()
        import shutil
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_store_with_source_tag_embeds_source(self):
        self.cm.store_memory_sync("key1", "some content", source="gmail")
        row = self.cm.get_memory_sync("key1")
        self.assertIsNotNone(row)
        self.assertIn("source:gmail", row["tags"])

    def test_store_with_tags_and_source_combines_both(self):
        self.cm.store_memory_sync("key2", "content", tags=["email"], source="gmail")
        row = self.cm.get_memory_sync("key2")
        self.assertIn("email", row["tags"])
        self.assertIn("source:gmail", row["tags"])

    def test_get_memory_returns_none_for_missing_key(self):
        result = self.cm.get_memory_sync("no_such_key")
        self.assertIsNone(result)

    def test_get_memory_returns_correct_row(self):
        self.cm.store_memory_sync("pet", "I have a dog named Rex")
        row = self.cm.get_memory_sync("pet")
        self.assertEqual(row["key"], "pet")
        self.assertEqual(row["content"], "I have a dog named Rex")

    def test_list_by_source_returns_matching(self):
        self.cm.store_memory_sync("a", "apple from gmail", source="gmail")
        self.cm.store_memory_sync("b", "calendar event", source="calendar")
        results = self.cm.list_by_source_sync("gmail")
        self.assertEqual(len(results), 1)
        self.assertEqual(results[0]["key"], "a")

    def test_list_by_source_excludes_other_sources(self):
        self.cm.store_memory_sync("m1", "from gmail", source="gmail")
        self.cm.store_memory_sync("m2", "from calendar", source="calendar")
        gmail_results = self.cm.list_by_source_sync("calendar")
        self.assertEqual(len(gmail_results), 1)
        self.assertEqual(gmail_results[0]["key"], "m2")

    def test_delete_memory_removes_row(self):
        self.cm.store_memory_sync("to_delete", "bye")
        deleted = self.cm.delete_memory_sync("to_delete")
        self.assertTrue(deleted)
        self.assertIsNone(self.cm.get_memory_sync("to_delete"))

    def test_delete_memory_returns_false_for_missing(self):
        deleted = self.cm.delete_memory_sync("ghost_key")
        self.assertFalse(deleted)

    def test_recall_with_source_scopes_results(self):
        self.cm.store_memory_sync("gm1", "meeting with alice", source="gmail")
        self.cm.store_memory_sync("cal1", "meeting with alice", source="calendar")
        results = self.cm.recall_sync("meeting", limit=5, source="gmail")
        for r in results:
            self.assertIn("source:gmail", r["tags"])
        self.assertFalse(any("source:calendar" in r["tags"] for r in results))

    def test_recall_without_source_returns_all(self):
        self.cm.store_memory_sync("x1", "shared topic", source="gmail")
        self.cm.store_memory_sync("x2", "shared topic", source="calendar")
        results = self.cm.recall_sync("shared topic", limit=10)
        keys = {r["key"] for r in results}
        self.assertIn("x1", keys)
        self.assertIn("x2", keys)

    def test_async_get_memory(self):
        self.cm.store_memory_sync("async_key", "async content")
        result = _run(self.cm.get_memory("async_key"))
        self.assertIsNotNone(result)
        self.assertEqual(result["content"], "async content")

    def test_async_list_by_source(self):
        self.cm.store_memory_sync("s1", "stuff", source="research")
        results = _run(self.cm.list_by_source("research"))
        self.assertEqual(len(results), 1)

    def test_async_delete_memory(self):
        self.cm.store_memory_sync("del_async", "gone soon")
        deleted = _run(self.cm.delete_memory("del_async"))
        self.assertTrue(deleted)
        self.assertIsNone(self.cm.get_memory_sync("del_async"))


# ---------------------------------------------------------------------------
# KnowledgeVault
# ---------------------------------------------------------------------------


class TestKnowledgeVault(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        db_path = os.path.join(self.tmp, "test.db")
        from memory_store import ConversationManager
        from knowledge_vault import KnowledgeVault
        self.cm = ConversationManager(db_path=db_path)
        self.vault = KnowledgeVault(self.cm)

    def tearDown(self):
        self.cm.close()
        import shutil
        shutil.rmtree(self.tmp, ignore_errors=True)

    def _item(self, ref, source="gmail", kind="email", summary="some summary", tags=None):
        from knowledge_vault import KnowledgeItem
        return KnowledgeItem(ref=ref, source=source, kind=kind, summary=summary, tags=list(tags or []))

    def test_ingest_stores_item_under_ref(self):
        item = self._item("gmail:msg_001", summary="Meeting rescheduled to 3pm")
        self.vault.ingest_sync(item)
        row = self.vault.get_by_ref_sync("gmail:msg_001")
        self.assertIsNotNone(row)
        self.assertEqual(row["key"], "gmail:msg_001")
        self.assertIn("Meeting", row["content"])

    def test_ingest_upserts_existing_ref(self):
        self.vault.ingest_sync(self._item("gmail:msg_002", summary="v1"))
        self.vault.ingest_sync(self._item("gmail:msg_002", summary="v2 updated"))
        row = self.vault.get_by_ref_sync("gmail:msg_002")
        self.assertEqual(row["content"], "v2 updated")

    def test_get_by_ref_returns_none_for_unknown_ref(self):
        result = self.vault.get_by_ref_sync("gmail:no_such_msg")
        self.assertIsNone(result)

    def test_kind_tag_is_searchable(self):
        self.vault.ingest_sync(self._item("cal:evt_001", source="calendar", kind="event", summary="Team standup"))
        results = self.vault.lookup_sync("standup")
        self.assertTrue(any("cal:evt_001" == r["key"] for r in results))

    def test_lookup_source_filter_excludes_other_sources(self):
        self.vault.ingest_sync(self._item("gmail:m1", source="gmail", summary="project deadline email"))
        self.vault.ingest_sync(self._item("cal:e1", source="calendar", kind="event", summary="project deadline meeting"))
        results = self.vault.lookup_sync("project deadline", source="gmail")
        for r in results:
            self.assertIn("source:gmail", r["tags"])
        self.assertFalse(any("source:calendar" in r["tags"] for r in results))

    def test_lookup_kind_filter_restricts_results(self):
        self.vault.ingest_sync(self._item("gmail:m2", kind="email", summary="budget review email"))
        self.vault.ingest_sync(self._item("cal:e2", kind="event", summary="budget review meeting"))
        results = self.vault.lookup_sync("budget review", kind="event")
        self.assertTrue(all("event" in r.get("tags", "").split(",") for r in results))
        self.assertFalse(any("gmail:m2" == r["key"] for r in results))

    def test_lookup_returns_empty_list_when_no_match(self):
        results = self.vault.lookup_sync("zzz_absolutely_no_match_xyz")
        self.assertEqual(results, [])

    def test_snapshot_context_returns_empty_when_no_match(self):
        ctx = self.vault.snapshot_context_sync("zzz_no_match")
        self.assertEqual(ctx, "")

    def test_snapshot_context_includes_relevant_items(self):
        self.vault.ingest_sync(self._item("gmail:m3", summary="Alice sent Q1 report"))
        ctx = self.vault.snapshot_context_sync("Q1 report")
        self.assertIn("Relevant knowledge:", ctx)
        self.assertIn("gmail:m3", ctx)
        self.assertIn("Alice", ctx)

    def test_list_by_source_returns_only_that_source(self):
        self.vault.ingest_sync(self._item("gmail:m4", source="gmail"))
        self.vault.ingest_sync(self._item("cal:e3", source="calendar", kind="event", summary="other"))
        results = self.vault.list_by_source_sync("gmail")
        self.assertTrue(all("source:gmail" in r["tags"] for r in results))
        self.assertFalse(any("cal:e3" == r["key"] for r in results))

    def test_delete_removes_item(self):
        self.vault.ingest_sync(self._item("gmail:m5", summary="delete me"))
        deleted = self.vault.delete_sync("gmail:m5")
        self.assertTrue(deleted)
        self.assertIsNone(self.vault.get_by_ref_sync("gmail:m5"))

    def test_delete_returns_false_for_unknown_ref(self):
        result = self.vault.delete_sync("gmail:ghost")
        self.assertFalse(result)

    def test_async_ingest_and_lookup(self):
        from knowledge_vault import KnowledgeItem
        item = KnowledgeItem(ref="gmail:m6", source="gmail", kind="email", summary="async ingestion test")
        _run(self.vault.ingest(item))
        results = _run(self.vault.lookup("async ingestion"))
        self.assertTrue(any("gmail:m6" == r["key"] for r in results))

    def test_async_snapshot_context(self):
        from knowledge_vault import KnowledgeItem
        item = KnowledgeItem(ref="gmail:m7", source="gmail", kind="email", summary="quarterly budget async")
        _run(self.vault.ingest(item))
        ctx = _run(self.vault.snapshot_context("quarterly budget"))
        self.assertIn("Relevant knowledge:", ctx)
        self.assertIn("gmail:m7", ctx)


# ---------------------------------------------------------------------------
# MemoryCapability
# ---------------------------------------------------------------------------


class TestMemoryCapability(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        db_path = os.path.join(self.tmp, "test.db")
        from memory_store import ConversationManager
        from knowledge_vault import KnowledgeVault
        from capabilities.memory_capability import MemoryCapability
        self.cm = ConversationManager(db_path=db_path)
        self.vault = KnowledgeVault(self.cm)
        self.cap = MemoryCapability(self.cm, self.vault)

    def tearDown(self):
        self.cm.close()
        import shutil
        shutil.rmtree(self.tmp, ignore_errors=True)

    async def test_remember_and_recall(self):
        await self.cap.remember("fav_city", "London")
        results = await self.cap.recall("city")
        self.assertTrue(any(r["key"] == "fav_city" for r in results))

    async def test_ingest_knowledge_and_lookup(self):
        await self.cap.ingest_knowledge(
            ref="gmail:msg_cap_001",
            source="gmail",
            kind="email",
            summary="Invoice from Acme Corp",
        )
        results = await self.cap.lookup_knowledge("Invoice Acme")
        self.assertTrue(any(r["key"] == "gmail:msg_cap_001" for r in results))

    async def test_build_context_includes_memories_and_knowledge(self):
        await self.cap.remember("hobby", "I enjoy cycling")
        await self.cap.ingest_knowledge(
            ref="gmail:msg_cap_002",
            source="gmail",
            kind="email",
            summary="Cycling group meetup next Saturday",
        )
        ctx = await self.cap.build_context("sess1", "cycling meetup")
        # Both raw memory and knowledge should be represented
        self.assertIn("cycling", ctx.lower())

    async def test_build_context_returns_empty_when_nothing_matches(self):
        ctx = await self.cap.build_context("sess1", "zzz_totally_unknown_topic")
        self.assertEqual(ctx, "")

    async def test_lookup_knowledge_with_source_scope(self):
        await self.cap.ingest_knowledge("gmail:cap_s1", "gmail", "email", "budget Q2")
        await self.cap.ingest_knowledge("cal:cap_s1", "calendar", "event", "budget Q2 meeting")
        results = await self.cap.lookup_knowledge("budget Q2", source="gmail")
        for r in results:
            self.assertIn("source:gmail", r["tags"])


# ---------------------------------------------------------------------------
# Tool registration
# ---------------------------------------------------------------------------


class TestMemoryCapabilityTools(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        db_path = os.path.join(self.tmp, "test.db")
        from agent import ToolRegistry
        ToolRegistry.reset()
        from memory_store import ConversationManager
        from knowledge_vault import KnowledgeVault
        from capabilities.memory_capability import register_memory_tools
        self.cm = ConversationManager(db_path=db_path)
        self.vault = KnowledgeVault(self.cm)
        register_memory_tools(vault=self.vault, manager=self.cm)
        self.TR = ToolRegistry

    def tearDown(self):
        from agent import ToolRegistry
        ToolRegistry.reset()
        self.cm.close()
        import shutil
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_expected_tools_registered(self):
        tools = self.TR.list_tools()
        self.assertIn("knowledge_ingest", tools)
        self.assertIn("knowledge_lookup", tools)
        self.assertIn("knowledge_context", tools)

    async def test_knowledge_ingest_tool_stores_item(self):
        result = await self.TR.invoke("knowledge_ingest", {
            "ref": "gmail:t_msg_001",
            "source": "gmail",
            "kind": "email",
            "summary": "Team offsite confirmed for March 20",
        })
        self.assertIn("gmail:t_msg_001", result)
        row = self.vault.get_by_ref_sync("gmail:t_msg_001")
        self.assertIsNotNone(row)
        self.assertIn("offsite", row["content"])

    async def test_knowledge_lookup_tool_finds_ingested(self):
        await self.TR.invoke("knowledge_ingest", {
            "ref": "gmail:t_msg_002",
            "source": "gmail",
            "kind": "email",
            "summary": "Quarterly review scheduled",
        })
        result = await self.TR.invoke("knowledge_lookup", {"query": "quarterly review"})
        self.assertIn("gmail:t_msg_002", result)
        self.assertIn("Quarterly review", result)

    async def test_knowledge_lookup_no_results(self):
        result = await self.TR.invoke("knowledge_lookup", {"query": "zzz_no_such_thing"})
        self.assertIn("No matching knowledge found", result)

    async def test_knowledge_context_tool_returns_summary(self):
        await self.TR.invoke("knowledge_ingest", {
            "ref": "cal:t_evt_001",
            "source": "calendar",
            "kind": "event",
            "summary": "Product launch on April 5",
        })
        result = await self.TR.invoke("knowledge_context", {"topic": "product launch"})
        self.assertIn("Relevant knowledge:", result)
        self.assertIn("cal:t_evt_001", result)

    async def test_knowledge_context_empty_when_no_match(self):
        result = await self.TR.invoke("knowledge_context", {"topic": "zzz_unknown_xyz"})
        self.assertEqual(result, "")

    async def test_knowledge_lookup_source_filter_via_tool(self):
        await self.TR.invoke("knowledge_ingest", {
            "ref": "gmail:flt_001",
            "source": "gmail",
            "kind": "email",
            "summary": "filter test content",
        })
        await self.TR.invoke("knowledge_ingest", {
            "ref": "cal:flt_001",
            "source": "calendar",
            "kind": "event",
            "summary": "filter test content",
        })
        result = await self.TR.invoke("knowledge_lookup", {
            "query": "filter test",
            "source": "gmail",
        })
        self.assertIn("gmail:flt_001", result)
        self.assertNotIn("cal:flt_001", result)


if __name__ == "__main__":
    unittest.main()
