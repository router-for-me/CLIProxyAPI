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

OAuth + model visibility quickstart:

```bash
# 1) Ensure iFlow auth exists and is loaded
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer demo-client-key" | jq -r '.data[].id' | rg '^iflow/'
```

If only non-CLI iFlow models are visible after OAuth login, route requests strictly to the model IDs returned by `/v1/models` and avoid hardcoding upstream-only aliases.

Validation (`glm-4.7`):

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"iflow/glm-4.7","messages":[{"role":"user","content":"ping"}],"stream":false}' | jq
```

If you see `406`, verify model exposure in `/v1/models`, retry non-stream, and then compare headers/payload shape against known-good requests.

Stream/non-stream parity probe (for usage and request counting):

```bash
# Non-stream
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"iflow/glm-4.7","messages":[{"role":"user","content":"usage parity non-stream"}],"stream":false}' | jq '.usage'

# Stream (expects usage in final stream summary or server-side request accounting)
curl -N -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"iflow/glm-4.7","messages":[{"role":"user","content":"usage parity stream"}],"stream":true}' | tail -n 5
```

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

## 10) Amp Routing Through CLIProxyAPI

Use explicit base URL and key so Amp traffic does not bypass the proxy:

```bash
export OPENAI_API_BASE="http://127.0.0.1:8317/v1"
export OPENAI_API_KEY="demo-client-key"
```

Sanity check before Amp requests:

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer demo-client-key" | jq -r '.data[].id' | head -n 20
```

If Amp still does not route through CLIProxyAPI, run one direct canary call to verify the same env is active in the Amp process:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"amp-route-check"}]}' | jq '.id,.model'
```

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

## Copilot Unlimited Mode Compatibility (`CPB-0691`)

Use this validation when enabling `copilot-unlimited-mode` for Copilot API compatibility:

```bash
curl -sS -X POST http://localhost:8317/v1/responses \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"copilot/gpt-5.1-copilot","input":[{"role":"user","content":[{"type":"input_text","text":"compat probe"}]}]}' | jq '{id,model,usage}'
```

Expected:
- Response completes without chat/responses shape mismatch.
- `usage` is populated for rate/alert instrumentation.

## OpenAI->Anthropic Event Ordering Guard (`CPB-0692`, `CPB-0694`)

Streaming translation now enforces `message_start` before any `content_block_start` event.
Use this focused test command when validating event ordering regressions:

```bash
go test ./pkg/llmproxy/translator/openai/claude -run 'TestEnsureMessageStartBeforeContentBlocks' -count=1
```

## Gemini Long-Output 429 Observability + Runtime Refresh (`CPB-0693`, `CPB-0696`)

For long-output Gemini runs that intermittently return `429`, collect these probes in order:

```bash
# non-stream probe
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/flash","messages":[{"role":"user","content":"long output observability probe"}],"stream":false}' | jq

# stream parity probe
curl -N -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/flash","messages":[{"role":"user","content":"long output streaming probe"}],"stream":true}'
```

If config or model aliases were changed, restart only the affected service process and re-run both probes before broad rollout.

## AiStudio Error DX Triage (`CPB-0695`)

When users report AiStudio-facing errors, run a deterministic triage:

1. Verify model exposure with `/v1/models`.
2. Run one non-stream call.
3. Run one stream call using identical model and prompt.
4. Capture HTTP status plus upstream provider error payload.

Keep this flow provider-agnostic so the same checklist works for Gemini/Codex/OpenAI-compatible paths.

## Global Alias + Model Capability Safety (`CPB-0698`, `CPB-0699`)

Before shipping a global alias change:

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer demo-client-key" | jq '.data[] | {id,capabilities}'
```

Expected:
- Aliases resolve to concrete model IDs.
- Capability metadata stays visible (`capabilities` field remains populated for discovery clients).

## Load-Balance Naming + Distribution Check (`CPB-0700`)

Use consistent account labels/prefix names and verify distribution with repeated calls:

```bash
for i in $(seq 1 12); do
  curl -sS -X POST http://localhost:8317/v1/responses \
    -H "Authorization: Bearer demo-client-key" \
    -H "Content-Type: application/json" \
    -d '{"model":"codex/codex-latest","stream":false,"input":[{"role":"user","content":[{"type":"input_text","text":"distribution probe"}]}]}' \
    | jq -r '"req=\($i) id=\(.id // "none") total=\(.usage.total_tokens // 0)"'
done
```

If calls cluster on one account, inspect credential health and prefix ownership before introducing retry/failover policy changes.

## Mac Logs Visibility (`CPB-0711`)

When users report `Issue with enabling logs in Mac settings`, validate log emission first:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude/claude-sonnet-4-6","messages":[{"role":"user","content":"ping"}]}' | jq '.choices[0].message.content'

ls -lah logs | sed -n '1,20p'
tail -n 40 logs/server.log
```

Expected: request appears in `logs/server.log` and no OS-level permission errors are present. If permission is denied, re-run install with a writable logs directory.

## Thinking configuration (`CPB-0712`)

For Claude and Codex parity checks, use explicit reasoning controls:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude/claude-opus-4-6-thinking","messages":[{"role":"user","content":"solve this"}],"stream":false,"reasoning_effort":"high"}' | jq '.choices[0].message.content'

curl -sS -X POST http://localhost:8317/v1/responses \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"codex/codex-latest","input":[{"role":"user","content":[{"type":"input_text","text":"solve this"}]}],"reasoning_effort":"high"}' | jq '.output_text'
```

Expected: reasoning fields are accepted, and the reply completes without switching clients.

## gpt-5 Codex model discovery (`CPB-0713`)

