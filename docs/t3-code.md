# T3 Code on auto-vps

T3 Code's server runs on auto-vps. Every other machine is a client. Each provider that can go
through the estate hub does: one Claude instance and one Codex instance, both on the hub, never
several accounts inside T3 (Waseem, 28 Oct 2026, following Theo's advice in
[theo-gist-2026-10.md](theo-gist-2026-10.md)). Every thread runs in **Full access**.

Last verified: 2026-10-28 on auto-vps (T3 0.1.2). Each instance below answered a one-word thread
started with `t3 thread start . "Reply with the single word pong" --provider <id> --model <model> --wait`.

## Where things are

| What | Where |
|---|---|
| Server | user unit `t3code.service`, `http://127.0.0.1:3773`; tailnet `https://automancer-vps.taild6c90e.ts.net:3773` (Tailscale Serve) |
| T3 home | `~/.t3` (settings `~/.t3/userdata/settings.json`; secrets in T3's own secret store, never in that file) |
| Provider config dirs | `~/.config/t3-hub/{claude,codex,pi,opencode}` (mode 700; no keys in them) |
| Settings as code | [estate-hub-devices/t3-hub-instances.mjs](estate-hub-devices/t3-hub-instances.mjs): writes every instance, the defaults and the hub usage source; its header has the run command |
| Hub keys | BWS `AUT_HUB_ACCESS_KEY` (instances, as sensitive env vars), `AUT_HUB_MANAGEMENT_KEY` (T3's usage panel) |

The script is the source of truth. A `providerInstances` write replaces the whole map, so edit the
script and rerun it rather than changing one instance through the API.

## Instances

| Instance id | Name in T3 | Reaches models through | Models |
|---|---|---|---|
| `claude-hub` | Claude (hub) | hub: `ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CONFIG_DIR=~/.config/t3-hub/claude` | T3's Claude list (Fable, Opus, Sonnet, Haiku) |
| `claude-hub-pinned` | Claude (hub, pinned) | as above, plus `ANTHROPIC_CUSTOM_HEADERS=X-CPA-Account: 1` | same |
| `codex-hub` | Codex (hub) | hub: `CODEX_HOME=~/.config/t3-hub/codex`, provider `cliproxy`, websockets on | Sol, Astra, Luna, and Terra as a custom model |
| `pi-hub` | Pi (hub) | hub: `PI_CODING_AGENT_DIR=~/.config/t3-hub/pi`, provider `hub` only | the hub's 25 non-Claude models (`hub/kimi-k3`, `hub/glm-5.3-flash`, ...) |
| `opencode` | OpenCode | hub provider from `OPENCODE_CONFIG=~/.config/t3-hub/opencode/opencode.json`, plus OpenCode's own logins | `hub/*`, `opencode/*` free models, `opencode-go/*` |
| `grok` | Grok | its own login (`grok login` on auto-vps); Grok also covers the Cursor models, so there is no Cursor instance | `t3` lists them |
| `muse` | Muse Code | its own login (`muse login` on auto-vps) | Wonder, Wonder Full, Spark |
| `antigravity` | Antigravity | T3's managed Antigravity runtime and its own Google sign-in (not the `agy` CLI's login) | listed after sign-in |

The built-in `claudeAgent`, `codex` and `cursor` instances are switched off. The Claude and Codex
config dirs link back to Waseem's instructions, skills, hooks and plugins in `~/.claude` and
`~/.codex`, but hold no login, so his own seat is untouched. Live lists: Settings → Providers, or
`orchestrator_capabilities` from any T3 agent.

Defaults: new threads and all eight projects start on Claude (hub) Opus in Full access; delegated
children inherit Full access. Thread titles and commit text use Codex (hub) Luna. Settings →
Providers → Usage shows the hub's pooled limits (usage source `hub`, kind `cliproxy`).

## Pins

The hub's `X-CPA-Account` header picks an account or group ([estate-hub.md](estate-hub.md#account-pins)).
T3 has no per-thread environment, so a pin is a property of an instance:

- Claude: pick **Claude (hub, pinned)**. It pins group `1`. To pin another group, change
  `ANTHROPIC_CUSTOM_HEADERS` in the script and rerun it, or add another pinned instance there.
- Codex: the `cliproxy` provider maps the header from `CODEX_HUB_ACCOUNT`. A pinned Codex instance is
  a copy of `codex-hub` in the script with `CODEX_HUB_ACCOUNT=<group>` added to its environment.

A thread can switch between Claude (hub) and Claude (hub, pinned) because they share a config dir.

## Delegation

Any T3 agent can start children on other instances with the `delegate_task` tool. Checked 28 Oct
2026: a Claude (hub) Sonnet thread delegated one child to Codex (hub) Terra and one to Pi (hub)
`hub/kimi-k3`, waited, and reported both replies. Both children show under the parent as child
threads. The hub log on m6 showed the parent on Claude Gmail Max 20, the Codex child on Codex Pro
Gmail and the Pi child on the Kimi login (`ssh m6 grep "new binding" ~/Library/Logs/hub.log`).

## Gandalf's route (designed, not wired)

Gandalf runs in a Cloudflare Durable Object, not on auto-vps. He reaches auto-vps services through
the `estate-gateway` tunnel behind a Cloudflare Access service token, like `llm-svc` and
`autodash-svc`.

T3's own MCP endpoint (`/mcp`) cannot serve him without approvals:

- Its sign-in is OAuth with an approval page; the grant lasts 30 days and has no refresh, so Waseem
  would re-approve every month.
- A session issued from the CLI (`t3 auth session issue --subject mcp-client`) needs no approval but
  is capped at **Supervised**: it was refused `runtime_mode_escalation_denied` both when starting a
  Full-access thread and when archiving one.

So the route is a small wrapper on auto-vps:

1. A user unit on `127.0.0.1:<port>` that checks `Authorization: Bearer <AUT_T3_GANDALF_TOKEN>` and
   runs `t3 thread start | send | show | list` (the [host scripts](https://github.com/pingdotgg/t3code/blob/main/docs/user/outside-agents.md#scripts-on-the-host)
   route: no sign-in, project defaults, so Full access). Arguments go as argv to `t3`, never a shell;
   messages go on stdin (`-`).
2. A hostname such as `t3-svc.automancer.uk` on the `estate-gateway` tunnel, added to the
   "estate-gateway (Executor)" Access app, as the hub cut-over's `hub-svc` step does.
3. An Executor app (or a Gandalf tool) holding the Access service token and the bearer.

BWS `AUT_T3_GANDALF_TOKEN` (project personal) holds the wrapper's bearer. It is a random value, not a
T3 session, so it never expires and T3 never asks for approval. The chair wires it after Waseem's trial.

## Still needs Waseem

- **Antigravity sign-in**: open T3 (desktop app, or `https://automancer-vps.taild6c90e.ts.net:3773`
  on the tailnet), Settings → Providers → Antigravity → **Sign in**, choose Google, finish in the
  browser. Until then its threads fail with "Sign in to Antigravity in Settings".

## Updates and versions

`t3 update` to 0.1.3 rolled back on 27 and 28 Oct (`~/.t3/runtime/service-state.json`): 0.1.3 binds
`0.0.0.0` when run as a managed service, and Tailscale Serve already holds the tailnet address on
:3773 (`EADDRINUSE`). The drop-in `~/.config/systemd/user/t3code.service.d/host.conf` sets
`T3CODE_HOST=127.0.0.1`, so the next update should start; it has not been retried.

Linked environments (one T3 driving another machine's threads, Settings → Labs) is not in 0.1.2 or
0.1.3 stable. It first ships in the nightly train (`v0.1.4-nightly.20261027.3796`). 0.1.3's Labs page
only has Guest links. Switching auto-vps to nightly is the chair's call (`t3 update --channel nightly`).
