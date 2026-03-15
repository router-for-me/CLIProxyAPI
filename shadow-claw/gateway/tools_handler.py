import asyncio
import time
from contextlib import suppress

from config import TOOL_PROBE_CACHE_TTL_SECONDS


def build_tool_probe(
    tool_name: str,
    inspection: dict,
    verified: bool = False,
    contract_capable: bool = False,
    checked_at: float | None = None,
) -> dict:
    return {
        "tool": tool_name,
        "configured": inspection.get("configured", False),
        "available": inspection.get("available", False),
        "resolved": inspection.get("resolved"),
        "message": inspection.get("message", "unknown"),
        "verified": verified,
        "contract_capable": contract_capable,
        "checked_at": checked_at if checked_at is not None else time.monotonic(),
    }


def format_tool_probe_status(probe: dict, tools_enabled: bool = True) -> str:
    if not tools_enabled:
        return "disabled by config"
    if not probe["configured"]:
        return "not configured"
    if not probe["available"]:
        return f"unavailable ({probe['message']})"

    layers = ["installed", "verified" if probe["verified"] else "unverified"]
    if probe["contract_capable"]:
        layers.append("contract-capable")
    else:
        layers.append("contract-unverified")
    return f"{', '.join(layers)} ({probe['resolved']})"


def tool_routes_enabled(config: dict) -> bool:
    return bool(config.get("tools_enabled"))


async def read_stream_limited(stream, capture_limit: int) -> dict:
    if stream is None:
        return {"data": b"", "bytes_seen": 0, "truncated": False}

    kept = bytearray()
    bytes_seen = 0
    truncated = False
    limit = max(capture_limit, 0)

    while True:
        chunk = await stream.read(4096)
        if not chunk:
            break
        bytes_seen += len(chunk)
        remaining = limit - len(kept)
        if remaining > 0:
            kept.extend(chunk[:remaining])
        if len(chunk) > max(remaining, 0):
            truncated = True

    if bytes_seen > len(kept):
        truncated = True

    return {"data": bytes(kept), "bytes_seen": bytes_seen, "truncated": truncated}


async def write_process_stdin(process, stdin_data: bytes | None) -> None:
    if stdin_data is None or process.stdin is None:
        return

    try:
        process.stdin.write(stdin_data)
        await process.stdin.drain()
    except (BrokenPipeError, ConnectionResetError):
        pass
    finally:
        process.stdin.close()
        with suppress(Exception):
            await process.stdin.wait_closed()


async def collect_process_output(process, stdin_data: bytes | None, capture_limit: int, timeout: int) -> dict:
    stdout_task = asyncio.create_task(read_stream_limited(process.stdout, capture_limit))
    stderr_task = asyncio.create_task(read_stream_limited(process.stderr, capture_limit))
    stdin_task = asyncio.create_task(write_process_stdin(process, stdin_data))

    try:
        await asyncio.wait_for(process.wait(), timeout=timeout)
    except asyncio.TimeoutError:
        process.kill()
        with suppress(Exception):
            await process.wait()
        await asyncio.gather(stdout_task, stderr_task, stdin_task, return_exceptions=True)
        return {
            "timed_out": True,
            "stdout": {"data": b"", "bytes_seen": 0, "truncated": False},
            "stderr": {"data": b"", "bytes_seen": 0, "truncated": False},
            "returncode": -9,
        }

    stdout_result, stderr_result, _ = await asyncio.gather(stdout_task, stderr_task, stdin_task)
    return {
        "timed_out": False,
        "stdout": stdout_result,
        "stderr": stderr_result,
        "returncode": process.returncode,
    }
