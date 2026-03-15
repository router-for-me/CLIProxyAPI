import asyncio
import json
import logging

import requests

from config import (
    SYSTEM_PROMPT,
    CHAT_ROUTE_DEFAULT,
    CHAT_ROUTE_CODING,
    CODING_KEYWORDS,
    RETRYABLE_STATUS_CODES,
    NON_RETRYABLE_STATUS_CODES,
    proxy_headers,
    truncate_text,
)

LOGGER = logging.getLogger("shadow_claw_gateway.chat_client")


def classify_text_route(text: str) -> str:
    lowered = (text or "").lower()
    if "```" in lowered:
        return CHAT_ROUTE_CODING
    if any(keyword in lowered for keyword in CODING_KEYWORDS):
        return CHAT_ROUTE_CODING
    return CHAT_ROUTE_DEFAULT


def get_chat_profile(config: dict, route_name: str) -> dict:
    if route_name == CHAT_ROUTE_CODING:
        return dict(config["coding_profile"])
    return dict(config["default_profile"])


def build_chat_payload(prompt: str, profile: dict) -> dict:
    payload = {
        "model": profile["model"],
        "messages": [
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user", "content": prompt},
        ],
        "stream": False,
    }
    reasoning_effort = profile.get("reasoning_effort", "").strip()
    if reasoning_effort:
        payload["reasoning_effort"] = reasoning_effort
    return payload


def should_fallback(status_code: int | None = None, error: Exception | None = None) -> bool:
    if error is not None:
        return isinstance(error, requests.RequestException)
    if status_code in NON_RETRYABLE_STATUS_CODES:
        return False
    if status_code in RETRYABLE_STATUS_CODES:
        return True
    return bool(status_code and status_code >= 500)


def response_error_details(response: requests.Response) -> str:
    details = truncate_text(response.text or "", 500)
    if details:
        return details
    return "no details returned"


def normalize_content(content) -> str:
    if isinstance(content, str):
        return content.strip()
    if isinstance(content, list):
        parts = []
        for item in content:
            if isinstance(item, dict) and item.get("type") == "text":
                parts.append(str(item.get("text", "")))
            else:
                parts.append(str(item))
        return "\n".join(part for part in parts if part).strip()
    return str(content).strip()


def extract_chat_content(response: requests.Response) -> str:
    body = response.json()
    choices = body.get("choices") or []
    if not choices:
        raise ValueError("missing choices")
    message = choices[0].get("message") or {}
    content = normalize_content(message.get("content", ""))
    if not content:
        raise ValueError("empty message content")
    return content


async def safe_edit_text(message, text: str) -> None:
    """Edit a Telegram message, logging warnings on API errors."""
    try:
        await message.edit_text(text)
    except Exception as exc:
        LOGGER.warning("safe_edit_text failed: %s", exc)


async def post_chat_completion(payload: dict, config: dict) -> requests.Response:
    loop = asyncio.get_running_loop()
    return await loop.run_in_executor(
        None,
        lambda: requests.post(
            config["api_url"],
            json=payload,
            headers=proxy_headers(config),
            timeout=config["chat_timeout_seconds"],
        ),
    )


async def get_proxy_endpoint(url: str, config: dict, timeout: int) -> requests.Response:
    loop = asyncio.get_running_loop()
    return await loop.run_in_executor(
        None,
        lambda: requests.get(
            url,
            headers=proxy_headers(config),
            timeout=timeout,
        ),
    )


async def attempt_chat_request(prompt: str, profile: dict, config: dict, label: str) -> dict:
    payload = build_chat_payload(prompt, profile)
    try:
        response = await post_chat_completion(payload, config)
    except requests.RequestException as error:
        return {
            "ok": False,
            "retryable": should_fallback(error=error),
            "error": f"{label} failed: {error}",
        }
    except Exception as error:
        return {
            "ok": False,
            "retryable": False,
            "error": f"{label} failed unexpectedly: {error}",
        }

    if response.status_code == 200:
        try:
            content = extract_chat_content(response)
        except Exception as error:
            return {
                "ok": False,
                "retryable": False,
                "error": f"{label} returned an invalid response: {error}",
            }
        return {"ok": True, "content": content, "status_code": response.status_code}

    return {
        "ok": False,
        "retryable": should_fallback(status_code=response.status_code),
        "error": f"{label} failed with HTTP {response.status_code}. {response_error_details(response)}",
        "status_code": response.status_code,
    }


# ---------------------------------------------------------------------------
# Agent-mode LLM calls (returns full message dict with tool_calls)
# ---------------------------------------------------------------------------


def build_agent_payload(
    messages: list[dict],
    profile: dict,
    tools: list[dict] | None = None,
) -> dict:
    """Build a chat completion payload that includes tool definitions."""
    payload = {
        "model": profile["model"],
        "messages": messages,
        "stream": False,
    }
    reasoning_effort = profile.get("reasoning_effort", "").strip()
    if reasoning_effort:
        payload["reasoning_effort"] = reasoning_effort
    if tools:
        payload["tools"] = tools
        payload["tool_choice"] = "auto"
    return payload


def extract_agent_message(response: requests.Response) -> dict:
    """Extract the full assistant message dict from a chat completion response.

    Returns a dict with keys: content, tool_calls, finish_reason.
    """
    body = response.json()
    choices = body.get("choices") or []
    if not choices:
        raise ValueError("missing choices in response")

    choice = choices[0]
    message = choice.get("message") or {}
    finish_reason = choice.get("finish_reason", "")

    content = normalize_content(message.get("content", ""))
    tool_calls = message.get("tool_calls") or []

    return {
        "content": content,
        "tool_calls": tool_calls,
        "finish_reason": finish_reason,
    }


async def attempt_agent_request(
    messages: list[dict],
    profile: dict,
    config: dict,
    tools: list[dict] | None = None,
    label: str = "Agent",
) -> dict:
    """Send messages + tools to LLM, return full message dict.

    On success: {"ok": True, "message": {content, tool_calls, finish_reason}}
    On failure: {"ok": False, "retryable": bool, "error": str}
    """
    payload = build_agent_payload(messages, profile, tools)
    try:
        response = await post_chat_completion(payload, config)
    except requests.RequestException as error:
        return {
            "ok": False,
            "retryable": should_fallback(error=error),
            "error": f"{label} failed: {error}",
        }
    except Exception as error:
        return {
            "ok": False,
            "retryable": False,
            "error": f"{label} failed unexpectedly: {error}",
        }

    if response.status_code == 200:
        try:
            msg = extract_agent_message(response)
        except Exception as error:
            return {
                "ok": False,
                "retryable": False,
                "error": f"{label} returned invalid response: {error}",
            }
        return {"ok": True, "message": msg, "status_code": response.status_code}

    return {
        "ok": False,
        "retryable": should_fallback(status_code=response.status_code),
        "error": f"{label} failed HTTP {response.status_code}. {response_error_details(response)}",
        "status_code": response.status_code,
    }
