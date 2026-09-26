# Agent-aware prompt-cache keepalive

`claude-code.cache-keepalive` keeps a Claude Code session's prompt cache warm
while one of its subagents is still running.

## Why

An orchestrator session that dispatches a subagent frequently blocks on it for
longer than the prompt cache TTL. When the subagent returns, the next request is
a full re-write of the whole context at the cache-write premium instead of a read
at a tenth of the base rate.

On the 1h pool the arithmetic is easy: one read per hour is close to free next to
a re-write of a large prefix. On the 5m pool it depends on the model, which the
next section works out.

The proxy is the right place for the refresh. It holds the last request body, the
credential, and the session-to-account binding, so its probe warms the same
per-account entry the next real request will hit. A client-side hook cannot
guarantee which account the refresh lands on.

## Why 5m sessions are worth probing on Fable 5.1

The break-even is the cache-read multiple of base input, because a probe buys one
read and avoids one write.

| | cache read | cache write (1h) | 12 reads/hour | vs. the write |
|---|---|---|---|---|
| most Claude models | 0.1x base | 1.25x base | 1.2x | a wash |
| claude-fable-5-1, claude-mythos-5-1 | 0.025x base | 1.25x base | 0.3x | ~4x cheaper |

On Fable 5.1 a cache read is $0.25/MTok against $10/MTok base input, so holding a
5m entry open costs about a third of what letting it expire and re-writing costs.
That is why `probe-5m` defaults to `auto` and probes exactly those models.
Anthropic's prompt-caching guidance makes the same point from the other side: on
these models prefer a keepalive on the 5m tier over paying the 1h TTL premium,
unless pauses regularly approach an hour.

The list of models lives in `CheapCacheReadModels` in
`internal/runtime/keepalive/probe_5m.go` and is matched case-insensitively as a
substring, so `us.anthropic.claude-fable-5-1-v1:0` and `claude-fable-5-1[1m]`
both resolve. `probe-5m-models` replaces it without a rebuild when a new model
lands first.

`probe-5m: always` probes every confirmed session regardless of model, and
`probe-5m: never` restores the original 1h-only rule.

## Policy

A probe is only sent while a task belonging to that session is still running. A
session idling on human input is never probed: that wait is unbounded, and the
guarantee that another turn is coming is what makes the probe pay for itself.
That gate, not the probe count, is what bounds the spend on either tier.

## How it works

1. **Observe.** Every request that a confirmed Claude Code client sent with a
   `cache_control` marker is recorded against its session id, taken from
   `metadata.user_id` or the `X-Claude-Code-Session-Id` header. A marker with an
   explicit `ttl` of `1h` puts the session on the 1h tier; anything else, a bare
   `{"type":"ephemeral"}` included, is the 5m tier and is scheduled only when
   `probe-5m` allows it. The scheduler stores the client body, the headers, and
   the credential that served it, then arms a timer for
   `request start + ttl - before-expiry`, using `before-expiry-5m` on the 5m tier.

   The clock starts at the **request's** start, not the response's, and
   generation time counts against the TTL. That is why the 5m lead time defaults
   to 45s: the margin has to cover a slow turn plus the probe's own round trip
   and still land inside the window.

2. **Fire.** At the timer, in order:
   - a newer request for that session supersedes the probe, which is dropped;
   - the liveness check must report a running agent, unless
     `only-when-agents-active` is false;
   - the session must still be bound to the same credential. If the binding is
     gone, because of a cooldown or a rotation, the probe is skipped: a probe on a
     different account warms nothing useful. With session-sticky routing turned
     off there is no binding to check and the probe proceeds.
   - the stored body is replayed through the ordinary execution path with the
     credential pinned, so it receives the same translation, cloaking, header and
     `cache_control` handling a real request does. Only `max_tokens`, `stream` and
     `thinking` are rewritten; tools, system, messages and every `cache_control`
     marker stay byte-identical, and the request keeps the observed
     `Anthropic-Beta` list including `extended-cache-ttl-2025-04-11`.

3. **Reschedule.** The next probe is measured from this probe's own start time,
   again with the tier's lead time. After `max-probes` consecutive probes with no
   real request in between, the session is dropped; the 5m tier uses
   `max-probes-5m`, because six probes buy six hours on the 1h pool and only 25
   minutes here. The default of 30 covers about two hours of idle.

## Liveness

`liveness: claude-code-tasks` reads Claude Code's on-disk agent state.

**The primary signal is the per-agent task output file:**

