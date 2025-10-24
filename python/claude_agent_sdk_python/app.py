import os
import asyncio
from typing import Any, Dict, List, Optional

from fastapi import FastAPI, Request, Response
from fastapi.responses import JSONResponse, PlainTextResponse


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
        from claude_agent_sdk import ClaudeSDKClient, AssistantMessage, TextBlock  # type: ignore
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
    # Fallback message if empty
    if not prompt:
        prompt = "Respond briefly."

    async def run_non_stream() -> Dict[str, Any]:
        content_texts: List[str] = []
        async with ClaudeSDKClient() as client:  # options can be default; env directs upstream
            await client.query(prompt)
            async for msg in client.receive_response():
                if isinstance(msg, AssistantMessage):
                    for block in msg.content:
                        if isinstance(block, TextBlock):
                            content_texts.append(block.text)
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
            from claude_agent_sdk import ClaudeSDKClient, AssistantMessage, TextBlock  # type: ignore
        except Exception as e:  # pragma: no cover
            yield f"data: {{\"error\":\"claude_agent_sdk not installed: {str(e)}\"}}\n\n"
            yield "data: [DONE]\n\n"
            return

        async with ClaudeSDKClient() as client:
            await client.query(prompt)
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
            # end marker
            yield "data: [DONE]\n\n"

    return PlainTextResponse(
        content=sse_generator(), status_code=200, media_type="text/event-stream"
    )


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

