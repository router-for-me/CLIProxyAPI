# Claw Master with separate inference subscriptions

`claw-master` launches the normal installed Claude Code client with its normal
master login. It intercepts only Claude inference requests and authenticates those
requests with one of several separately logged-in Claude subscription profiles.
Claude Code still owns the conversation, Remote Control, tools, permissions,
subagents, compaction, and model selection.

There is no second agent and no model allowlist. The proxy changes the inference
account; it does not replace Claude Code or reinterpret its protocol.

## Renamed from claude-master

The project was called claude-master until 2026-10. claw-master is the pool's name, kept free of
any one provider; what is about a provider says so by its real name (Claude Code, Anthropic, the
`provider=claude` metric dimension), so another provider could join without a rename.

The old names keep working, so servers, client boxes and their configuration can move in any
order:

- `CLAUDE_MASTER_<NAME>` is read when `CLAW_MASTER_<NAME>` is unset (the server, client directory,
  open tunnel, CA, agent listen address and OTLP endpoint settings: `SERVER`, `CLIENT_DIR`, `OPEN`,
  `CA`, `AGENT_LISTEN`, `OTLP_ENDPOINT`; and `CLAUDE_MASTER_BACKUP_API_KEY`). Both backup-key
  names are always kept out of the launched Claude's environment.
- `~/.local/share/claude-master` holds the profiles while `~/.local/share/claw-master` does not
  exist; move it to switch.
- The server reads the project from `X-Claw-Master-Project` or `X-Claude-Master-Project`, and
  answers the reachability probe at both `/.claw-master/probe` and `/.claude-master/probe` with
  both `X-Claw-Master` and `X-Claude-Master`. Clients probe the old path and accept either
  header, so either side can be updated first.

