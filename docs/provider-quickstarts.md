# Provider Quickstarts

Use this page for fast, provider-specific `config.yaml` setups with a single request success check.

## Prerequisites

- Service running and reachable on `http://localhost:8317`.
- Client API key configured in `api-keys` (or management endpoint auth in your deployment model).
- `jq` installed for response inspection.

## 1) Claude

`config.yaml`:

```yaml
api-keys:
  - "demo-client-key"

claude-api-key:
  - api-key: "sk-ant-..."
    prefix: "claude"
```

Validation:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude/claude-3-5-sonnet-20241022","messages":[{"role":"user","content":"ping"}]}' | jq
```

Sonnet 4.6 compatibility check:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude/claude-sonnet-4-6","messages":[{"role":"user","content":"ping"}]}' | jq
```

If your existing `claude-sonnet-4-5` route starts failing, switch aliases to `claude-sonnet-4-6` and confirm with `GET /v1/models` before rollout.

Opus 4.6 quickstart sanity check:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude/claude-opus-4-6","messages":[{"role":"user","content":"reply with ok"}],"stream":false}' | jq '.choices[0].message.content'
```

Opus 4.6 streaming parity check:

```bash
curl -N -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude/claude-opus-4-6","messages":[{"role":"user","content":"stream test"}],"stream":true}'
```

If Opus 4.6 is missing from `/v1/models`, verify provider alias mapping and prefix ownership before routing production traffic.

## 2) Codex

`config.yaml`:

```yaml
api-keys:
  - "demo-client-key"

codex-api-key:
  - api-key: "codex-key-a"
    prefix: "codex"
  - api-key: "codex-key-b"
    prefix: "codex"
```

Validation:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"codex/codex-latest","reasoning_effort":"low","messages":[{"role":"user","content":"hello"}]}' | jq
```

### Codex `/responses/compact` sanity check

Use this when validating codex translator compatibility for compaction payloads:

```bash
curl -sS -X POST http://localhost:8317/v1/responses/compact \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"codex/codex-latest","input":[{"role":"user","content":[{"type":"input_text","text":"compress this session"}]}]}' | jq '{object,usage}'
```

Expected: `object` is `response.compaction` and `usage` is present.

### Codex Responses load-balancing quickstart (two accounts)

Use two Codex credentials with the same `prefix` and validate with repeated `/v1/responses` calls:

```bash
for i in $(seq 1 6); do
  curl -sS -X POST http://localhost:8317/v1/responses \
    -H "Authorization: Bearer demo-client-key" \
    -H "Content-Type: application/json" \
    -d '{"model":"codex/codex-latest","stream":false,"input":[{"role":"user","content":[{"type":"input_text","text":"lb check"}]}]}' \
    | jq -r '"req=\($i) id=\(.id // "none") usage=\(.usage.total_tokens // 0)"'
done
```

Sanity checks:

- `/v1/models` should include your target Codex model for this client key.
- Requests should complete consistently across repeated calls (no account-level 403 bursts).
- If one account is invalid, remove or repair that entry first; do not keep partial credentials in active rotation.

Troubleshooting (`Question: Does load balancing work with 2 Codex accounts for the Responses API?`):

1. `403`/`401` on every request:
   - Validate both credentials independently (temporarily keep one `codex-api-key` entry at a time).
2. Mixed success/failure:
   - One credential is unhealthy or suspended; re-auth that entry and retry the loop.
3. `404 model_not_found`:
   - Check model exposure via `/v1/models` for the same client key and switch to an exposed Codex model.
4. Stream works but non-stream fails:
   - Compare `/v1/responses` payload shape and avoid legacy chat-only fields in Responses requests.

### Codex conversation-tracking alias (`conversation_id`)

For `/v1/responses`, `conversation_id` is accepted as a DX alias and normalized to `previous_response_id`:

```bash
curl -sS -X POST http://localhost:8317/v1/responses \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"codex/codex-latest","input":"continue","conversation_id":"resp_prev_123"}' | jq
```

Expected behavior:
- Upstream payload uses `previous_response_id=resp_prev_123`.
- If both are sent, explicit `previous_response_id` wins.

