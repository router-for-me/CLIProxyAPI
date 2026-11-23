# Quota-aware routing fork

This fork preserves upstream `main` and adds `routing.strategy: soonest-reset-first`.
Set it through `/v8/management/config/routing/strategy` or the YAML config. Keep
`routing.session-affinity: true` to retain usable session bindings. The existing
TTL, subagent inheritance, rebind policy, explicit auth pinning and WebSocket
continuation behavior still apply. A new ranking does not move a usable session.

## Selection

The manager first checks provider/model support, disabled state, token validity,
existing cooldowns and request restrictions. Explicit higher priority still wins
among usable accounts. Within that tier, known usable subscription quota sorts
before unknown quota, then by earliest reset and auth ID. WebSocket requests
retain upstream's preference for WebSocket-capable credentials within the tier.

Consumption ordering uses the **longest applicable subscription window**. For
Codex this means the main `rate_limit.primary_window` and `secondary_window`,
using `limit_window_seconds` rather than assuming primary always means five hours.
A weekly window drives ordering when present. An exhausted applicable short or
weekly window blocks use until reset. Codex `allowed: false` or `limit_reached:
true` uses the individually exhausted windows for the recovery deadline. When
none of the counters identifies the exhausted window, it conservatively blocks
the group's returned windows until their latest reset.
Additional pools apply only when their normalized `limit_name` exactly matches
the requested upstream model; a matched named pool replaces the default pool. Code-review limits and credits never rank ordinary
inference. Claude uses `five_hour`, `seven_day`, and applicable `seven_day_opus`
or `seven_day_sonnet` windows. Fable requests also check the `weekly_scoped`
Fable entry in `limits`, with the active entry preferred when duplicates exist.
Older non-dollar `iguana_necktie` counters are supported when no scoped Fable
entry is present. Fable quota supplements the shared weekly and five-hour limits;
exhausting any applicable limit skips the account. It does not affect requests
for other model families. Among usable accounts, the earliest applicable weekly
reset drives ordering, normally the same boundary for shared and Fable quota.
Model aliases resolve before the Fable family check. Reset grants are never
spent automatically; subsequent successful usage probes observe restored quota.
A Claude window with null reset and zero or null
utilization has not started and is omitted. A null optional Codex named pool is
also omitted. Other Claude usage groups are not interpreted.

Antigravity meters model families in pools, read from `retrieveUserQuotaSummary`.
The bucket ID names the pool: `3p-*` is shared by Claude and GPT models,
`gemini-5h`/`gemini-weekly` by every `gemini-*` model, and a separate
`gemini-<name>-*` bucket (for example Argon) by models with that name, which then
ignore the shared Gemini buckets. Unknown pools are ignored, and models outside
every pool have unknown quota. A bucket's remaining fraction becomes used percent;
none left blocks its pool until reset. Disabled buckets and full buckets without
a reset are omitted. A missing fraction, unknown window or invalid reset makes the
snapshot malformed.

Only finite, nonnegative numeric utilization, valid reset timestamps and bounded
window lengths are accepted. Missing, malformed, future-dated observations,
observations older than 15 minutes, elapsed resets and unsupported providers fall
back to deterministic fill-first. An elapsed reset does not establish renewed
quota. A failed refresh retains the previous successful observation until stale.
A malformed partial response cannot replace the last complete observation.
A valid all-idle Claude response clears the previous snapshot and falls back to
fill-first without inventing a reset or retaining an exhausted window.
Unsupported provider credentials and API keys remain eligible through the same
fallback. SIWC inference uses a different credential partition, so ChatGPT usage
is not applied to non-Codex SIWC traffic.

## Background refresh

A service-owned worker runs without the dashboard. It probes enabled OAuth
credentials at `https://chatgpt.com/backend-api/wham/usage` for Codex and
`https://api.anthropic.com/api/oauth/usage` for Claude. Original ChatGPT OAuth
credentials authenticate Codex probes, including accounts with nested SIWC.
Antigravity probes `POST /v1internal:retrieveUserQuotaSummary` with the
credential's `project_id` on the host its inference uses (`base_url`, otherwise
the daily host); other hosts can meter the same account differently. Its executor
refreshes expiring tokens inside a request, so a token within five minutes of
expiry, or without a known expiry, is reported as `token_expiring` and not probed.
Existing executor transports supply headers, proxies and TLS behavior.

