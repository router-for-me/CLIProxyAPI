# Claude quota snapshot collector

Run with Node.js and util-linux `flock` available on `PATH`:

```sh
CLIPROXY_MANAGEMENT_KEY_FILE=/path/to/management-key \
CLAUDE_QUOTA_SNAPSHOT=/path/to/claude-quota.json \
node /path/to/tools/claude-quota/snapshot.mjs
```

The collector reads enabled Claude credentials through the local management API,
requests OAuth usage, and atomically replaces the snapshot after collection.
Accounts with failed or invalid observations are excluded until a later refresh;
healthy accounts remain usable. If all queries fail, publication fails and
preserves the last snapshot. Tokens and upstream response bodies
are never written to the snapshot. Output retains `observed_at`, `accounts`,
`weekly_reset`, `eligible`, and `five_hour_reset`, and includes each account's
`blocked_model_families`.

`observed_at` records collection start, before the first network request and
after acquiring the publication lock. Long collections therefore consume the
snapshot's freshness budget; completion does not make old observations fresh.
A consumer must enforce its own maximum age.

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
run without publishing. Failed collection preserves the preceding snapshot;
consumers must reject it once stale. A scheduler may enforce a run-duration
limit if required; a terminated collector releases its lock automatically.

Shared five-hour, weekly, and OAuth-app exhaustion makes the whole account
ineligible. Legacy model windows and expanded `limits` entries with
`scope.model.id` or `scope.model.display_name` block the recognized model
family (Opus, Sonnet, Fable, Haiku, or Mythos). Exhausted or locked unknown scopes
make the account ineligible, while malformed windows abort publication. An
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

Missing, malformed, or over-two-minute-old quota data prevents Claude OAuth
selection. Mixed routes can still select other providers. Existing auth cooldowns
and request retry exclusions remain enforced. Snapshot refreshes are observations,
not reservations: an upstream rate limit reached between refreshes still uses the
proxy's normal cooldown and credential failover.

Install the collector outside the temporary worktree. `collector.service` and
`collector.timer` are user-systemd templates: replace `@NODE@` with the installed
Node executable and install them as `cliproxy-claude-quota.service` and
`cliproxy-claude-quota.timer`. Copy `snapshot.mjs` to
`~/.local/share/cliproxyapi-quota/`, start the service once, then enable the timer.
It refreshes 30 seconds after each run; failures preserve the previous snapshot.
Back up the binary and proxy configuration before deploying a native build.
Rollback restores those two files and restarts `cliproxyapi.service`.