Metrics are `claw_master.*`. Measurements about Claude carry `provider=claude`; the pool's own
(connections, TLS handshakes, the process) carry none. The metric namespace is whatever the
collector is configured with (the reference deployment's is `ClawMaster`). To carry CloudWatch
history across the rename, `scripts/claw-master-migrate-metrics.py` copies the old `ClaudeMaster`
streams to the new names (a dry run unless given `--write`; `--verify` compares the copy).

## Data path

```text
Claude app <-> Claude Code using the normal master login
                         |
              process-local HTTPS proxy
                 /                 \
       control/session APIs       POST /v1/messages
       unchanged master auth      POST /v1/messages/count_tokens
                 |                 |
          api.anthropic.com      selected subscription profile
```

The native request is forwarded without translation. The selected profile replaces
only authentication and account/device identity. The conversation/session identity,
model, tools, messages, thinking settings, betas, compression negotiation, and other
end-to-end protocol headers remain native Claude Code values. The request is re-signed
after the selected account identity is installed.

Responses retain Anthropic's status, body, duplicate header values, compression, and
unknown end-to-end headers. Successful streaming headers are exposed as soon as
Anthropic accepts the request, without waiting for the first body bytes. Streaming
bytes are forwarded as each upstream read arrives; the proxy does not buffer,
parse/rebuild, or synthesize SSE events. HTTP
hop-by-hop fields and connection framing are necessarily regenerated for the local
connection.

Inference (`POST /v1/messages`, `POST /v1/messages/count_tokens`) goes to the pool. Every other
Anthropic route goes to Anthropic unchanged, on the session's own login, exactly as Claude Code
would send it without claw-master: compatibility comes first, so a new Claude Code release that
calls a new endpoint keeps working. claw-master adds no credential to these requests, so relaying
them grants nothing. A route not in the reviewed list (`proxyControlPath`) is logged once per shape
(`relayed an unlisted Anthropic route`, method and path with ids replaced). Only an ambiguous or
encoded path, or the pool's own routes with the wrong method, are refused. There is no Bedrock or master-account inference fallback. An explicitly
provided Anthropic API key can serve as the final backup after Anthropic has rejected every
subscription; without one, inference stays subscription-only.

## Install, login, and run

Requirements are Go 1.26+ and `claude` on `PATH`. Linux and macOS run everything (Linux has no `agent install`;
run the agent under its service manager); Windows runs `check`, `connect`, `client-init` and `agent` (see the agent
section below):

```bash
go build -o claw-master ./cmd/claw-master
./claw-master check
```

The launcher uses the currently installed Claude Code executable. It does not pin,
download, downgrade, or silently update Claude Code. `check` validates the executable
and local settings without logging in or sending inference.

Create each inference profile with a normal Anthropic OAuth login:

```bash
./claw-master login claude-primary
./claw-master login claude-secondary
./claw-master login claude-tertiary
```

Each command opens the normal Anthropic authorization page. Sign in to the intended
subscription account, using a separate/incognito browser session when necessary. On a
remote host the command prints the callback/tunnel instructions. Profiles do not copy,
import, or modify the native master login.

Run one fixed inference profile:

```bash
./claw-master run claude-primary -- --remote-control
```

Run a quota-aware pool:

```bash
./claw-master run claude-primary \
  --next-profile claude-secondary \
  --next-profile claude-tertiary \
  -- --remote-control
```

Arguments after `--` go directly to Claude Code. Claude Code may use its default model
or its normal model flags:

```bash
./claw-master run claude-primary \
  --next-profile claude-secondary \
  -- --model claude-sonnet-5-5 --fallback-model haiku --remote-control
```

The launcher does not define or constrain those models. `probe --model` is different:
that flag selects the model only for the launcher's small diagnostic request.

### Optional final API-key backup

Supply a paid Anthropic API key from a file or an environment variable, never as a
literal key in process arguments:

```bash
./claw-master run claude-primary --next-profile claude-secondary \
  --backup-api-key /home/me/claude_api.txt -- --remote-control
```

The file must contain only the key (surrounding whitespace is trimmed), be a regular
file no larger than 16 KiB, and be readable by the launcher. Keep it private, for example
with `chmod 600 /home/me/claude_api.txt`. `file:/home/me/claude_api.txt` is also
accepted.

The dedicated environment variable is consumed automatically when exported. Read it
without echoing or putting it in shell history:

```bash
read -rsp 'Anthropic API key (final backup): ' CLAW_MASTER_BACKUP_API_KEY
printf '\n'
export CLAW_MASTER_BACKUP_API_KEY
./claw-master run claude-primary --next-profile claude-secondary -- --remote-control
unset CLAW_MASTER_BACKUP_API_KEY
```

The launcher uses this key only for a self-contained request after Anthropic has rejected
every subscription (a credential-scoped `429` still in force on each). A usage poll at 100% is
not a rejection and never sends work to the key. Authentication, request, model,
and transport failures do not trigger the backup. Account-bound opaque continuations
stay on their originating subscription; an exposed streaming response is never replayed
on the key.

Conversation origins are persisted in private routing directories beside each
profile's auth directory. Records contain hashed session and account identities,
never request content or API keys. Restarting or reordering a pool preserves the
origin, including API-backup conversations when the same key is supplied. A new
route is staged before dispatch and confirmed only after upstream accepts the
generation. An interrupted or incomplete handoff stays ambiguous instead of
being treated as a successful account switch. If an opaque continuation has no
recorded origin, or that origin is no longer in the pool, the proxy refuses to
silently send it to another account.

API-key usage is billed separately from subscriptions. Model selection remains Claude
Code's native value unless explicitly mapped below; a subscription model may not be
available under the API key. The proxy does not silently substitute another model.

To consume a key already exported under another name, select that environment variable
before `--`:

```bash
./claw-master run claude-primary --backup-api-key env:MY_KEY -- --remote-control
```

An explicitly selected variable must exist and be nonempty. The key is held only by the
proxy backend: it is not saved in a profile, passed to the native Claude child, or used
for the master login. `--backup-api-key env:ANTHROPIC_API_KEY` consumes an existing
standard API-key variable and removes it from native startup, so Claude Code continues
to use its normal subscription login and Remote Control. An explicit file or environment
source overrides the optional dedicated environment variable.

The backup is used only while it can pay. claw-master checks it at start and about every five minutes (jittered by 20%) with a
token count (not billed; an organization with no credit is refused there too), and marks it unavailable at
once when a real request to it is refused for credit. While it is unavailable the pool behaves as if there
were no backup: a session gets Anthropic's weekly-limit 429 with `retry-after` and waits, instead of the
credit error. The next check that passes brings it back. The log says each change (`API-key backup
available` / `unavailable`) and the routing summary carries `backup=`.

### Optional exact model mappings

Use repeatable `--map INCOMING:TARGET` options before `--` to change a model explicitly:

```bash
./claw-master run claude-primary --next-profile claude-secondary \
  --map claude-sonnet-5-5:claude-sonnet-4-6 \
  -- --model claude-sonnet-5-5 --remote-control
```

Mappings apply equally to subscription requests, the final API-key backup, and token
counting. Matching is exact and happens once; there is no alias/suffix normalization,
chained mapping, or model allowlist. Unmapped models pass through unchanged. Mappings
do not relax account-bound continuation or streaming retry constraints.

The repository helper builds the launcher, prompts for any missing normal logins, and
starts the configured pool:

```bash
/home/me/CLIProxyAPI-claw-master/try-claw-master.sh --remote-control
```

Edit the short `profiles=(...)` list at the top of that script for the desired profile
names. It also consumes `CLAW_MASTER_BACKUP_API_KEY` automatically when exported;
the key does not require another profile or login. The helper extracts `--backup-api-key`
and repeatable `--map` options for the launcher; other arguments go to native Claude.
Use `--` to stop helper option extraction and pass everything after it literally:

```bash
/home/me/CLIProxyAPI-claw-master/try-claw-master.sh \
  --backup-api-key /home/me/claude_api.txt \
  --map claude-sonnet-5-5:claude-sonnet-4-6 --remote-control
```

## Weekly quota selection

For a multi-profile run, or a subscription with an API-key backup, startup asks
Anthropic's OAuth usage endpoint for each profile's weekly utilization and reset time.
New sessions use the usable account whose
weekly quota resets soonest, draining the quota that will be replenished first. The
configured profile order is the deterministic fallback when usage is unavailable or
reset times tie.

**The poll is advisory; Anthropic decides.** The polled weekly figure (and the one in response
headers) only orders the candidates: a subscription in its ok band before an unknown one, before
one in its reserve band (90%), before one the poll calls exhausted (100%); within a band, the
earliest reset first. It never refuses a request and never moves a conversation. On 2026-10-11
every profile polled at 100% and the pool refused every request itself, while a native Claude
Code session on the same Anthropic account as one of those profiles kept being served. A
subscription is taken out only by Anthropic's own answer to a real request: a credential-scoped
`429` (`anthropic-ratelimit-unified-status: rejected`), for its `retry-after` or unified reset,
or a dead login. When Anthropic accepts a request on a subscription the poll puts in its reserve
band or calls exhausted, the log says so once per profile per band
(`Anthropic accepted a request on a subscription the usage poll calls exhausted`).

Quota state is refreshed from Anthropic's rate-limit response headers. A
credential-scoped `429` temporarily blocks that account until its retry/reset deadline;
it does not turn a five-hour rejection into exhausted weekly usage. Request-scoped,
model-scoped, authentication, validation, and transport failures do not drain or rotate
the account.

After startup, the usage API is polled about every 3 minutes (each round jittered 20% either
way) for all subscription profiles, never the API-key backup. The profiles do not ask at once:
each waits its own fixed phase after the round starts, spread across the first half of the
interval. Anthropic limits this endpoint per account (polled every minute, every other request
was refused with 429), so a 429 holds that profile's poll back for its `Retry-After` but at least
one interval, at most 15 minutes. Busy profiles are also updated from every
inference response's rate-limit headers; the poll keeps idle ones and reset times current. This retries failed startup reads and picks up weekly resets,
quota grants, and usage from other processes. Accounts are queried independently, with
at most one usage request in flight per account, so a stalled account does not stop
the others from updating. Failed or unknown responses keep the last known quota.
Fresh usage snapshots can lower utilization or correct the predicted reset time, but
a slow poll cannot overwrite newer quota headers or a credential-scoped rejection.

Weekly usage alone does not prove a five-hour limit has recharged: existing credential
quota blocks and SDK cooldowns remain until their retry/reset deadlines. Known weekly
resets also reopen capacity on the next request, with polling and response headers
confirming the actual state. Polling never changes opaque continuation bindings or
replays a stream. Backend shutdown cancels and joins usage requests before releasing
profile locks. The separate OAuth refresh loop renews login tokens.

A conversation moves only on Anthropic's rejection:

- Whatever the poll says (ok, reserve or 100%), a session remains on its selected account.
- After a credential-scoped `429` on its account, a self-contained request moves to the
  best-ranked account Anthropic has not rejected.
- Opaque/account-bound continuations remain on their bound account; while Anthropic's
  rejection holds they get Anthropic's `429` back with the time it ends.
- When Anthropic has rejected every account, the pool answers Anthropic's `429` with the
  earliest time one comes back (or uses the API-key backup, if configured and able to pay).
- When the rejection's deadline passes, the account is asked again, whatever the poll says.

The usage endpoint is an OAuth product endpoint used by Claude Code rather than a
publicly versioned API. Failure to read it does not block startup; routing falls back to
configured order and subsequently observed rate-limit headers.

## Payload-based continuation affinity

Claude's Messages API receives the complete active context on each request, except for a
thread continuation (below). Prompt caching changes server work and billing, not the request
into an opaque session handle. After compaction, Claude Code sends the compacted active context
rather than all discarded history.

The selector therefore inspects the request body itself; it does not depend on a
version-specific "compaction happened" header. Ordinary text plus client-side
`tool_use`/`tool_result` history is self-contained and can move once Anthropic rejects its
account. A request stays on its current account when it contains known opaque or
provider-side state, including:

- container or uploaded/file identifiers;
- server-tool continuation state;
- encrypted server-tool content; or
- signed compaction blocks; or
- a payload that cannot be classified as valid JSON.

Signed `thinking` and `redacted_thinking` blocks do NOT pin a conversation to its account.
Anthropic's preserved-thinking rules say so and it was checked on this pool's own subscriptions on
2026-10-09 with the `thinking-binding-controls-2026-08-01` beta: a Claude Opus 5.5 block minted on
one subscription replays on another with `input_transformations: []`, even with
`prefix_mismatch_behavior: "error"`; a Claude Sonnet 5.5 block is dropped by the API
(`thinking_dropped`, reason `end_user_binding_mismatch`) and the request still succeeds. The proxy
never strips thinking itself: the API already drops what the target model or account cannot read,
and a client-side strip would be an edit that invalidates every later block.

Bindings use Claude Code's conversation and agent hierarchy. A subagent can receive a
separate account for self-contained work; if its first request carries opaque state, it
inherits the parent agent's binding only if the parent has never changed accounts.
After a parent handoff, an unbound opaque child is ambiguous: its copied state
could predate the switch. The proxy refuses that request; a self-contained child
can still start normally, and already-bound children retain their own origins.
This lets independent fanout use separate subscriptions without guessing the
owner of account-bound continuation data.

Errors reach the session exactly as Anthropic sends them, so the client reacts as it would
to Anthropic. An upstream error response is relayed byte for byte (its status, its body, and
the headers the client acts on: `request-id`, `retry-after`, `x-should-retry`,
`anthropic-ratelimit-*`), including when the scheduler retried after it and the pool then
refused: the last upstream error of the request is what the session gets. A failure that is
not a dead login (401) or a used-up quota (429) never benches the subscription: the scheduler
underneath would otherwise keep a subscription off a model for twelve hours after one 404,
and two 404s Anthropic did not repeat took the whole pool down twice on 2026-10-09. The next
request of the same conversation goes to the same subscription; the server log records each
upstream error's type and message. Only a refusal with no upstream error behind it is synthesized, in Anthropic's error shape, with the type and
status the client would get from Anthropic for that situation: every subscription out of
weekly quota is a 429 `rate_limit_error` with Anthropic's rate-limit text, the unified
rate-limit headers and a `retry-after` pointing at the earliest reset; opaque state the pool
will not move is a 400 `invalid_request_error`; a bound subscription that does not serve the
model is a 404 `not_found_error`; a cooldown or an active handoff is a 529
`overloaded_error`; the pool's own fault is a 500 `api_error`. The reason is in the message
(`claw-master: ...`) or, for the 429, in the `Claw-Master-Reason` header; it is
claw-master's text (a profile name, a model, a status), never upstream text. Only a used-up
weekly quota moves a conversation; an upstream error on the bound subscription keeps it there.

An account handoff is deferred while another generation for the same session is
active. Clients must preserve chronological history under a session ID; replaying
an old opaque branch after a completed handoff requires its own previously bound
child/session ID, not reuse of the parent's current ID.

Only failures received before the response is exposed can be retried on another
profile. Once successful streaming headers are delivered, that request is never replayed.
Any upstream SSE error
remains exactly the event Claude Code received, and routing changes can affect only a
later request.

### Server-side threads (`thread`, `previous_message_id`)

Claude Code 2.1.29x sends most turns as a thread continuation: `thread: {"type": "continue",
"previous_message_id": ...}` with only the messages after that one, and Anthropic supplies the
rest from the thread it keeps for the account that served the earlier turns. A continuation
that reaches an account without that thread, or after the thread has expired, is answered 404
`not_found_error` "No thread state was found for the requested `previous_message_id`" (details
`error_code: thread_not_found`). Claude Code then resends the whole conversation with
`thread: {"type": "create"}` and the turn succeeds; its debug log shows
`[tether] forcing create for retry: replay_not_found`.

claw-master does not pin a conversation to its account for `thread`, and does not hide the 404:
it behaves as if the person had switched accounts, and Claude Code's own replay is how a client
recovers from that. The 404 is relayed as sent (logged as `upstream error` at warn and
`request ended with Anthropic's error, relayed as sent` at info).

Most of these 404s are expiry, not moves. Measured on 2026-10-11 through the pool, one Claude Code
process with two turns on the same subscription: a 290-second pause continued the thread; a
400-second pause got the 404 and the replay. Claude Code decides when a thread has expired from
its feature flags, which it fetches on the person's own login, while the thread lives on the
pooled subscription; it still sent a continuation at 400 seconds. So a turn after a pause of
more than about five minutes costs one fast 404 round trip. The replay sends what a fresh
create would have sent anyway, and no turn fails.

## Profiles and process boundaries

- Profiles live under `~/.local/share/claw-master/profiles/` in private directories.
  OAuth refresh persistence uses atomic replacement. A complete staged refresh
  left by an interrupted write is recovered while holding the profile lock.
- Any number of launchers (one per Claude Code window) may use the same profiles at once. A
  launch holds each profile SHARED until shutdown; only a login needs it alone. A subscription
  login rotates on every refresh and the previous access token stops working immediately, so the
  launchers coordinate through the credential file, which is the single source of truth:
  - a refresh runs under an exclusive lock (`auth.refresh.lock`, beside the auth directory) and
    first re-reads the file. If another launcher already rotated, it adopts that credential and
    does not call Anthropic;
  - every request notices a newer saved credential (one `stat`) and uses it;
  - a 401 adopts a newer saved credential, or rotates under the lock (at most once per 30 seconds
    per launcher), then retries once before any response is exposed;
  - a save never writes older tokens over newer ones;
  - the usage poll is served from a short-lived cache file (`auth.usage`, 2 minutes, shorter
    than the shortest jittered interval) when another launcher fetched it, so N launchers make about
    one usage request per interval per account.
  The kernel releases the lock when its holder dies, so a crashed launcher cannot wedge the rest.
  Conversation routing records are one file per session and need no coordination.
- **Launcher flags and Claude settings.** `run` and `connect` start Claude with the process proxy and the master login, so
  they refuse launcher flags that would change them: `--setting-sources`, `--sdk-url`, `--api-key`, `--base-url`,
  `--claudeai-user-id`, `--claudeai-org-id`, `--remote-control-session-id`, `--cwd`, `--worktree` (`-w`). `--settings FILE` (or
  inline JSON) is allowed, so a launcher can carry hooks and notification settings, **unless it is known to conflict**: the
  login and provider settings `apiKeyHelper`, `awsAuthRefresh`, `awsCredentialExport`, `gcpAuthRefresh`, `forceLoginMethod`
  and `forceLoginOrgUUID`, or an `env` block that sets a provider variable (`ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY`,
  `CLAUDE_CODE_USE_BEDROCK`, ...), a proxy variable (`HTTPS_PROXY`, `NO_PROXY`, ...) or `NODE_EXTRA_CA_CERTS`. The error
  names the setting, never its value. `NODE_OPTIONS` is refused too, in settings and in the environment, with one
  allowance: a value made only of V8 heap-size flags (`--max-old-space-size=N`, `--max-semi-space-size=N`) is kept, because
  a project commonly caps its builds' memory that way and those flags cannot load code, change TLS trust or route traffic;
  anything else in it (`--require`, `--import`, `--use-openssl-ca`, ...) stays refused. A value that cannot be read as a settings object is refused, because its effect is
  unknown. This is a guard against accidental or wrapper-injected redirection, not a defence against a user who can
  already run Claude directly.
- Claude's proxy is a **local forwarder**: plain HTTP on an ephemeral loopback port, given to
  Claude as `HTTPS_PROXY=http://claw-master:<token>@127.0.0.1:<port>` with a fresh random token
  per launch. A CONNECT to `api.anthropic.com:443` that carries the token goes on to the
  claw-master proxy; every other CONNECT is dialled from this box and relayed blind. So the
  commands, hooks, MCP servers and status line Claude starts inherit a proxy that is transparent
  to them (`git`, `curl`, `gh`, package managers work unchanged), their traffic leaves from this
  box rather than the server, and a `claude` they start stays on the pool. A process that reaches
  `api.anthropic.com` through the forwarder without Node's `NODE_EXTRA_CA_CERTS` (a Python SDK
  script, say) sees the launch CA and fails, as before.
