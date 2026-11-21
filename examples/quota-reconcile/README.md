# Opt-in provider-neutral quota reconciliation

A reset on a provider website can restore allowance before the proxy's cached
quota deadline. The worker examines quota-blocked credentials **across all
providers**, including model-only blocks on otherwise usable accounts, every
five minutes by default. It generates no inference and does not redeem reset
credits, enable paid usage, refresh tokens, or change models.

## Coverage, not assumptions

The core atomic reconciliation and worker are provider-neutral. Native adapters
are deliberately explicit about what their non-spending evidence can establish:

| Provider | Evidence required before local quota reset |
| --- | --- |
| Claude OAuth | Usage GET: aggregate recovery plus every known window and scoped limit below threshold |
| Codex OAuth | Account-bound wham usage GET: primary, secondary, additional model limits and code-review limits recovered; credit balance alone never qualifies |
| Antigravity (including Gemini) OAuth | Quota-summary POST: every group/bucket positive, plus model-discovery POST proving positive quota for each exact blocked model; no aliases or disabled model substitution |
| Kimi OAuth | Same-domain usages GET: every known usage counter/window recovered |
| xAI/Grok | Explicitly unsupported: billing balance and the panel's paid inference health fallback do not establish rate-limit recovery without inference |
| Gemini API keys, Gemini CLI, other/plugin providers | Explicitly unsupported until a reviewed complete non-inference recovery adapter exists; display-only plugin quota buckets are not attestations |

Current adapters require an existing access token with known, future expiry.
Missing coverage, unknown schema, expired grants, auth/billing restrictions,
HTTP rejection, missing models or ambiguous evidence leave cooldowns unchanged.
Account-wide reset is allowed only if **all** retained restrictions are quota
restrictions and the adapter covers them. Healthy accounts are not probed.

## Activation

Requires a backend containing GET and POST
`/v8/management/routing/cooldown/reconcile`. The capability GET runs independently
of inventory; old backends are rejected even with zero blocked credentials.
Home-authoritative deployments are explicitly unsupported, not silently skipped.
There is no fallback to the unconditional reset endpoint. A removed credential
is reported separately and does not stop checks for other accounts.

```sh
python3 examples/quota-reconcile/reconcile.py \
  --origin http://127.0.0.1:8317 \
  --key-file /private/existing-management-key \
  --lock-file /private/quota-reconcile.lock
```

Use an existing management key in an owner-only regular file, never in argv, a
URL or source control. Run one instance under the operator's process supervisor;
a nonblocking lock prevents duplicate workers. Add `--once` for one pass.
Intervals below 300 seconds are refused. Failures back off up to an hour, or
longer for explicit Retry-After. The management connection has a connect-only
bound, not a read timeout after connection. No automatic service installation or
restart is included. Updating the worker alone cannot activate backend support.

## Safety and implementation

- Provider adapters construct fixed non-spending routes; no caller-supplied URLs.
  Each uses its native transport/proxy policy. Claude OAuth uses its existing
  control-plane TLS/header transport; Antigravity retains HTTP/1.1 without ALPN.
- No lock is held over provider I/O. The complete routing/credential snapshot is
  compared under the existing reset mutation lock. Concurrent failures (even
  same-deadline), refreshes, replacements, disables, cancellation, Home mode
  changes, or evidence older than one minute abort.
- All known quota windows must agree. For example, Claude's reset-credit
  `at_limit=false` can coexist with weekly utilization of 100 and must not clear
  a cooldown. Unknown restriction fields fail closed rather than looking healthy.
- Non-quota restrictions, including failures hidden beneath propagated quotas,
  remain intact. Existing scheduler/registry/cooldown persistence is reused.
- Existing v8 management authentication protects both endpoints. Logs omit
  tokens, provider response bodies, emails and raw transport errors. Unsupported
  coverage is counted explicitly. No new credential or refresh owner is created.

`reconciled` means a verified obsolete **local** quota block was cleared; it is
not a guarantee of future availability or a change to subscription limits.

## Tests and evidence

```sh
go test -race ./sdk/cliproxy/auth ./internal/api/handlers/management -run 'Reconcile|UsageProvesRecovery|QuotaRecovery'
python3 -m unittest discover -s examples/quota-reconcile -v
```

Tests use synthetic fixtures and loopback, not production requests. Native route
and schema references are the stock Management Center provider data/constants at
commit `e5eb4fe63ad8a2a20d13022e13e6dc4d386ac07b`, the existing Codex quota parser,
and the Antigravity SDK discovery transport. Runtime provider-schema drift must
be reviewed, not converted into permission to clear deadlines.
