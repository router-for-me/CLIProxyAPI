import asyncio
import importlib.util
import sys
import types
import unittest
from pathlib import Path
from unittest.mock import AsyncMock, Mock, patch


class _DummyFilter:
    def __and__(self, other):
        return self

    def __rand__(self, other):
        return self

    def __invert__(self):
        return self


if "telegram" not in sys.modules:
    telegram_module = types.ModuleType("telegram")

    class Update:
        ALL_TYPES = object()

    telegram_module.Update = Update
    sys.modules["telegram"] = telegram_module

if "telegram.ext" not in sys.modules:
    telegram_ext_module = types.ModuleType("telegram.ext")

    class Application:
        @staticmethod
        def builder():
            raise RuntimeError("Application builder stub should be mocked in tests")

    class CommandHandler:
        def __init__(self, *args, **kwargs):
            self.args = args
            self.kwargs = kwargs

    class MessageHandler:
        def __init__(self, *args, **kwargs):
            self.args = args
            self.kwargs = kwargs

    class ContextTypes:
        DEFAULT_TYPE = object

    class _Filters:
        TEXT = _DummyFilter()
        COMMAND = _DummyFilter()

        @staticmethod
        def Regex(_pattern):
            return _DummyFilter()

    telegram_ext_module.Application = Application
    telegram_ext_module.CommandHandler = CommandHandler
    telegram_ext_module.ContextTypes = ContextTypes
    telegram_ext_module.MessageHandler = MessageHandler
    telegram_ext_module.filters = _Filters()
    sys.modules["telegram.ext"] = telegram_ext_module

BOT_PATH = Path(__file__).resolve().parents[1] / "bot.py"
SPEC = importlib.util.spec_from_file_location("shadow_claw_gateway_bot", BOT_PATH)
bot = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(bot)


class FakeResponse:
    def __init__(self, status_code=200, payload=None, text=""):
        self.status_code = status_code
        self._payload = payload or {}
        self.text = text

    def json(self):
        return self._payload


class FakeStream:
    def __init__(self, data=b""):
        self._data = data
        self._offset = 0

    async def read(self, size=-1):
        if self._offset >= len(self._data):
            return b""
        if size is None or size < 0:
            size = len(self._data) - self._offset
        chunk = self._data[self._offset : self._offset + size]
        self._offset += len(chunk)
        return chunk


class FakeStdin:
    def __init__(self):
        self.writes = []
        self.closed = False

    def write(self, data):
        self.writes.append(data)

    async def drain(self):
        return None

    def close(self):
        self.closed = True

    async def wait_closed(self):
        return None


class FakeProcess:
    def __init__(self, returncode=0, stdout=b"", stderr=b""):
        self.returncode = returncode
        self.stdout = FakeStream(stdout)
        self.stderr = FakeStream(stderr)
        self.stdin = FakeStdin()
        self.killed = False
        self.wait_calls = 0

    async def wait(self):
        self.wait_calls += 1
        return self.returncode

    def kill(self):
        self.killed = True


class TestRouteClassification(unittest.TestCase):
    def test_default_route_uses_default_profile(self):
        route = bot.classify_text_route("How are you?")
        self.assertEqual(route, bot.CHAT_ROUTE_DEFAULT)

    def test_coding_route_detects_keywords(self):
        route = bot.classify_text_route("Please debug this python function")
        self.assertEqual(route, bot.CHAT_ROUTE_CODING)

    def test_coding_route_detects_code_fences(self):
        route = bot.classify_text_route("```python\nprint('hi')\n```")
        self.assertEqual(route, bot.CHAT_ROUTE_CODING)

    def test_code_command_parses_prompt(self):
        prompt = bot.extract_prompt_from_command("/code write a bash script", "code")
        self.assertEqual(prompt, "write a bash script")


class TestPayloadBuilding(unittest.TestCase):
    def test_default_payload_uses_medium_reasoning(self):
        payload = bot.build_chat_payload(
            "hello",
            {"model": "gpt-5.4", "reasoning_effort": "medium"},
        )
        self.assertEqual(payload["model"], "gpt-5.4")
        self.assertEqual(payload["reasoning_effort"], "medium")

    def test_coding_payload_uses_high_reasoning(self):
        payload = bot.build_chat_payload(
            "fix bug",
            {"model": "gpt-5.4", "reasoning_effort": "high"},
        )
        self.assertEqual(payload["model"], "gpt-5.4")
        self.assertEqual(payload["reasoning_effort"], "high")