- The claw-master proxy behind the forwarder is an HTTPS listener that requires a client
  certificate; there is no password. The forwarder presents it: the launch's own throwaway
  client certificate for `run`, this box's for `connect`; Claude holds none. The CA is trusted
  only by Claude through `NODE_EXTRA_CA_CERTS`; it is not installed in the system trust store. The CA is
  constrained to the DNS name `api.anthropic.com`. It carries no IP-range constraint, because
  Claude's TLS stack rejects a trusted CA that has one (`unsupported name constraint type`). Leaf certificates renew on new handshakes without interrupting
  existing streams, so a long-running launcher does not lose TLS after a week.
- Only `api.anthropic.com` is TLS-terminated. Other HTTPS destinations are blind
  tunnels. This is routing isolation, not an operating-system network sandbox.
- Request bodies, tokens, and raw provider errors are not logged by the launcher.
  Claude Code keeps its own normal session/debug behavior.
- The current `claude` symlink is resolved before launch so an updater cannot replace
  the running executable midway through the process.

For numeric proxy counters without payloads or account identifiers, put
`--diagnostics` before the `--` separator.

## Manual two-session gym

[`scripts/claw-master-gym/README.md`](../scripts/claw-master-gym/README.md)
describes the checked-in tmux gym. It runs two disjoint sessions on
`claude-sonnet-5-5`, applies `/effort ultracode` interactively, and asks each session to
fan out to two bounded read-only subagents. Start it after creating two normal profiles:

