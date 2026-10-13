# Claude Code Web Search Router (ModelRouter example)

This plugin demonstrates **ModelRouter** on Claude Code built-in `web_search` requests (see `temp/1.json` in the repo root for a captured request/response).

## What it detects

- Inbound protocol `claude` / `anthropic`
- `tools[]` with `type` `web_search_20250305` or `web_search_20260209`
- Optional Claude Code heuristics: system text like “web search tool use”, or user text
  `Perform a web search for the query: …`

## Routes (`route` config)

| Value                    | Behavior                                                                                                                                                                                                                                                                                                  |
| ------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `fallback` (**default**) | Plugin **executor** runs **antigravity → codex → xai → kimi_responses → kimi_coding → kimi_web_search → tavily** (built-ins via `host.model.*`, Kimi/Tavily in-plugin). On **429/503/502/504**, tries the next backend in the same request. Backends that fail often are **deprioritized on later requests** (in-memory penalty; no extra config). |
| `antigravity_google` / `codex_web_search` / `xai_web_search` / `kimi_responses` / `kimi_coding` / `kimi_web_search` / `tavily` | Same orchestration for that backend’s chain member(s): execution retry + penalty apply when multiple backends are eligible. |
| `default_provider`             | `default_provider` + optional `default_provider_model` via built-in AuthManager (not orchestrated).                                                                                                                                                                                                                          |
Routing for `fallback` requires at least one runnable backend (providers in `AvailableProviders` where needed, resolvable antigravity model, `kimi_coding_api_keys`, `kimi_api_keys`, or `tavily_api_keys`).

### Kimi host-auth backend (`kimi_responses`)

`kimi_responses` reuses the host `kimi` provider’s **existing credential** — no
plugin keys required. It calls `host.model.execute` with
`entry_protocol=openai-response` and a Responses body carrying
`{"type": "web_search"}`, so the host executor posts to
`{credential base}/v1/responses` — `api.kimi.com/coding` for the Kimi for Coding
OAuth login (web_search-capable per Kimi’s Codex integration) or
`api.moonshot.ai` for a platform API key stored as kimi auth. The reply is
translated back to Claude blocks, including `web_search_call` →
`server_tool_use` / `web_search_tool_result`.

The model is auto-selected from the credential’s `base_url`
(`kimi_responses_model` overrides): `kimi-k2.8-code` → `kimi-for-coding`
upstream on coding endpoints, `kimi-k3` on Moonshot platform endpoints. When
both credential kinds exist the coding credential is preferred, and the
credential is pinned via `auth_id` so model and endpoint never mismatch.
Conversation history is forwarded as text-only input items; Claude tool options
(`allowed_domains`, `max_uses`, …) apply to the `kimi_web_search` backend, not
this one — use `route: kimi_responses` to force it or let `fallback` reach it.

- **Retry**: when several compatible credentials exist, a retryable failure
  (429/503/502/504) moves to the next credential before the backend gives
  up; a non-retryable failure stops immediately. The highest priority tier
  is rotated first and lower tiers are tried after retryable top-tier
  failures, mirroring the host selector's highest-*currently-available*-tier
  semantics. Rotation within a tier is uniform round-robin because
  `host.auth.list` does not expose per-credential weights, so the host's
  weighted-round-robin (honored only when no `auth_id` is pinned) cannot be
  mirrored while pinning keeps model and endpoint matched.
- **Degraded fallback**: when `host.auth.list` is unavailable (older host),
  execution degrades to an unpinned coding-plan model tried across both kimi
  provider buckets (`kimi`, then `kimi-ai`, or hint-ordered when the route
  metadata named a family) — a `kimi` bucket miss (`auth_not_found`) falls
  through to `kimi-ai`, so a kimi-ai-only deployment still executes.

### xAI web search notes (aligned with upstream docs)

- **Model**: xAI documents `grok-4.3` for server-side `web_search`. This example sets `TargetModel` to **`grok-4.3`** when `xai_model` is empty (do not forward `claude-sonnet-4-6` to xAI).
- **Request shape**: Responses API `input` + `tools[]` with `"type": "web_search"`. Optional `filters.allowed_domains` / `filters.excluded_domains` (max 5 each, mutually exclusive).
- **Claude mapping today**: `internal/translator/codex/claude` copies Claude `allowed_domains` → `filters.allowed_domains`. Claude `blocked_domains` is **not** mapped to `excluded_domains` yet.
- **Executor**: `xai_executor` normalizes tools (drops unsupported `external_web_access` if present) and posts to `/responses`.
- **Response**: Citations / server tool metadata come back through OpenAI Responses SSE and are converted toward Claude `server_tool_use` / `web_search_tool_result` where the response translator supports it.

### Kimi web search notes

