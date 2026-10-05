# Direct harness cutover

Harnesses call this proxy directly and LiteLLM only receives accounting events afterwards. Nothing here changes production, the cutover itself stays a separate approved step.

## Example configuration

```yaml
observability:
  accounting-outbox:
    enabled: true
    data-path: /var/lib/ai-proxy/accounting   # persistent volume, one process per path
    litellm:
      enabled: true
      url: https://litellm.internal            # stock LiteLLM
      admin-key-env: LITELLM_ACCOUNTING_ADMIN_KEY
      clients:
        # key: sha256("client\0" + the key the harness sends to this proxy), hex
        "<client-key-id>":
          key-hash: "<sha256 of an existing LiteLLM virtual key>"
          user-id: harness-user
          team-id: harness-team
    pricing:
      overrides: []                            # or litellm-catalog-path
```

Compute the client key id with `printf 'client\0%s' "$KEY" | sha256sum`.

## Migration checklist

- Persistent storage: mount a volume at `data-path`, run a single replica per path, size `max-disk-mb` for the expected backlog.
- Identity mapping: create a LiteLLM user, team and virtual key per harness client, then map each client key id as above. Unmapped, unpriced or partial events stay local and retry hourly.
- LiteLLM: stock release with spend logs enabled and PostgreSQL persistence, admin key injected only through `admin-key-env`.
- Pricing: every exported model needs an override, alias or catalog entry, otherwise its events stay unpriced and local.
- Health: watch the 30 second accounting log line (backlog, oldest pending age, dropped events, persistence failures) and `Service.AccountingExporterStatus`. Alert on a growing backlog and any dropped events.
- Pre-cutover: run `go test ./test/...` and, on a host with the clients installed, `AI_PROXY_LIVE_HARNESS=1 go test ./test -run TestLiveHarnessCLIs`.
- Cutover: point one harness at the proxy, confirm spend rows appear for its identity, then move the rest.
- Rollback: set `litellm.enabled: false` (or `accounting-outbox.enabled: false`) and restart. Inference is unaffected in every accounting mode, pending events stay in the outbox and resume when export is re-enabled.

## What was verified

`go test ./test -run 'TestAccounting|TestCancellation|TestSlowOrFailed|TestQuota'` drives the real HTTP routes against fixture providers.

- Contracts: OpenAI Responses (Codex, SSE and downstream WebSocket with an upstream WebSocket), OpenAI Chat Completions (Grok) and Anthropic Messages (Claude), each with a tool call, streaming and non-streaming.
- Isolation: with accounting disabled, enabled, and exporting to a LiteLLM that is offline, stalled or returning 500, the client sees identical status, headers, body and stream framing, and the provider receives identical requests. Cancelling a stream reaches the provider in every mode with no retry.
- Slow or failed exporter: 300 requests at concurrency 8 against a local fixture, measured while the exporter was already stuck. p50 was about 0.7 ms with accounting off and 0.85 ms with it on, and a stalled, offline or failing exporter was no slower than plain accounting. Goroutines and heap stayed bounded, no event was dropped, and a stalled receiver held exactly one in-flight export request.
- Quota: Codex `usage_limit_reached`, Claude `rate_limit_error` and chat `insufficient_quota` keep their error type, the Codex `resets_in_seconds` is untouched, nothing is invented when the provider gave no reset, the following request is answered from the cooldown with a `Retry-After` matching the provider reset, and one probe succeeds after the cooldown. A rejected attempt is accounted as failed with no tokens and no price. Accounting costs are never read as quota.
- Reconciliation: one history with a failover (two attempts under one trace), an interrupted stream, a restart on the same data path, a partial batch failure and a lost response. Success events were exported with 1 delivery attempt, or 3 for the record that failed, lost its response and then succeeded. Failure events were claimed once and rejected locally. Spend rows matched the outbox exactly (3 rows, 36 input and 15 output tokens, 0.000096 USD).
- Live: Claude Code 2.1.289, Codex 0.160.0 and Grok 1.0.46 each completed a turn through the proxy against a fixture provider (opt-in test above).

## Findings

- Delivery to stock LiteLLM is at-least-once. The replayed record kept one spend row but counted twice in the identity aggregates (12 tokens, 0.000032 USD over-counted in the scenario). Compare aggregates with the unique spend rows to detect it. Failed and interrupted attempts are never exported, so LiteLLM does not see them.
- Cancelling a Codex SSE stream publishes no usage record, so that attempt is not accounted (`codex_executor_stream.go`, the `ctx.Err() != nil` return). Inference is unaffected. Chat and Claude record cancelled attempts as failed.
- The first 429 for a request passes the provider body through but does not forward the provider's `Retry-After` header. Only the cooldown answer that follows carries one.
- Existing cooldown rules floor a provider reset hint at 10 s and add 1 to 30 s of jitter for Claude. A provider that resets in under 10 s keeps getting 429 from the proxy for up to 10 s (31 s for Claude). Real quota resets are far longer and are honoured.
- No contract was found that stops a harness from waking after a quota reset, so no follow-up issue was opened.

## Not verified

Real provider quota behaviour, a deployed LiteLLM (PostgreSQL commit, dashboards, budget counters), a crash while an export is in flight (such events stay claimed until pruned), and harness behaviour against real providers. The live checks above use fixture providers.