```bash
/home/me/CLIProxyAPI-claw-master/scripts/claw-master-gym/start.sh \
  claude-gym-a claude-gym-b
```

The gym records private pane/debug output plus sanitized routing evidence and includes
`status.sh`, `inspect.sh`, `drive.sh`, and `stop.sh`. Its version check compares the
installed client with Anthropic's current official release; updating is a separate,
explicit action.

## Verification and compatibility

Automated tests cover credential separation, weekly-quota parsing and selection, the
poll as ordering only, durable payload affinity, parent/subagent bindings, pre-response retry, raw compressed
responses, exact streaming chunks, request/response header boundaries, token counting,
profile locking, and cancellation/shutdown.

The Claude-specific CI job runs for all PR authors, including fork contributors.
It downloads Anthropic's current official Claude Code artifact,
checks its published manifest checksum and reported version, then runs a credential-free
CONNECT/TLS/header/SSE smoke test. The repository intentionally has no Claude Code
version constant.

The historical transport audit exercised 53 releases from 2.1.209 through 2.1.270 in
both synthetic API-key and OAuth modes. Follow-up smoke tests through 2.1.286 found no
transport or streaming protocol break. Claude Code 2.1.283 added prompt-ID and
request-class headers without changing the endpoint protocol; this is why the proxy
uses an open end-to-end header boundary instead of a version pin or allowed-header
list.

Header preservation is an internal trusted-adapter mode and cannot be enabled by a
public client header. The proxy removes credentials, cookies, account-identity fields,
forwarding headers, and RFC hop-by-hop fields, then installs the selected profile's
authorization and identity. All other end-to-end fields pass through. TLS records and
HTTP connection framing are not expected to be byte-identical to a direct connection;
the Claude request/response semantics are.

