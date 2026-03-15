"""Agent core: ToolRegistry with @tool decorator, AgentLoop state machine.

Transforms Shadow-Claw from a stateless chat proxy into an LLM-native
tool-calling agent. The LLM decides which tools to invoke autonomously;
the agent loop iterates until a final text response or circuit breaker.
"""

import asyncio
import json
import logging
import time
from dataclasses import dataclass, field
from typing import Any, Callable, Awaitable

LOGGER = logging.getLogger("shadow_claw_gateway.agent")


# ---------------------------------------------------------------------------
# Custom exceptions
# ---------------------------------------------------------------------------

class ToolNotFoundError(Exception):
    pass


class ToolExecutionError(Exception):
    pass


class MaxIterationsError(Exception):
    pass


# ---------------------------------------------------------------------------
# Tool definition + registry
# ---------------------------------------------------------------------------

@dataclass
class ToolDef:
    name: str
    description: str
    parameters: dict
    fn: Callable[..., Awaitable[str]]


class ToolRegistry:
    """Global registry of tool definitions and their async callables."""

    _tools: dict[str, ToolDef] = {}

    @classmethod
    def register(cls, name: str, description: str, parameters: dict):
        """Decorator that registers an async function as an agent tool.

        Usage::

            @ToolRegistry.register(
                "memory_store",
                "Store a fact or note for later recall",
                {
                    "type": "object",
                    "properties": {"key": {"type": "string"}, ...},
                    "required": ["key", "content"],
                },
            )
            async def memory_store(key: str, content: str, **kw) -> str:
                ...
        """
        def decorator(fn: Callable[..., Awaitable[str]]):
            cls._tools[name] = ToolDef(name, description, parameters, fn)
            return fn
        return decorator

    @classmethod
    def get_definitions(cls) -> list[dict]:
        """Return tool definitions in OpenAI function-calling format."""
        return [
            {
                "type": "function",
                "function": {
                    "name": td.name,
                    "description": td.description,
                    "parameters": td.parameters,
                },
            }
            for td in cls._tools.values()
        ]

    @classmethod
    async def invoke(cls, name: str, arguments: dict, log_event=None) -> str:
        """Execute a registered tool by name, returning its string result.

        All exceptions are caught and returned as error strings so the LLM
        can self-correct. Never raises to the caller.
        """
        td = cls._tools.get(name)
        if td is None:
            msg = f"Tool '{name}' not found. Available: {', '.join(cls._tools.keys())}"
            if log_event:
                log_event("agent.tool.error", tool=name, error="not_found")
            return msg

        started = time.monotonic()
        try:
            result = await asyncio.wait_for(td.fn(**arguments), timeout=60)
            duration_ms = int((time.monotonic() - started) * 1000)
            if log_event:
                log_event("agent.tool.result", tool=name, ok=True, duration_ms=duration_ms)
            return str(result)
        except asyncio.TimeoutError:
            duration_ms = int((time.monotonic() - started) * 1000)
            if log_event:
                log_event("agent.tool.result", tool=name, ok=False, duration_ms=duration_ms, error="timeout")
            return f"Tool '{name}' timed out after 60 seconds."
        except Exception as exc:
            duration_ms = int((time.monotonic() - started) * 1000)
            if log_event:
                log_event("agent.tool.result", tool=name, ok=False, duration_ms=duration_ms, error=str(exc))
            return f"Tool '{name}' failed: {exc}"

    @classmethod
    def list_tools(cls) -> list[str]:
        return list(cls._tools.keys())

    @classmethod
    def reset(cls) -> None:
        """Clear all registered tools (for testing)."""
        cls._tools.clear()


# Convenience alias
tool = ToolRegistry.register


# ---------------------------------------------------------------------------
# Agent loop
# ---------------------------------------------------------------------------

class AgentLoop:
    """Iterative agent that sends messages + tools to the LLM, executes
    tool_calls, feeds results back, and repeats until a text response.

    Parameters
    ----------
    send_fn : callable
        ``async def send(messages, tools) -> dict`` — calls the LLM and
        returns the raw assistant message dict with keys:
        ``content``, ``tool_calls``, ``finish_reason``.
    max_iterations : int
        Circuit breaker.  Default 5.
    total_timeout : float
        Hard wall-clock limit for the entire loop.  Default 120s.
    log_event : callable or None
        The gateway's ``log_event`` function for observability.
    """

    def __init__(
        self,
        send_fn: Callable,
        max_iterations: int = 5,
        total_timeout: float = 120.0,
        log_event: Callable | None = None,
    ):
        self._send = send_fn
        self._max_iterations = max_iterations
        self._total_timeout = total_timeout
        self._log = log_event or (lambda *a, **kw: None)

    async def run(self, messages: list[dict]) -> str:
        """Execute the agent loop.  Returns the final text reply."""
        tools = ToolRegistry.get_definitions()
        deadline = time.monotonic() + self._total_timeout
        last_content = ""

        for iteration in range(1, self._max_iterations + 1):
            if time.monotonic() > deadline:
                self._log("agent.loop.timeout", iteration=iteration)
                return last_content or "Reached time limit. Please try again."

            self._log("agent.loop.iteration", iteration=iteration, tool_calls_count=0)

            assistant_msg = await self._send(messages, tools if tools else None)

            content = assistant_msg.get("content") or ""
            tool_calls = assistant_msg.get("tool_calls") or []
            finish_reason = assistant_msg.get("finish_reason", "")

            if content:
                last_content = content

            # No tool calls → final answer
            if not tool_calls:
                self._log(
                    "agent.loop.complete",
                    iterations=iteration,
                    finish_reason=finish_reason,
                )
                return content or last_content or "I couldn't generate a response."

            # Append assistant message to conversation
            assistant_record = {"role": "assistant", "content": content}
            if tool_calls:
                assistant_record["tool_calls"] = tool_calls
            messages.append(assistant_record)

            # Execute each tool call
            self._log(
                "agent.loop.iteration",
                iteration=iteration,
                tool_calls_count=len(tool_calls),
            )

            tools_used = []
            for tc in tool_calls:
                tc_id = tc.get("id", "")
                fn_info = tc.get("function", {})
                fn_name = fn_info.get("name", "")
                fn_args_raw = fn_info.get("arguments", "{}")

                self._log("agent.tool.call", tool=fn_name, iteration=iteration)

                try:
                    fn_args = json.loads(fn_args_raw) if isinstance(fn_args_raw, str) else fn_args_raw
                except json.JSONDecodeError:
                    fn_args = {}

                result = await ToolRegistry.invoke(fn_name, fn_args, log_event=self._log)
                tools_used.append(fn_name)

                # Append tool result to messages
                messages.append({
                    "role": "tool",
                    "tool_call_id": tc_id,
                    "content": result,
                })

            self._log(
                "agent.loop.tools_executed",
                iteration=iteration,
                tools_used=tools_used,
            )

        # Circuit breaker
        self._log("agent.loop.max_iterations", max=self._max_iterations)
        return last_content or "Reached maximum processing steps. Here's what I found so far."
