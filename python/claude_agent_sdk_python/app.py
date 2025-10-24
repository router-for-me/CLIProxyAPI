import os
import asyncio
from typing import Any, Dict, List, Optional

from fastapi import FastAPI, Request, Response
from fastapi.responses import JSONResponse, PlainTextResponse
import logging
import traceback
import socket
import ssl
import json
import urllib.request
import urllib.error
from functools import partial


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
        try:
            async with ClaudeSDKClient() as client:
                options = ClaudeAgentOptions(env={k: str(v) for k, v in env_map.items()})
                await client.query(prompt, options=options)
                async for msg in client.receive_response():
                    if isinstance(msg, AssistantMessage):
                        for block in msg.content:
                            if isinstance(block, TextBlock):
                                content_texts.append(block.text)
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
        return {
            "id": "chatcmpl-bridge",
            "object": "chat.completion",
            "created": int(asyncio.get_event_loop().time()),
            "model": model,
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": text},
                    "finish_reason": "stop",
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
            async with ClaudeSDKClient() as client:
                options = ClaudeAgentOptions(env={
                    "ANTHROPIC_BASE_URL": base_url,
                    "ANTHROPIC_AUTH_TOKEN": token,
                    "ANTHROPIC_MODEL": model,
                    **{k: os.getenv(k, "") for k in ("API_TIMEOUT_MS", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC")},
                })
                await client.query(prompt, options=options)
                async for msg in client.receive_response():
                    if isinstance(msg, AssistantMessage):
                        for block in msg.content:
                            if hasattr(block, "text") and isinstance(block.text, str):
                                chunk = {
                                    "id": "chatcmpl-bridge",
                                    "object": "chat.completion.chunk",
                                    "created": int(asyncio.get_event_loop().time()),
                                    "model": model,
                                    "choices": [
                                        {
                                            "index": 0,
                                            "delta": {"content": block.text},
                                            "finish_reason": None,
                                        }
                                    ],
                                }
                                yield "data: " + __import__("json").dumps(chunk) + "\n\n"
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
            # end marker
            yield "data: [DONE]\n\n"

    return PlainTextResponse(
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

    url = base_url.rstrip("/") + "/v1/chat/completions"
    body = {"model": model, "messages": messages, "stream": False}
    data = json.dumps(body).encode("utf-8")
    req_obj = urllib.request.Request(url, data=data, method="POST")
    req_obj.add_header("Content-Type", "application/json")
    req_obj.add_header("Authorization", f"Bearer {token}")
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
        cat = ""
        if isinstance(e, urllib.error.HTTPError):
            cat = f"HTTP_{e.code}"
            try:
                err_text = e.read().decode("utf-8", errors="replace")
            except Exception:
                err_text = str(e)
            _log_structured(logging.ERROR, "upstream_http_error", url=url, status=e.code, body_preview=err_text[:512])
            return JSONResponse({
                "url": url,
                "status": e.code,
                "error": {"type": cat, "message": err_text},
            }, status_code=200)
        else:
            cat = _classify_error(e)
            _log_structured(logging.ERROR, "upstream_transport_error", category=cat, url=url, traceback=tb)
            return JSONResponse({
                "url": url,
                "error": {"type": cat, "message": str(e)},
            }, status_code=200)


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