## 3) Gemini

`config.yaml`:

```yaml
api-keys:
  - "demo-client-key"

gemini-api-key:
  - api-key: "AIza..."
    prefix: "gemini"
    models:
      - name: "gemini-2.5-flash"
        alias: "flash"
```

Validation:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/flash","messages":[{"role":"user","content":"ping"}]}' | jq
```

Strict tool schema note:
- Function tools with `strict: true` are normalized to Gemini-safe schema with root `type: "OBJECT"`, explicit `properties`, and `additionalProperties: false`.

Gemini 3 Flash `includeThoughts` quickstart:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"gemini/flash",
    "messages":[{"role":"user","content":"ping"}],
    "reasoning_effort":"high",
    "stream":false
  }' | jq
```

If you pass `generationConfig.thinkingConfig.include_thoughts`, the proxy normalizes it to `includeThoughts` before upstream calls.

ToolSearch compatibility quick check (`defer_loading`):

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"gemini/flash",
    "messages":[{"role":"user","content":"search latest docs"}],
    "tools":[{"google_search":{"defer_loading":true,"lat":"1"}}]
  }' | jq
```

`defer_loading`/`deferLoading` fields are removed in Gemini-family outbound payloads to avoid Gemini `400` validation failures.

### Gemini CLI 404 quickstart (`Error 404: Requested entity was not found`)

Use this path when Gemini CLI/Gemini 3 requests return provider-side `404` and you need a deterministic isolate flow.

1. Verify model is exposed to the same client key:

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer demo-client-key" | jq -r '.data[].id' | rg 'gemini|gemini-2\.5|gemini-3'
```

2. Run non-stream check first:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/flash","messages":[{"role":"user","content":"ping"}],"stream":false}' | jq
```

3. Run stream parity check immediately after:

```bash
curl -N -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/flash","messages":[{"role":"user","content":"ping"}],"stream":true}'
```

If non-stream succeeds but stream fails, treat it as stream transport/proxy compatibility first. If both fail with `404`, fix alias/model mapping before retry.

### NVIDIA OpenAI-compat QA scenarios (stream/non-stream parity)

Use these checks when an OpenAI-compatible NVIDIA upstream reports connect failures.

```bash
# Non-stream baseline
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"openai-compat/nvidia-model","messages":[{"role":"user","content":"ping"}],"stream":false}' | jq

# Stream parity
curl -N -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"openai-compat/nvidia-model","messages":[{"role":"user","content":"ping"}],"stream":true}'
```

Edge-case payload checks:

```bash
# Empty content guard
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"openai-compat/nvidia-model","messages":[{"role":"user","content":""}],"stream":false}' | jq

# Tool payload surface
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"openai-compat/nvidia-model","messages":[{"role":"user","content":"return ok"}],"tools":[{"type":"function","function":{"name":"noop","description":"noop","parameters":{"type":"object","properties":{}}}}],"stream":false}' | jq
```

## 4) GitHub Copilot

`config.yaml`:

```yaml
api-keys:
  - "demo-client-key"

github-copilot:
  - name: "copilot-gpt-5"
    prefix: "copilot"
```

Validation:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"copilot-gpt-5","messages":[{"role":"user","content":"help me draft a shell command"}]}' | jq
```

Model availability guardrail (plus/team mismatch cases):

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer demo-client-key" | jq -r '.data[].id' | rg 'gpt-5.3-codex|gpt-5.3-codex-spark'
```

Only route traffic to models that appear in `/v1/models`. If `gpt-5.3-codex-spark` is missing for your account tier, use `gpt-5.3-codex`.

## 5) Kiro

`config.yaml`:

```yaml
api-keys:
  - "demo-client-key"

kiro:
  - token-file: "~/.aws/sso/cache/kiro-auth-token.json"
    prefix: "kiro"
```

Validation:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"kiro/claude-opus-4-5","messages":[{"role":"user","content":"ping"}]}' | jq
```

Large-payload sanity checks (to catch truncation/write failures early):