Every account is due at startup and after five minutes. A 30-second discovery
sweep finds added credentials; oldest attempts go first so never-observed accounts
are not starved by already-ranked accounts. At most four probes run concurrently.
Slow probes can delay refresh; no network deadlines are added. Service shutdown
cancels the worker and its requests. No inference request or status read polls
quota. Probes never acquire or refresh tokens, update auth files, clear cooldowns
or change session bindings. Credential replacement invalidates cached quota, and
an old credential's result cannot become the replacement's observation.

## Authenticated status contract

`GET /v8/management/routing/status?provider=codex&model=gpt-5.4`
uses the existing management key and access restrictions. There is no v0 alias.
The optional `transport` is `http` by default or `websocket`. Codex status predicts
a Codex client's original OAuth partition and identifies this as
`request_profile: "codex"`. The response includes:

```json
{
  "schema_version": 1,
  "strategy": "soonest-reset-first",
  "provider": "codex",
  "model": "gpt-5.4",
  "transport": "http",
  "request_profile": "codex",
  "generated_at": "2026-11-09T04:00:00Z",
  "prediction_scope": "new_session",
  "next_auth_index": "opaque-credential-index",
  "fallback_reason": "",
  "session_affinity": true,
  "refresh_interval_seconds": 300,
  "max_age_seconds": 900,
  "accounts": [
    {
      "auth_index": "opaque-credential-index",
      "priority": 0,
      "eligible": true,
      "skipped_reason": "",
      "rank": 1,
      "quota_state": "fresh",
      "observed_at": "2026-11-09T03:59:00Z",
      "age_seconds": 60,
      "window": {
        "key": "secondary_window",
        "resets_at": "2026-11-10T00:00:00Z",
        "used_percent": 45
      }
    }
  ]
}
```

Indices match the credentials API. Eligible account ranks include lower priority
fallbacks. Skipped accounts have rank zero. Reasons include `disabled`,
`model_unsupported`, `unavailable`, `cooldown` and `quota_exhausted`. Quota states
are `fresh`, `missing`, `stale`, `malformed`, `expired` and `unsupported`.
`refresh_error`, when present, is a fixed diagnostic code, never an upstream body.
`fallback_reason` is `quota_unavailable` when the predicted account has unknown
quota, or `no_eligible_accounts` when the pool cannot serve the request.

Providers with model pools list them in `pools` (`key` and `label`); Antigravity
has `gemini` and `claude-gpt`. Without a model their status ranks one pool and
names it in `pool`: the optional `pool` parameter, or the first listed pool.
A `pool` that the provider does not list, or one sent with `model`, returns 400.
Other providers omit both fields.

Other selectors return `strategy_not_predictable` without a next prediction.
Home or a plugin scheduler reports `home_authoritative` or `plugin_scheduler`
and suppresses prediction because it can override local ordering. This is a
snapshot for a new unbound session, not a report of an actual bound session or
request with special credential restrictions. No session identifier, token,
filename, email address, raw error or upstream body appears in the response.
The dashboard must handle 404 from stock/older backends and must not label a local
sort as the backend's actual selection.

## Offline candidate validation and restart safety

Stock upstream has no equivalent checker. This fork supports both Go flag forms:

```sh
./cli-proxy-api --capabilities
./cli-proxy-api -check-config -config /path/to/candidate.yaml
```

Capabilities are one JSON line with `schema_version: 1`, `config_check: true`,
`routing_status_path`, `routing_strategies`, `version`, `commit` and
`safe_drain: true`, `drain_schema_version: 1` and
`drain_status_path: /v8/management/server/drain`. The checker parses v8 and legacy configs, rejects unknown
routing strategies and returns `{"valid":true}` or a nonzero exit code. It never
starts a listener, bootstraps plugins, loads `.env`, reads auth files, acquires or
refreshes credentials, or rewrites config. Errors omit config contents.
It validates local parsing and supported strategy, not upstream availability.