class TestFallbackBehavior(unittest.IsolatedAsyncioTestCase):
    async def test_fallback_on_timeout(self):
        status_message = AsyncMock()
        config = {
            "default_profile": {"model": "gpt-5.4", "reasoning_effort": "medium"},
            "coding_profile": {"model": "gpt-5.4", "reasoning_effort": "high"},
            "fallback_model": "kimi-k2.5",
        }

        with patch.object(
            bot,
            "attempt_chat_request",
            side_effect=[
                {"ok": False, "retryable": True, "error": "Primary route failed: timeout"},
                {"ok": True, "content": "fallback response", "status_code": 200},
            ],
        ):
            reply = await bot.send_with_fallback("hello", bot.CHAT_ROUTE_DEFAULT, status_message, config)

        status_message.edit_text.assert_awaited_once_with(bot.FALLBACK_STATUS_MESSAGE)
        self.assertIn("Fallback model kimi-k2.5 answered successfully.", reply)
        self.assertIn("fallback response", reply)

    async def test_fallback_on_retryable_status_codes(self):
        self.assertTrue(bot.should_fallback(status_code=429))
        self.assertTrue(bot.should_fallback(status_code=500))
        self.assertTrue(bot.should_fallback(status_code=503))

    async def test_no_fallback_on_client_errors(self):
        for status_code in (400, 401, 403, 404):
            self.assertFalse(bot.should_fallback(status_code=status_code))

    async def test_send_with_fallback_raises_for_non_retryable_error(self):
        status_message = AsyncMock()
        config = {
            "default_profile": {"model": "gpt-5.4", "reasoning_effort": "medium"},
            "coding_profile": {"model": "gpt-5.4", "reasoning_effort": "high"},
            "fallback_model": "kimi-k2.5",
        }

        with patch.object(
            bot,
            "attempt_chat_request",
            return_value={"ok": False, "retryable": False, "error": "Primary route failed with HTTP 400"},
        ):
            with self.assertRaisesRegex(RuntimeError, "HTTP 400"):
                await bot.send_with_fallback("hello", bot.CHAT_ROUTE_DEFAULT, status_message, config)

        status_message.edit_text.assert_not_awaited()


class TestToolProbeCaching(unittest.TestCase):
    def setUp(self):
        bot.TOOL_PROBE_CACHE.clear()

    def test_get_tool_probe_caches_recent_probe(self):
        tool_config = {"command": "tool"}
        with patch.object(
            bot,
            "inspect_tool_command",
            return_value={
                "configured": True,
                "available": True,
                "resolved": "/usr/bin/tool",
                "message": "/usr/bin/tool",
            },
        ) as inspect_command:
            first = bot.get_tool_probe("ruflo", tool_config, now=100.0)
            second = bot.get_tool_probe("ruflo", tool_config, now=110.0)

        self.assertIs(first, second)
        inspect_command.assert_called_once_with("ruflo", "tool")

    def test_get_tool_probe_refreshes_after_ttl(self):
        tool_config = {"command": "tool"}
        with patch.object(
            bot,
            "inspect_tool_command",
            side_effect=[
                {
                    "configured": True,
                    "available": True,
                    "resolved": "/usr/bin/tool",
                    "message": "/usr/bin/tool",
                },
                {
                    "configured": True,
                    "available": False,
                    "message": "executable not found: tool",
                },
            ],
        ) as inspect_command:
            first = bot.get_tool_probe("ruflo", tool_config, now=100.0)
            second = bot.get_tool_probe("ruflo", tool_config, now=100.0 + bot.TOOL_PROBE_CACHE_TTL_SECONDS + 1)

        self.assertTrue(first["available"])
        self.assertFalse(second["available"])
        self.assertEqual(inspect_command.call_count, 2)


