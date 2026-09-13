# Claude master with separate inference profiles

`claude-master` keeps native Claude Code as the agent and Remote Control client,
while routing inference through one explicitly selected Claude or Codex login.
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
                                              ONE pinned profile + model
                                                (Claude OR Codex login)
```

Claude Code owns the conversation, permission prompts, tools, and filesystem
changes. The inference backend returns model output; it does not run another agent
or execute tools independently. Native Remote Control may send conversation state
as part of its normal operation; separating inference does not hide the session
from the master account.

Only exact POST requests to `/v1/messages` and `/v1/messages/count_tokens` are
redirected. The interceptor preserves the reviewed native protocol header names,
values, and multi-value order, including user agent, client software/OS/architecture,
request correlation, compression negotiation, and feature betas. It drops
the master account's credentials, and the backend removes `metadata.user_id` and
pins the model and credential. Known control endpoints keep their original master
authentication and query string and go to the fixed Anthropic origin. Unknown
Anthropic routes fail closed instead of falling through to master inference.

The backend is an in-memory HTTP handler: no management API, public inference
listener, dashboard, credential watcher, remote model updater, or automatic
account rotation. Exhaustion, authentication errors, and unsupported models are
errors, not reasons to use another subscription. There is no API-key or Bedrock
billing fallback.

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
./claude-master login claude-work --provider claude
./claude-master login codex-work --provider codex
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
as `run`.

Each login starts a new provider authorization flow. Use the intended inference
account in that flow. Codex uses device authorization; Claude supports its callback
flow and manual callback entry. Do not paste native token files into a profile.

Start a new session with one account and an explicit model supported by that
provider's bundled CLIProxyAPI model catalog:

```bash
./claude-master run claude-work --model <claude-model-id> -- --remote-control
./claude-master run codex-work --model <codex-model-id> -- --remote-control
```

For a Claude backend, the launcher supplies the same exact model ID to native
Claude Code. A native `--model` override must match it exactly; drifting aliases
such as `sonnet` and fallback models are rejected. This matters because native
Sonnet 5 request features are not valid for a pinned Sonnet 4.6 backend. Codex model
translation remains a separately gated live test.

For troubleshooting, add `--diagnostics` before `--`. It prints numeric counts of
accepted/rejected proxy connections, parsed requests, inference/control dispatches,
blocked routes, and active connections every five seconds and at shutdown, plus
a fixed backend error-stage label at shutdown. It does
not log URLs, headers, prompts, responses, or account identifiers. An inference
dispatch count means the adapter was called, not that the provider accepted it.

The master Claude login remains the native login for the current Unix user. The
selected inference profile is fixed for the lifetime of the launched process.
Switch by ending that session and launching another profile/model. Profile locking
allows only one login or running launcher per profile, preventing competing token
refresh writers in this first version. Separate profiles can run concurrently.

## Credential and host boundaries

- Profiles are under `~/.local/share/claude-master/profiles/`, with private
  directories and credential files. Metadata records the provider and pinned auth
  ID, not a token. A profile's `current/` directory atomically contains its
  `profile.json` and `auth/` directory. Unexpected credentials, unsafe paths, and
  provider mismatches are rejected. Token refresh uses atomic, fsynced replacement;
  shutdown waits for in-flight background refresh persistence before unlocking.
- No native `~/.claude` or `~/.codex` credential is imported, replaced, or copied
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
credential separation, pinned model/account selection, path validation, streaming,
cancellation, shutdown, profile permissions, locking, and child environment setup.
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
session. Phone-side round-trip confirmation, Codex OAuth inference, full native
subagent behavior, and unattended fleet rollout remain separate acceptance gates.

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
using the master for inference. Verify both Claude and Codex backends separately.
Only then install the same reviewed build on other hosts. Do not restart aggregate
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
headers; it intentionally substitutes the selected account's authorization and
session identity. Master account, remote-container, remote-session, and
additional-protection identity headers are not forwarded to inference. Unknown
custom headers remain excluded. HTTP header ordering, framing, compression, and
TLS bytes are not byte-identical to a direct native connection. This is a bounded
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