Stage and checksum the candidate first, verify these capabilities, then validate
the proposed config with that candidate before replacing the installed binary or
config. Keep a copy of the working binary outside mise's prune directory.
Rollback to it if startup fails. Ordinary shutdown still immediately closes
connections. Use the authenticated drain protocol below before an automated
update. Older binaries without this protocol still require zero established
connections and five minutes without requests.

## Safe update drain

All drain endpoints use the existing v8 management authentication. There is no
v0 alias. `GET /v8/management/server/drain` returns schema 1 with an opaque
`instance_id`, `supported`, `blockers`, `draining`, `stopping`,
`lease_remaining_ms`, `active_requests`, `websocket_sessions`,
`active_websocket_turns` and `ready`. Counts never include payloads, credentials,
session identifiers or lease tokens. Health, panel and explicitly listed
read-only management requests do not count. OAuth GETs are mutating requests
and obey the same fence as credential writes. Management writes and custom inference routes do count.
HTTP requests remain active until their handler returns **and** net/http finishes
flushing the response. HTTP/2 reports idle before its final frame flush, so
connections that carried inference must close naturally before an update.
Completed requests coalesce into one completion barrier per HTTP/2 connection;
only live handlers retain individual request tickets. Missing connection
metadata similarly keeps one fail-closed barrier rather than request history. Unknown WebSocket types count for their full lifetime, including handlers that
return after handing the socket to a background goroutine. If an embedder serves
`Handler()` without the server's connection callbacks, HTTP requests cannot be
proven flushed and maintenance defers.

1. Stage and validate the retained candidate before acquiring a drain.
2. POST to the status path with `{"instance_id":"<from GET>",
   "owner_token":"<random 32-128 character token>"}`. An exclusive two-minute
   lease fences new requests with retryable 503 and `Retry-After: 2`.
   Existing responses continue without a new network deadline. Repeating the
   same POST is idempotent and does not extend its lease.
3. Idle Responses WebSockets close with the existing 1012 replay-required
   protocol. Clients must reconnect and resend full conversation history.
   Sessions awaiting tool results remain busy, and their continuations can
   finish. Duplex steering stays busy until its execution returns. Restart
   readiness follows socket and upstream execution-session cleanup.
4. When `ready`, atomically select the candidate in the supervisor's launch path,
   then POST the same owner body to `/v8/management/server/drain/stop`. The proxy
   rechecks ownership, lease, readiness and detached-work blockers under its admission lock and commits
   a permanent fence before stopping **its own** API server. It returns 202.
   The service exits; a supervisor such as systemd `Restart=always` launches the
   selected binary. Do not replace this step with a readiness GET followed by
   killing a PID. The lease could expire and admit work between those actions.
5. On an abort before stop, DELETE the status path with the same owner body.
   If the updater crashes or loses contact, the lease expires and restores
   admission. A committed stop cannot expire or be cancelled. If the stop reply
   is lost, inspect the service and running executable; never kill the original
   process based on an ambiguous response.

Home, configured/loaded plugins, detached realtime calls and pending OAuth
logins or credential commits report blockers. The stop commit checks those
blockers after all admitted handlers have completed, while holding the admission
lock, so a request cannot hand off new detached work between the checks.
Their background work has no complete drain accounting, so automated maintenance
must defer. An in-flight WebRTC bootstrap remains counted until its detached
session is visible. The status reports runtime support separately from the
binary's protocol capability. Authentication failure, malformed/unknown schema,
instance changes and unavailable status must defer rather than fall back to a
less strict check. The normal direct Stop semantics remain unchanged.

Opt-in client compatibility test, using only an isolated local executor:

```bash
CLIPROXY_CODEX_DRAIN_SMOKE=1 go test ./internal/api -run '^TestDrainCodexClientReplaysConversation$' -v
```


## Fork sync and releases

`.github/workflows/pd-sync-release.yml` runs every six hours, on main pushes and
by manual dispatch. PRs run the same verification script. Sync merges the newest
release tag on `router-for-me/CLIProxyAPI:main` (upstream tags most days; its
main between tags is often mid-change), verifies, then stages the entire release
before pushing the merge. It never resets or force-pushes custom commits. A
failure creates or updates the fork's failure issue and action summary. The
previous published release remains available. When the newest tag conflicts,
the run still verifies and releases main as it is, so a just-merged fix ships,
and then fails. Publication uploads all assets to a draft before marking it latest.
Unchanged commits do not produce duplicate releases. The upstream tag release
workflow is restricted to the upstream repository so fork tags cannot publish
partial upstream-style releases.

