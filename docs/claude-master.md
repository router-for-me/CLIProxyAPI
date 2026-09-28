# Claude master with separate inference profiles

`claude-master` keeps native Claude Code as the agent and Remote Control client,
while routing inference through a separately authorized Claude subscription. One
profile can be fixed for a run, or an explicit profile series can drain those
subscriptions in order.
This is an experimental integration, not a vendor-supported subscription gateway.
The account holder must authorize each inference login separately and review the
providers' current subscription and credential-use terms. A working interception
mechanism does not establish permission to reuse a subscription in another client.

## Architecture

```text
Claude app <--> native Claude Remote Control <--> Claude Code (master login)
                                                    |
                                      process-local HTTPS interceptor
                                           /                    \
                         control, using master auth       inference only
                                  |                            |
                           api.anthropic.com        embedded CLIProxyAPI
                                                              |
                                    ONE fixed Claude login (default), or
                                  ordered Claude logins drained in sequence
                                      with native-selected models
```

Claude Code owns the conversation, permission prompts, tools, and filesystem
changes. The inference backend returns model output; it does not run another agent
or execute tools independently. Native Remote Control may send conversation state
as part of its normal operation; separating inference does not hide the session
from the master account.

Only exact POST requests to `/v1/messages` and `/v1/messages/count_tokens` are
redirected. The interceptor preserves the reviewed native protocol header names,
values, and multi-value order, including user agent, client software/OS/architecture,
request correlation, compression negotiation, feature betas, and the Claude Code
conversation session. The request body and native-selected model reach the Claude
executor unchanged. At the final upstream boundary, only the account-specific
parts are substituted: the master bearer/API key is discarded, the selected
profile's OAuth bearer is installed, and `metadata.user_id` receives the selected
account and device while retaining its conversation session and other native
fields. Known control endpoints keep their original master authentication and query
string and go to the fixed Anthropic origin. Unknown
Anthropic routes fail closed instead of falling through to master inference.

The backend is an in-memory HTTP handler: no management API, public inference
listener, dashboard, credential watcher, remote model updater, quota balancing,
or unconfigured account rotation. In the default single-profile mode, exhaustion,
authentication errors, and unsupported models are errors, not reasons to use
another subscription. The ordered mode advances only under the narrow condition
documented below. There is no API-key or Bedrock billing fallback.

## Build and use

Requires Go 1.26+, Linux or macOS, and an installed native Claude Code **2.1.269**. The launcher
resolves the reviewed executable before starting; if the default CLI has advanced,
it uses the existing versioned 2.1.269 installation. It does not download a version,
roll back the default CLI, or change global update settings. Background updates are
disabled only in the child environment. Build on a
development server, not an administration jumpbox:

```bash
go build -o claude-master ./cmd/claude-master
./claude-master check
./claude-master login claude-work
```

On macOS, build and run these commands as the same Mac user who owns the native
Claude login. Apple Silicon and Intel builds are separate binaries; a Linux build
cannot run on a Mac. The launcher uses the same native installation discovery and
private profile layout on both platforms. Native macOS Keychain credentials remain
owned by Claude Code: the launcher does not read, export, import, or replace them.
Its temporary CA is passed only to the child, never installed in Keychain or system
trust. A Mac inference profile requires its own authorized login; do not copy a
profile from a development server.

`check` validates the installed native version and local startup settings without
opening a profile, logging in, or creating a session. It is not a live inference test.

After authorizing the intended account, test the selected backend with one small,
fixed prompt before starting the native client:

```bash
./claude-master probe claude-work --model <claude-model-id>
```

`probe` sends only its built-in test prompt, with no tools or project context. It
reports an HTTP status, whether the expected reply matched, and a fixed error-stage
label, never a raw response or token. It uses the same profile lock and account pin
as `run`. Its required `--model` is a diagnostic input and does not configure the
profile or a later Claude `run`.

Each `login` starts a fresh, normal Claude OAuth authorization and never imports the
native Claude Code login. It opens the Anthropic sign-in page when a local browser
is available; otherwise it prints the authorization URL. Sign in as the intended
subscription account. On a remote host, use the printed SSH-tunnel instructions or
paste the full callback URL when prompted. Use a separate or incognito browser
session for each profile so the intended account is selected. The resulting OAuth
credential is stored only in that profile; existing profiles are never overwritten.
Do not paste native token files into a profile.

Start a new session with one account. Native Claude Code selects the model:

```bash
./claude-master run claude-work -- --remote-control
```

For a Claude backend, the launcher does not inject or pin a model. Claude Code may
use its normal default selection or its native `--model` and `--fallback-model`
options; put those native options after the launcher separator:

```bash
./claude-master run claude-work -- --model sonnet --fallback-model haiku --remote-control
```

The backend preserves the model on each Claude request. Model and fallback changes
do not change the selected inference login. There is no local allowed-model list;
Anthropic decides whether the selected subscription can use the requested model.

### Ordered drain-then-advance profiles (Claude only)

`run` accepts repeatable `--next-profile` flags to form an explicit order. The
positional profile is always first, and flags are appended in command-line order:

```bash
./claude-master login claude-primary
./claude-master login claude-secondary
./claude-master login claude-tertiary

./claude-master run claude-primary \
  --next-profile claude-secondary \
  --next-profile claude-tertiary \
  -- --remote-control
```

Every profile must be a separately authorized Claude login. The series orders
credentials, not models: native Claude Code can select any supported Claude model
for each request, and qualifying credential exhaustion advances the whole series
regardless of the current model. Omitting `--next-profile` retains single-profile
behavior.

The series always begins with its first profile when `run` starts. Progress is
process-local and is not persisted, so a restart begins with the first profile
again. The launcher acquires and holds the exclusive lock for every profile in the
series for the full run, including credential-refresh persistence and shutdown.
If any profile cannot be locked, loaded, or validated, startup fails rather than
running a partial series.

Advancement has one exact predicate: the active profile must produce a structured
provider result with HTTP status 429 that is classified as credential-scoped.
Request-scoped errors and short, model-scoped rate limits do not qualify, nor do
authentication, transport, validation, or other failures. Those failures remain
errors and do not select the next profile.

When a qualifying rejection arrives before response output begins, the same
request is retried on the next profile; it may continue through later configured
profiles if they are also drained. A rejection after streaming output has begun
is not replayed, but the next request starts on the next profile.

Advancement is monotonic within a run: after moving forward, the series never
returns to an earlier profile and never wraps from the last profile to the first.
When no later profile exists, inference fails closed. At no point does the proxy
send an inference request through the native master login; that login remains only
on the native control path.

For troubleshooting, add `--diagnostics` before `--`. It prints numeric counts of
accepted/rejected proxy connections, parsed requests, inference/control dispatches,
blocked routes, and active connections every five seconds and at shutdown, plus
a fixed backend error-stage label at shutdown. It does
not log URLs, headers, prompts, responses, or account identifiers. An inference
dispatch count means the adapter was called, not that the provider accepted it.

The master Claude login remains the native login for the current Unix user. In the
default single-profile mode, the selected inference profile is fixed for the
lifetime of the launched process; Claude Code remains free to choose models while
using that credential. Switch inference profiles by ending that session and
launching another profile. In ordered mode, only the explicit monotonic advancement
described above can change the active profile. Profile locking allows only one login
or running launcher per profile, preventing competing token refresh writers.
Separate profiles can run concurrently only when they are not members of the same
running ordered series; a series holds every member's lock.

## Credential and host boundaries

- Profiles are under `~/.local/share/claude-master/profiles/`, with private
  directories and credential files. Metadata records the provider and pinned auth
  ID, not a token. A profile's `current/` directory atomically contains its
  `profile.json` and `auth/` directory. Unexpected credentials, unsafe paths, and
  provider mismatches are rejected. Token refresh uses atomic, fsynced replacement;
  shutdown waits for in-flight background refresh persistence before unlocking.
- No native `~/.claude` credential is imported, replaced, or copied
  between Unix users or hosts. Existing sessions and systemd services are untouched.
- The local CONNECT listener has a random process-specific proxy credential. The
  child receives it only through its environment; it is not printed or persisted.
- An ephemeral certificate authority is trusted only by the child process through
  `NODE_EXTRA_CA_CERTS`. No system trust store is modified.
  The certificate expires after seven days; end and relaunch experimental sessions
  before then. A continuously running fleet service would need certificate renewal.
- Only `api.anthropic.com` is TLS-terminated. Other HTTPS destinations use blind
  CONNECT tunnels. This is routing isolation, **not an operating-system network
  fence**; tools and subprocesses may make their own network connections.
- Inference request bodies, provider tokens, and raw upstream errors are not logged
  by this launcher. Native Claude Code retains its own normal session/log behavior.
- No jumpbox broker or dev-to-jumpbox connection is involved. Each host and Unix
  user owns its own profiles and local process.

Do not launch with alternate Anthropic base URLs, API-key overrides, or alternative
provider settings. Startup checks reject incompatible environment, flags, and
known local settings. These checks are not lifetime enforcement: native settings
can change after startup, and server-managed policy is outside the local file
preflight. Do not change routing settings or project directories during an
experimental session. Claude updates can introduce new routes or change proxy/TLS
behavior; fail-closed errors must be investigated before expanding the route
allowlist or native-version pin.

The reviewed executable pin covers the launched parent. Native subprocess launch
paths can select the user's default Claude executable, so full subagent behavior
is not yet certified across differing installed versions. An old version can also
be removed by native installation cleanup; if the reviewed version is unavailable,
the launcher refuses to start. Do not roll this experimental build out as an
unattended fleet service yet.

## Verification and rollout

Automated tests use synthetic credentials and fake HTTP/TLS upstreams. They check
credential separation, credential pinning, Claude request-model preservation,
path validation, streaming, cancellation, shutdown, profile
permissions, locking, and child environment setup.
The trusted-contributor CI matrix runs these tests on Linux, Apple Silicon macOS,
and Intel macOS. Cross-compilation establishes build compatibility only; an actual
macOS test run is required before claiming runtime verification. Native Remote
Control with a real Mac login remains a separate acceptance test even when the
synthetic macOS tests pass.
Passing these tests does not establish live Remote Control compatibility or billing
behavior. The pristine upstream baseline currently fails
`TestOpenAICompatExecutorToolResultContentByInputModalities` in four subcases;
that unrelated existing failure must not be reported as a passing full suite.

