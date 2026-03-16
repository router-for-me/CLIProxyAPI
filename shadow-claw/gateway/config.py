import os
import re
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

from dotenv import load_dotenv

ENV_PATH = Path(__file__).with_name(".env")

SYSTEM_PROMPT = (
    "Você é o Shadow-Claw, um agente pessoal rodando num terminal Linux via CLIProxyAPI. "
    "Responda de forma direta e concisa, como um hacker experiente."
)
CHAT_ROUTE_DEFAULT = "default"
CHAT_ROUTE_CODING = "coding"
PROMPT_PLACEHOLDER = "{prompt}"
FALLBACK_STATUS_MESSAGE = "Primary route failed, trying fallback..."
MAX_TELEGRAM_MESSAGE_LENGTH = 3800
DEFAULT_CHAT_TIMEOUT_SECONDS = 60
DEFAULT_HEALTH_TIMEOUT_SECONDS = 10
DEFAULT_TOOL_TIMEOUT_SECONDS = 300
CODING_KEYWORDS = (
    "write code",
    "fix bug",
    "refactor",
    "debug",
    "python",
    "golang",
    "bash script",
    "implement",
)
RETRYABLE_STATUS_CODES = {429}
NON_RETRYABLE_STATUS_CODES = {400, 401, 403, 404}
MAX_TOOL_CAPTURE_BYTES = 32 * 1024
TOOL_PROBE_CACHE_TTL_SECONDS = 15

# Agent mode settings
DEFAULT_MAX_TOOL_ITERATIONS = 5
DEFAULT_AGENT_LOOP_TIMEOUT_SECONDS = 120


def parse_int_env(name: str, default: int) -> int:
    raw_value = os.getenv(name, "").strip()
    if not raw_value:
        return default
    try:
        return int(raw_value)
    except ValueError:
        return default


def parse_bool_env(name: str, default: bool = False) -> bool:
    raw_value = os.getenv(name, "").strip().lower()
    if not raw_value:
        return default
    return raw_value in {"1", "true", "yes", "on"}


def load_config() -> dict:
    return {
        "telegram_token": os.getenv("TELEGRAM_TOKEN", "").strip(),
        "allowed_user_id": parse_int_env("ALLOWED_USER_ID", 0),
        "api_url": os.getenv(
            "CLIPROXY_API_URL",
            "http://localhost:8317/v1/chat/completions",
        ).strip(),
        "api_key": os.getenv("CLIPROXY_API_KEY", "shadow-claw-internal").strip() or "shadow-claw-internal",
        "default_profile": {
            "route": CHAT_ROUTE_DEFAULT,
            "model": os.getenv("DEFAULT_MODEL", "gpt-5.4").strip() or "gpt-5.4",
            "reasoning_effort": os.getenv("DEFAULT_REASONING_EFFORT", "medium").strip() or "medium",
        },
        "coding_profile": {
            "route": CHAT_ROUTE_CODING,
            "model": os.getenv("CODING_MODEL", "gpt-5.4").strip() or "gpt-5.4",
            "reasoning_effort": os.getenv("CODING_REASONING_EFFORT", "high").strip() or "high",
        },
        "fallback_model": os.getenv("FALLBACK_MODEL", "kimi-k2.5").strip() or "kimi-k2.5",
        "chat_timeout_seconds": parse_int_env("CHAT_TIMEOUT_SECONDS", DEFAULT_CHAT_TIMEOUT_SECONDS),
        "health_timeout_seconds": parse_int_env("HEALTH_TIMEOUT_SECONDS", DEFAULT_HEALTH_TIMEOUT_SECONDS),
        "tool_output_limit": parse_int_env("TOOL_OUTPUT_LIMIT", MAX_TELEGRAM_MESSAGE_LENGTH),
        "tools_enabled": parse_bool_env("ENABLE_TOOL_ROUTES", default=False),
        "tools": {
            "autoresearch": {
                "command": os.getenv("AUTORESEARCH_COMMAND", "").strip(),
                "timeout": parse_int_env("AUTORESEARCH_TIMEOUT_SECONDS", DEFAULT_TOOL_TIMEOUT_SECONDS),
            },
            "ruflo": {
                "command": os.getenv("RUFLO_COMMAND", "").strip(),
                "timeout": parse_int_env("RUFLO_TIMEOUT_SECONDS", DEFAULT_TOOL_TIMEOUT_SECONDS),
            },
            "browser-use": {
                "command": os.getenv("BROWSER_USE_COMMAND", "").strip(),
                "timeout": parse_int_env("BROWSER_USE_TIMEOUT_SECONDS", DEFAULT_TOOL_TIMEOUT_SECONDS),
            },
        },
        # Agent mode
        "agent_mode_enabled": parse_bool_env("AGENT_MODE_ENABLED", default=True),
        "max_tool_iterations": parse_int_env("MAX_TOOL_ITERATIONS", DEFAULT_MAX_TOOL_ITERATIONS),
        "agent_loop_timeout_seconds": parse_int_env("AGENT_LOOP_TIMEOUT_SECONDS", DEFAULT_AGENT_LOOP_TIMEOUT_SECONDS),
        "memory_db_path": os.getenv("MEMORY_DB_PATH", "").strip() or None,
        # Personal Agent core milestone
        "personal_agent_enabled": parse_bool_env("PERSONAL_AGENT_ENABLED", default=True),
        "gmail_access_token": os.getenv("SHADOW_CLAW_GMAIL_ACCESS_TOKEN", "").strip(),
        "gmail_refresh_token": os.getenv("SHADOW_CLAW_GMAIL_REFRESH_TOKEN", "").strip(),
        "calendar_access_token": os.getenv("SHADOW_CLAW_CALENDAR_ACCESS_TOKEN", "").strip(),
        "calendar_refresh_token": os.getenv("SHADOW_CLAW_CALENDAR_REFRESH_TOKEN", "").strip(),
    }


def proxy_headers(config: dict) -> dict:
    headers = {"Content-Type": "application/json"}
    if config["api_key"]:
        headers["Authorization"] = f"Bearer {config['api_key']}"
    return headers


def proxy_root_url(api_url: str) -> str:
    parsed = urlsplit(api_url)
    path = parsed.path.rstrip("/")
    suffix = "/v1/chat/completions"
    if path.endswith(suffix):
        path = path[: -len(suffix)]
    rebuilt = urlunsplit((parsed.scheme, parsed.netloc, path, "", ""))
    return rebuilt.rstrip("/")


def build_proxy_endpoint(config: dict, suffix: str) -> str:
    return f"{proxy_root_url(config['api_url'])}{suffix}"


def extract_prompt_from_command(message_text: str, command_name: str) -> str:
    pattern = re.compile(rf"^/{re.escape(command_name)}(?:@[A-Za-z0-9_]+)?(?:\s+(.*))?$", re.IGNORECASE)
    match = pattern.match((message_text or "").strip())
    if not match:
        return ""
    return (match.group(1) or "").strip()


def truncate_text(text: str, limit: int = MAX_TELEGRAM_MESSAGE_LENGTH) -> str:
    if text is None:
        return ""
    normalized = str(text).strip()
    if len(normalized) <= limit:
        return normalized
    if limit <= 32:
        return normalized[:limit]
    return f"{normalized[: limit - 28].rstrip()}\n\n[{limit - 28} de {len(normalized)} chars]"
