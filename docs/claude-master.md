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
redirected. The interceptor constructs fresh allowlisted inference headers, drops
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

Requires Go 1.26+, Linux, and native Claude Code **2.1.269**. The launcher rejects
other native versions until their routing behavior has been reviewed. Build on a
development server, not an administration jumpbox:

```bash
go build -o claude-master ./cmd/claude-master
./claude-master check
./claude-master login claude-work --provider claude
./claude-master login codex-work --provider codex
```

`check` validates the installed native version and local startup settings without
opening a profile, logging in, or creating a session. It is not a live inference test.

Each login starts a new provider authorization flow. Use the intended inference
account in that flow. Codex uses device authorization; Claude supports its callback
flow and manual callback entry. Do not paste native token files into a profile.

Start a new session with one account and an explicit model supported by that
provider's bundled CLIProxyAPI model catalog:

```bash
./claude-master run claude-work --model <claude-model-id> -- --remote-control
./claude-master run codex-work --model <codex-model-id> -- --remote-control
```

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

## Verification and rollout

Automated tests use synthetic credentials and fake HTTP/TLS upstreams. They check
credential separation, pinned model/account selection, path validation, streaming,
cancellation, shutdown, profile permissions, locking, and child environment setup.
Passing these tests does not establish live Remote Control compatibility or billing
behavior. The pristine upstream baseline currently fails
`TestOpenAICompatExecutorToolResultContentByInputModalities` in four subcases;
that unrelated existing failure must not be reported as a passing full suite.

Before enabling this for normal sessions, complete a separately authorized profile
login and a disposable session on the development server. Verify that it appears
in the Claude app, streams replies, executes an approved harmless tool through
Claude Code, and reports errors when the selected backend is unavailable without
using the master for inference. Verify both Claude and Codex backends separately.
Only then install the same reviewed build on other hosts. Do not restart aggregate
Claude services or migrate existing sessions as part of that test.

## Reference lineage

This fork starts at CLIProxyAPI commit
`ac02da6c05e18f465aa7e3ed5b0a65a2f060917d`. It reuses CLIProxyAPI's authentication,
provider executors, protocol translation, and pinned-auth request context. The
process-scoped interception design follows
[remote-claw](https://github.com/ejc3/remote-claw): retain the native control plane
and intercept only the intended API traffic. It does not modify the Claude binary
or repurpose remote-claw's existing relay/Bedrock mode.