Verify the low/medium/high variants are exposed before rollout:

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer demo-client-key" | jq -r '.data[].id' | rg '^gpt-5-codex-(low|medium|high)$'
```

If any IDs are missing, reload auth/profile config and confirm provider key scope.

## Mac/GUI Gemini privilege flow (`CPB-0714`)

For the `CLI settings privilege` repro in Gemini flows, confirm end-to-end with the same payload used by the client:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/flash","messages":[{"role":"user","content":"permission check"}],"stream":false}' | jq '.choices[0].message.content'
```

Expected: no interactive browser auth is required during normal request path.

## Images with Antigravity (`CPB-0715`)

When validating image requests, include a one-shot probe:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude/antigravity-gpt-5-2","messages":[{"role":"user","content":[{"type":"text","text":"analyze image"},{"type":"image","source":{"type":"url","url":"https://example.com/sample.png"}}]}]}' | jq '.choices[0].message.content'
```

Expected: image bytes are normalized and request succeeds or returns provider-specific validation with actionable details.

## `explore` tool workflow (`CPB-0716`)

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude/claude-opus-4-5-thinking","messages":[{"role":"user","content":"what files changed"}],"tools":[{"type":"function","function":{"name":"explore","description":"check project files","parameters":{"type":"object","properties":{}}}}],"stream":false}' | jq '.choices[0].message'
```

Expected: tool invocation path preserves request shape and returns tool payloads (or structured errors) consistently.

## Antigravity status and error parity (`CPB-0717`, `CPB-0719`)

Use a paired probe set for API 400 class failures:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"antigravity/gpt-5","messages":[{"role":"user","content":"quick parity probe"}],"stream":false}' | jq '.error.status_code? // .error.type // .'

curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer demo-client-key" | jq '{data_count:(.data|length),data:(.data|map(.id))}'
```

Expected: malformed/unsupported payloads return deterministic messages and no silent fallback.

## `functionResponse`/`tool_use` stability (`CPB-0718`, `CPB-0720`)

Run translator-focused regression checks after code changes:

```bash
go test ./pkg/llmproxy/translator/antigravity/gemini -run 'TestParseFunctionResponseRawSkipsEmpty|TestFixCLIToolResponseSkipsEmptyFunctionResponse|TestFixCLIToolResponse' -count=1
go test ./pkg/llmproxy/translator/antigravity/claude -run 'TestConvertClaudeRequestToAntigravity_ToolUsePreservesMalformedInput' -count=1
```

Expected: empty `functionResponse` content is not propagated as invalid JSON, and malformed tool args retain the `functionCall` block instead of dropping the tool interaction.

## Antigravity thinking-block + tool schema guardrails (`CPB-0731`, `CPB-0735`, `CPB-0742`, `CPB-0746`)

Use this when Claude/Antigravity returns `400` with `thinking` or `input_schema` complaints.

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"claude/claude-opus-4-5-thinking",
    "messages":[{"role":"user","content":"ping"}],
    "tools":[{"type":"function","function":{"name":"read_file","description":"read","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}],
    "thinking":{"type":"enabled","budget_tokens":1024},
    "max_tokens":2048,
    "stream":false
  }' | jq
```

Expected:
- Request succeeds without `max_tokens must be greater than thinking.budget_tokens`.
- Tool schema is accepted without `tools.0.custom.input_schema: Field required`.
- If failure persists, lower `thinking.budget_tokens` and re-check `/v1/models` for thinking-capable alias.

## Antigravity parity + model mapping (`CPB-0743`, `CPB-0744`)

Use this when Antigravity traffic is inconsistent between CLI tooling and API clients.

1) Validate CLI coverage matrix:

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer demo-client-key" | jq -r '.data[].id' | rg '^antigravity/'
```

2) Run CLI parity request for a model you expect to work:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"antigravity/gpt-5","messages":[{"role":"user","content":"ping"}],"stream":false}' | jq '.id,.model,.choices[0].message.content'
```

3) Add or update Amp model mappings for deterministic fallback:

```yaml
ampcode:
  force-model-mappings: true
  model-mappings:
    - from: "claude-opus-4-5-thinking"
      to: "gemini-claude-opus-4-5-thinking"
      params:
        custom_model: "iflow/tab"
        enable_search: true
```

4) Confirm params are injected and preserved:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-opus-4-5-thinking","messages":[{"role":"user","content":"mapping probe"}],"stream":false}' | jq
```

Expected:
- `/v1/models` includes expected Antigravity IDs.
- Mapping request succeeds even if source model has no local providers.
- Injected params appear in debug/trace payloads (or equivalent internal request logs) when verbose/request logging is enabled.

## Gemini OpenAI-compat parser probe (`CPB-0748`)

Use this quick probe when clients fail parsing Gemini responses due to non-standard fields:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/flash","messages":[{"role":"user","content":"return a short answer"}],"stream":false}' \
  | jq '{id,object,model,choices,usage,error}'
```

Expected: payload shape is OpenAI-compatible (`choices[0].message.content`) and does not require provider-specific fields in downstream parsers.

## Codex reasoning effort normalization (`CPB-0764`)

Validate `xhigh` behavior and nested `reasoning.effort` compatibility:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"codex/codex-latest","messages":[{"role":"user","content":"reasoning check"}],"reasoning":{"effort":"x-high"},"stream":false}' | jq
```

Expected: reasoning config is accepted; no fallback parse errors from nested/variant effort fields.

## Structured output quick probe (`CPB-0778`)

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"codex/codex-latest",
    "messages":[{"role":"user","content":"Return JSON with status"}],
    "response_format":{"type":"json_schema","json_schema":{"name":"status_reply","strict":true,"schema":{"type":"object","properties":{"status":{"type":"string"}},"required":["status"]}}},
    "stream":false
  }' | jq
```

Expected: translated request preserves `text.format.schema` and response remains JSON-compatible.