A workflow-changing upstream merge requires a repository `SYNC_TOKEN` secret,
with Contents and Workflows write access to **PDiTO/CLIProxyAPI**. Only the push
step receives it. Other publishing uses the built-in repository token. Without
that secret a workflow-changing push fails visibly and keeps the last release.
Create/renew the secret through GitHub's secret UI; never commit it.

### Verification

`.github/scripts/pd-verify.sh` runs `pd-verify.py`, then the build, capabilities
and example config checks, which always block. Upstream's own CI builds but
never runs its tests or gofmt, so its tags often carry failing or flaky tests.
`pd-verify.py` compares the fork with the upstream commit it is built on (the
merge base of HEAD and `upstream/main`):

- A Go file the fork adds must be gofmt-formatted. A file the fork changes must
  be formatted if upstream's copy is.
- A failing test blocks when the fork owns it, meaning upstream's package has no
  test of that name, or when it fails again on the fork while passing three
  reruns on upstream. Tests that fail on upstream too, and tests that pass
  every rerun, are listed in the run summary without blocking.
- Each rerun is a fresh `go test -count=1` process, because some upstream tests
  leave global state behind and fail a second run in one process.

### Keeping merges clean

Most sync failures were conflicts between fork edits and upstream's next change
to the same lines. To keep them rare:

- Never edit upstream's tests or reformat upstream files to get a green run.
  The verification above already ignores upstream's own failures, and the edit
  conflicts as soon as upstream fixes the test its own way.
- Put fork code in fork-owned files. Upstream files keep one-line hooks into it,
  as in `server.go`, `selector.go` and `openai_responses_websocket.go`. Don't
  append functions to the end of an upstream file; upstream appends there too.
- Don't edit upstream comments, `config.example.yaml` or `.gitignore`. This
  document stays tracked without a `.gitignore` exception.

### Automatic conflict resolution

The `cliproxy-fork-sync` job on ai (in the fleet repository) checks hourly.
When the latest sync run on main failed and main lacks upstream's newest tag, it
merges the tag on a `sync/upstream-<tag>` branch in its own clone. If the merge
conflicts or verification fails, Claude Code resolves it under the rules above.
The job then refuses the result if conflict markers remain, if any `pd-` script
or workflow changed, if it edited an upstream test file that main didn't already
edit or if a fork test was deleted, and it runs `pd-verify.sh` itself. It opens a PR and merges it
once the PR's checks pass. That push runs this workflow, which releases it. A
result it refuses is left for a person, and `fleet status ai` says why.

Tags are `pd-v<upstream-version>-<UTC YYYYMMDDHHMMSS>-<12-char fork SHA>`.
The timestamp precedes the hash so consecutive builds of one upstream version
sort chronologically. Assets are:

- `CLIProxyAPI_<full-tag>_linux_amd64.tar.gz`
- `checksums.txt`
- `routing-capabilities.json`
- `release-metadata.json`

The archive contains `cli-proxy-api`, `config.example.yaml`, both upstream
READMEs, LICENSE and the two metadata files. Metadata preserves the upstream
repository, base tag, upstream commit, fork commit and build time. The Linux
amd64 build uses CGO disabled for portability; dynamic shared-library plugins
are unavailable. Other upstream executors and WebSocket support remain built in.

Use only `github:PDiTO/CLIProxyAPI` in mise, with `version_prefix = "pd-v"`,
`version_regex = "^pd-v[0-9]+\\.[0-9]+\\.[0-9]+-[0-9]{14}-[0-9a-f]+$"` and
`asset_pattern = "CLIProxyAPI_*_linux_amd64.tar.gz"`. The GitHub backend supports
these options and uses explicit asset patterns without platform autodetection.
Never fall back to the upstream binary when a fork update fails. Fleet owns
minimum release age, staging, rollback, restart deferral and deployment.

The fork also carries one small upstream fix: Claude setup-token flags use the
metadata writers' existing lock. It does not change the Home protocol or require
a Home repository update.
