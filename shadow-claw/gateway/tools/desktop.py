"""Desktop tools: screenshots and basic automation.

Inspired by trycua/cua. Uses available system utilities for
screenshots and xdotool for simple desktop automation.
"""

import asyncio
import os
import tempfile

from agent import tool

_ALLOWED_ACTIONS = frozenset({
    "type", "key", "click", "move", "sleep",
})


@tool(
    "desktop_screenshot",
    "Capture a screenshot of the current desktop. "
    "Returns the file path of the saved screenshot.",
    {
        "type": "object",
        "properties": {},
        "required": [],
    },
)
async def desktop_screenshot() -> str:
    screenshot_path = os.path.join(tempfile.gettempdir(), "shadow_claw_screenshot.png")

    for cmd in [
        ["scrot", screenshot_path],
        ["screencapture", screenshot_path],
        ["import", "-window", "root", screenshot_path],
    ]:
        try:
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            await asyncio.wait_for(proc.wait(), timeout=15)
            if proc.returncode == 0 and os.path.exists(screenshot_path):
                size = os.path.getsize(screenshot_path)
                return f"Screenshot saved: {screenshot_path} ({size} bytes)"
        except (asyncio.TimeoutError, FileNotFoundError, OSError):
            continue

    return "Screenshot failed: no supported tool found (scrot, screencapture, import)."


@tool(
    "desktop_command",
    "Execute a simple desktop automation command using xdotool. "
    "Allowed actions: type, key, click, move, sleep.",
    {
        "type": "object",
        "properties": {
            "action": {
                "type": "string",
                "enum": ["type", "key", "click", "move", "sleep"],
                "description": "The action to perform",
            },
            "value": {
                "type": "string",
                "description": "Value for the action (text to type, key name, coordinates, etc.)",
            },
        },
        "required": ["action", "value"],
    },
)
async def desktop_command(action: str, value: str) -> str:
    if action not in _ALLOWED_ACTIONS:
        return f"Action '{action}' not allowed. Allowed: {', '.join(sorted(_ALLOWED_ACTIONS))}"

    # Sanitize value — alphanumeric, spaces, basic punctuation only
    safe_value = "".join(c for c in value if c.isalnum() or c in " .-_+")[:200]

    cmd_map = {
        "type": ["xdotool", "type", "--clearmodifiers", safe_value],
        "key": ["xdotool", "key", safe_value],
        "click": ["xdotool", "click", safe_value],
        "move": ["xdotool", "mousemove", *safe_value.split()],
        "sleep": ["sleep", safe_value],
    }

    cmd = cmd_map.get(action)
    if not cmd:
        return f"Unknown action: {action}"

    try:
        proc = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
        )
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=30)
        if proc.returncode == 0:
            return f"Desktop action '{action}' executed successfully."
        return f"Desktop action failed (exit {proc.returncode}): {stderr.decode()[:200]}"
    except FileNotFoundError:
        return f"xdotool not installed. Install with: sudo apt install xdotool"
    except asyncio.TimeoutError:
        return f"Desktop action '{action}' timed out after 30s."
