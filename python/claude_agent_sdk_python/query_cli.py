import os
import sys
import json
import asyncio
import argparse
import subprocess
import tempfile
from pathlib import Path
from typing import Any, AsyncIterator

# Ensure default model before importing SDK
if not os.getenv("ANTHROPIC_MODEL", "").strip():
    os.environ["ANTHROPIC_MODEL"] = "glm-4.6"

try:
    from claude_agent_sdk import query, ClaudeAgentOptions  # type: ignore
except Exception as e:  # pragma: no cover
    sys.stderr.write(f"Error: claude_agent_sdk is not installed or failed to import: {e}\n")
    sys.exit(1)


def _read_prompt_from_argv_or_stdin() -> str:
    if len(sys.argv) > 1:
        return " ".join(sys.argv[1:]).strip()
    data = sys.stdin.read()
    return (data or "").strip() or "Hello from query()"


def _build_options() -> ClaudeAgentOptions:
    # Ensure upstream model default at process env-level for runtime compatibility
    if not os.getenv("ANTHROPIC_MODEL", "").strip():
        os.environ["ANTHROPIC_MODEL"] = "glm-4.6"

    env_keys = (
        "ANTHROPIC_BASE_URL",
        "ANTHROPIC_AUTH_TOKEN",
        "API_TIMEOUT_MS",
        "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
        "ANTHROPIC_MODEL",
    )
    env_map: dict[str, str] = {k: v.strip() for k in env_keys if (v := os.getenv(k, "").strip())}

    system_prompt = os.getenv("CLAUDE_SYSTEM_PROMPT", "").strip() or None
    permission_mode = os.getenv("CLAUDE_PERMISSION_MODE", "").strip() or None
    cwd = os.getenv("CLAUDE_CWD", "").strip() or None

    return ClaudeAgentOptions(
        system_prompt=system_prompt,
        permission_mode=permission_mode,
        cwd=cwd,
        env=env_map or None,
        model=env_map.get("ANTHROPIC_MODEL") or "glm-4.6",
    )


async def _run(prompt: str, json_output: bool = False) -> int:
    opts = _build_options()
    async def _aiter() -> AsyncIterator[Any]:
        async for message in query(prompt=prompt, options=opts):
            yield message

    async for message in _aiter():
        if json_output:
            try:
                # Many SDK message objects can be JSON-serialized via __dict__ fallback
                sys.stdout.write(json.dumps(getattr(message, "to_dict", lambda: message.__dict__)()))
                sys.stdout.write("\n")
            except Exception:
                sys.stdout.write(str(message) + "\n")
        else:
            # Best-effort textual rendering
            text = None
            try:
                content = getattr(message, "content", None)
                if isinstance(content, list):
                    chunks = []
                    for b in content:
                        t = getattr(b, "text", None)
                        if isinstance(t, str) and t:
                            chunks.append(t)
                    if chunks:
                        text = "".join(chunks)
            except Exception:
                pass
            sys.stdout.write((text if text else str(message)) + "\n")
            sys.stdout.flush()
    return 0


def main() -> None:
    parser = argparse.ArgumentParser(description="Minimal query()-only CLI for Claude Agent SDK")
    parser.add_argument("prompt", nargs="*", help="Prompt text; if omitted, read from stdin")
    parser.add_argument("--model", dest="model", help="Override model (high precedence)")
    parser.add_argument("--no-subproc", dest="no_subproc", action="store_true", help="Do not force subprocess isolation")
    parser.add_argument("--json", dest="json_output", action="store_true", help="Output JSON lines")
    args = parser.parse_args()

    # Prompt collection
    prompt = (" ".join(args.prompt).strip() if args.prompt else _read_prompt_from_argv_or_stdin())

    # High-precedence model override
    if args.model and args.model.strip():
        os.environ["ANTHROPIC_MODEL"] = args.model.strip()

    # Optional JSON via flag or env var
    json_output = args.json_output or (os.getenv("QUERY_JSON", "").strip().lower() in {"1", "true", "yes"})

    # Subprocess isolation: avoid reusing existing Claude Code instance/session
    if not args.no_subproc:
        env = os.environ.copy()
        # Use absolute path for reliability
        script_path = Path(__file__).resolve()
        cmd = [sys.executable, str(script_path), "--no-subproc"]
        if json_output:
            cmd.append("--json")
        if args.model:
            cmd += ["--model", args.model]
        if args.prompt:
            cmd += [*args.prompt]
        try:
            rc = subprocess.call(cmd, env=env)
            raise SystemExit(rc)
        except KeyboardInterrupt:
            raise SystemExit(130)

    try:
        code = asyncio.run(_run(prompt, json_output=json_output))
        raise SystemExit(code)
    except KeyboardInterrupt:
        raise SystemExit(130)


if __name__ == "__main__":
    """Minimal query()-only CLI for Claude Agent SDK (Python).

    Usage examples:
      - Provide prompt by args (direct path):
          python python/claude_agent_sdk_python/query_cli.py "Summarize this repo in 3 bullets"

      - Or pipe from stdin (direct path):
          echo "Hello" | python python/claude_agent_sdk_python/query_cli.py

      - As a module (if PYTHONPATH includes ./python):
          PYTHONPATH=python python -m claude_agent_sdk_python.query_cli "Hello"

    Optional environment:
      CLAUDE_SYSTEM_PROMPT="You are an expert."
      CLAUDE_PERMISSION_MODE="acceptEdits"
      CLAUDE_CWD="/path/to/project"

      # Upstream gateway (if your runtime expects these):
      ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_MODEL,
      API_TIMEOUT_MS, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC

    Output mode:
      Set QUERY_JSON=1 for JSON lines output.
    """
    main()
