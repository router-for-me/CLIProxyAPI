# Claude quota snapshot collector

Run with Node.js and util-linux `flock` available on `PATH`:

```sh
CLIPROXY_MANAGEMENT_KEY_FILE=/path/to/management-key \
CLAUDE_QUOTA_SNAPSHOT=/path/to/claude-quota.json \
node /path/to/tools/claude-quota/snapshot.mjs
```

The collector reads enabled Claude credentials and their access-token SHA-256
fingerprints from `GET /v8/management/quotas/claude/credentials`,
requests OAuth usage, and atomically replaces the snapshot after collection.
Each successful account stores its own `observed_at`, `auth_index`, sanitized
`usage`, `weekly_reset`, `eligible`, `five_hour_reset`, and
`blocked_model_families`. Failed observations preserve that enabled account's
last success without changing its timestamp. Deleted or disabled accounts are
pruned. Changing `auth_index` under an existing ID discards both its observation
and its retry schedule. Legacy entries without a verifiable per-account
timestamp, index and usage are refreshed rather than assigned a new timestamp.

The usage allowlist contains shared and recognized model windows (numeric
utilization, reset timestamp and a fixed restriction marker), expanded limits
(numeric percent, reset, restriction marker and recognized canonical model
family), and numeric extra-usage totals plus `is_enabled`. Unknown exhausted
windows become a synthetic shared restriction, preserving fail-closed behavior
without copying arbitrary upstream field names. Raw bodies, credentials,
arbitrary metadata and original lock-reason strings are never written.

Top-level `observed_at` records collection start after acquiring the lock. Each
account's timestamp records the start of its successful usage request. Completion,
failed retries and skipped runs never make a cached account observation fresh.
A consumer must enforce its own maximum age and require valid cached usage.

Enabled accounts refresh at most once every five minutes. Failures persist in
top-level `retry_state`, keyed by credential ID, with `auth_index`, `failures`,
and `next_attempt_at`. `last_status`, when present, is a numeric upstream HTTP
status; separate numeric `management_status` records the local wrapper's HTTP
status. Unknown status values and error bodies are omitted. Exponential retry
delays start at 60 seconds, add up to 20% positive jitter, and cap at 15 minutes.
Upstream wrapper `header` values honor case-insensitive `Retry-After` delta
seconds or HTTP dates, even when longer than the exponential cap.

An upstream 429 persists top-level `global_retry_at` and stops querying remaining
accounts immediately. Their observations and individual retry schedules survive.
While any applicable deadline is active, no usage request is sent for that account.
The local credential list is still read to prune removed and disabled accounts.
The collector uses the compatible `claude-code/2.1.273` User-Agent for OAuth usage.

## Scheduled services and locking

A timer or scheduled service should invoke the command above directly. Every
invocation must use the same absolute `CLAUDE_QUOTA_SNAPSHOT` path on a local
filesystem with working `flock` semantics. The script's `collectAndPublish`
function holds an exclusive `${CLAUDE_QUOTA_SNAPSHOT}.lock` across the entire
collection and atomic publication. Concurrent runs wait before collecting, so
an older collection cannot overwrite a newer one. Do not schedule calls to
`collectSnapshot` and `publishSnapshot` separately; those low-level exports do
not acquire locks.

The lock uses an open file descriptor inherited by util-linux `flock` and
retained by Node until publication completes. The kernel releases it on normal
exit, failure, or process death, including SIGKILL. No PID reuse checks or stale
lock cleanup are needed. The empty lock file persists: **never delete or rotate
it while collectors may run**, because doing so would allow different processes
to lock different inodes. Missing `flock` or lock acquisition failure aborts the
run without publishing. Previous state is read under the same lock, bounded to
2 MiB and validated before reuse; malformed or oversized state is discarded.
Failure to list credentials preserves the preceding snapshot. Individual usage
failures publish durable retry state even when no usable observations exist;
publication alone does not imply quota recovery. Consumers must reject stale
observations. A scheduler may enforce a run-duration
limit if required; a terminated collector releases its lock automatically.

Shared five-hour, weekly, and OAuth-app exhaustion makes the whole account
ineligible. Legacy model windows and expanded `limits` entries with
`scope.model.id` or `scope.model.display_name` block the recognized model
family (Opus, Sonnet, Fable, Haiku, or Mythos). Exhausted or locked unknown scopes
make the account ineligible, while malformed windows fail that observation. An
inactive window with no reset uses the existing far-future reset sentinel.

## Validation

```sh
node --test tools/claude-quota/snapshot.test.mjs
```

Fixtures contain only synthetic usage windows and model labels; tests use no
live endpoints or credentials. Coverage includes freshness, shared and unknown
scopes, Fable, publication ordering, failure preservation, and lock recovery
after process death.

## Native routing and deployment

Enable the native selector by setting `routing.claude-quota-snapshot` to the
absolute snapshot filename. The ordinary `routing.strategy` remains the fallback
for other credentials. Claude OAuth selections use the earliest weekly reset
among accounts with shared, five-hour, and requested-model quota. Five-hour reset
is the tie-breaker. Inactive windows sort after dated reset windows. The manager
resolves OAuth aliases before checking model-family limits. Plugin schedulers
cannot override this policy when Claude OAuth candidates are present.

New per-account observations have a maximum age of 30 minutes; legacy snapshots
retain the two-minute limit. Missing, malformed or expired cache stays
fail-closed. The authenticated native endpoint
`GET /v8/management/quotas/claude?auth_index=...` returns
`{ "observed_at": "...", "usage": { ... } }` from the validated cache, with
matching credential index and access-token fingerprint and no upstream network request. T3
reads this endpoint instead of polling Anthropic directly. Mixed routes can
still select other providers. Existing auth cooldowns
and request retry exclusions remain enforced. Snapshot refreshes are observations,
not reservations: an upstream rate limit reached between refreshes still uses the
proxy's normal cooldown and credential failover.

The fingerprint changes when secret material is replaced, even if a file path
and `auth_index` stay unchanged. Replacement invalidates cached observations
and per-account retry state. Both routing and cached reads enforce the binding.
Neither endpoint returns a token. The collector uses native
`POST /v8/management/requests/api-call` for usage observations.

Install the collector outside the temporary worktree. `collector.service` and
`collector.timer` are user-systemd templates: replace `@NODE@` with the installed
Node executable and install them as `cliproxy-claude-quota.service` and
`cliproxy-claude-quota.timer`. Copy `snapshot.mjs` to
`~/.local/share/cliproxyapi-quota/`, start the service once, then enable the timer.
The timer may continue invoking it every 30 seconds: the persisted scheduler
skips usage requests before the five-minute refresh or retry deadlines. Such
skips are successful runs, not collection failures. Only fresh available account
observations establish recovery; preserved exhausted or expired cache does not.
Back up the binary and proxy configuration before deploying a native build.
Rollback restores those two files and restarts `cliproxyapi.service`.