On September 13, 2026, the ARM development-server test passed direct Claude OAuth
inference, native streamed replies, and an actual native Bash `pwd` tool turn using
the independently authorized profile. Native Remote Control reported an active
session. Phone-side round-trip confirmation, full native subagent behavior, and
unattended fleet rollout remain separate acceptance gates.

On the same date, the launcher and ordinary server built on an Apple Silicon Mac,
the six affected packages and targeted executor header tests passed with the race
detector, and the verified native 2.1.269 Darwin executable passed the credential-free
CONNECT/TLS/header/SSE test in both synthetic API-key and OAuth modes. This did not start a
personal inference profile or validate live Mac Remote Control. The built
launcher's `check` also passed against existing local settings, using a scratch
`PATH` entry for the reviewed executable rather than changing the default. The Mac's existing
default Claude 2.1.263 installation, login, Keychain, and sessions were left alone;
the tested 2.1.269 executable and builds lived only in disposable scratch storage.

Before enabling this for normal sessions, complete a separately authorized profile
login and a disposable session on the development server. Verify that it appears
in the Claude app, streams replies, executes an approved harmless tool through
Claude Code, and reports errors when the selected backend is unavailable without
using the master for inference. Only then install the same reviewed build on other
hosts. Do not restart aggregate
Claude services or migrate existing sessions as part of that test.

## Compatibility contract and version audit

The September 13, 2026 audit inspected the last 50 stable GitHub releases, 2.1.209
through 2.1.270, plus published npm/native versions 2.1.213, 2.1.242, and 2.1.243:
53 verified Linux ARM64 artifacts in total. The two inference paths and seven
bridge-work lifecycle patterns were present throughout; no normalized endpoint
change was found from 2.1.269 to 2.1.270. Adjacent-version comparisons located SDK
package-version header changes at 2.1.228 and diagnostics beta/body additions at
2.1.246. These observations do not certify all payloads, runtime feature flags,
or service-side behavior. Sources: [official releases](https://github.com/anthropics/claude-code/releases),
[npm metadata](https://registry.npmjs.org/@anthropic-ai/claude-code), and
[native manifests](https://downloads.claude.ai/claude-code-releases/2.1.270/manifest.json).

Credential-free basic native CONNECT/TLS/SSE inference smoke tests with the revised
header boundary passed all 53 Linux versions in both synthetic API-key and OAuth
modes under the race detector: 106 native test cases. Unknown native header names
fail the compatibility test rather than being silently omitted. The
current header boundary is separately covered through the real
executor's synthetic non-streaming, streaming, token-counting, and error paths,
plus the reviewed native executable's synthetic transport test. No live OAuth
header capture or all-feature compatibility claim follows from those tests.

The audit found an existing conditional Remote Control gap in the original proxy:
native legacy `/v1/sessions` calls were blocked with HTTP 403 throughout the
inspected versions, including the reviewed 2.1.269 pin. Native feature flags choose
between legacy and `/v1/code/sessions` operations; this was not a newly introduced
2.1.270 regression. The compatibility fix permits only native create, read, title
update (`PATCH`), events, archive, and unarchive operations. CLI requests require
the reviewed BYOC beta and an organization identifier. Native bridge-worker event
and archive callbacks instead use the reviewed environments beta and a valid
runner-version header; that profile is not accepted for other legacy operations.
Managed-agent betas are always rejected. Creation additionally requires the native
`remote-control` source, events and session context, and exactly one environment or runner-pool
identifier; unknown payload fields are refused. The whole subtree remains closed
because it also contains managed-agent operations. Synthetic acceptance and
rejection tests cover this gate; full live Remote Control behavior and server-side
feature-flag variations still require separate verification.

Header preservation is an explicit in-process native-adapter opt-in, not a public
HTTP flag or a change to ordinary CLIProxyAPI clients. It keeps measured protocol
headers, including the conversation session and additional-protection flag, while
intentionally substituting only the selected account's authorization, account UUID,
and device ID. Native remote-container and remote-session context is retained;
master authorization, cookies, API keys, and account headers are not. Unknown custom
headers remain excluded. HTTP framing and TLS bytes are not byte-identical to a
direct native connection. This is a bounded
compatibility contract, not a promise of perfect native equivalence or readiness
for unattended fleet rollout.

## Reference lineage

This fork starts at CLIProxyAPI commit
`ac02da6c05e18f465aa7e3ed5b0a65a2f060917d`. It reuses CLIProxyAPI's authentication,
provider executors, protocol translation, and pinned-auth request context. The
process-scoped interception design follows
[remote-claw](https://github.com/ejc3/remote-claw): retain the native control plane
and intercept only the intended API traffic. It does not modify the Claude binary
or repurpose remote-claw's existing relay/Bedrock mode.