## Reference lineage

This branch targets CLIProxyAPI v8 and reuses its OAuth refresh, executor, and request
scheduling infrastructure. The process-scoped interception design follows
[remote-claw](https://github.com/ejc3/remote-claw): retain native Claude's control
plane and intercept only the intended inference traffic. It does not modify the Claude
binary.


## Shared server

One box can hold the subscription logins and serve every other box, so client boxes hold no
login at all. Clients authenticate with a client certificate the server issued; no password
exists anywhere.

```bash
# on the server (profiles are shared, so this can run beside local launches)
claw-master serve claude-primary --next-profile claude-secondary --next-profile claude-tertiary \
  --listen 10.0.0.10:8443 --state-dir /var/lib/claw-master

# on a new client box: make its key (never leaves the box) and a request
claw-master client-init --dir ~/.config/claw-master --name dev-box-1

# on the server: sign it (at most 90 days), then copy client.pem and the server's ca.pem back
claw-master issue --state-dir /var/lib/claw-master --request client.csr --days 30 --out client.pem

# on the client box
claw-master connect --server 10.0.0.10:8443 --dir ~/.config/claw-master -- --remote-control
```

- `--listen` must be a specific loopback or private address. A public bind is refused.
- The server's CA is created once in `--state-dir` and reloaded on every start, so issued
  certificates keep working across restarts. A state directory with only half of the CA is an
  error, not a silent new CA.
- Issued certificates are short-lived (default 30 days, at most 90), so a lost box expires on its
  own. `connect` warns when its certificate has under three days left.
- `connect` holds no profile and never falls back to the box's own login: it makes one TLS
  handshake with the server first and names the problem if the server is down or does not accept
  the certificate. `CLAW_MASTER_SERVER` and `CLAW_MASTER_CLIENT_DIR` supply the defaults.
- The server accepts `CONNECT` only from a certificate-bearing client, and terminates TLS only
  for `api.anthropic.com`, exactly as a local launch does.

**Running the shared server on AWS:** [claw-master-aws.md](claw-master-aws.md) walks through it, with a working
Terraform example in [`examples/claw-master-aws`](../examples/claw-master-aws).

### Clients that arrive through a tunnel (no certificate)

A client certificate is the right credential between machines you control. For a machine that is
awkward to enrol (a laptop), the server can also serve an OPEN listener that asks for no
certificate, on a loopback address only:

```bash
# on the server: the certificate listener as before, plus a loopback-only open one
claw-master serve claude-primary --next-profile claude-secondary --next-profile claude-tertiary \
  --listen 10.0.0.10:8443 --open-loopback 127.0.0.1:8444 --state-dir /var/lib/claw-master

# on the laptop, behind a tunnel that authenticates (for example `cloudflared access tcp` to a
# tunnel whose origin is the server's 127.0.0.1:8444, protected by an access policy):
claw-master connect --open 127.0.0.1:8444 --ca ca.pem -- --remote-control
```

- **The trust is the tunnel.** Anything that can reach the open listener is served, so it must only
  ever be reachable through something that authenticates. `serve` refuses an `--open-loopback`
  address that is not a literal loopback address, and says so on its status line when the listener
  is on. The certificate listener keeps demanding a certificate.
- **`connect --open` also insists on loopback.** It speaks plain HTTP to the local end of the tunnel,
  so it refuses any other address; an unauthenticated proxy request never crosses a network.
- **It still needs the server's public `ca.pem`** (not a secret): the conversation inside the tunnel
  is TLS to `api.anthropic.com`, terminated by the server's CA, exactly as for a certificate client.
- **A tunnel that is down is reported clearly** (nothing listening, or not a claw-master proxy)
  instead of as a hang inside Claude.
- Defaults come from `CLAW_MASTER_OPEN` and `CLAW_MASTER_CA`.

### Programs that launch `claude` themselves (OpenClaw and other agent hosts)

`connect` and `run` are launchers, not a proxy a client is pointed at: each launch starts its own forwarder
with a fresh token and execs `claude` from `PATH` with `HTTPS_PROXY` and `NODE_EXTRA_CA_CERTS` set. A host
that spawns `claude` itself (OpenClaw's Claude CLI backend runs the bare command `claude`, with no setting to
change it) has to find a `claude` on its own `PATH` that is a wrapper around `connect`. Found putting
OpenClaw on the pool from a Mac through the tunnel above:

- **Recursion.** `connect` resolves `claude` on the `PATH` it is given. A wrapper named `claude` must hand
  `connect` a `PATH` whose first `claude` is the real one, or `connect` launches the wrapper again. Install
  the wrapper in a directory that comes earlier only on the host's `PATH` (OpenClaw's gateway service puts
  `/opt/homebrew/bin` before `~/.local/bin`; interactive shells do the reverse), so ordinary shells keep
  plain `claude`.
- **The host passes a refused flag.** OpenClaw adds `--setting-sources user` to every launch. `connect`
  refuses it (`Claude launcher flags cannot override master identity or provider routing`), so every turn
  exits 1 in about half a second, and the host's model fallback chain fails the same way on each model. The
  wrapper drops `--setting-sources` and its value; Claude's default sources include `user`. The stderr line
  is in the host's own log, not in claw-master's.
- **Plain-HTTP loopback services break.** `connect` removes `NO_PROXY` from Claude's environment, and the
  forwarder only tunnels `CONNECT`. A host that serves its tools as an MCP server on a plain-HTTP loopback
  URL (OpenClaw does) gets `405 this proxy only tunnels CONNECT`; the turn still runs, but without the
  host's tools, which is easy to miss. Restore a loopback-only exemption after `connect`, not before: put a
  second small `claude` first on the `PATH` the wrapper gives `connect`, which exports
  `NO_PROXY=localhost,127.0.0.1,::1` and execs the real `claude`. Anthropic traffic is unaffected; it never
  goes to loopback.
- **Fail open to the native login, not closed.** The wrapper checks that the tunnel's local port is
  listening and otherwise execs the real `claude`, and it passes `--version`, `auth`, `update`, `mcp` and the
  other management subcommands straight through, so a dead tunnel or a host's version probe never takes
  the host down.
