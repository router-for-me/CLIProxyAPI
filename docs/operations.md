# Operations (Security + Observability)

This proxy borrows operational patterns from production-grade systems: environment-based secret loading, safe credential storage, guardrails (rate limits / cooldowns), response caching, and Prometheus metrics.

## Environment-Sourced Secrets (`os.environ/`)

Any string value in `config.yaml` can be set from an environment variable by using the prefix:

```yaml
some-key: os.environ/MY_ENV_VAR
```

The config loader resolves these references after YAML unmarshal (works for nested structs, slices, and maps).

If the env var is missing, startup fails (unless running in optional/cloud-deploy mode).

- Keeps secrets out of `config.yaml` by referring to env vars instead of hard-coding secrets.
- Makes it easier to run the same config across machines/environments.

### Safety note (no “secret persistence”)
When `os.environ/` references are resolved, config normalization steps that would normally write back to disk are skipped to avoid accidentally writing the resolved secret into `config.yaml`.

## Strict Config Parsing (Reject Unknown YAML Fields)

Strongly typed proxies typically surface unknown fields quickly. In Go/YAML it’s easy to silently ignore typos, so CLIProxyAPI supports strict parsing:

```yaml
strict-config: true
```

You can also force strict parsing via env:
- `CLIPROXY_STRICT_CONFIG=true`

## Encrypted Auth Storage (auth-dir)

Auth JSON files under `auth-dir` can be encrypted-at-rest and are always written using:
- file locking
- atomic replace
- `0600` permissions

Config:

```yaml
auth-storage:
  encrypt: true
  encryption-key: os.environ/CLIPROXY_AUTH_ENCRYPTION_KEY
  allow-plaintext-fallback: true
```

Also supported via env: `CLIPROXY_AUTH_ENCRYPTION_KEY` (or legacy `CLI_PROXY_API_AUTH_ENCRYPTION_KEY`).

### What gets encrypted
- Files under `auth-dir` (typically `*.json`) created by login flows or uploaded via management endpoints.
- The stored format is an **envelope JSON** (AES-256-GCM). The plaintext JSON is only recovered in-memory.

### Migration behavior
If encryption is enabled and `allow-plaintext-fallback: true`, legacy plaintext auth files are still readable and will be best-effort rewritten into the encrypted envelope format.

### Remote stores (Postgres/Object store)
If you mirror auth files to Postgres/S3-backed stores, the raw bytes are stored as-is. When encryption is enabled, those remote payloads remain encrypted envelopes.

## Prometheus Metrics

Enable the metrics endpoint:

```yaml
metrics:
  enable: true
  endpoint: "/metrics"
  require-auth: false
```

Metrics include request counts/latency, token totals, cache hits/misses, rate-limit rejections, and cooldown counters.

Key metric names:
- `cliproxy_requests_total`
- `cliproxy_request_duration_ms`
- `cliproxy_tokens_input_total` / `cliproxy_tokens_output_total`
- `cliproxy_cache_hits_total` / `cliproxy_cache_misses_total`
- `cliproxy_ratelimit_rejections_total`
- `cliproxy_cooldowns_triggered_total`

## Response Cache

Enable in-memory response caching:

```yaml
cache:
  enable: true
  max-size: 1000
  ttl: 300
```

### What is cached
- Only **non-streaming** requests.
- Only JSON responses with **2xx** status.
- Applies to:
  - `POST /v1/chat/completions`
  - `POST /v1/completions`
  - `POST /v1/responses` (OpenAI Responses API)
  - `POST /v1/messages`

### Cache key
Cache keys include the authenticated `apiKey` + method + path + query + request body, so different users/inputs do not collide.

### Response header
Cached requests return `X-CLIProxy-Cache: HIT` (and uncached attempts return `X-CLIProxy-Cache: MISS`).

## Rate Limits

Configure concurrency + RPM limits:

```yaml
rate-limits:
  enable: true
  max-parallel-requests: 100
  max-per-key: 10
  max-rpm: 60
  max-tpm: 120000
```

Rate-limited requests return HTTP `429` with `{"error":"rate_limited", ...}` and increment `cliproxy_ratelimit_rejections_total`.

