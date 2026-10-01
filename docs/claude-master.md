# Claude Master with separate inference subscriptions

`claude-master` launches the normal installed Claude Code client with its normal
master login. It intercepts only Claude inference requests and authenticates those
requests with one of several separately logged-in Claude subscription profiles.
Claude Code still owns the conversation, Remote Control, tools, permissions,
subagents, compaction, and model selection.

There is no second agent and no model allowlist. The proxy changes the inference
account; it does not replace Claude Code or reinterpret its protocol.

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

Known control and Remote Control routes continue to Anthropic with the master login.
Unknown Anthropic routes fail closed instead of accidentally using the master account
for inference. There is no Bedrock or master-account inference fallback. An explicitly
provided Anthropic API key can serve as the final backup after subscription quota is
exhausted; without one, inference stays subscription-only.

## Install, login, and run

Requirements are Go 1.26+, Linux or macOS, and `claude` on `PATH`:

```bash
go build -o claude-master ./cmd/claude-master
./claude-master check
```

The launcher uses the currently installed Claude Code executable. It does not pin,
download, downgrade, or silently update Claude Code. `check` validates the executable
and local settings without logging in or sending inference.

Create each inference profile with a normal Anthropic OAuth login:

```bash
./claude-master login claude-primary
./claude-master login claude-secondary
./claude-master login claude-tertiary
```

Each command opens the normal Anthropic authorization page. Sign in to the intended
subscription account, using a separate/incognito browser session when necessary. On a
remote host the command prints the callback/tunnel instructions. Profiles do not copy,
import, or modify the native master login.

Run one fixed inference profile:

```bash
./claude-master run claude-primary -- --remote-control
```

Run a quota-aware pool:

```bash
./claude-master run claude-primary \
  --next-profile claude-secondary \
  --next-profile claude-tertiary \
  -- --remote-control
```

Arguments after `--` go directly to Claude Code. Claude Code may use its default model
or its normal model flags:

```bash
./claude-master run claude-primary \
  --next-profile claude-secondary \
  -- --model claude-sonnet-5-5 --fallback-model haiku --remote-control
```

The launcher does not define or constrain those models. `probe --model` is different:
that flag selects the model only for the launcher's small diagnostic request.

### Optional final API-key backup

Supply a paid Anthropic API key from a file or an environment variable, never as a
literal key in process arguments:

```bash
./claude-master run claude-primary --next-profile claude-secondary \
  --backup-api-key /home/me/claude_api.txt -- --remote-control
```

The file must contain only the key (surrounding whitespace is trimmed), be a regular
file no larger than 16 KiB, and be readable by the launcher. Keep it private, for example
with `chmod 600 /home/me/claude_api.txt`. `file:/home/me/claude_api.txt` is also
accepted.

The dedicated environment variable is consumed automatically when exported. Read it
without echoing or putting it in shell history:

```bash
read -rsp 'Anthropic API key (final backup): ' CLAUDE_MASTER_BACKUP_API_KEY
printf '\n'
export CLAUDE_MASTER_BACKUP_API_KEY
./claude-master run claude-primary --next-profile claude-secondary -- --remote-control
unset CLAUDE_MASTER_BACKUP_API_KEY
```

The launcher uses this key only for a self-contained request after every subscription
has exhausted its credential-scoped quota. The 10% continuation reserve is still usable
subscription quota, not a reason to switch to the key. Authentication, request, model,
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
./claude-master run claude-primary --backup-api-key env:MY_KEY -- --remote-control
```

An explicitly selected variable must exist and be nonempty. The key is held only by the
proxy backend: it is not saved in a profile, passed to the native Claude child, or used
for the master login. `--backup-api-key env:ANTHROPIC_API_KEY` consumes an existing
standard API-key variable and removes it from native startup, so Claude Code continues
to use its normal subscription login and Remote Control. An explicit file or environment
source overrides the optional dedicated environment variable.

### Optional exact model mappings

Use repeatable `--map INCOMING:TARGET` options before `--` to change a model explicitly:

```bash
./claude-master run claude-primary --next-profile claude-secondary \
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
/home/me/CLIProxyAPI-claude-master/try-claude-master.sh --remote-control
```

Edit the short `profiles=(...)` list at the top of that script for the desired profile
names. It also consumes `CLAUDE_MASTER_BACKUP_API_KEY` automatically when exported;
the key does not require another profile or login. The helper extracts `--backup-api-key`
and repeatable `--map` options for the launcher; other arguments go to native Claude.
Use `--` to stop helper option extraction and pass everything after it literally:

```bash
/home/me/CLIProxyAPI-claude-master/try-claude-master.sh \
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

