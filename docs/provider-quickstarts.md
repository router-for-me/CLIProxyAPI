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

## 2) Codex

`config.yaml`:

```yaml
api-keys:
  - "demo-client-key"

codex-api-key:
  - api-key: "codex-key"
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

Team-account fallback probe (`400` on Spark):

```bash
for m in gpt-5.3-codex-spark gpt-5.3-codex; do
  echo "== $m =="
  curl -sS -X POST http://localhost:8317/v1/chat/completions \
    -H "Authorization: Bearer demo-client-key" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"copilot/$m\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}],\"stream\":false}" \
    | jq '{error,model:(.model // .error.model // "n/a")}'
done
```

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

## 6) MiniMax

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

MiniMax-M2.5 via iFlow parity check:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"iflow/minimax-m2.5","messages":[{"role":"user","content":"ping"}],"stream":false}' | jq
```

```bash
curl -sS -N -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"iflow/minimax-m2.5","messages":[{"role":"user","content":"ping"}],"stream":true}' | head -n 6
```

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

## 8) Gemini 3 Pro Image Preview

`config.yaml`:

```yaml
api-keys:
  - "demo-client-key"

gemini-api-key:
  - api-key: "AIza..."
    prefix: "gemini"
    models:
      - name: "gemini-3-pro-image-preview"
        alias: "image-preview"
```

Validation:

```bash
curl -sS -X POST http://localhost:8317/v1/images/generations \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/image-preview","prompt":"a scenic valley at dawn","size":"1024x1024"}' | jq
```

Timeout sanity check (for long-running image calls):

```bash
curl -sS -m 70 -X POST http://localhost:8317/v1/images/generations \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/image-preview","prompt":"high-detail concept art city at night","size":"1024x1024"}' | jq '{created,data_count:(.data|length)}'
```

## 9) Quota-Aware Credential Rotation

Use this when one credential repeatedly hits quota and you need safe fallback behavior.

`config.yaml`:

```yaml
api-keys:
  - "demo-client-key"

antigravity-api-key:
  - api-key: "ag-key-1"
    prefix: "ag"
  - api-key: "ag-key-2"
    prefix: "ag"
```

Validation:

```bash
for i in 1 2 3; do
  curl -sS -X POST http://localhost:8317/v1/chat/completions \
    -H "Authorization: Bearer demo-client-key" \
    -H "Content-Type: application/json" \
    -d '{"model":"ag/claude-sonnet-4-6","messages":[{"role":"user","content":"quota probe"}],"stream":false}' \
    | jq '{error,model,usage}';
done
```

## 10) Quota Protection Threshold (Prevent Last-Quota Burn)

Use this when you want a credential to stop receiving traffic before it is completely exhausted.

`config.yaml` example:

```yaml
quota-exceeded:
  switch-project: true
  switch-preview-model: true

routing:
  strategy: "round-robin"
```

Operational probe (non-stream + stream parity):

```bash
for mode in false true; do
  echo "== stream=$mode =="
  curl -sS -X POST http://localhost:8317/v1/chat/completions \
    -H "Authorization: Bearer demo-client-key" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"ag/claude-sonnet-4-6\",\"messages\":[{\"role\":\"user\",\"content\":\"quota-protection probe\"}],\"stream\":$mode}" \
    | head -n 8
done
```

If one credential starts returning repeated `429` while another succeeds, keep threshold protection enabled and rotate traffic to the healthy credential set.

If repeated `429` persists on one credential, rotate auth and re-run `/v1/models` before restoring traffic.

## 11) Kiro Hope

Use this flow when validating Kiro auth + model route stability.

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
  -d '{"model":"kiro/claude-sonnet-4-6","messages":[{"role":"user","content":"kiro hope sanity check"}],"stream":false}' | jq '{id,model,error}'
```

Troubleshooting starter:
- Re-auth Kiro token and retry once with `stream:false`.
- Compare `/v1/models` output before/after token refresh.

## 12) GLM Coding Plan Setup Helper

Use this when you need an interactive starter for GLM Coding Plan routing.

1. Run helper:
   - `./cliproxyapi++ --glm-coding-plan`
2. Copy the emitted `openai-compatibility` snippet into `config.yaml`.
3. Replace `REPLACE_WITH_GLM_API_KEY` with your actual key.
4. Reload service, then verify:
   - `curl -sS http://localhost:8317/v1/models -H "Authorization: Bearer <api-key>" | jq -r '.data[].id' | rg '^glm/'`

## Related

- [Getting Started](/getting-started)
- [Provider Usage](/provider-usage)
- [Provider Catalog](/provider-catalog)
- [Provider Operations](/provider-operations)

## Kiro + Copilot Endpoint Compatibility

- For Copilot Codex-family models (for example `gpt-5.1-codex-mini`), prefer `/v1/responses`.
- `/v1/chat/completions` is still valid for non-Codex Copilot traffic and most non-Copilot providers.
- If a Codex-family request fails on `/v1/chat/completions`, retry the same request on `/v1/responses` first.

