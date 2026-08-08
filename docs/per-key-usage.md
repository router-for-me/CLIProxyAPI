# Per-key usage accounting and limits

CLIProxyAPI can optionally issue named downstream API keys, persist their usage in SQLite, and enforce model, weekly, and monthly limits. The feature is disabled by default and existing `api-keys` continue to work unchanged.

## Configuration

```yaml
api-key-profiles:
  - id: alice
    name: Alice
    api-key: sk-cpa-use-a-long-random-secret
    allowed-models:
      - "gpt-*"
      - "gemini-3.*"
    weekly:
      requests: 1000
      tokens: 5000000
    monthly:
      requests: 4000
      tokens: 20000000

api-key-usage:
  enabled: true
  database-path: api-key-usage.db
  retention-days: 400
  timezone: UTC
```

A zero request or token limit means unlimited. Model patterns are case-insensitive and support `*`. Periods use calendar weeks starting Monday and calendar months in the configured IANA timezone.

The database path is relative to `config.yaml` unless it is absolute. Mount both the configuration directory and the SQLite database on persistent storage when running in a container.

## Management API

The authenticated Management API exposes:

- `GET/POST /v0/management/api-key-profiles`
- `PUT/DELETE /v0/management/api-key-profiles/:id`
- `PUT /v0/management/api-key-usage-settings`
- `GET /v0/management/api-key-usage-summary?period=week|month`
- `GET /v0/management/api-key-usage-events`

Generated secrets are returned only by the create response. List and update responses expose a SHA-256 fingerprint instead of the plaintext key.

## Limit semantics

Request limits are reserved atomically before a request reaches an upstream provider. Token limits are checked against completed usage before the next request starts, so one in-flight response can exceed the configured token ceiling. This avoids guessing an output size while still blocking subsequent requests for the rest of the period.

Token accounting uses CLIProxyAPI's canonical token breakdown and records input, output, reasoning, cache, and total tokens without storing prompts or responses. API keys are stored in `config.yaml` for authentication; the usage database stores only SHA-256 key identifiers.

For billing-grade cost estimation, long-term dashboards, or external databases, use a dedicated usage service in addition to this built-in operator control.
