"""Lightweight metrics collector for Shadow-Claw gateway.

Hooks into the existing log_event() function to track counters and latency
distributions without external dependencies.
"""

import statistics
import threading
import time
from collections import defaultdict, deque


_DEFAULT_WINDOW_SECONDS = 3600
_DEFAULT_MAX_EVENTS = 10000


class MetricsCollector:
    """Thread-safe singleton that aggregates gateway event data.

    Tracks chat request outcomes (success/fail/fallback), tool execution
    outcomes (success/fail/timeout), auth denials, and latency percentiles
    over a configurable rolling window.
    """

    _instance = None
    _lock = threading.Lock()

    def __new__(cls, *args, **kwargs):
        if cls._instance is None:
            with cls._lock:
                if cls._instance is None:
                    cls._instance = super().__new__(cls)
        return cls._instance

    def __init__(
        self,
        window_seconds: int = _DEFAULT_WINDOW_SECONDS,
        max_events: int = _DEFAULT_MAX_EVENTS,
    ):
        if getattr(self, "_initialized", False):
            return
        self._initialized = True

        self._window_seconds = window_seconds
        self._max_events = max_events
        self._mu = threading.Lock()

        # Each entry: (timestamp, {field: value, ...})
        self._chat_events = deque(maxlen=max_events)
        self._tool_events = deque(maxlen=max_events)
        self._auth_events = deque(maxlen=max_events)

    # ------------------------------------------------------------------
    # Public: reset (useful for tests)
    # ------------------------------------------------------------------

    @classmethod
    def reset(cls) -> None:
        """Destroy the singleton so a fresh instance is created next time."""
        with cls._lock:
            cls._instance = None

    # ------------------------------------------------------------------
    # Ingest
    # ------------------------------------------------------------------

    def record(self, event: str, fields: dict) -> None:
        """Dispatch a log event into the appropriate bucket."""
        now = time.monotonic()

        if event == "chat.request.success":
            self._append_chat(now, "ok", fields)
        elif event == "chat.request.error":
            self._append_chat(now, "failed", fields)
        elif event == "tool.job.completed":
            self._append_tool(now, fields)
        elif event == "tool.exec.timeout":
            self._append_tool_timeout(now, fields)
        elif event == "gateway.auth.denied":
            self._append_auth(now, fields)

    def _append_chat(self, ts: float, outcome: str, fields: dict) -> None:
        fallback_used = fields.get("fallback_used", False)
        if outcome == "ok" and fallback_used:
            outcome = "fallback"
        entry = {
            "outcome": outcome,
            "route": fields.get("route", "unknown"),
            "model": fields.get("model", "unknown"),
            "duration_ms": fields.get("duration_ms"),
        }
        with self._mu:
            self._chat_events.append((ts, entry))

    def _append_tool(self, ts: float, fields: dict) -> None:
        ok = fields.get("ok", False)
        entry = {
            "outcome": "ok" if ok else "failed",
            "tool_name": fields.get("tool_name", "unknown"),
            "duration_ms": fields.get("duration_ms"),
        }
        with self._mu:
            self._tool_events.append((ts, entry))

    def _append_tool_timeout(self, ts: float, fields: dict) -> None:
        entry = {
            "outcome": "timeout",
            "tool_name": fields.get("tool_name", "unknown"),
            "duration_ms": None,
        }
        with self._mu:
            self._tool_events.append((ts, entry))

    def _append_auth(self, ts: float, fields: dict) -> None:
        with self._mu:
            self._auth_events.append((ts, fields))

    # ------------------------------------------------------------------
    # Query
    # ------------------------------------------------------------------

    def snapshot(self) -> dict:
        """Return a point-in-time snapshot of all metrics within the window.

        Returns a dict with keys: chat, tools, auth, window_seconds.
        """
        cutoff = time.monotonic() - self._window_seconds

        with self._mu:
            chat_window = [(ts, e) for ts, e in self._chat_events if ts >= cutoff]
            tool_window = [(ts, e) for ts, e in self._tool_events if ts >= cutoff]
            auth_window = [(ts, e) for ts, e in self._auth_events if ts >= cutoff]

        return {
            "window_seconds": self._window_seconds,
            "chat": self._summarize_chat(chat_window),
            "tools": self._summarize_tools(tool_window),
            "auth": {"denials": len(auth_window)},
        }

    # ------------------------------------------------------------------
    # Summarizers
    # ------------------------------------------------------------------

    @staticmethod
    def _summarize_chat(events: list[tuple[float, dict]]) -> dict:
        total = len(events)
        ok = sum(1 for _, e in events if e["outcome"] == "ok")
        fallback = sum(1 for _, e in events if e["outcome"] == "fallback")
        failed = sum(1 for _, e in events if e["outcome"] == "failed")

        latencies_by_route: dict[str, list[float]] = defaultdict(list)
        for _, entry in events:
            ms = entry.get("duration_ms")
            if ms is not None:
                latencies_by_route[entry["route"]].append(float(ms))

        routes: dict[str, dict] = {}
        for route, values in sorted(latencies_by_route.items()):
            routes[route] = _percentiles(values)

        return {
            "total": total,
            "ok": ok,
            "fallback": fallback,
            "failed": failed,
            "routes": routes,
        }

    @staticmethod
    def _summarize_tools(events: list[tuple[float, dict]]) -> dict:
        total = len(events)
        ok = sum(1 for _, e in events if e["outcome"] == "ok")
        failed = sum(1 for _, e in events if e["outcome"] == "failed")
        timeout = sum(1 for _, e in events if e["outcome"] == "timeout")

        by_tool: dict[str, dict] = defaultdict(lambda: {"runs": 0, "latencies": []})
        for _, entry in events:
            name = entry["tool_name"]
            by_tool[name]["runs"] += 1
            ms = entry.get("duration_ms")
            if ms is not None:
                by_tool[name]["latencies"].append(float(ms))

        tools: dict[str, dict] = {}
        for name in sorted(by_tool):
            info = by_tool[name]
            tool_summary = {"runs": info["runs"]}
            if info["latencies"]:
                tool_summary.update(_percentiles(info["latencies"]))
            tools[name] = tool_summary

        return {
            "total": total,
            "ok": ok,
            "failed": failed,
            "timeout": timeout,
            "tools": tools,
        }