The `kimi_web_search` backend runs **in-plugin** against the Moonshot Open
Platform tools APIs (`api.moonshot.ai`), the same way Tavily does — it does not
use the host `kimi` provider, whose OAuth credential targets `api.kimi.com/coding`
(Kimi for Coding) rather than the platform key endpoints.

- **Keys**: `kimi_api_keys` are Moonshot Open Platform keys (platform.kimi.ai
  console), round-robined. They are billed per call and are unrelated to the
  host `kimi` auth.
- **Levels** (`kimi_search_level`):
  - `basic` → `POST /v1/tools/search` (titles/URLs/snippets; `date` → `page_age`)
  - `pro` → `POST /v1/tools/search_pro` (adds `sites`, `time_window`, ranked
    `chunks[]`; top chunk becomes the hit snippet)
  - `fetch` → `POST /v1/tools/fetch` (Markdown body of a single URL; falls back
    to `basic` when the query has no URL)
  - `model` → `POST /v1/responses` with `{"type": "web_search"}`; the server
    searches and **kimi writes the answer itself** (`kimi_model`, default
    `kimi-k3`). Higher quality, adds model token cost + latency.
  - `auto` (**default**) picks per request: fetch when the query is a bare/intent
    URL, pro when `allowed_domains`/`kimi_sites` are set or the query looks
    research-grade, otherwise basic — and when `kimi_pro_escalation` is on, an
    **empty** basic result set is retried through pro (empty calls are unbilled).
- **Claude mapping**: `max_uses` → `limit` (clamped 1–20),
  `allowed_domains` → `sites` (≤5, Pro only), `blocked_domains` → client-side
  hit filtering. `user_location` is not mapped (no Kimi equivalent).
- **Host transport, host payload rules**: upstream calls go through the
  transport-only `host.http.do` bridge (so `requests.proxy-url` and
  request-log capture apply), but the plugin SDK exposes no payload-rule
  callback, so host `payloads` config filters are **not** applied to this
  backend's direct calls (including the `model` level) or to `kimi_coding`.
  Deployments that rely on payload rules should use `kimi_responses`, which
  executes through the host executor where payload rules are the final
  barrier before sending.
- **Deprecated upstream**: the `$web_search` `builtin_function` chat-completions
  tool retires 2026-10-20 and is intentionally not used.

### Kimi for Coding backend (`kimi_coding`)

`kimi_coding` calls the **Kimi for Coding Responses endpoint directly** —
`{base}/v1/responses` (default `https://api.kimi.ai/coding`, override with
`kimi_coding_base_url`) — with a server-side `web_search` tool, so the coding
model (`kimi_coding_model`, default `kimi-for-coding`) answers using live
search. The Responses output is translated into Claude `server_tool_use` /
`web_search_result` / `text` blocks.

It covers deployments where kimi access is an **OpenAI-compat API key group**
(e.g. `api.kimi.ai/coding`) rather than a kimi OAuth auth (which
`kimi_responses` needs) or a Moonshot Open Platform key (which
`kimi_web_search` needs). Keys come from `kimi_coding_api_keys` (round-robin).
Streaming requests are served by **buffering**: the upstream call is
non-streaming and a complete Claude SSE stream is emitted when it completes.

## Configuration

Plugin config lives under `plugins.configs.claude-web-search-router` (key must match the plugin name). Load the shared library via `plugins.path`.

### Recommended: fallback chain (default)

Tries **antigravity → codex → xai → kimi_responses → kimi_coding → kimi_web_search → tavily**; `kimi_responses` needs no plugin config (it reuses host kimi auth), configure `kimi_coding_api_keys`, `kimi_api_keys` and/or `tavily_api_keys` so the in-plugin steps can succeed when built-in providers are missing or unavailable.

```yaml
plugins:
  path:
    - /absolute/path/to/examples/plugin/bin/claude-web-search-router-go.dylib
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: fallback
      antigravity_model: "" # empty: registry lookup, then first supports_web_search
      codex_model: "gpt-5.4-mini"
      xai_model: "grok-4.3"
      # kimi_responses_model: ""  # empty: auto by credential type
      # kimi_coding_api_keys:     # optional: enables the kimi_coding fallback step
      #   - "sk-xxxxxxxx"         # Kimi for Coding API key (api.kimi.ai/coding)
      # kimi_coding_base_url: ""  # empty: https://api.kimi.ai/coding
      # kimi_coding_model: ""     # empty: kimi-for-coding
      kimi_api_keys:
        - "sk-xxxxxxxx"
      kimi_search_level: auto # auto | basic | pro | fetch | model
      tavily_api_keys:
        - "tvly-xxxxxxxx"
        # - "tvly-yyyyyyyy" # optional: round-robin
      require_web_search_only: true
```

Omit `route` to use the same default (`fallback`).

### Minimal fallback (Tavily as last resort only)

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: fallback
      tavily_api_keys:
        - "tvly-xxxxxxxx"
      require_web_search_only: true