class TestHealthBehavior(unittest.IsolatedAsyncioTestCase):
    async def test_health_formatting_with_models_and_tools(self):
        config = {
            "telegram_token": "token",
            "allowed_user_id": 1,
            "api_url": "http://localhost:8317/v1/chat/completions",
            "default_profile": {"model": "gpt-5.4", "reasoning_effort": "medium"},
            "coding_profile": {"model": "gpt-5.4", "reasoning_effort": "high"},
            "fallback_model": "kimi-k2.5",
            "tools_enabled": True,
            "tools": {
                "autoresearch": {"command": "autoresearch"},
                "ruflo": {"command": ""},
                "browser-use": {"command": "browser-use"},
            },
        }

        with patch.object(bot, "probe_keep_alive", AsyncMock(return_value={"ok": True, "message": "ok"})), patch.object(
            bot,
            "fetch_models",
            AsyncMock(return_value={"ok": True, "message": "2 models visible", "model_ids": ["gpt-5.4", "kimi-k2.5"]}),
        ), patch.object(
            bot,
            "get_tool_probe",
            side_effect=[
                {
                    "configured": True,
                    "available": True,
                    "verified": True,
                    "contract_capable": False,
                    "resolved": "/usr/bin/autoresearch",
                    "message": "/usr/bin/autoresearch",
                },
                {
                    "configured": False,
                    "available": False,
                    "verified": False,
                    "contract_capable": False,
                    "message": "not configured",
                    "resolved": None,
                },
                {
                    "configured": True,
                    "available": False,
                    "verified": False,
                    "contract_capable": False,
                    "message": "executable not found: browser-use",
                    "resolved": None,
                },
            ],
        ):
            report = await bot.build_health_report(config)

        self.assertIn("Gateway status: running", report)
        self.assertIn("Tool routes: enabled", report)
        self.assertIn("CLIProxy models (primary): ok (2 models visible)", report)
        self.assertIn("CLIProxy keep-alive (optional): ok (ok)", report)
        self.assertIn("Required models visible: gpt-5.4, kimi-k2.5", report)
        self.assertIn("- autoresearch: installed, verified, contract-unverified (/usr/bin/autoresearch)", report)
        self.assertIn("- ruflo: not configured", report)
        self.assertIn("- browser-use: unavailable (executable not found: browser-use)", report)

    async def test_health_reports_missing_models(self):
        config = {
            "telegram_token": "token",
            "allowed_user_id": 1,
            "api_url": "http://localhost:8317/v1/chat/completions",
            "default_profile": {"model": "gpt-5.4", "reasoning_effort": "medium"},
            "coding_profile": {"model": "gpt-5.4", "reasoning_effort": "high"},
            "fallback_model": "kimi-k2.5",
            "tools_enabled": False,
            "tools": {
                "autoresearch": {"command": ""},
                "ruflo": {"command": ""},
                "browser-use": {"command": ""},
            },
        }

        with patch.object(bot, "probe_keep_alive", AsyncMock(return_value={"ok": True, "message": "ok"})), patch.object(
            bot,
            "fetch_models",
            AsyncMock(return_value={"ok": True, "message": "1 models visible", "model_ids": ["gpt-5.4"]}),
        ), patch.object(
            bot,
            "get_tool_probe",
            return_value={
                "configured": False,
                "available": False,
                "verified": False,
                "contract_capable": False,
                "message": "not configured",
                "resolved": None,
            },
        ):
            report = await bot.build_health_report(config)

        self.assertIn("Tool routes: disabled by config", report)
        self.assertIn("- autoresearch: disabled by config", report)
        self.assertIn("Required models visible: missing kimi-k2.5", report)


