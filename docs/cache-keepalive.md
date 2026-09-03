# Agent-aware prompt-cache keepalive

`claude-code.cache-keepalive` keeps a Claude Code session's prompt cache warm
while one of its subagents is still running.

## Why

An orchestrator session that dispatches a subagent frequently blocks on it for
longer than the prompt cache TTL. When the subagent returns, the next request is
a full re-write of the whole context at the cache-write premium instead of a read
at a tenth of the base rate.

On the 5m pool a refresh is not worth it: thirteen reads an hour cost more than
the single write they avoid. On the 1h pool the arithmetic reverses, and one read
per hour is close to free next to a re-write of a large prefix.

The proxy is the right place for the refresh. It holds the last request body, the
credential, and the session-to-account binding, so its probe warms the same
per-account entry the next real request will hit. A client-side hook cannot
guarantee which account the refresh lands on.

## Policy

A probe is only sent while a task belonging to that session is still running. A
session idling on human input is never probed: that wait is unbounded, and the
guarantee that another turn is coming is what makes the probe pay for itself.

## How it works

1. **Observe.** Every request that a confirmed Claude Code client sent with an
   explicit `cache_control` `ttl` of `1h` is recorded against its session id, taken
   from `metadata.user_id` or the `X-Claude-Code-Session-Id` header. The scheduler
   stores the client body, the headers, and the credential that served it, then
   arms a timer for `request start + ttl - before-expiry`. A request carrying only
   a 5m marker never schedules anything.

2. **Fire.** At the timer, in order:
   - a newer request for that session supersedes the probe, which is dropped;
   - the liveness check must report a running task, unless
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

3. **Reschedule.** The next probe is measured from this probe's own start time.
   After `max-probes` consecutive probes with no real request in between, the
   session is dropped.

## Liveness

`liveness: claude-code-tasks` reads Claude Code's on-disk task state. A session
counts as live when either signal holds:

- a file under `<task-state-dir>/<session>/*.json` has a `status` that is not
  `completed`, `failed`, `cancelled`, `killed`, or another terminal value;
- a file under `<task-output-dir>/<project>/<session>/tasks/*.output` was modified
  within the TTL window.

A session directory is matched as either the full session UUID or `session-` plus
its first eight characters, because Claude Code uses both spellings. An
unrecognised status counts as live: a false negative silently disables the
feature, while a false positive costs one cheap read.

`liveness: always` skips the check. It exists for clients whose agent state the
proxy cannot see, and it will keep probing an idle session until `max-probes`.

## Cost

One read of the prefix per `ttl - before-expiry` while agents run. With a 400k
prefix on the 1h pool that is roughly 40k token-equivalents an hour, against the
~800k a re-write would cost. `max-probes` bounds an abandoned session.

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
    before-expiry: 5m              # fire at ttl - before-expiry
    only-when-agents-active: true
    liveness: claude-code-tasks    # or: always
    max-probes: 6
    max-tokens: 1
    # task-state-dirs:
    #   - "~/.claude/tasks"
    # task-output-dirs:
    #   - "/private/tmp/claude-<uid>"
```

The block hot-reloads with the rest of the configuration. `~` and `<uid>` are
expanded in both path lists.

## Logs

At info level, one line per event:

```
cache-keepalive: scheduled | session=4463ede6... auth=<id> model=<model> ttl=1h0m0s fires_in=55m0s
cache-keepalive: fired     | session=4463ede6... auth=<id> model=<model> cache_read_input_tokens=161937 probes=1 rescheduled=true
cache-keepalive: skipped   | session=4463ede6... reason=no-live-agents
```

`reason` is one of `superseded`, `max-probes`, `no-live-agents`,
`auth-binding-lost`, `no-prober`, or `probe-body-build-failed`.