- **Prove it with the native login out of quota.** A host turn that succeeds while the machine's own
  subscription is exhausted can only have come through the pool; the server logs the first tunnel client
  as `client connected for the first time client=tunnel listener=open`.

A sketch of the two files (`/opt/homebrew/bin/claude` -> wrapper; `inner/claude` is the second one):

```sh
# wrapper
REAL=$HOME/.local/bin/claude
if nc -z -G 2 127.0.0.1 8444 2>/dev/null; then
  case "${1:-}" in --version|-v|auth|login|logout|setup-token|update|doctor|mcp|plugin|install|config) exec "$REAL" "$@";; esac
  n=$#; i=0; skip=0                                   # drop --setting-sources VALUE
  for a in "$@"; do i=$((i+1))
    if [ $skip = 1 ]; then skip=0; elif [ "$a" = --setting-sources ]; then skip=1
    else case "$a" in --setting-sources=*) ;; *) set -- "$@" "$a";; esac; fi
    [ $i = $n ] && break; done; shift $n
  PATH=$HOME/.config/claw-master/inner:$HOME/.local/bin:$PATH exec claw-master connect \
    --open 127.0.0.1:8444 --ca "$HOME/.config/claw-master/ca.pem" -- "$@"
fi
exec "$REAL" "$@"

# inner/claude
NO_PROXY=localhost,127.0.0.1,::1; no_proxy=$NO_PROXY; export NO_PROXY no_proxy
exec "$HOME/.local/bin/claude" "$@"
```

### Every Claude Code on a Mac or Windows PC, the desktop app included: `claw-master agent`

The desktop app's Code tab runs its own copy of Claude Code, which the app downloads and starts itself, so
no launcher can set its environment. `claw-master agent` is a long-running forwarder for it, and for every
other Claude Code on the machine: the same forwarder a launch starts, on a fixed loopback address and
with a token kept in a file (`agent.token` beside the CA, or in `--dir`; created with mode 0600 on first
start and refused if others can read it).

Flags: `--listen` (or `CLAW_MASTER_AGENT_LISTEN`) is where Claude reaches it, and must differ from `--open`;
`--open` and `--ca`, or `--server` and `--dir`, say how it reaches the server; `--token-file` places the token
elsewhere (not with `install`, which keeps it where `connect` looks); `--log-level`, `--log-file` and
`--log-format` as for `serve`, of which `install` carries only `--log-file` into the job. `agent env` creates the
token if it does not exist yet.

It fails closed, like `connect`. A CONNECT to `api.anthropic.com:443` goes to the claw-master server or
nowhere: while the server or the tunnel is down it is answered `502`, and one without the agent's token
(a stale or mistyped proxy URL) is answered `407` instead of being sent to Anthropic with the machine's own
login. Both are logged. Other hosts (OAuth refresh, telemetry) are tunnelled directly, as in a launch.

On macOS and Windows, one command sets it up, behind the tunnel above:

```sh
claw-master agent install --listen 127.0.0.1:8445 --open 127.0.0.1:8444 --ca ~/.config/claw-master/ca.pem
```

It writes a launchd job (`~/Library/LaunchAgents/dev.claw-master.agent.plist`: started at login, restarted
if it exits, logging to `~/Library/Logs/claw-master-agent.log`, and anything it prints before logging starts
to `claw-master-agent.stderr.log` beside it), starts it, and waits until that agent answers a probe with its
token (an unrelated program on the port does not count). Only then does it set three entries in the `env`
of `~/.claude/settings.json`, which the CLI, the desktop app's Code tab and the IDE extensions all read:
`HTTPS_PROXY` (its URL carries the token, so the file is made private, mode 0600),
`NODE_EXTRA_CA_CERTS`, and `NO_PROXY=localhost,127.0.0.1,::1` (Claude uses one proxy for every scheme, and
the agent only tunnels CONNECT, so loopback MCP servers need the exemption). Other settings are kept; the
file is rewritten with its keys in sorted order, and the file as it was before the first install is kept as
`settings.json.before-claw-master`. It refuses to install while that `env` already sets another proxy setting
(`https_proxy`, which Claude prefers over `HTTPS_PROXY`, or `HTTP_PROXY`, `http_proxy`, `ALL_PROXY`,
`all_proxy`, `no_proxy`).
Restart the desktop app and running sessions afterwards. From then on every Claude Code on the Mac uses the
pool, and none falls back to the Mac's login while the agent or the server is down. Project settings and
`CLAUDE_CONFIG_DIR` can still override user settings, as for any setting. Cloud and SSH sessions of the
desktop app do not use the local agent.

`claw-master agent uninstall` (no flags) undoes what the install recorded in
`~/Library/Application Support/claw-master/agent-install.json`: it stops the job (and changes nothing if the
job will not stop), puts back the entries the install replaced, removes those it added (unless they were
changed since), and removes the plist.

`connect` refuses settings files whose `env` sets proxy or CA variables, with one exception: the entries
`agent install` writes, while they name a loopback address with the token in `agent.token` beside
`connect`'s own CA (or in its `--dir`) and that same CA. A launch through `connect` also sets the lowercase
`https_proxy`, which Claude prefers, so it keeps using its own forwarder and its tokens are still counted by
project.

On Windows, `connect`, `agent` and `agent env` work the same way (and `client-init` for a box with a
certificate): files that must be private (the agent token, `client.key`, the settings `agent install`
writes, logs) are created owned by you, with a protected DACL that grants only you, SYSTEM and Administrators
(an existing one is given back to you where Windows allows, so files an elevated run left owned by
Administrators heal), and a private file or directory is accepted only when its owner is you (or Administrators,
for an administrator) and nobody else but SYSTEM and Administrators has access. `login`, `run`, `serve` and
`probe` refuse: they need profiles, and subscription logins belong to a server. `check`, `issue` and
`account-key` work. Claude Code on Windows reads
`%USERPROFILE%\.claude\settings.json`, and managed settings from `C:\Program Files\ClaudeCode`.