class TestToolExecution(unittest.IsolatedAsyncioTestCase):
    async def test_tool_adapter_success_with_prompt_placeholder(self):
        process = FakeProcess(stdout=b"done", stderr=b"")
        with patch.object(
            bot,
            "inspect_tool_command",
            return_value={
                "configured": True,
                "available": True,
                "argv": ["/usr/bin/tool", "--prompt", bot.PROMPT_PLACEHOLDER],
                "uses_prompt_placeholder": True,
            },
        ), patch.object(bot.asyncio, "create_subprocess_exec", AsyncMock(return_value=process)) as create_process:
            result = await bot.run_tool_command(
                "autoresearch",
                "compare pgvector and qdrant",
                {"command": "tool --prompt {prompt}", "timeout": 30},
                3800,
            )

        self.assertTrue(result["ok"])
        self.assertEqual(result["output"], "done")
        create_process.assert_awaited_once()
        args = create_process.await_args.args
        self.assertEqual(args[:3], ("/usr/bin/tool", "--prompt", "compare pgvector and qdrant"))
        self.assertEqual(process.stdin.writes, [])

    async def test_tool_adapter_success_with_stdin(self):
        process = FakeProcess(stdout=b"done", stderr=b"")
        with patch.object(
            bot,
            "inspect_tool_command",
            return_value={
                "configured": True,
                "available": True,
                "argv": ["/usr/bin/tool"],
                "uses_prompt_placeholder": False,
            },
        ), patch.object(bot.asyncio, "create_subprocess_exec", AsyncMock(return_value=process)):
            result = await bot.run_tool_command(
                "ruflo",
                "summarize this repo",
                {"command": "tool", "timeout": 30},
                3800,
            )

        self.assertTrue(result["ok"])
        self.assertEqual(process.stdin.writes, [b"summarize this repo"])
        self.assertTrue(process.stdin.closed)

    async def test_tool_adapter_reports_nonzero_exit(self):
        process = FakeProcess(returncode=2, stdout=b"", stderr=b"bad things")
        with patch.object(
            bot,
            "inspect_tool_command",
            return_value={
                "configured": True,
                "available": True,
                "argv": ["/usr/bin/tool"],
                "uses_prompt_placeholder": False,
            },
        ), patch.object(bot.asyncio, "create_subprocess_exec", AsyncMock(return_value=process)):
            result = await bot.run_tool_command(
                "browser-use",
                "open a site",
                {"command": "tool", "timeout": 30},
                3800,
            )

        self.assertFalse(result["ok"])
        self.assertIn("browser-use exited with status 2", result["output"])
        self.assertIn("bad things", result["output"])

    async def test_tool_adapter_handles_missing_configuration(self):
        result = await bot.run_tool_command("autoresearch", "hello", {"command": "", "timeout": 30}, 3800)
        self.assertFalse(result["ok"])
        self.assertIn("AUTORESEARCH_COMMAND", result["output"])

    async def test_tool_adapter_handles_timeout(self):
        class SlowProcess(FakeProcess):
            async def wait(self):
                raise asyncio.TimeoutError

        process = SlowProcess()
        with patch.object(
            bot,
            "inspect_tool_command",
            return_value={
                "configured": True,
                "available": True,
                "argv": ["/usr/bin/tool"],
                "uses_prompt_placeholder": False,
            },
        ), patch.object(bot.asyncio, "create_subprocess_exec", AsyncMock(return_value=process)):
            result = await bot.run_tool_command(
                "autoresearch",
                "hello",
                {"command": "tool", "timeout": 1},
                3800,
            )

        self.assertFalse(result["ok"])
        self.assertEqual(result["output"], "autoresearch timed out after 1s.")
        self.assertTrue(process.killed)

    async def test_tool_adapter_marks_truncated_output(self):
        process = FakeProcess(stdout=b"x" * 32, stderr=b"")
        with patch.object(
            bot,
            "inspect_tool_command",
            return_value={
                "configured": True,
                "available": True,
                "argv": ["/usr/bin/tool"],
                "uses_prompt_placeholder": False,
                "resolved": "/usr/bin/tool",
            },
        ), patch.object(bot.asyncio, "create_subprocess_exec", AsyncMock(return_value=process)), patch.object(
            bot, "MAX_TOOL_CAPTURE_BYTES", 8
        ):
            result = await bot.run_tool_command(
                "autoresearch",
                "hello",
                {"command": "tool", "timeout": 30},
                3800,
            )

        self.assertTrue(result["ok"])
        self.assertIn("[output truncated after 8 bytes]", result["output"])

    async def test_tool_adapter_logs_truncation_event(self):
        process = FakeProcess(stdout=b"x" * 32, stderr=b"")
        with patch.object(
            bot,
            "inspect_tool_command",
            return_value={
                "configured": True,
                "available": True,
                "argv": ["/usr/bin/tool"],
                "uses_prompt_placeholder": False,
                "resolved": "/usr/bin/tool",
            },
        ), patch.object(bot.asyncio, "create_subprocess_exec", AsyncMock(return_value=process)), patch.object(
            bot, "MAX_TOOL_CAPTURE_BYTES", 8
        ), patch.object(bot, "log_event") as log_event:
            await bot.run_tool_command(
                "autoresearch",
                "hello",
                {"command": "tool", "timeout": 30},
                3800,
            )

        event_names = [call.args[0] for call in log_event.call_args_list]
        self.assertIn("tool.exec.start", event_names)
        self.assertIn("tool.exec.truncated", event_names)
        self.assertIn("tool.exec.completed", event_names)


class TestCommandHandlers(unittest.IsolatedAsyncioTestCase):
    async def test_code_command_requires_prompt(self):
        update = Mock()
        update.effective_user = Mock(id=1)
        update.message = Mock(text="/code")
        update.message.reply_text = AsyncMock()
        context = Mock(args=[])
        config = {
            "allowed_user_id": 1,
            "telegram_token": "token",
        }

        old_config = bot._config
        try:
            bot._config = config
            await bot.code_command(update, context)
        finally:
            bot._config = old_config

        update.message.reply_text.assert_awaited_once_with("Usage: /code <prompt>")

    async def test_tool_command_refuses_when_routes_disabled(self):
        update = Mock()
        update.update_id = 1
        update.effective_user = Mock(id=1)
        update.effective_chat = Mock(id=99)
        update.message = Mock(text="/ruflo hello")
        update.message.reply_text = AsyncMock()
        config = {
            "allowed_user_id": 1,
            "telegram_token": "token",
            "tools_enabled": False,
        }

        runner = AsyncMock()
        with patch.object(bot, "log_event") as log_event:
            await bot.handle_tool_prompt(update, "hello", "ruflo", runner, config)

        update.message.reply_text.assert_awaited_once_with(
            "Tool routes are disabled by config. Chat and /health remain available."
        )
        runner.assert_not_awaited()
        log_event.assert_called_once()
        self.assertEqual(log_event.call_args.args[0], "tool.route.disabled")


if __name__ == "__main__":
    unittest.main()