## 13) iFlow CLI Extraction and Claude-Style Rate-Quota Fallback

`CPB-0741` asks for a deterministic fallback when `gemini-2.5-pro` model expiry/renames happen during quota exhaustion.

`config.yaml` (manual `iflow` helper profile):

```yaml
api-keys:
  - "demo-client-key"

iflow:
  - token: "reuse existing token from iflow login"
    prefix: "iflow"
    models:
      - name: "gemini-2.5-pro"
        alias: "gemini-25-pro"
      - name: "gemini-2.5-flash"
        alias: "gemini-25-flash"
```

Validation:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"iflow/gemini-25-pro","messages":[{"role":"user","content":"ping"}],"stream":false}' \
  | jq '{id,model,error}'
```

If this fails, compare:
- `GET /v1/models` output for `iflow/` entries
- `/v1/metrics/providers` error rate by provider
- model alias drift against `iflow` console metadata

## 14) iFlow `thinking.budget_tokens` and `max_tokens` Guardrail

`CPB-0742` addresses `invalid_request_error: max_tokens must be greater than thinking.budget_tokens`.

Use this request contract check before rollout:

```bash
for mode in "claude-opus-4-6-thinking" "claude-sonnet-4-6-thinking"; do
  echo "== $mode =="
  curl -sS -X POST http://localhost:8317/v1/chat/completions \
    -H "Authorization: Bearer demo-client-key" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"ag/$mode\",\"thinking\":{\"type\":\"enabled\",\"budget_tokens\":1500},\"max_tokens\":1600,\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}" \
    | jq '{model,error}'
done
```

If a request returns `invalid_request_error`, rerun with a larger `max_tokens` or smaller `thinking.budget_tokens` and keep `thinking.budget_tokens < max_tokens`.

## 15) Antigravity-Compatible CLI Surface Discovery

`CPB-0743` asks for a quick deterministic view of which CLIs support Antigravity.

```bash
thegent cliproxy login antigravity --help
thegent cliproxy login iflow --help
thegent cliproxy login kiwi --help
```

Recommended check:

```bash
thegent cliproxy status --provider antigravity --json | jq '.supported_clients,.login_methods'
thegent cliproxy status --json | jq '.providers | keys'
```

Keep supported-provider assumptions versioned in your deployment notes before changing traffic routing.

## 16) Dynamic Mapping and Custom Parameter Injection Validation

`CPB-0744` asks for stable custom parameter injection for dynamic model mapping (for example iFlow `/tab` style model variants).

`config.yaml`:

```yaml
api-keys:
  - "demo-client-key"

iflow:
  - token: "..."
    prefix: "iflow"
    models:
      - name: "glm-4.5"
        alias: "glm"
      - name: "glm-4.5-tab"
        alias: "glm/tab"
```

Validation:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"iflow/glm/tab","messages":[{"role":"user","content":"tab mapping probe"}],"stream":false}' \
  | jq '{model,error}'
```

Use this whenever `/` or slash-like model suffixes are introduced to prevent accidental provider aliasing.

## 16.5) `gemini-3-pro-preview` and `gmini` Compatibility Probe

`CPB-0747` and `CPB-0751` ask for deterministic parity checks for `gemini-3-pro-preview` behavior.

`config.yaml`:

```yaml
api-keys:
  - "demo-client-key"

gemini-api-key:
  - api-key: "AIza..."
    prefix: "gemini"
    models:
      - name: "gemini-3-pro-preview"
        alias: "3-pro-preview"
```

Sanity checks:

```bash
for mode in false true; do
  echo "== stream=$mode =="
  curl -sS -X POST http://localhost:8317/v1/chat/completions \
    -H "Authorization: Bearer demo-client-key" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"gemini/3-pro-preview\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}],\"stream\":$mode}" \
    | jq '{model,object,error}'
done
```

If this works in non-stream but not stream, keep `stream:false` for that alias during rollout, then switch back after one stable release.

## 17) Gemini Native Upload/File-Upload Runtime Probe (`CPB-0754`)

Use this before enabling upload-heavy Gemini native API paths:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/3-pro-preview","messages":[{"role":"user","content":"upload probe"}],"stream":false}' \
  | jq '{id,model,error}'

curl -sS -X POST http://localhost:8317/v1/images/generations \
  -H "Authorization: Bearer demo-client-key" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/image-preview","prompt":"upload-path smoke","size":"1024x1024"}' \
  | jq '{id,model,error,data:(.data|length)}'
```

After config updates, use deterministic local reload + validation:

```bash
process-compose -f examples/process-compose.dev.yaml restart cliproxy
curl -sS http://localhost:8317/health
```

Keep `/v1/models` in the runbook as the compatibility ground truth before moving upload workflows into CI.
