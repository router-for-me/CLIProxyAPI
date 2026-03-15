"""Tests for Shadow-Claw agent core: loop, registry, memory, SSRF blocklist.

Core path tests only (~200 lines):
- Agent loop with mock LLM response containing tool_calls
- Circuit breaker at MAX_TOOL_ITERATIONS
- Memory store/recall roundtrip
- URL blocklist for private IPs
"""

import asyncio
import json
import os
import sqlite3
import tempfile
import unittest

import sys
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))


class TestToolRegistry(unittest.TestCase):
    """Test ToolRegistry registration and invocation."""

    def setUp(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    def tearDown(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    def test_register_and_list(self):
        from agent import ToolRegistry

        @ToolRegistry.register("test_tool", "A test tool", {
            "type": "object", "properties": {"x": {"type": "string"}}, "required": ["x"],
        })
        async def test_tool(x: str) -> str:
            return f"got {x}"

        self.assertIn("test_tool", ToolRegistry.list_tools())

    def test_get_definitions_format(self):
        from agent import ToolRegistry

        @ToolRegistry.register("echo", "Echoes input", {
            "type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"],
        })
        async def echo(text: str) -> str:
            return text

        defs = ToolRegistry.get_definitions()
        self.assertEqual(len(defs), 1)
        self.assertEqual(defs[0]["type"], "function")
        self.assertEqual(defs[0]["function"]["name"], "echo")

    def test_invoke_success(self):
        from agent import ToolRegistry

        @ToolRegistry.register("add", "Add numbers", {
            "type": "object", "properties": {}, "required": [],
        })
        async def add() -> str:
            return "42"

        result = asyncio.get_event_loop().run_until_complete(
            ToolRegistry.invoke("add", {})
        )
        self.assertEqual(result, "42")

    def test_invoke_unknown_tool(self):
        from agent import ToolRegistry

        result = asyncio.get_event_loop().run_until_complete(
            ToolRegistry.invoke("nonexistent", {})
        )
        self.assertIn("not found", result)

    def test_invoke_exception_caught(self):
        from agent import ToolRegistry

        @ToolRegistry.register("fail", "Always fails", {
            "type": "object", "properties": {}, "required": [],
        })
        async def fail() -> str:
            raise ValueError("boom")

        result = asyncio.get_event_loop().run_until_complete(
            ToolRegistry.invoke("fail", {})
        )
        self.assertIn("failed", result)
        self.assertIn("boom", result)


class TestAgentLoop(unittest.TestCase):
    """Test AgentLoop state machine with mocked LLM responses."""

    def setUp(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    def tearDown(self):
        from agent import ToolRegistry
        ToolRegistry.reset()

    def test_simple_text_response(self):
        """LLM returns text only — loop should return immediately."""
        from agent import AgentLoop

        async def mock_send(messages, tools):
            return {"content": "Hello!", "tool_calls": [], "finish_reason": "stop"}

        loop = AgentLoop(send_fn=mock_send)
        result = asyncio.get_event_loop().run_until_complete(
            loop.run([{"role": "user", "content": "hi"}])
        )
        self.assertEqual(result, "Hello!")

    def test_tool_call_then_text(self):
        """LLM calls a tool, gets result, then responds with text."""
        from agent import AgentLoop, ToolRegistry

        @ToolRegistry.register("greet", "Greet someone", {
            "type": "object",
            "properties": {"name": {"type": "string"}},
            "required": ["name"],
        })
        async def greet(name: str) -> str:
            return f"Hi, {name}!"

        call_count = 0

        async def mock_send(messages, tools):
            nonlocal call_count
            call_count += 1
            if call_count == 1:
                # First call: LLM requests tool
                return {
                    "content": "",
                    "tool_calls": [{
                        "id": "tc_1",
                        "function": {
                            "name": "greet",
                            "arguments": json.dumps({"name": "Alice"}),
                        },
                    }],
                    "finish_reason": "tool_calls",
                }
            # Second call: LLM returns text after seeing tool result
            return {"content": "Alice says hi!", "tool_calls": [], "finish_reason": "stop"}

        loop = AgentLoop(send_fn=mock_send)
        result = asyncio.get_event_loop().run_until_complete(
            loop.run([{"role": "user", "content": "greet Alice"}])
        )
        self.assertEqual(result, "Alice says hi!")
        self.assertEqual(call_count, 2)

    def test_circuit_breaker(self):
        """Loop must stop at max_iterations even if LLM keeps calling tools."""
        from agent import AgentLoop, ToolRegistry

        @ToolRegistry.register("noop", "Does nothing", {
            "type": "object", "properties": {}, "required": [],
        })
        async def noop() -> str:
            return "ok"

        call_count = 0

        async def mock_send(messages, tools):
            nonlocal call_count
            call_count += 1
            return {
                "content": "partial",
                "tool_calls": [{
                    "id": f"tc_{call_count}",
                    "function": {"name": "noop", "arguments": "{}"},
                }],
                "finish_reason": "tool_calls",
            }

        loop = AgentLoop(send_fn=mock_send, max_iterations=3)
        result = asyncio.get_event_loop().run_until_complete(
            loop.run([{"role": "user", "content": "loop forever"}])
        )
        self.assertEqual(call_count, 3)
        # Should return last partial content
        self.assertIn("partial", result)

    def test_malformed_tool_args(self):
        """LLM sends invalid JSON in tool arguments — should not crash."""
        from agent import AgentLoop, ToolRegistry

        @ToolRegistry.register("echo", "Echo", {
            "type": "object", "properties": {"x": {"type": "string"}}, "required": [],
        })
        async def echo(x: str = "default") -> str:
            return f"echo: {x}"

        call_count = 0

        async def mock_send(messages, tools):
            nonlocal call_count
            call_count += 1
            if call_count == 1:
                return {
                    "content": "",
                    "tool_calls": [{
                        "id": "tc_1",
                        "function": {"name": "echo", "arguments": "{invalid json!!!"},
                    }],
                    "finish_reason": "tool_calls",
                }
            return {"content": "Done", "tool_calls": [], "finish_reason": "stop"}

        loop = AgentLoop(send_fn=mock_send)
        result = asyncio.get_event_loop().run_until_complete(
            loop.run([{"role": "user", "content": "test"}])
        )
        self.assertEqual(result, "Done")


class TestConversationManager(unittest.TestCase):
    """Test memory store/recall roundtrip with in-memory SQLite."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.db_path = os.path.join(self.tmp, "test.db")

    def tearDown(self):
        import shutil
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_store_and_recall(self):
        from memory_store import ConversationManager
        cm = ConversationManager(db_path=self.db_path)

        cm.store_memory_sync("fav_color", "Blue is my favorite color", ["preference"])
        results = cm.recall_sync("favorite color")
        self.assertTrue(len(results) > 0)
        self.assertEqual(results[0]["key"], "fav_color")
        self.assertIn("Blue", results[0]["content"])
        cm.close()

    def test_store_upsert(self):
        from memory_store import ConversationManager
        cm = ConversationManager(db_path=self.db_path)

        cm.store_memory_sync("key1", "value1")
        cm.store_memory_sync("key1", "value2")  # Should update, not fail
        results = cm.recall_sync("key1")
        self.assertEqual(results[0]["content"], "value2")
        cm.close()

    def test_conversation_history(self):
        from memory_store import ConversationManager
        cm = ConversationManager(db_path=self.db_path)

        cm.store_message_sync("sess1", "user", "Hello")
        cm.store_message_sync("sess1", "assistant", "Hi there!")
        cm.store_message_sync("sess1", "user", "How are you?")

        history = cm.get_history_sync("sess1")
        self.assertEqual(len(history), 3)
        self.assertEqual(history[0]["role"], "user")
        self.assertEqual(history[0]["content"], "Hello")
        self.assertEqual(history[-1]["content"], "How are you?")
        cm.close()

    def test_memory_context_builder(self):
        from memory_store import ConversationManager
        cm = ConversationManager(db_path=self.db_path)

        cm.store_memory_sync("pet", "I have a dog named Rex")
        # Query matches the key "pet" via LIKE fallback
        ctx = cm.build_memory_context_sync("sess1", "pet")
        self.assertIn("Rex", ctx)
        self.assertIn("Relevant memories", ctx)
        cm.close()

    def test_empty_recall(self):
        from memory_store import ConversationManager
        cm = ConversationManager(db_path=self.db_path)

        results = cm.recall_sync("something that doesn't exist")
        self.assertEqual(results, [])
        cm.close()

    def test_list_memories_with_tag(self):
        from memory_store import ConversationManager
        cm = ConversationManager(db_path=self.db_path)

        cm.store_memory_sync("a", "apple", ["fruit"])
        cm.store_memory_sync("b", "beef", ["meat"])
        results = cm.list_memories_sync(tag="fruit")
        self.assertEqual(len(results), 1)
        self.assertEqual(results[0]["key"], "a")
        cm.close()


class TestSSRFBlocklist(unittest.TestCase):
    """Test the _is_private_url helper blocks internal addresses."""

    def test_localhost_blocked(self):
        from tools.browser import _is_private_url
        self.assertTrue(_is_private_url("http://localhost/admin"))

    def test_loopback_blocked(self):
        from tools.browser import _is_private_url
        self.assertTrue(_is_private_url("http://127.0.0.1/secret"))

    def test_private_10_blocked(self):
        from tools.browser import _is_private_url
        self.assertTrue(_is_private_url("http://10.0.0.1/internal"))

    def test_private_172_blocked(self):
        from tools.browser import _is_private_url
        self.assertTrue(_is_private_url("http://172.16.0.1/admin"))

    def test_private_192_blocked(self):
        from tools.browser import _is_private_url
        self.assertTrue(_is_private_url("http://192.168.1.1/router"))

    def test_link_local_blocked(self):
        from tools.browser import _is_private_url
        self.assertTrue(_is_private_url("http://169.254.169.254/metadata"))

    def test_ipv6_loopback_blocked(self):
        from tools.browser import _is_private_url
        self.assertTrue(_is_private_url("http://[::1]/admin"))

    def test_public_ip_allowed(self):
        from tools.browser import _is_private_url
        self.assertFalse(_is_private_url("http://93.184.216.34/page"))

    def test_domain_name_allowed(self):
        from tools.browser import _is_private_url
        self.assertFalse(_is_private_url("https://example.com/page"))


if __name__ == "__main__":
    unittest.main()
