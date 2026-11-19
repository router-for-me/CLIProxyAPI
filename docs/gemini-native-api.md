# Native Gemini Embeddings & Decisions API

CLIProxyAPI serves the Google AI Studio native (non-chat) endpoints under `/v1beta`. Point a Google SDK's
`base_url` at the proxy and use `embedContent`, `batchEmbedContents`, or `decisions` exactly as you would against
`generativelanguage.googleapis.com` — requests and responses are passed through without chat translation.

## Supported Endpoints

| Endpoint | Method | Purpose |
|---|---|---|
| `/v1beta/models/{model}:embedContent` | POST | Embed a single content |
| `/v1beta/models/{model}:batchEmbedContents` | POST | Embed up to the upstream batch limit in one call |
| `/v1beta/decisions` | POST | Structured decisions (calibrated probabilities, zero output tokens) |

Any other `{action}` on `/v1beta/models/{model}:{action}` returns **404** with an explicit error body. Actions the
proxy does not implement never hang or return an empty `200`.

## Supported Models

| Model | Endpoint | Channels |
|---|---|---|
| `gemini-embedding-001` | `embedContent` / `batchEmbedContents` | `gemini` (API key), `aistudio` |
| `gemini-embedding-2` | `embedContent` / `batchEmbedContents` | `gemini` (API key), `aistudio` |
| `gemini-decision-1` | `decisions` | `gemini` (API key), `aistudio` |

The embedding and decision entries are built into the model catalog, so they remain listed after a remote model
catalog refresh.

### Channel support

| Channel | Embeddings | Decisions |
|---|---|---|
| `gemini` (Generative Language API key) | ✅ | ✅ |
| `aistudio` | ✅ | ✅ |
| `vertex` | 501 | 501 |

The Decisions API is only available on the Gemini Developer API, and Vertex embedding support is not uniform across
models, so Vertex rejects both with an explicit `501 Not Implemented` before any upstream call.

## Embeddings

### Single content

```bash
curl -sS http://localhost:8317/v1beta/models/gemini-embedding-001:embedContent \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "models/gemini-embedding-001",
    "content": {"parts": [{"text": "The quick brown fox"}]}
  }'
```

### Batch

```bash
curl -sS http://localhost:8317/v1beta/models/gemini-embedding-2:batchEmbedContents \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "requests": [
      {"model": "models/gemini-embedding-2", "content": {"parts": [{"text": "first"}]}},
      {"model": "models/gemini-embedding-2", "content": {"parts": [{"text": "second"}]}}
    ]
  }'
```

Batch responses are returned in request order; the proxy never reorders, merges, or splits entries.

### SDK example

```python
from google import genai

client = genai.Client(
    api_key="unused",                      # CPA authenticates upstream; send your CPA key instead
    http_options={"base_url": "http://localhost:8317"},
)

result = client.models.embed_content(
    model="gemini-embedding-001",
    contents=["The quick brown fox", "jumps over the lazy dog"],
)
print([e.values for e in result.embeddings])
```

## Decisions

The model travels in the **request body** using the bare model ID (no `models/` prefix). The endpoint is
non-streaming.

```bash
curl -sS http://localhost:8317/v1beta/decisions \
  -H "Authorization: Bearer $CPA_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gemini-decision-1",
    "input": "The blue mug I ordered on Monday arrived cracked on Wednesday.",
    "questions": {
      "is_damaged": {
        "type": "noul",
        "instructions": "Did the item arrive damaged?"
      }
    }
  }'
```

The response carries the calibrated answers plus a snake_case usage block:

```json
{
  "answers": {"is_damaged": {"type": "noul", "noul": 0.99, "confidence": 0.99}},
  "usage": {
    "total_input_tokens": 100,
    "input_tokens_by_modality": [{"modality": "text", "tokens": 100}],
    "total_output_tokens": 0,
    "total_tokens": 100
  }
}
```

`input` may also be a multimodal array (text, image, audio, video, document). Question keys, types, and internal
validation rules are forwarded to the upstream service unchanged.

## Error Semantics

| Situation | Response |
|---|---|
| Unknown `models/{model}:{action}` | `404` with an error body naming the unsupported action |
| `stream: true` on `/v1beta/decisions` | `400 invalid_request_error` (the endpoint is non-streaming) |
| Missing `model`, missing/empty `questions`, non-object body | `400 invalid_request_error` |
| Vertex credential selected for an embedding or decision request | `501 Not Implemented` |
| Missing or invalid CPA API key | `401` from the auth middleware |
| Upstream 4xx/5xx | Passed through with the upstream status and body |

## Usage Accounting

Both capabilities are billed through the normal usage pipeline, so they appear in usage records, quotas, and
round-robin accounting alongside chat requests.

- Embeddings report input tokens only; there are no output tokens.
- Decisions reports `total_input_tokens` from the snake_case `usage` block and **always zero output tokens**. The
  zero-output cooldown and retry guards do not apply to this endpoint, so repeated calls never cool the credential.
- If an upstream response omits usage, the request is still recorded with zero tokens rather than dropped.

## Payload Configuration

Request payload rules (`payload.override` / `payload.filter` in `config.yaml`) still apply, evaluated exactly once on
the final outbound payload. Chat-only injection (`contents`, `safetySettings`, `tools`, `generationConfig`, thinking
fields) is never added to these endpoints.

## Not Supported

- OpenAI-shaped `POST /v1/embeddings` (needs a separate translator layer).
- `asyncBatchEmbedContents` (long-running operations).
- Streaming variants of embeddings or decisions.
- Downgrading a Decisions request to `/v1/chat/completions` (the output is probabilities, not text).
- Legacy `text-embedding-*` model aliases (tracked as an open question; not implemented).
