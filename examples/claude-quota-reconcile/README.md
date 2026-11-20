# Opt-in Claude quota reconciliation

A reset on the provider website can restore allowance before the deadline saved
by the proxy. This example checks only quota-blocked Claude OAuth credentials,
every five minutes by default. It generates **no inference** and does not redeem
reset credits, enable paid usage, refresh tokens, or change models.

Requires a backend containing `POST /v8/management/routing/cooldown/reconcile`.
An older backend is refused; there is **no fallback** to unconditional cooldown
reset. Updating the script alone does not activate reconciliation.

```sh
python3 examples/claude-quota-reconcile/reconcile.py \
  --origin http://127.0.0.1:8317 \
  --key-file /private/existing-management-key \
  --lock-file /private/claude-quota-reconcile.lock
```

Use an existing management key in an owner-only regular file, never in argv, a
URL or source control. Run one instance under the operator's existing process
supervisor; a nonblocking file lock prevents duplicate workers. Omit scheduling
and add `--once` for one bounded pass. Intervals below 300 seconds are refused.
There is no automatic installation or service restart.
Failures back off up to an hour, or longer when the provider returns an explicit
Retry-After. Healthy credentials are not polled.

The management endpoint fetches the fixed non-spending Claude usage URL with the
selected credential's **existing** access token and proxy configuration. It:

- Checks both the aggregate limit signal and every reported usage window/limit.
  `at_limit=false` alone is insufficient; a 100% weekly or scoped window blocks
  recovery.
- Refuses missing/unknown schema, provider cooldowns, HTTP/auth failures,
  Retry-After, cached responses, missing/expired access tokens, and disabled
  accounts. Unsupported provider schema may require a reviewed parser update.
- Excludes non-quota/mixed restrictions, including independent failures hidden
  beneath credential-wide quota propagation.
- Fetches without holding routing/mutation locks, then compares the complete
  snapshot under the same mutation lock as reset. New failures (even with the
  same deadline), refreshes, replacements, removals and operator changes abort.
  Evidence older than one minute cannot clear a deadline.
- Uses existing scheduler/registry and cooldown persistence mechanisms. No
  account is deleted and no new refresh owner is created.

The endpoint is protected by the existing v8 management authentication. Responses
and example logs omit provider bodies, tokens, emails and raw exception text.
`reconciled` means a verified obsolete **local** quota block was cleared; it does
not promise provider availability or change subscription limits.

Tests:

```sh
go test ./sdk/cliproxy/auth ./internal/api/handlers/management -run 'Reconcile|ClaudeUsageProvesRecovery|ClaudeQuotaRecovery'
python3 -m unittest discover -s examples/claude-quota-reconcile -v
```