# ------------------------------------------------------------------
# Helpers
# ------------------------------------------------------------------


def _percentiles(values: list[float]) -> dict:
    """Compute p50/p95/p99 from a list of numeric values."""
    if not values:
        return {}
    sorted_vals = sorted(values)
    result = {"p50": int(_quantile(sorted_vals, 0.50))}
    if len(sorted_vals) >= 2:
        result["p95"] = int(_quantile(sorted_vals, 0.95))
        result["p99"] = int(_quantile(sorted_vals, 0.99))
    return result


def _quantile(sorted_vals: list[float], q: float) -> float:
    """Compute quantile using linear interpolation (matching statistics.quantiles)."""
    if len(sorted_vals) == 1:
        return sorted_vals[0]
    # Use exclusive method consistent with statistics.quantiles
    n = len(sorted_vals)
    index = q * (n + 1) - 1  # 1-based to 0-based
    if index <= 0:
        return sorted_vals[0]
    if index >= n - 1:
        return sorted_vals[-1]
    lo = int(index)
    frac = index - lo
    return sorted_vals[lo] + frac * (sorted_vals[lo + 1] - sorted_vals[lo])


# ------------------------------------------------------------------
# Human-readable summary
# ------------------------------------------------------------------


def get_metrics_summary() -> str:
    """Return a multi-line human-readable summary of current metrics."""
    collector = MetricsCollector()
    snap = collector.snapshot()
    window_label = _format_window(snap["window_seconds"])

    lines: list[str] = []
    lines.append(f"Shadow-Claw Metrics (last {window_label})")
    lines.append("")

    # Chat section
    chat = snap["chat"]
    parts = []
    if chat["ok"]:
        parts.append(f"{chat['ok']} ok")
    if chat["fallback"]:
        parts.append(f"{chat['fallback']} fallback")
    if chat["failed"]:
        parts.append(f"{chat['failed']} failed")
    detail = f" ({', '.join(parts)})" if parts else ""
    lines.append(f"Chat: {chat['total']} total{detail}")

    for route, pcts in chat["routes"].items():
        label = route.capitalize()
        pct_parts = [f"p50={pcts['p50']}ms"]
        if "p95" in pcts:
            pct_parts.append(f"p95={pcts['p95']}ms")
        if "p99" in pcts:
            pct_parts.append(f"p99={pcts['p99']}ms")
        lines.append(f"  {label}: {' '.join(pct_parts)}")

    lines.append("")

    # Tools section
    tools = snap["tools"]
    parts = []
    if tools["ok"]:
        parts.append(f"{tools['ok']} ok")
    if tools["failed"]:
        parts.append(f"{tools['failed']} failed")
    if tools["timeout"]:
        parts.append(f"{tools['timeout']} timeout")
    detail = f" ({', '.join(parts)})" if parts else ""
    lines.append(f"Tools: {tools['total']} total{detail}")

    for name, info in tools["tools"].items():
        pct_parts = []
        if "p50" in info:
            pct_parts.append(f"p50={info['p50']}ms")
        if "p95" in info:
            pct_parts.append(f"p95={info['p95']}ms")
        suffix = f", {' '.join(pct_parts)}" if pct_parts else ""
        lines.append(f"  {name}: {info['runs']} runs{suffix}")

    lines.append("")

    # Auth section
    lines.append(f"Auth: {snap['auth']['denials']} denials")

    return "\n".join(lines)


def _format_window(seconds: int) -> str:
    """Turn seconds into a short label like '1h' or '30m'."""
    if seconds >= 3600 and seconds % 3600 == 0:
        return f"{seconds // 3600}h"
    if seconds >= 60 and seconds % 60 == 0:
        return f"{seconds // 60}m"
    return f"{seconds}s"


# ------------------------------------------------------------------
# Installation hook
# ------------------------------------------------------------------


def install_metrics(original_log_event):
    """Wrap the original log_event function to also feed MetricsCollector.

    Returns a new callable with the same signature as log_event.
    Does NOT mutate global state outside the collector singleton.

    Usage::

        from metrics import install_metrics
        log_event = install_metrics(log_event)
    """
    collector = MetricsCollector()

    def wrapped_log_event(event: str, **fields) -> None:
        original_log_event(event, **fields)
        collector.record(event, fields)

    return wrapped_log_event