`agent install` on Windows (same flags; `--ca` names the `ca.pem` copied to the PC) registers a Task
Scheduler task, `claw-master agent <your SID>` (task names are machine-wide, so each user of a shared PC has
their own), in place of the launchd job. It runs at your own logon, as you, with your normal rights, so it
needs no administrator (a logon trigger for any user would). It runs under `conhost --headless`, so no
console window opens, and with `--exit-with-parent`, because ending the task ends only that conhost.
conhost exits 0 whatever the agent does, so a failure cannot trigger a restart; instead the task is also
started every minute from the install on, which does nothing while it runs, so an agent that has ended is
back within a minute. Paths containing `%` are refused (Windows would expand them). Its definition, log and
record are in `%LOCALAPPDATA%\claw-master` (`agent-task.xml`, `agent.log`, `agent-install.json`); a hidden
agent has no console, so a failure to start is in the log. Everything else (the probe, the settings, the
refusals, `agent uninstall`) is as on macOS.

Elsewhere, run `claw-master agent` under the system's service manager and put the lines
`claw-master agent env` prints (same flags) into the settings `env` yourself. A box with a client
certificate uses `--server ADDRESS:PORT --dir DIR` instead of `--open` and `--ca`. The server logs the
agent's first tunnel as `client connected for the first time client=tunnel listener=open`, as for any
tunnel client.

Releases: every push to `feature/claude-master-inference` (a merge, or a run started by hand) is released by CI
(`.github/workflows/claw-master-release.yml`) as the pre-release `claw-master-<sha7>`, with
`claw-master-<os>-<arch>` for darwin-arm64, linux-amd64, linux-arm64, windows-amd64 and windows-arm64
(`.exe`) and their `SHA256SUMS`, after the claw-master tests pass on Linux (pull requests also run them on macOS
and Windows). A tag pushed by hand is built by `release.yaml` instead and carries the CLIProxyAPI archives only,
listed in its `checksums.txt`, not the `claw-master-<os>-<arch>` binaries. A binary uploaded to a release by hand
(some early `claude-master-*` releases) has no `SHA256SUMS`; check it against the digest GitHub records for the
asset (`gh api repos/OWNER/CLIProxyAPI/releases/tags/TAG --jq '.assets[] | [.name, .digest]'`).

### Stopping and rolling restarts

A stopping server drains: requests already running get up to `--drain-timeout` (default 60s) to
finish before they are cancelled, and a cancelled request is answered with a retryable `529
overloaded_error`, never a 499, so Claude Code retries it. Keep the service manager's stop timeout
(systemd `TimeoutStopSec`, default 90s) above the drain timeout.

For restarts no session notices, run two servers behind a TCP load balancer on the same box (for
example Envoy with a `tcp_proxy` listener on the client-facing address) and pass `--balanced` to
both. A rollout starts the idle server, points the balancer's new connections at it, then stops the
old one. A `--balanced` server that is draining keeps serving the connections it already has, and
each response closes its connection, so the client's next request opens a new connection, which the
balancer sends to the new server. Give the two servers distinct `--instance` names so their metrics
stay apart, and the same `--state-dir`: the CA and the server certificate are shared, and the
certificate must name the address clients dial (listen on that address with another port).

## Logging