```
<task-output-dir>/<project-slug>/<session-uuid>/tasks/*.output
```

Every background agent and shell of a session has one, and a running agent keeps
writing to it. The session is live when any of those files was modified inside
`agent-idle-window`. The project slug is the client's working directory with
every `/` replaced by `-`, so `/Users/me/src` becomes `-Users-me-src`; the proxy
matches it with a wildcard rather than deriving it, since it does not know the
client's directory.

Some of these files are symlinks into
`~/.claude/projects/<slug>/<session>/subagents/agent-*.jsonl`, where the real
writes land. The symlink's own timestamp lags the target's, so the check follows
the link and reads the target. Reading the link's own timestamp would report a
busy agent as gone.

**The TodoWrite state files are a secondary fallback only.** A file under
`<task-state-dir>/<session>/*.json` whose `status` is not `completed`, `failed`,
`cancelled`, `killed` or another terminal value also counts as live. These files
are the user-facing todo list, not subagent state: a session with a running
subagent frequently has no todo file at all, and a stale todo left at
`in_progress` would keep a finished session looking alive. Never rely on it
alone.

A session directory is matched as either the full session UUID or `session-`
plus its first eight characters, because Claude Code uses both spellings. An
unrecognised status counts as live: a false negative silently disables the
feature, while a false positive costs one cheap read.

### The idle window

`agent-idle-window` (default 10m) is how long an agent may be silent and still
count as running. It is deliberately **not** the cache TTL: an agent that has
written nothing for an hour is finished, not busy, and probing for it would
burn the budget on a dead session.

The flip side is that an agent doing long work that emits nothing looks idle
once the window passes. Timestamps only advance when the process actually
writes, so a background step that prints nothing for twenty minutes will not
hold the session live under a 10m window. Set the window above the longest
silence worth paying a probe for.

`liveness: always` skips the check entirely. It exists for clients whose agent
state the proxy cannot see, and it will keep probing an idle session until
`max-probes`.

## Cost

One read of the prefix per `ttl - before-expiry` while agents run. With a 400k
prefix on the 1h pool that is roughly 40k token-equivalents an hour, against the
~800k a re-write would cost. On the 5m pool the same prefix on Fable 5.1 costs
about 120k token-equivalents an hour against that same ~500k write. `max-probes`
and `max-probes-5m` bound an abandoned session, and the liveness gate bounds a
live one.

## Safety

Probe bodies are held in memory only, one per session, replaced on every real
request and dropped when the session stops qualifying or exhausts its budget.
They are never written to disk and never sent to a credential other than the one
the session is bound to.

## Configuration

```yaml
claude-code:
  cache-keepalive:
    enabled: false                 # opt-in
    before-expiry: 5m              # 1h pool: fire at ttl - before-expiry
    before-expiry-5m: 45s          # 5m pool: same, measured from the request start
    probe-5m: auto                 # auto | always | never
    # probe-5m-models:             # replaces the built-in cheap-cache-read list
    #   - claude-fable-5-1
    #   - claude-mythos-5-1
    only-when-agents-active: true
    liveness: claude-code-tasks    # or: always
    agent-idle-window: 10m         # how long an agent may be silent and still count as running
    max-probes: 6                  # 1h pool
    max-probes-5m: 30              # 5m pool, ~2h of idle
    max-tokens: 1
    # task-state-dirs:
    #   - "~/.claude/tasks"
    # task-output-dirs:
    #   - "/private/tmp/claude-<uid>"
```

The block hot-reloads with the rest of the configuration. `~` and `<uid>` are
expanded in both path lists.

## Observability

The feature is not a black box. Everything it does is visible two ways.

### Logs

Every line is prefixed `cache-keepalive:`, so `grep cache-keepalive main.log`
tells the whole story with no other source.

```
cache-keepalive: enabled | before-expiry=5m0s before-expiry-5m=45s probe-5m=auto only-when-agents-active=true liveness=claude-code-tasks agent-idle-window=10m0s max-probes=6 max-probes-5m=30 max-tokens=1
cache-keepalive: scheduled | session=4463ede6... auth=<id> model=<model> ttl=1h0m0s ttl_tier=1h probe_5m=n/a fires_in=55m0s next_probe_at=2026-09-02T20:51:57-07:00
cache-keepalive: scheduled | session=8f21ac03... auth=<id> model=claude-fable-5-1 ttl=5m0s ttl_tier=5m probe_5m=model-auto fires_in=4m15s next_probe_at=2026-09-02T20:36:12-07:00
cache-keepalive: probe | session=4463ede6... auth=<id> model=<model> ttl_tier=1h probe_5m=n/a status=hit cache_read_input_tokens=161937 cache_creation_input_tokens=0 duration=612ms probes_sent=1 consecutive_probes=1 rescheduled=true next_probe_at=2026-09-02T21:46:57-07:00
cache-keepalive: skipped | session=4463ede6... auth=<id> model=<model> ttl_tier=1h reason=no-live-agents
```