### Token-Per-Minute (TPM)

TPM limits protect upstream quotas from a small number of very large requests.

Notes:
- TPM is tracked per authenticated principal (`cfg:<sha256>` for static `api-keys`, `vk:<sha256>` for virtual keys).
- Tokens are recorded after request completion (usage plugin), so enforcement is best-effort and may allow brief bursts.

## Request/Response Size Limits

CLIProxyAPI supports request/response size caps:

```yaml
limits:
  max-request-size-mb: 10
  max-response-size-mb: 50
```

Behavior:
- Request bodies above the cap return HTTP `413`.
- When `max-response-size-mb` is set, non-streaming upstream responses larger than the cap return HTTP `502`.

## Cooldown Override

Optionally apply a fixed cooldown window for specific HTTP status codes:

```yaml
cooldown:
  enable: true
  duration: 60
  trigger-on: [429, 500, 502, 503, 504]
```

This is a simple “guardrail cooldown” that prevents immediate re-selection of a credential after repeated error codes. If the upstream returns `Retry-After`, that value is honored/extended.

Note: quota backoff for 429 is still controlled separately via `disable-cooling`.

## Fallback Chains (Cross-Provider Failover)

Fallback chains provide model/provider failover on transient failures (network, 408, 429, 5xx):

```yaml
fallback-chains:
  enable: true
  chains:
    - primary-model: "gpt-4o"
      fallbacks:
        - model: "claude-3-5-sonnet-20241022"
          provider: "claude"
```

When a fallback succeeds, responses include `X-CLIProxy-Fallback` headers for debugging.

## Retry Policy (Exponential Backoff)

`retry-policy` adds exponential backoff retries for transient failures (network, 408, 5xx):

```yaml
retry-policy:
  enable: true
  max-retries: 3
  initial-delay-ms: 1000
  max-delay-ms: 30000
  multiplier: 2.0
  jitter: 0.1
```

Notes:
- 429 is intentionally not retried via backoff; prefer cooldown/Retry-After.
- This is additive to the existing cooldown-based `request-retry` behavior.
- For OpenAI-compatible upstreams, you can pass `Idempotency-Key` to reduce duplicate charges when retries occur.

## Routing Strategy

When multiple credentials match, you can choose a selection strategy:

```yaml
routing:
  strategy: "fill-first"    # fill-first, round-robin (default), random, least-busy, lowest-latency
  health-aware: true        # Filter unhealthy credentials (COOLDOWN, ERROR)
  prefer-healthy: true      # Prefer HEALTHY over DEGRADED when health-aware
  fill-first-max-inflight-per-auth: 4  # 0 = unlimited
  fill-first-spillover: "next-auth"    # next-auth (default), least-busy
```

Notes:
- `least-busy` uses in-flight request counts; `lowest-latency` requires `health-tracking.enable: true`.
- `fill-first` drains one account to rate limit/cooldown, then moves to the next to stagger rolling windows; spillover prevents overload under bursty concurrency.
- `next-auth` preserves deterministic “drain first”; `least-busy` maximizes throughput.

### Fill-first spillover (recommended for “many creds”)

`fill-first` intentionally drains one account to its rate limit/cooldown, then moves to the next to keep throughput going by staggering rolling windows across accounts. With many concurrent terminals it can also overload a single credential, leading to avoidable `429` errors. Use `fill-first-max-inflight-per-auth` and `fill-first-spillover` to keep the intent while enabling safe spillover.

- When the preferred credential is at capacity (`max-inflight`), selection spills over to another credential instead of overloading one.
- `next-auth` preserves deterministic “drain first”; `least-busy` maximizes throughput under bursty load.

Health-aware filtering uses `health-aware` and `prefer-healthy` (requires `health-tracking.enable: true`).

## Streaming (Keep-Alives + Safe Bootstrap Retries)

Streaming failures are only safe to “retry/fail over” **before any bytes are written** to the client. After that, a retry would duplicate/diverge output.

```yaml
streaming:
  keepalive-seconds: 15    # SSE heartbeats (: keep-alive\n\n); <= 0 disables
  bootstrap-retries: 2     # retries allowed before first byte; 0 disables
```

