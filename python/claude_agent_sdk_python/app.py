import os
import asyncio
from typing import Any, Dict, List, Optional

from fastapi import FastAPI, Request, Response
from fastapi.responses import JSONResponse, StreamingResponse
import logging
import traceback
import socket
import ssl
import json
import urllib.request
import urllib.error
from functools import partial

# 配置日志级别（支持环境变量 PYTHONLOGLEVEL 或默认 INFO）
logging.basicConfig(
    level=os.getenv("PYTHONLOGLEVEL", "INFO").upper(),
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)

app = FastAPI(title="claude agent sdk python bridge", version="0.1.0")


def build_prompt_from_messages(messages: List[Dict[str, Any]]) -> str:
    parts: List[str] = []
    for m in messages or []:
        role = m.get("role", "user")
        content = m.get("content", "")
        if isinstance(content, list):
            # join text parts
            content = "\n".join(
                x.get("text", "") if isinstance(x, dict) else str(x) for x in content
            )
        parts.append(f"[{role}] {content}")
    return "\n\n".join(parts).strip()


@app.get("/healthz")
async def healthz() -> Dict[str, str]:
    return {"status": "ok"}


@app.post("/v1/chat/completions")
async def chat_completions(req: Request) -> Response:
    try:
        payload = await req.json()
    except Exception:
        return JSONResponse({"error": {"message": "invalid json"}}, status_code=400)

    stream = bool(payload.get("stream"))
    messages = payload.get("messages") or []
    model = payload.get("model", "glm-4.6")

    # Validate required env for zhipu via anthropic-compatible gateway
    base_url = os.getenv("ANTHROPIC_BASE_URL", "").strip()
    token = os.getenv("ANTHROPIC_AUTH_TOKEN", "").strip()
    if not base_url or not token:
        return JSONResponse(
            {
                "error": {
                    "message": "missing environment: ANTHROPIC_BASE_URL and/or ANTHROPIC_AUTH_TOKEN",
                    "type": "configuration_error",
                }
            },
            status_code=500,
        )

    # Try to use claude agent sdk python
    try:
        from claude_agent_sdk import ClaudeSDKClient, AssistantMessage, TextBlock, ClaudeAgentOptions  # type: ignore
    except Exception as e:  # pragma: no cover - optional dependency
        return JSONResponse(
            {
                "error": {
                    "message": f"claude_agent_sdk not installed: {e}",
                    "type": "missing_dependency",
                }
            },
            status_code=501,
        )

    prompt = build_prompt_from_messages(messages)
    if not prompt:
        prompt = "Respond briefly."

    # Build env for SDK options; merge request-provided env if any
    req_env = payload.get("env") or {}
    env_map: Dict[str, Any] = {
        "ANTHROPIC_BASE_URL": base_url,
        "ANTHROPIC_AUTH_TOKEN": token,
        "ANTHROPIC_MODEL": model,
    }
    for k in ("API_TIMEOUT_MS", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"):
        v = os.getenv(k, "").strip()
        if v:
            env_map[k] = v
    if isinstance(req_env, dict):
        env_map.update({k: str(v) for k, v in req_env.items()})

    def _mask_token(val: str) -> str:
        v = val or ""
        if len(v) <= 10:
            return "***"
        return v[:6] + "..." + v[-4:]

    def _classify_error(e: Exception) -> str:
        if isinstance(e, socket.gaierror):
            return "DNS"
        if isinstance(e, ConnectionRefusedError):
            return "ECONNREFUSED"
        if isinstance(e, TimeoutError):
            return "ETIMEDOUT"
        if isinstance(e, ssl.SSLError):
            return "SSL"
        return e.__class__.__name__

    def _log_structured(level: int, msg: str, **fields: Any) -> None:
        logging.log(level, msg + " | " + json.dumps(fields))

    async def run_non_stream() -> Dict[str, Any]:
        content_texts: List[str] = []
        tool_calls_list: List[Dict[str, Any]] = []
        try:
            options = ClaudeAgentOptions(
                env={k: str(v) for k, v in env_map.items()},
                model=model
            )
            async with ClaudeSDKClient(options=options) as client:
                _log_structured(logging.INFO, "sdk_query_start", prompt_preview=prompt[:100])
                await client.query(prompt)
                async for msg in client.receive_response():
                    if isinstance(msg, AssistantMessage):
                        block_index = 0
                        for block in getattr(msg, "content", []):
                            block_type = type(block).__name__
                            if isinstance(block, TextBlock):
                                text = getattr(block, "text", "") or ""
                                preview = text[:100] if text else ""
                                _log_structured(
                                    logging.DEBUG,
                                    "content_block_received",
                                    index=block_index,
                                    type=block_type,
                                    preview=preview,
                                    has_tool_calls_before=len(tool_calls_list) > 0
                                )
                                if text:
                                    content_texts.append(text)
                            elif hasattr(block, "id") and hasattr(block, "name") and hasattr(block, "input"):
                                block_name = getattr(block, "name", "")
                                _log_structured(
                                    logging.DEBUG,
                                    "content_block_received",
                                    index=block_index,
                                    type=block_type,
                                    tool_name=block_name,
                                    has_tool_calls_before=len(tool_calls_list) > 0
                                )
                                raw_args = getattr(block, "input", {}) or {}
                                if not isinstance(raw_args, dict):
                                    try:
                                        raw_args = dict(raw_args)  # type: ignore[arg-type]
                                    except Exception:
                                        raw_args = {}
                                try:
                                    arguments_json = json.dumps(raw_args, ensure_ascii=False)
                                except (TypeError, ValueError):
                                    arguments_json = "{}"
                                tool_calls_list.append(
                                    {
                                        "id": getattr(block, "id", ""),
                                        "type": "function",
                                        "function": {
                                            "name": block_name,
                                            "arguments": arguments_json,
                                        },
                                    }
                                )
                            block_index += 1
        except Exception as e:
            tb = traceback.format_exc()
            _log_structured(
                logging.ERROR,
                "sdk_query_error",
                category=_classify_error(e),
                url=os.getenv("ANTHROPIC_BASE_URL", ""),
                auth_preview=_mask_token(os.getenv("ANTHROPIC_AUTH_TOKEN", "")),
                model=model,
                env_keys=sorted(list(env_map.keys())),
                traceback=tb,
            )
            return {
                "error": {
                    "message": str(e),
                    "type": _classify_error(e),
                    "url": os.getenv("ANTHROPIC_BASE_URL", ""),
                    "auth_preview": _mask_token(os.getenv("ANTHROPIC_AUTH_TOKEN", "")),
                    "model": model,
                }
            }
        text = "".join(content_texts) if content_texts else ""
        message_dict: Dict[str, Any] = {"role": "assistant"}

        if tool_calls_list:
            message_dict["content"] = text if text else None
            message_dict["tool_calls"] = tool_calls_list
            finish_reason = "tool_calls"
        else:
            message_dict["content"] = text
            finish_reason = "stop"

        return {
            "id": "chatcmpl-bridge",
            "object": "chat.completion",
            "created": int(asyncio.get_event_loop().time()),
            "model": model,
            "choices": [
                {
                    "index": 0,
                    "message": message_dict,
                    "finish_reason": finish_reason,
                }
            ],
            "usage": {"prompt_tokens": None, "completion_tokens": None, "total_tokens": None},
        }

    if not stream:
        data = await run_non_stream()
        return JSONResponse(data)

    # Stream mode via SSE
    async def sse_generator():
        try:
            from claude_agent_sdk import ClaudeSDKClient, AssistantMessage, TextBlock, ClaudeAgentOptions  # type: ignore
        except Exception as e:  # pragma: no cover
            yield f"data: {{\"error\":\"claude_agent_sdk not installed: {str(e)}\"}}\n\n"
            yield "data: [DONE]\n\n"
            return

        try:
            opts = {
                "ANTHROPIC_BASE_URL": base_url,
                "ANTHROPIC_AUTH_TOKEN": token,
                "ANTHROPIC_MODEL": model,
                **{k: os.getenv(k, "") for k in ("API_TIMEOUT_MS", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC")},
            }
            _log_structured(logging.INFO, "sdk_stream_start", prompt_preview=prompt[:100])
            tool_calls_count = 0
            async with ClaudeSDKClient(options=ClaudeAgentOptions(env=opts, model=model)) as client:
                await client.query(prompt)
                async for msg in client.receive_response():
                    if isinstance(msg, AssistantMessage):
                        block_index = 0
                        for block in getattr(msg, "content", []):
                            block_type = type(block).__name__
                            if isinstance(block, TextBlock):
                                text_piece = getattr(block, "text", "") or ""
                                preview = text_piece[:100] if text_piece else ""
                                _log_structured(
                                    logging.DEBUG,
                                    "content_block_stream",
                                    index=block_index,
                                    type=block_type,
                                    preview=preview,
                                    has_tool_calls_before=tool_calls_count > 0
                                )
                                if not text_piece:
                                    block_index += 1
                                    continue
                                chunk = {
                                    "id": "chatcmpl-bridge",
                                    "object": "chat.completion.chunk",
                                    "created": int(asyncio.get_event_loop().time()),
                                "model": model,
                                "choices": [
                                    {
                                        "index": 0,
                                        "delta": {"content": text_piece},
                                        "finish_reason": None,
                                        }
                                    ],
                                }
                                yield "data: " + __import__("json").dumps(chunk) + "\n\n"
                            elif hasattr(block, "id") and hasattr(block, "name") and hasattr(block, "input"):
                                block_name = getattr(block, "name", "")
                                _log_structured(
                                    logging.DEBUG,
                                    "content_block_stream",
                                    index=block_index,
                                    type=block_type,
                                    tool_name=block_name,
                                    has_tool_calls_before=tool_calls_count > 0
                                )
                                tool_calls_count += 1
                                raw_args = getattr(block, "input", {}) or {}
                                if not isinstance(raw_args, dict):
                                    try:
                                        raw_args = dict(raw_args)  # type: ignore[arg-type]
                                    except Exception:
                                        raw_args = {}
                                try:
                                    arguments_json = json.dumps(raw_args, ensure_ascii=False)
                                except (TypeError, ValueError):
                                    arguments_json = "{}"
                                tool_call = {
                                    "id": getattr(block, "id", ""),
                                    "type": "function",
                                    "function": {
                                        "name": block_name,
                                        "arguments": arguments_json,
                                    }
                                }
                                chunk = {
                                    "id": "chatcmpl-bridge",
                                    "object": "chat.completion.chunk",
                                    "created": int(asyncio.get_event_loop().time()),
                                    "model": model,
                                    "choices": [
                                        {
                                            "index": 0,
                                            "delta": {"tool_calls": [tool_call]},
                                            "finish_reason": None,
                                        }
                                    ],
                                }
                                yield "data: " + __import__("json").dumps(chunk) + "\n\n"
                            block_index += 1
        except Exception as e:
            tb = traceback.format_exc()
            _log_structured(
                logging.ERROR,
                "sdk_stream_error",
                category=_classify_error(e),
                url=os.getenv("ANTHROPIC_BASE_URL", ""),
                auth_preview=_mask_token(os.getenv("ANTHROPIC_AUTH_TOKEN", "")),
                model=model,
                traceback=tb,
            )
            yield "data: {\"error\": " + json.dumps({
                "type": _classify_error(e),
                "message": str(e),
            }) + "}\n\n"
            yield "data: [DONE]\n\n"
            return

        # Normal completion: send end marker
        yield "data: [DONE]\n\n"

    return StreamingResponse(
        content=sse_generator(), status_code=200, media_type="text/event-stream"
    )


# Diagnostic endpoint: server-side upstream check with 90s timeout
@app.post("/debug/upstream-check")
async def upstream_check(req: Request) -> Response:
    try:
        payload = await req.json()
    except Exception:
        payload = {}
    model = (payload.get("model") or "glm-4.6").strip()
    messages = payload.get("messages") or [{"role": "user", "content": "hi"}]
    base_url = os.getenv("ANTHROPIC_BASE_URL", "").strip()
    token = os.getenv("ANTHROPIC_AUTH_TOKEN", "").strip()
    if not base_url or not token:
        return JSONResponse({
            "error": {
                "message": "missing environment: ANTHROPIC_BASE_URL and/or ANTHROPIC_AUTH_TOKEN",
                "type": "configuration_error",
            }
        }, status_code=500)

    # Try multiple common upstream paths based on provided base_url
    # 1) OpenAI-compatible chat completions
    # 2) OpenAI-compatible v1 chat completions
    # 3) Anthropic messages API (requires anthropic-version)
    paths = [
        "/chat/completions",
        "/v1/chat/completions",
        "/v1/messages",
    ]
    last_err: Optional[Dict[str, Any]] = None
    for suffix in paths:
        url = base_url.rstrip("/") + suffix
        if suffix == "/v1/messages":
            # Anthropic-style body
            body = {"model": model, "messages": messages, "max_tokens": 64}
        else:
            # OpenAI-compatible body
            body = {"model": model, "messages": messages, "stream": False}
        data = json.dumps(body).encode("utf-8")
        req_obj = urllib.request.Request(url, data=data, method="POST")
        req_obj.add_header("Content-Type", "application/json")
        req_obj.add_header("Authorization", f"Bearer {token}")
        # Some gateways expect x-api-key; Anthropic expects anthropic-version
        req_obj.add_header("x-api-key", token)
        if suffix == "/v1/messages":
            req_obj.add_header("anthropic-version", "2023-06-01")
        try:
            with urllib.request.urlopen(req_obj, timeout=90) as resp:
                raw = resp.read()
                text = raw.decode("utf-8", errors="replace")
                try:
                    parsed = json.loads(text)
                except Exception:
                    parsed = None
                return JSONResponse({
                    "url": url,
                    "status": resp.getcode(),
                    "body": parsed if parsed is not None else text,
                })
        except Exception as e:
            tb = traceback.format_exc()
            if isinstance(e, urllib.error.HTTPError):
                err_text = None
                try:
                    err_text = e.read().decode("utf-8", errors="replace")
                except Exception:
                    err_text = str(e)
                last_err = {"url": url, "type": f"HTTP_{e.code}", "message": err_text}
                _log_structured(logging.ERROR, "upstream_http_error", url=url, status=e.code, body_preview=(err_text or "")[:512])
                # If 404, try next suffix; else return immediately
                if e.code != 404:
                    return JSONResponse({"url": url, "status": e.code, "error": last_err}, status_code=200)
            else:
                cat = _classify_error(e)
                _log_structured(logging.ERROR, "upstream_transport_error", category=cat, url=url, traceback=tb)
                return JSONResponse({"url": url, "error": {"type": cat, "message": str(e)}}, status_code=200)
    # If all attempts failed, return last error (likely 404)
    return JSONResponse(last_err or {"error": {"type": "UNKNOWN", "message": "all attempts failed"}}, status_code=200)


if __name__ == "__main__":
    import uvicorn

    port = int(os.getenv("PORT", "35331"))
    uvicorn.run(
        "app:app",
        host="127.0.0.1",
        port=port,
        reload=False,
        log_level="info",
    )