Quota state is refreshed from Anthropic's rate-limit response headers. A
credential-scoped `429` temporarily blocks that account until its retry/reset deadline;
it does not turn a five-hour rejection into exhausted weekly usage. Request-scoped,
model-scoped, authentication, validation, and transport failures do not drain or rotate
the account.

After startup, the usage API is polled every 60 seconds for all subscription profiles,
never the API-key backup. This retries failed startup reads and picks up weekly resets,
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

When another account still has capacity, the final 10% of an account is reserved for
continuations carrying account-bound state:

- Below 90%, a session remains on its selected account.
- At or above 90%, a self-contained request may move to another usable account.
- Opaque/account-bound continuations remain on their bound account and may consume the
  reserve.
- When every usable account is in its reserve, the earliest-reset account remains
  eligible and is drained instead of alternating accounts every turn.
- After the reported weekly reset, that account becomes eligible again.

The usage endpoint is an OAuth product endpoint used by Claude Code rather than a
publicly versioned API. Failure to read it does not block startup; routing falls back to
configured order and subsequently observed rate-limit headers.

## Payload-based continuation affinity

Claude's Messages API receives the complete active context on each request. Prompt
caching changes server work and billing, not the request into an opaque session handle.
After compaction, Claude Code sends the compacted active context rather than all
discarded history.

The selector therefore inspects the request body itself; it does not depend on a
version-specific "compaction happened" header. Ordinary text plus client-side
`tool_use`/`tool_result` history is self-contained and can move once the 90% boundary
is reached. A request stays on its current account when it contains known opaque or
provider-side state, including:

- container or uploaded/file identifiers;
- server-tool continuation state;
- encrypted content or redacted thinking;
- signed thinking or signed compaction blocks; or
- a payload that cannot be classified as valid JSON.

Bindings use Claude Code's conversation and agent hierarchy. A subagent can receive a
separate account for self-contained work; if its first request carries opaque state, it
inherits the parent agent's binding only if the parent has never changed accounts.
After a parent handoff, an unbound opaque child is ambiguous: its copied state
could predate the switch. The proxy refuses that request; a self-contained child
can still start normally, and already-bound children retain their own origins.
This lets independent fanout use separate subscriptions without guessing the
owner of account-bound continuation data.

An account handoff is deferred while another generation for the same session is
active. Clients must preserve chronological history under a session ID; replaying
an old opaque branch after a completed handoff requires its own previously bound
child/session ID, not reuse of the parent's current ID.

Only failures received before the response is exposed can be retried on another
profile. Once successful streaming headers are delivered, that request is never replayed.
Any upstream SSE error
remains exactly the event Claude Code received, and routing changes can affect only a
later request.

## Profiles and process boundaries

- Profiles live under `~/.local/share/claude-master/profiles/` in private directories.
  OAuth refresh persistence uses atomic replacement. A complete staged refresh
  left by an interrupted write is recovered while holding the profile lock.
- A launcher holds an exclusive lock on every profile in its pool until shutdown. Two
  concurrent launchers therefore need disjoint profile sets.
- The local CONNECT proxy uses a random process-only credential. Its temporary CA is
  trusted only by the child through `NODE_EXTRA_CA_CERTS`; it is not installed in the
  system trust store. Leaf certificates renew on new handshakes without interrupting
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

[`scripts/claude-master-gym/README.md`](../scripts/claude-master-gym/README.md)
describes the checked-in tmux gym. It runs two disjoint sessions on
`claude-sonnet-5-5`, applies `/effort ultracode` interactively, and asks each session to
fan out to two bounded read-only subagents. Start it after creating two normal profiles:

```bash
/home/me/CLIProxyAPI-claude-master/scripts/claude-master-gym/start.sh \
  claude-gym-a claude-gym-b
```

The gym records private pane/debug output plus sanitized routing evidence and includes
`status.sh`, `inspect.sh`, `drive.sh`, and `stop.sh`. Its version check compares the
installed client with Anthropic's current official release; updating is a separate,
explicit action.

## Verification and compatibility

Automated tests cover credential separation, weekly-quota parsing and selection, the
10% reserve, durable payload affinity, parent/subagent bindings, pre-response retry, raw compressed
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