Notes:
- Keep-alives reduce idle timeouts (Cloudflare/Nginx/proxies) during long pauses between chunks.
- Bootstrap retries/fallbacks only run if the stream fails before producing any payload (safe failover).

## “10 Terminals / Many Subscriptions” Recommended Defaults

This configuration biases toward **predictable** routing (burn one account first) while reducing avoidable interruptions under bursty concurrency. Start with the routing block above and add:

```yaml
health-tracking:
  enable: true

cooldown:
  enable: true
  duration: 60
  trigger-on: [429, 500, 502, 503, 504]

retry-policy:
  enable: true
  max-retries: 3
  initial-delay-ms: 1000
  max-delay-ms: 30000
  multiplier: 2.0
  jitter: 0.1

streaming:
  keepalive-seconds: 15
  bootstrap-retries: 2
```

## Request Body Guardrails (Client-Side Upstream Targets)

To prevent redirect attacks, CLIProxyAPI blocks `api_base` / `base_url` in request bodies by default:

```yaml
security:
  allow-client-side-credentials: false
```

When disabled (default), requests containing `api_base` or `base_url` are rejected with HTTP `400`.

## Virtual Keys (Managed Client Keys)

This pattern generates per-user/team keys without editing `config.yaml`.

Enable:

```yaml
virtual-keys:
  enable: true
```

Management endpoints (require management key):
- `GET /v0/management/virtual-keys`
- `POST /v0/management/virtual-keys` (returns plaintext key once)
- `DELETE /v0/management/virtual-keys/:selector`
- `GET /v0/management/virtual-keys/:selector/budget`

Policy enforcement (automatic for `vk:*` principals):
- Budget caps (tokens and/or USD) with fixed windows
- Model allowlists (wildcards)
- Per-key model aliases (`model_aliases`) applied by rewriting the request JSON `model`

## Pricing (Spend Tracking)

Virtual-key cost budgets require pricing rules:

```yaml
pricing:
  enable: true
  models:
    - match: "gpt-4o*"
      input-per-1k: 5.0
      output-per-1k: 15.0
```

When `pricing.enable: false`, virtual keys can still enforce token budgets, but cost budgets will return `cost_unknown`.

## Pass-Through Endpoints

Pass-through routes forward requests to an upstream base URL without writing a full translator.

```yaml
pass-through:
  enable: true
  endpoints:
    - path: "/v1/rerank"
      method: "POST"
      base-url: "https://api.openai.com"
      timeout: 60
      headers:
        Authorization: "Bearer os.environ/OPENAI_API_KEY"
```

Security behavior:
- Hop-by-hop headers are stripped.
- Proxy auth headers (`Authorization`, `X-Goog-Api-Key`, `X-Api-Key`) are stripped and must be provided via `headers`.
- If the proxy key was provided via query (`?key=` / `?auth_token=`), that parameter is removed from the forwarded query string.

## Health Endpoints + Background Probes

Endpoints:
- `GET /health/liveness` (fast, no upstream calls)
- `GET /health/readiness` (feature status + optional probe summary)
- `GET /health` (alias for readiness)

Optional background probes:

```yaml
health:
  background-checks:
    enable: true
    interval: 300
```

Probes are lightweight TCP connectivity checks to configured provider base URLs (no auth, no quota usage).

## Management API Hardening

- Auth file downloads are blocked for non-local clients by default.
- To allow it, set:
  ```yaml
  remote-management:
    allow-auth-file-download: true
  ```

### Auth file download behavior
- By default, downloads return the stored bytes (encrypted envelope if encryption is enabled).
- `GET /v0/management/auth-files/download?name=...&decrypt=1` is **localhost-only** and returns plaintext JSON (requires encryption key when files are encrypted).

New endpoints:
- `GET /v0/management/auth-files/errors`
- `GET /v0/management/auth-providers`
- `GET /v0/management/virtual-keys` (+ create/revoke/budget)

### Config Redaction

`GET /v0/management/config` returns a redacted config view (API keys/tokens masked). Use `GET /v0/management/config.yaml` to fetch the raw file (preserves comments).
