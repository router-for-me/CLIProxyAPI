@AGENTS.md

# Local Operational Notes

## Two-Proxy Setup (2026-07-04)

Two CLIProxyAPI instances run from the same project directory on different ports:

### Port 8317 — Primary (config.yaml)
- Auth dir: `~/.cli-proxy-api`
- API keys: `local`, `opencode`
- `force-model-prefix: true` — models must use prefix (e.g. `zen/deepseek-v4-flash`, `or/model-name`)
- `routing.strategy: fill-first`
- `disable-cooling: true`
- Providers: antigravity (OAuth), codex (OAuth), openai-compatibility (opencode-zen, deepseek, nvidia, openrouter)
- Used by: main agent, IDE integrations

### Port 8318 — Sub-agent / Light Tasks (rr-config.yaml)
- Auth dir: `~/.cli-proxy-api-2`
- API keys: `local`, `opencode`
- `force-model-prefix: true`
- `routing.strategy: round-robin`
- Providers: gemini-api-key (5 Google API keys), openai-compatibility (cerebras, groq)
- Used by: sub-agents, small tasks
- **NO deepseek/claude/codex/antigravity** — only gemini + cerebras + groq

### Known Issues

#### gemma-4-26b-a4b-it intermittent 500/503
- Model is free, high demand → Google API returns 503 "high demand" or 500 "Internal error"
- Streaming works more reliably than non-streaming during overload
- All 6 gemini-api-keys get the same error (model-level overload, not key-level)
- After all keys exhausted → proxy returns "auth_unavailable: no auth available"
- This is a Google-side capacity issue, not a proxy bug

#### Log collision
- Both proxies write to `logs/main.log` because both run from same working directory
- Request logs are separate files (per-request UUID), so those are fine
- To fix: run second proxy from a different directory or set a custom log path

#### DeepSeek on zen provider
- `zen/deepseek-v4-flash` works on port 8317 (tested 2026-07-04)
- Two API keys: `sk-9...BdZY` (direct), `sk-4...8iBU` (via socks5 proxy)
- If client sees "No provider available" — check which port the client connects to
- Port 8318 does NOT have deepseek configured

#### gemma JSON response_format not translated (OPEN)
- **Problem:** `response_format: {"type": "json_object"}` in OpenAI format is NOT translated to `responseMimeType: "application/json"` for Gemini
- **Effect:** Gemma models wrap JSON in markdown fenced blocks (` ```json ``` `) even when client requests JSON mode
- **Root cause:** `ConvertOpenAIRequestToGemini` (internal/translator/gemini/openai/chat-completions/gemini_openai_request.go:30) handles temperature, top_p, top_k, reasoning_effort, modalities, messages, tools — but NOT response_format
- **Workaround exists:** Native Gemini format (`responseMimeType: application/json`) returns raw JSON. Plugin at `/Users/adidos/Projects/cliproxy-plugins/gemma-json-fix/` strips markdown but is incomplete (no model filter, no streaming)
- **Gemma models missing from models.json:** `internal/registry/models/models.json` has 10 gemini models but NO gemma models (gemma-4-31b-it, gemma-4-26b-a4b-it). Works anyway because GeminiExecutor passes model names directly to Google API URL.

## Request Flow Architecture

```
Client → Gin Router → AuthMiddleware (API key check)
  → Handler (OpenAI/Gemini/Claude format detection)
    → Pipeline.TranslateRequest() — format conversion
      → Manager.selectAuth() — round-robin/fill-first credential selection
        → ProviderExecutor.Execute()/ExecuteStream()
          → Google/OpenAI/Anthropic API
        ← response
      ← Pipeline.TranslateResponse() — format conversion back
    ← client
```

### Key Code Paths
- **Gemini URL construction**: `internal/runtime/executor/gemini_executor.go:145`
  - `url = baseURL/v1beta/models/{model}:{action}`
  - Non-streaming: `:generateContent`, Streaming: `:streamGenerateContent?alt=sse`
- **Auth selection**: `sdk/cliproxy/auth/scheduler.go` — cooldown-based, round-robin/fill-first
- **Model registration**: `internal/registry/model_registry.go` — models registered from config at startup
- **OpenAI-compat model mapping**: config `models[].name` → `models[].alias`, prefix stripped before routing

### Error Response Patterns from CLIProxyAPI
- `401`: `{"error":"Missing API key"}` — no Bearer token
- `502`: `{"error":{"message":"unknown provider for model X"}}` — model not in any provider config
- `503`: `{"error":{"message":"auth_unavailable: no auth available (providers=X, model=Y)"}}` — all credentials exhausted/cooldown
- NOT from CLIProxyAPI: `"No provider available"` — this comes from client applications