```

### Single backend (no fallback)

**Antigravity only:**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: antigravity_google
      antigravity_model: "gemini-3.1-flash-lite"
      require_web_search_only: true
```

**Codex only:**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: codex_web_search
      codex_model: "gpt-5.4-mini"
      require_web_search_only: true
```

**xAI only:**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: xai_web_search
      xai_model: "grok-4.3"
      require_web_search_only: true
```

**Kimi via existing host auth (no plugin keys):**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: kimi_responses # requires a host kimi credential
      # kimi_responses_model: "" # empty: kimi-k2.8-code / kimi-k3 by credential
      require_web_search_only: true
```

**Kimi only (plugin executor):**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: kimi_web_search
      kimi_api_keys:
        - "sk-xxxxxxxx"
      kimi_search_level: auto
      # kimi_base_url: ""           # empty: https://api.moonshot.ai
      # kimi_model: "kimi-k3"       # for kimi_search_level=model
      # kimi_max_results: 10        # 1-20; empty: Claude max_uses, else 5
      # kimi_sites: ["docs.example"]# Pro only, max 5
      # kimi_time_window: {start: "2026-01", end: "2026-09"} # Pro only
      # kimi_pro_escalation: true   # auto: empty Basic results retried via Pro
      require_web_search_only: true
```

**Kimi for Coding only:**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: kimi_coding
      kimi_coding_api_keys:
        - "sk-xxxxxxxx"
      # kimi_coding_base_url: ""  # empty: https://api.kimi.ai/coding
      # kimi_coding_model: ""     # empty: kimi-for-coding
      require_web_search_only: true
```

**Tavily only (plugin executor):**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: tavily
      tavily_api_keys:
        - "tvly-xxxxxxxx"
      require_web_search_only: true
```

**Built-in provider via `default_provider`:**

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: true
      priority: 20
      route: default_provider
      default_provider: claude
      default_provider_model: ""
      require_web_search_only: true
```

### Disable or relax detection

```yaml
plugins:
  configs:
    claude-web-search-router:
      enabled: false # plugin declines; host may use default Claude path

# Or keep enabled but allow mixed tool lists:
    claude-web-search-router:
      enabled: true
      route: fallback
      require_web_search_only: false
```

### Config field reference

| Field | Description |
| ----- | ----------- |
| `enabled` | `false` → `Handled: false` for all web_search matches |
| `priority` | Host plugin order for ModelRouter (higher runs earlier; see main repo plugins docs) |
| `route` | `fallback` (default), `antigravity_google`, `codex_web_search`, `xai_web_search`, `kimi_responses`, `kimi_coding`, `kimi_web_search`, `tavily`, `default_provider` |
| `antigravity_model` | Antigravity execution model; never the client Claude model name |
| `codex_model` | Codex model; empty → `gpt-5.4-mini` |
| `xai_model` | xAI model; empty → `grok-4.3` |
| `default_provider` / `default_provider_model` | Used when `route=default_provider` |
| `tavily_api_keys` | Required for `route=tavily` or fallback last step |
| `kimi_responses_model` | Model for `route=kimi_responses` / the fallback host-auth step; empty → `kimi-k2.8-code` (coding credentials) or `kimi-k3` (platform keys) |
| `kimi_coding_api_keys` | Kimi for Coding API keys (round-robin); required for `route=kimi_coding` or the fallback kimi_coding step |
| `kimi_coding_base_url` | Kimi for Coding base URL; empty → `https://api.kimi.ai/coding` |
| `kimi_coding_model` | Coding model for `route=kimi_coding`; empty → `kimi-for-coding` |
| `kimi_api_keys` | Moonshot Open Platform keys (round-robin); required for `route=kimi_web_search` or the fallback kimi step |
| `kimi_base_url` | Kimi tools API base URL; empty → `https://api.moonshot.ai` |
| `kimi_search_level` | `auto` (default), `basic`, `pro`, `fetch`, `model` — see Kimi notes above |
| `kimi_model` | Responses model for `kimi_search_level=model`; empty → `kimi-k3` |
| `kimi_max_results` | Search result limit 1–20; empty → Claude `max_uses`, else 5 |
| `kimi_sites` | Pro-only site restriction (max 5); Claude `allowed_domains` wins when set |
| `kimi_time_window` | Pro-only `{start, end}` date filter (`YYYY` / `YYYY-MM` / `YYYY-MM-DD`) |
| `kimi_pro_escalation` | `auto` only: retry an empty Basic result set via Pro (default `true`) |
| `require_web_search_only` | `true` matches Claude Code–style exclusive `web_search` tools |

## Build

```bash
make -C examples/plugin bin/claude-web-search-router-go.dylib
```

Use `.so` on Linux and `.dll` on Windows. Point `plugins.path` at the built artifact.
