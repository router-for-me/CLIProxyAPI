# Claude Code with native Claude and Codex models on Windows

## Routing contract

The `claude` PowerShell function starts normal Claude Code with permission
prompts disabled. It does not change provider or compaction environment.

The `claude-codex` function points Claude Code at CLIProxyAPI and uses:

| Claude class | Routed model |
| --- | --- |
| Opus | `gpt-5.6-sol` |
| Sonnet | `gpt-5.6-terra` |
| Haiku | `gpt-5.6-luna` |
| Fable 5 selected through `/model` | native `claude-fable-5` |

The wrapper sets `CLAUDE_CODE_ALWAYS_ENABLE_EFFORT=1` so Claude Code sends its
selected effort even for custom gateway model IDs. It starts at `xhigh` unless
the caller supplies `--effort`, and `/effort` can change the level during an
interactive session. The model names deliberately have no `(xhigh)` suffix:
suffix-based thinking has higher priority inside CLIProxyAPI and would otherwise
override Claude Code's per-request selection.

Claude Code's **Ultracode** workflow sends `xhigh` model effort plus additional
client-side orchestration. There is no `ultra` API effort value. Use Ultracode
when that workflow is desired, or select `max` with `/effort max` (or
`--effort max`) when maximum model inference effort is desired. CLIProxyAPI
passes both `xhigh` and `max` through to compatible Codex models. The Codex
client's separately named **Ultra** preset is also client-side and is converted
to `max` before a Responses API request. Claude's request does not expose an
Ultracode marker distinct from ordinary `xhigh`, so the proxy must not silently
upgrade every `xhigh` request to `max`.

The wrapper temporarily removes Claude Code's client-side compaction overrides
only for `claude-codex`. This lets the proxy trigger Codex-native compaction at
240,000 logical input tokens and enforce a 272,000-token hard boundary. Normal
`claude` retains its native Claude/Fable context and compaction behavior.

When native compaction is enabled, the Anthropic `/v1/models` response reports
a virtual 1,000,000-token input window for models supplied by the Codex
provider. This delays Claude Code's client compaction while the proxy repeatedly
compacts at the real 240,000-token trigger. Native Claude/Fable entries keep
their provider-reported window, so selecting Fable in the same `/model` picker
still uses Claude Code's normal client compaction.

Do not set `CLAUDE_CODE_MAX_CONTEXT_TOKENS` globally for this hybrid setup. A
global value would also shrink the native Fable lane.

## Proxy configuration

Add this block to the canonical YAML configuration:

```yaml
codex:
  native-compaction:
    enabled: true
    trigger-tokens: 240000
    context-window: 272000
    claude-client-context-window: 1000000
    preserve-recent-tokens: 32000
    retained-message-tokens: 64000
    state-ttl: 168h
```

`claude-client-context-window` is picker metadata, not the upstream Codex
limit. Values at or below zero use the conservative 1,000,000-token default.
CLIProxyAPI still triggers native compaction at `trigger-tokens` and enforces
`context-window`. The override applies only to models currently supplied by
the Codex provider; native Claude/Fable and generic OpenAI-compatible providers
are unchanged.

Claude Code may impose an internal maximum or change how it interprets model
metadata in a future release. In that case this setting remains best-effort;
the proxy's native compaction and hard boundary continue to apply, but Claude
Code could still compact earlier than the advertised value.

The proxy first uses Codex's current Responses v2 protocol: it appends
`{"type":"compaction_trigger"}` to an otherwise normal `/responses` request
and advertises `remote_compaction_v2`. It accepts the result only after one
opaque compaction item and `response.completed` are observed. An unsupported v2
request falls back to `/responses/compact`. Transient transport or stream
failures retry v2 once and never permanently downgrade a conversation.

The proxy retains a recent exact tail outside compaction so the user's active
turn and tool-call pairs are not split. After compaction, later Claude Code
requests are rewritten as the saved replacement history plus only the exact
post-boundary delta. This produces one expected cache-root transition at a
successful compaction, then restores append-only cache continuity.

Claude-originated Codex turns also reuse the most recent valid encrypted
reasoning item. Replay is inserted before compaction is planned, so it is
either included once in the summarized prefix or kept once in the exact tail.
The durable source boundary is still calculated only from client-owned history;
the transient replay item never becomes a source hash or a second append.
Rejected encrypted reasoning is tombstoned per Codex credential and session,
without deleting the auth-independent replay cache used for failover. Companion
tool calls remain available so recovery cannot orphan a tool output.

Claude Code's session header identifies the conversation, while its agent
header identifies each worker. The proxy scopes Codex prompt-cache identity,
reasoning replay, and native-compaction state by both values. Interleaved Opus
workers can therefore use different tool envelopes without resetting or
recompacting one another's history. Headerless requests keep a distinct main
conversation lane for compatibility with older clients.

If native compaction has a transient failure below 272,000 tokens, the current
valid lane is sent and a warning is logged. If Codex rejects encrypted state,
the proxy atomically retires the suspect summary, rebuilds from authoritative
Claude history, suppresses only implicated reasoning, preserves tool pairs, and
retries once. A summary rejected by the following generation is treated as the
first suspect so unrelated recent reasoning is not blacklisted. At or above the
hard boundary the request fails explicitly instead of silently discarding
history. Compaction state is committed only after a validated terminal response
and an atomic durable write. The agent-aware state files live under
`<auth-dir>\state\codex-native-compaction\claude-code-compaction-v2-*.json`;
they are versioned, checksum-verified, and expire with `state-ttl`. Authenticated
v1 files are retired automatically because their session-wide keys cannot be
safely reused by agent-scoped lanes. The state
directory and files receive a user-only ACL on Windows (and `0700`/`0600`
permissions on Unix-like systems); periodic sweeps remove expired valid lane
files while leaving unrelated or corrupt evidence untouched. Do not delete
active files during a Claude Code conversation: they preserve the exact
post-compaction cache root across proxy restarts.