```bash
python - <<'PY'
print("A"*120000)
PY > /tmp/kiro-large.txt

curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d @<(jq -n --rawfile p /tmp/kiro-large.txt '{model:"kiro/claude-opus-4-5",messages:[{role:"user",content:$p}],stream:false}') | jq '.choices[0].finish_reason'
```

Kiro IAM login hints:

- Prefer AWS login/authcode flows when social login is unstable.
- Keep one auth file per account to avoid accidental overwrite during relogin.
- If you rotate accounts often, run browser login in incognito mode.

## 7) iFlow

Validation (`glm-4.7`):

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"iflow/glm-4.7","messages":[{"role":"user","content":"ping"}],"stream":false}' | jq
```

If you see `406`, verify model exposure in `/v1/models`, retry non-stream, and then compare headers/payload shape against known-good requests.

## 8) MiniMax

`config.yaml`:

```yaml
api-keys:
  - "demo-client-key"

minimax:
  - token-file: "~/.minimax/oauth-token.json"
    base-url: "https://api.minimax.io/anthropic"
```

Validation:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"minimax/abab6.5s","messages":[{"role":"user","content":"ping"}]}' | jq
```

## 9) MCP Server (Memory Operations)

Use this quickstart to validate an MCP server that exposes memory operations before wiring it into your agent/client runtime.

MCP `tools/list` sanity check:

```bash
curl -sS -X POST http://localhost:9000/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":"list-1","method":"tools/list","params":{}}' | jq
```

Expected: at least one memory tool (for example names containing `memory` like `memory_search`, `memory_write`, `memory_delete`).

MCP `tools/call` sanity check:

```bash
curl -sS -X POST http://localhost:9000/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":"call-1","method":"tools/call","params":{"name":"memory_search","arguments":{"query":"release notes"}}}' | jq
```

Expected: valid JSON-RPC result payload (or explicit MCP error payload with a concrete code/message pair).

## 7) OpenAI-Compatible Providers

For local tools like MLX/vLLM-MLX, use `openai-compatibility`:

```yaml
api-keys:
  - "demo-client-key"

openai-compatibility:
  - name: "mlx-local"
    prefix: "mlx"
    base-url: "http://127.0.0.1:8000/v1"
    api-key-entries:
      - api-key: "dummy-key"
```

Validation:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"mlx/your-local-model","messages":[{"role":"user","content":"hello"}]}' | jq
```

Streaming compatibility sanity check (`/v1/responses` vs `/v1/chat/completions`):

```bash
# 1) Baseline stream via /v1/responses
curl -sN -X POST http://localhost:8317/v1/responses \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"copilot/gpt-5.3-codex","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"say ping"}]}]}' | head -n 6

# 2) Compare with /v1/chat/completions stream behavior
curl -sN -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"copilot/gpt-5.3-codex","stream":true,"messages":[{"role":"user","content":"say ping"}]}' | head -n 6
```

Expected:
- `/v1/responses` should emit `data:` events immediately for Codex-family models.
- If `/v1/chat/completions` appears empty, route Codex-family traffic to `/v1/responses` and verify model visibility with `GET /v1/models`.

## Related

- [Getting Started](/getting-started)
- [Provider Usage](/provider-usage)
- [Provider Catalog](/provider-catalog)
- [Provider Operations](/provider-operations)

## Kiro + Copilot Endpoint Compatibility

- For Copilot Codex-family models (for example `gpt-5.1-codex-mini`), prefer `/v1/responses`.
- `/v1/chat/completions` is still valid for non-Codex Copilot traffic and most non-Copilot providers.
- If a Codex-family request fails on `/v1/chat/completions`, retry the same request on `/v1/responses` first.

## Qwen Model Visibility Check

If auth succeeds but clients cannot see expected Qwen models (for example `qwen3.5`), verify in this order:

```bash
# 1) Confirm models exposed to your client key
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer demo-client-key" | jq -r '.data[].id' | rg -i 'qwen|qwen3.5'

# 2) Confirm provider-side model listing from management
curl -sS http://localhost:8317/v0/management/config \
  -H "Authorization: Bearer <management-secret>" | jq '.providers[] | select(.provider=="qwen")'
```

If (1) is empty while auth is valid, check prefix rules and alias mapping first, then restart and re-read `/v1/models`.