claw-master has its own log, separate from the upstream SDK's output (which stays discarded: it can carry
credential paths and upstream response text). `serve` logs at **info** to stderr by default; `run` logs
nothing unless asked (its stderr is Claude's terminal), and then only to a file.

```bash
claw-master serve ... --log-level info --log-file /var/log/claw-master/claw-master.log --log-max-mb 20 --log-keep 10
claw-master run PROFILE --log-level debug --log-file ~/claw-master.log -- ...
```

`--log-level debug|info|warn|error|off`, `--log-format text|json`, `--quota-log-interval 5m` (a negative value
turns the snapshot off). A log file rotates by size (`file` becomes `file.1`, `file.1` becomes `file.2`, up to
`--log-keep`; the oldest is removed) and is `0600`.

**What is never logged:** tokens, request or response bodies, URLs, account identifiers (emails, UUIDs) or
upstream error text. Logs carry profile names (your own labels), a hashed conversation tag (`s-xxxxxxxx`),
model names, status codes, durations, counts, quota fractions and client certificate names. A redaction filter
backs that discipline up by masking secret-shaped keys and values.

| level | event |
|---|---|
| info | `inference account switched` (conversation moved: `from`, `to`, `reason` = `weekly_exhausted` (Anthropic rejected it and the poll agrees the week is used up), `rate_limited` (rejected for another limit), `rebalanced`, `subscriptions_exhausted`, `subscription_capacity_returned`) |
| info | `profile rate limited` / `profile available again`; `quota band changed` (`ok` / `reserve` at 90% / `exhausted`: what the poll says, not a refusal); `Anthropic accepted a request on a subscription the usage poll calls exhausted` (or `reserve`), once per profile per band |
| info | `login refreshed`, `login refresh adopted from another claw-master process`, `adopted a newer login after a 401` |
| info | `quota` per profile (weekly, plus `five_hour_used_pct` and `five_hour_resets_in`) and `routing summary` (requests per profile, switches, backup picks, refusals) every 5 minutes; `proxy summary` (connections, requests, active) every 5 minutes |
| info | `client connected for the first time` (certificate name, or `tunnel`), `proxy listening`, `inference backend started` |
| warn | `using the paid API-key backup` (once a minute), `no inference account could be chosen`, `profile credential rejected by Anthropic` (401/403: the login may need redoing), `Anthropic server error`, `login refresh failed`, `subscription usage poll failed`, `client TLS handshake failed` (once a minute per remote address) |
| debug | every routing decision (`account chosen`), `conversation bound`, every quota observation, each client tunnel |

## Metrics (OpenTelemetry)

> An overview of what the metrics answer, example CloudWatch queries and alarm suggestions are in
> [claw-master-aws.md](claw-master-aws.md#metrics-at-a-glance). The reference below is the full catalogue.

`serve` and `run` can export metrics over OTLP/HTTP to anything that accepts it (a CloudWatch agent, an
OpenTelemetry Collector): `--otlp-endpoint http://127.0.0.1:4318 --otlp-interval 30s`. Nothing in claw-master
is specific to a cloud. Every metric carries the resource attributes `service.name` (`claw-master`) and
`service.instance.id`, which is `--instance NAME` or by default the hostname, so several servers exporting to one
backend stay apart.

```bash
# which dashboard key is which Anthropic account? (the account id is never exported, only this key)
claw-master account-key 11111111-2222-3333-4444-555555555555     # -> acct-666ff6cc
# give an account a readable name in every dashboard
claw-master serve ... --account-label 11111111-2222-3333-4444-555555555555=alice --account-labels-file labels.txt
```

`serve` looks at `--account-labels-file` every minute and, when the file has changed (replace it with a rename),
loads it again and swaps the labels in: no restart. A file with a line that is not `ACCOUNT_UUID=NAME` is not
loaded; the labels already in use stay and the log says why.

**Dimensions.** `profile` (the subscription profile's own name, or `api-backup`), `client` (the connecting box's
certificate name, `tunnel` on the open listener, or `claw-master launch` for a launch's own Claude), `provider` (`claude` on
Claude measurements; absent on the pool's own), **`client_account`** (the INCOMING user's Anthropic account: your
label, else `acct-` and 8 hex of a hash; `unknown` when the request has none), `model`, `status_class`, `status`,
`stream`, `route`, `reason`, `result`, `window`, `measure`. A profile with no name is shown as a short hash, never
from its credential file name (those carry the account's email address).

| metric | kind | what it answers |
|---|---|---|
| `claw_master.inference.requests` {profile, client_account, status_class} | counter | which subscription served which user, and how it went |
| `claw_master.inference.requests.by_model` {model, status_class} | counter | traffic by model |
| `claw_master.inference.requests.by_client` {client, client_account} | counter | which box (certificate name) carries which user's traffic |
| `claw_master.inference.duration`, `.ttfb`, `.upstream_ttfb` {profile, status_class} | histogram ms | how fast: whole request, first byte, and Anthropic's own time to first byte |
| `claw_master.inference.duration.by_model`, `.ttfb.by_model` {model} | histogram ms | latency by model |
| `claw_master.proxy.overhead` {profile} | histogram ms | claw-master's own added time (request in to account chosen) |
| `claw_master.inference.duration_quantile` {profile, quantile 0.5/0.95/0.99} | gauge ms | recent p50/p95/p99, so CloudWatch can chart percentiles |
| `claw_master.inference.errors` {profile, status, client_account} | counter | Anthropic errors by status |
| `claw_master.inference.tokens` {profile, type}, `.tokens.by_client_account` {client_account, type}, `.tokens.by_client` {client, type}, `.tokens.by_project` {project, type} | counter | tokens each subscription (or `api-backup`) served, each user used, each box carried and each project asked for (`project` is the name of the git repository a launch was started in, or of its directory, sent by the launch's forwarder on its tunnel; `none` without one, `other` past 100 names per process); `type` is `input`, `output`, `cache_read` or `cache_creation` (input excludes the cache fields, output includes thinking), from the usage Anthropic reports in each successful response |
| `claw_master.inference.request_bytes`, `.response_bytes` {profile} | histogram | sizes |
| `claw_master.quota.used_fraction`, `.resets_in_seconds`, `.rate_limited_for_seconds`, `.band` {profile} | gauge | each subscription's weekly allowance, when it resets, any cooldown, band (0 ok, 1 reserve, 2 exhausted, -1 unknown) |
| `claw_master.quota.five_hour.used_fraction`, `.five_hour.resets_in_seconds` {profile} | gauge | each subscription's five-hour window from the usage poll (about every 3 minutes, idle or not, in a pool of two or more profiles or with a backup key); 0 once the window has reset, and no countdown while no window is open |
| `claw_master.anthropic.ratelimit` {profile, window, measure} | gauge | EVERY `Anthropic-Ratelimit-*` header: windows `5h`, `7d`, `api`; measures `utilization`, `resets_in_seconds`, `remaining`, `limit` ... |
| `claw_master.anthropic.ratelimit.state` {profile, window, measure, value} | counter | status words: `allowed`, `allowed_warning`, `rejected` |
| `claw_master.routing.picks` {profile}, `.switches` {from, to, reason}, `.backup_requests`, `.pick_duration` | counter / histogram | routing and failover |
| `claw_master.quota.rate_limited` {profile} | counter | times Anthropic rate limited a profile |
| `claw_master.auth.refresh` {profile, result}, `claw_master.auth.token_expires_in_seconds` {profile} | counter / gauge | login health |
| `claw_master.usage.polls` {profile, result} | counter | usage polls: `ok`, `failed`, `cache_hit` |
| `claw_master.proxy.connections` {listener, result}, `.active_connections`, `.tls_handshake_errors` | counter / gauge | clients and refused clients |
| `claw_master.requests` {route, client_account}, `claw_master.requests.by_client` {route, client} | counter | everything through the proxy (inference, count_tokens, control) |
| `claw_master.sessions.tracked`, `claw_master.process.*` | gauge | conversations tracked; uptime, goroutines, heap |

**Why the dimensions are not crossed.** A metrics backend such as CloudWatch bills every distinct combination of
a metric's attributes as its own series. Crossing profile, model, client and account on every metric would be
hundreds of series for a few users, so each axis has its own projection and no request or quota metric carries
more than three attributes besides `provider`; `anthropic.ratelimit.state` carries four (`profile`, `window`,
`measure`, `value`). You can still split by any one axis, and `inference.requests` gives
profile x account x status; what you give up is the full cross-product (say, one model on one box for one user).
Through the CloudWatch agent: counters arrive as deltas (a Sum is a count), histograms as approximate statistic
sets (Sum, SampleCount and Min/Max, no percentiles: use `inference.duration_quantile`), and the resource's
`service.name` and `service.instance.id` are extra dimensions (a query that names a metric's exact dimension set,
such as a Metrics Insights `SCHEMA(...)`, includes both).

**Bounded by design.** The model and the user's account come from the client's request, so neither is used as
written. A `model` must look like a Claude model name (it contains `claude`) and at most 64 distinct ones are
kept; at most 256 unlabelled accounts are kept; everything else is `other` (or `unknown` when absent). Labelled
accounts are bounded by your labels file. A client cannot create series, or put text of its own in a metric or
a log, by choosing a model or an account.

**Token counts** are read from the usage Anthropic reports in each successful response, on the bytes' way to the
client, without changing them: in a stream, `message_start` and then the cumulative `message_delta`; in a JSON
answer, its `usage` object. Only usage-sized stream lines and the last 64 KiB of a JSON answer are held while
reading. `count_tokens` is a question, not consumption, and is not counted. The paid API-key backup's tokens are
under `profile=api-backup`, so its cost can be derived.

Not exported: credentials, bodies, URLs, account ids, upstream error text.