## Authentication and the `/model` picker

Both Codex and Claude OAuth must be active in the same CLIProxyAPI instance.
Run the interactive logins from the fork binary when either provider has no
usable auth file:

```powershell
C:\Code\CLIProxyAPI\bin\cli-proxy-api.exe -codex-login
C:\Code\CLIProxyAPI\bin\cli-proxy-api.exe -claude-login
```

Claude OAuth is what allows native Fable selected inside `/model` to remain on
Claude. A stale Claude auth file produces `503 auth_unavailable` even when the
three default Claude classes route successfully to Codex.

Optional extra picker labels can be added without replacing the native class
mapping:

```yaml
oauth-model-alias:
  codex:
    - name: gpt-5.6-sol
      alias: claude-codex-opus
      fork: true
    - name: gpt-5.6-terra
      alias: claude-codex-sonnet
      fork: true
    - name: gpt-5.6-luna
      alias: claude-codex-haiku
      fork: true
  claude:
    - name: claude-fable-5
      alias: claude-native-fable
      fork: true
```

Leave `force-mapping` disabled. The class environment variables remain the
primary Opus/Sonnet/Haiku mapping.

The installed PowerShell launcher accepts an existing listener on port 8317
only when its executable path is the fork binary and both the health endpoint
and authenticated observability endpoint respond successfully. An unrelated
or unhealthy listener fails explicitly instead of being mistaken for the
proxy.

## Observability

Every upstream attempt emits one metadata-only `request_event` line with
provider, resolved model, operation, input/output/cache tokens, estimated cost,
failure state, and latency.

Inference failures that happen before an executor can publish usage (for
example `auth_unavailable` or request validation failures) produce one
unpriced synthetic event. A per-request publication marker prevents a normal
executor record from being counted a second time.

- A reported cache miss is red. A low-reuse request, where more than half of
  normalized input was not served from cache, is also red without being
  reclassified as a strict miss.
- A compaction is magenta unless the same attempt is a cache miss, in which case
  red takes precedence.
- Redirected output and file logs never receive ANSI escape sequences.

The fixed TUI row shows request count, input/output tokens, strict cache misses,
low-reuse requests, successful compactions, compaction-lane resets, and
estimated cost. Resets are metadata-only diagnostics with hashed lane, agent,
and tool-envelope identifiers; they do not expose prompts or raw IDs and do not
inflate request/token/cost totals. Failed compaction attempts remain in the
event log and are exposed separately as `compaction_attempts` by:

```text
GET /v0/management/observability
```

When file logging is disabled, a remote TUI still populates its Logs tab through
the endpoint's cursor-based metadata-only event feed. A bounded-buffer gap or
server restart is shown explicitly instead of silently dropping events. This
keeps colored request tracking available without persisting prompt-bearing
error logs. The cost row shows `partial` when any request is unpriced and `—`
when no priced request exists; values are estimates, not subscription charges.
Fable cache writes use the provider's reported TTL breakdown: 5-minute writes
are estimated at `$12.50/MTok`, 1-hour writes at `$20/MTok`, and an
unclassified remainder is conservatively estimated at the 1-hour rate.

The Logs tab defaults to a fixed-header request monitor with these columns:
provider, model, effort, input, output, cache read, cache write, cache-read
percentage, input cost, output cost, and cache cost. Press `v` to switch between
the request table and ordinary raw server logs. Cache misses and low-reuse rows
remain red; compaction rows remain magenta.

OpenAI Responses usage includes cached reads but the Codex subscription endpoint
currently reports `cache_write_tokens: 0` even while the next request reuses the
new prefix. For Codex rows, the proxy therefore displays the uncached portion as
a compatibility estimate for cache-eligible prompts (1,024+ input tokens) and
prefixes it with `~`; metadata logs also emit
`cache_write_estimated=true`. With
`codex-claude-estimate-cache-write-usage: true`, the same estimate is returned
to Claude Code as `cache_creation_input_tokens` so its cache-creation counter is
useful. This is an explicit compatibility mode because Anthropic's wire schema
cannot label that counter as estimated. It is not provider-confirmed and is
never used for cache-write pricing: the provider's reported zero remains
authoritative for cost, and the estimated portion remains normal OpenAI input.
Provider-confirmed GPT-5.6 writes use OpenAI's documented 1.25x input rate.
Native Claude/Fable cache-write values stay provider-reported.

For bounded request diagnostics on this workstation, use:

```yaml
request-log: true
request-log-success-summary: true
request-log-summary-rotation-hours: 5
request-log-summary-max-files: 48
error-logs-max-files: 50
logs-max-total-size-mb: 1024
codex-claude-estimate-cache-write-usage: true
```

Successful requests then append one masked, body-free JSON object to a
`request-summary-*.log` file. Files use fixed five-hour UTC windows, and the
oldest summary windows are removed after the configured limit. Failed requests,
including streamed failures detected after downstream HTTP 200 headers, retain
a full `error-*.log` diagnostic. Those error files can include prompts, source,
tool inputs/results, and response content; treat them as sensitive. Temporary
streaming parts are removed after the final summary or error log is committed.

Run `-tui -standalone` for an all-in-one foreground instance. When attaching a
TUI to the background proxy, configure a loopback-only management secret. This
fork lets the pure TUI client inherit `MANAGEMENT_PASSWORD` when `-password` is
omitted, so the secret does not need to appear in process arguments or logs.
