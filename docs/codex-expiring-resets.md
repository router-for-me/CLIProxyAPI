# Automatically redeem expiring Codex resets

This is opt-in **per immutable ChatGPT user/workspace pair**, disabled by default.
It redeems existing banked resets, not OAuth refresh tokens, paid credits, or a
purchase. It never sends inference requests to manufacture eligibility.

The server runs the worker even when the management panel is closed. Standalone
mode and the existing persistent KV store are required (`server.kv-store.disabled:
false`, the default). Back up the KV database with credentials; do not delete or
restore an older reset journal while automation is enabled. Independent instances
with independent KV databases do not coordinate. CLIProxyAPIHome is unsupported.

## Management API

Both routes require normal management authentication:

```text
GET /v8/management/routing/codex-reset-policy?auth_index=<credential-index>
PUT /v8/management/routing/codex-reset-policy?auth_index=<credential-index>
```

Read first, then PUT the returned `account_key` with explicit consent:

```json
{
  "account_key": "codex-reset:<opaque-identity-hash-from-GET>",
  "enabled": true,
  "before_expiry_seconds": 3600
}
```

The lead time accepts 60..86400 seconds. GET also returns `result`, `pending`,
`last_checked_at`, `last_reset_at`, `next_check_at`, and the selected credit and
idempotency IDs. It never returns access tokens. PUT cannot alter the journal;
disabling and re-enabling consent does **not** clear an unresolved redemption.
Disabling stops new claims; an already claimed upstream request may still finish.
Renaming a credential does not lose consent; replacing its user/workspace does
not inherit consent. Missing or inconsistent immutable identities fail closed.
Credentials overriding Authorization, Host or ChatGPT-Account-Id headers are not
eligible for automation, because those overrides could change the debited account.

HTTP 409 requires reloading before retrying a policy change. HTTP 503 means the
persistent store is unavailable; there is no in-memory fallback.

## Redemption and safety

- The worker wakes every minute and probes each opted-in account at most once
  per five minutes, with exponential read-failure backoff capped at one hour.
  A longer upstream `Retry-After` is honored. Within a claimed probe, a read-only
  HTTP 401 tries other enabled aliases of the same user/workspace before backing
  off. Both reads must succeed with one alias; other failures do not switch aliases,
  and consume requests are never retried.
- It reads live usage and reset-credit details, requiring a positive upstream
  `available_count` and a still-active five-hour/weekly primary/secondary usage
  window with **less than 80% remaining**. Optional applicability counts, when
  provided, must also be positive; explicit denials always win. Unknown window
  types and missing/malformed eligibility never spend. This also supports the
  official Codex response schema, which has no applicability field.
- It selects the earliest-expiring available `codex_rate_limits` credit within
  the requested lead time and sends its explicit `credit_id`. Expired credits,
  absent IDs and a known previously redeemed credit are skipped. Reads older than
  one minute are not used for redemption. Timing is best effort, not a guarantee
  that every credit can be used before expiry; the provider is authoritative.
- Atomic persistent claims serialize probes/redemptions. The idempotency ID and
  selected credit are saved **before** POST. Only `reset` or `already_redeemed`
  confirms success, including `windows_reset: 0`. Only then is the gateway's
  pre-reset cooldown cleared for current credentials sharing that user/workspace,
  including prefixed and disabled aliases. A persisted nanosecond boundary taken
  before POST protects newer runtime observations, including during cleanup retries.
  Credentials or models updated at/after that boundary are conservatively left alone;
  older journals use the recorded reset's whole-second boundary. The manager checks
  registration, credential version and generation under its mutation gate before
  clearing, so replacement or intervening activity cannot clear a stale observation.
  Cleanup remains `cooldown_pending` after storage failures; retries never spend.
- `nothing_to_reset` and `no_credit` do not clear gateway cooldowns. Lost responses,
  non-success HTTP responses, malformed JSON and unknown outcomes leave `pending:
  true` and stop automatic redemption for that account, including after restart.
  There is deliberately no automatic POST retry: upstream idempotency retention
  is undocumented. Do not erase that record or generate another request to guess
  the outcome. Disable automation and reconcile the specific credit/request with
  the upstream account history/support before any operator-led journal recovery.

Existing manual resets remain available. Avoid manual redemption while an
automatic result is unresolved. This worker cannot coordinate independent tools
or provide exactly-once guarantees after its persistent journal is lost.

The request's `credit_id`, `redeem_request_id` and four known outcomes follow the
[official Codex contract tests](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/app-server/tests/suite/v2/rate_limit_reset_credits.rs).
Eligibility follows [OpenAI's reset guidance](https://help.openai.com/en/articles/20001498-how-resets-work-in-codex).
No real resets are consumed by the automated tests.
