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
unknown end-to-end headers. Streaming bytes are forwarded as each upstream read
arrives; the proxy does not buffer, parse/rebuild, or synthesize SSE events. HTTP
hop-by-hop fields and connection framing are necessarily regenerated for the local
connection.

Known control and Remote Control routes continue to Anthropic with the master login.
Unknown Anthropic routes fail closed instead of accidentally using the master account
for inference. There is no API-key, Bedrock, or master-account inference fallback.

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

The repository helper builds the launcher, prompts for any missing normal logins, and
starts the configured pool:

```bash
/home/me/CLIProxyAPI-claude-master/try-claude-master.sh --remote-control
```

Edit the short `profiles=(...)` list at the top of that script for the desired profile
names.

## Weekly quota selection

For a multi-profile run, startup asks Anthropic's OAuth usage endpoint for each
profile's weekly utilization and reset time. New sessions use the usable account whose
weekly quota resets soonest, draining the quota that will be replenished first. The
configured profile order is the deterministic fallback when usage is unavailable or
reset times tie.

Quota state is refreshed from Anthropic's rate-limit response headers. A confirmed
credential-scoped weekly `429` also marks that account exhausted. Request-scoped,
model-scoped, authentication, validation, and transport failures do not drain or rotate
the account.

When another account still has capacity, the final 10% of an account is reserved for
continuations carrying account-bound state:

- Below 90%, a session remains on its selected account.
- At or above 90%, a self-contained request may move to another usable account.
- Opaque/account-bound continuations remain on their bound account and may consume the
  reserve.
- When every usable account is in its reserve, the earliest-reset account remains
  eligible rather than stranding available tokens.
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
inherits the parent agent's binding. This lets independent fanout use separate
subscriptions without moving account-bound continuation data.

Only failures received before any response bytes are exposed can be retried on another
profile. Once streaming begins, that request is never replayed. Any upstream SSE error
remains exactly the event Claude Code received, and routing changes can affect only a
later request.

## Profiles and process boundaries

- Profiles live under `~/.local/share/claude-master/profiles/` in private directories.
  OAuth refresh persistence uses atomic replacement.
- A launcher holds an exclusive lock on every profile in its pool until shutdown. Two
  concurrent launchers therefore need disjoint profile sets.
- The local CONNECT proxy uses a random process-only credential. Its temporary CA is
  trusted only by the child through `NODE_EXTRA_CA_CERTS`; it is not installed in the
  system trust store.
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
10% reserve, payload affinity, parent/subagent bindings, pre-byte retry, raw compressed
responses, exact streaming chunks, request/response header boundaries, token counting,
profile locking, and cancellation/shutdown.

Trusted-contributor CI downloads Anthropic's current official Claude Code artifact,
checks its published manifest checksum and reported version, then runs a credential-free
CONNECT/TLS/header/SSE smoke test. The repository intentionally has no Claude Code
version constant.

The historical transport audit exercised 53 releases from 2.1.209 through 2.1.270 in
both synthetic API-key and OAuth modes. Follow-up smoke tests through 2.1.285 found no
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
