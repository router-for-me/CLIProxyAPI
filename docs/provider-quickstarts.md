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

## Related

- [Getting Started](/getting-started)
- [Provider Usage](/provider-usage)
- [Provider Catalog](/provider-catalog)
- [Provider Operations](/provider-operations)