`ttl_tier` is `5m` or `1h`, and `probe_5m` is the tier decision: `model-auto`
when `auto` matched the request model, `always`, `skipped-never`,
`skipped-model`, or `n/a` on the 1h pool where the policy does not apply. The two
skips are logged at debug level, since a proxy serving mixed models emits one per
request, and are counted under `counters.skipped_by_reason` where they stay
visible at any log level.

A probe that fails to refresh the entry is the malfunction signal, logged at
**warning** level with `diagnostics.cache_miss_reason` when the account has the
cache-diagnosis beta and the upstream supplied one. Two cases count as a miss:

- the probe read nothing at all, so the entry had already expired;
- the probe read less than half of what the observed real request read, so most
  of the prefix it was meant to keep warm was already gone. The real request's
  read is recorded as `baseline_read_input_tokens`; a zero baseline, which is the
  normal case on the streaming path where usage is not yet available, leaves only
  the first check.

```
cache-keepalive: probe MISSED | session=4463ede6... auth=<id> model=<model> status=miss cache_read_input_tokens=0 cache_creation_input_tokens=25154 baseline_read_input_tokens=161937 duration=743ms probes_sent=2 consecutive_probes=2 rescheduled=true next_probe_at=... cache_miss_reason="messages_changed" cache_missed_input_tokens=25154
```

A probe request that fails outright logs at warning with `status=error`.

`reason` is one of `superseded`, `max-probes`, `no-live-agents`,
`auth-binding-lost`, `probe-error`, `no-prober`, or `probe-body-build-failed`.

### Management endpoint

```
GET /v0/management/cache-keepalive
```

Read-only, no parameters, same authentication as every other `/v0/management`
route. It returns the live scheduler state:

```json
{
  "enabled": true,
  "before_expiry": "58m0s",
  "before_expiry_5m": "45s",
  "probe_5m": "auto",
  "probe_5m_models": ["claude-fable-5-1", "claude-mythos-5-1"],
  "only_when_agents_active": false,
  "max_probes": 2,
  "max_probes_5m": 30,
  "max_tokens": 1,
  "sessions": [
    {
      "session_id": "aa11bb22-cc33-dd44-ee55-ff6677889900",
      "auth_id": "claude-account.json",
      "provider": "mixed",
      "model": "claude-haiku-4-5-20251001",
      "ttl": "1h0m0s",
      "ttl_seconds": 3600,
      "ttl_tier": "1h",
      "probe_5m_decision": "n/a",
      "last_request_at": "2026-09-02T20:31:12-07:00",
      "next_probe_at": "2026-09-02T20:35:11-07:00",
      "probes_sent": 1,
      "consecutive_probes": 1,
      "active": true,
      "last_probe": {
        "at": "2026-09-02T20:33:11-07:00",
        "status": "hit",
        "cache_read_input_tokens": 12468,
        "cache_creation_input_tokens": 0
      }
    }
  ],
  "counters": {
    "scheduled": 1,
    "fired": 1,
    "hits": 1,
    "misses": 0,
    "errors": 0,
    "skipped_by_reason": {}
  }
}
```

`ttl_tier` and `probe_5m_decision` say which pool the session is on and why it is
being probed, so a session missing from the list is explained by the counters
rather than by guesswork.

`status` is `hit`, `miss`, `error`, or `skipped`; a skipped entry carries
`skipped_reason`, and a miss carries `diagnosis` and `cache_missed_input_tokens`
when the upstream supplied them. Misses are counted under `counters.misses`.

The diagnostics come from `diagnostics.cache_miss_reason`, which a non-streaming
body carries beside `usage` and a streaming response carries inside the
`message_start` event's `message`. Both shapes are read.

A session that has stopped probing stays listed with `active: false`, a
`retired_at` timestamp and a `retired_reason`, so an operator can tell "nothing
to do" apart from "silently broken". `retired_reason` is separate from
`last_probe` on purpose: ending a session never erases the result of the last
probe it actually sent. The retired history is capped at 64 sessions.
Retiring a session drops its stored request body immediately, so no request
content is ever reachable through this endpoint.
