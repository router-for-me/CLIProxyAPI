# Estate hub: one proxy for every AI subscription

Coordinator: bb thread thr_jniue4739r (Waseem's interactive session). Opened 2026-10-26.

## Goal (Waseem, 26 Oct 2026)

Every machine on the estate (auto-vps, mbp, wasnewpc, omarchy, naz, m6) stops holding its own
Claude/Codex/other logins. One always-on home box, the Mac mini **m6** (residential UK IP, never
sleeps), runs this fork of CLIProxyAPI with every subscription signed in, published on the tailnet at
`https://waseems-mac-mini-m6.taild6c90e.ts.net:8317`. Claude Code, Codex, pi, T3 Code and bb point at it.
Method: Theo's guide, copied to `docs/theo-gist-2026-10.md`.

This fork must replace three estate tools:

- **cswap** (Claude account rotation, `~/.local/share/uv/tools/claude-swap`, CLI `cswap`) and
  **cxswap** (Codex rotation, `/opt/automancer/bin/cxswap`, 2.8k-line Python, read its docstring and
  `rank`/consume-first logic): both pick the account whose allowance window **resets soonest** and
  skip one that is spent or held out ("consume-first"). Upstream CLIProxyAPI only does round-robin,
  weighted-round-robin and fill-first. Theo says soonest-reset-first is the one thing he forked for.
- **Pinning**: `cswap run <slot> -- …` / `cxswap run <slot> -- …` / bb `[pin:<account>]` pin one
  lane to one account. The hub needs the same: a client must be able to ask for one specific account
  (or a named group) and be refused or held when it is at its limit, instead of silently rotating.
- **sysdash / allowances / codexbar** (`/opt/automancer/ops/sysdash/`: `subs.py` collector,
  `sysdash.py` terminal dashboard, `allowances` wrapper; `~/.local/share/codexbar-cli`): Waseem wants a
  **web dashboard** on the hub that shows what sysdash shows (every plan, used/left, reset time,
  consume-first order, which account is active, holds), better than the stock management panel.
  That is a **second lane, later**: not in scope for the first lane, but design the routing data so
  the dashboard can read it (per-credential usage, reset, hold, pin state via the management API).

Hard rules from Waseem: never serve other people's traffic; Claude subscriptions are only ever used
through Claude Code. Fable is Waseem's own seat: never dispatch a lane on it.

## Lanes

| Lane | Deliverable | Route | State |
|---|---|---|---|
| L1 routing | `consume-first` routing strategy + account pinning, merged to `main` of Automancer-Ltd/CLIProxyAPI | bb thread, Claude Code, Opus 5.5, high | merged 2026-10-26 from bb thread thr_cu7cfdji97 (tracker t8305 has the PR and merge SHA; L2 t8304; migration t8306) |
| L2 dashboard | web dashboard replacing sysdash/codexbar, plus the usage refresher | bb thread, Claude Code, Opus 5.5 | merged 2026-10-26 from bb thread thr_yh5dttfgcu (tracker t8304 has the PR and merge SHA) |
| L3 design + parity | full frontend treatment of /hub, banked resets, will-it-last, API-key balances | bb thread, Claude Code, Opus 5.5, high | dispatched 2026-10-26 21:40 as bb thread thr_b5ufx4t2am; PR #3 (tracker t8318 has the merge SHA) |

Base for L1: upstream `main` at 1f917e77 (fork point). Keep the diff upstream-mergeable: new files
where possible, config-driven, no rewrites of existing strategies.

## L1 brief

Deliverable, in this repository:

1. **Routing strategy `consume-first`** (`routing.strategy: "consume-first"`): among the credentials
   that can serve the requested model/provider, prefer the one whose relevant allowance window resets
   soonest, skipping any that is at its limit (429/quota cooldown), disabled, or held. Read how the
   proxy already learns each credential's quota/reset (the quota page Theo screenshots; cooldown
   state; provider usage endpoints) and build on that; if the proxy has no reset-time data for a
   provider, fall back to that provider's existing order and say so in the docs. Session affinity must
   keep working (a thread stays on its account; cache writes must not explode). Mirror the semantics
   of cxswap's ranking where they are sensible; document any deliberate difference.
2. **Pinning**: a request can name the account or account group it must use, and the proxy either
   serves it from that account or returns a clear error when that account is at its limit or held.
   Choose the mechanism that fits upstream best (per-API-key account allowlist in `access.api-keys`,
   a request header, or both) and document it. Holds ("take this account out of rotation for N
   hours", like `cxswap disable`) must be settable without a restart, via the auth JSON `disabled`
   flag and/or the management API.
3. **Docs**: `docs/estate-hub.md` in this repo: config example, how pinning and holds work, how a
   dashboard reads the state. Keep `docs/estate-hub-plan.md` (this file) current: cursor, findings,
   decisions.
4. Tests for the strategy and the pin refusal (Go, beside the code). `go build ./...` and
   `go test` on touched packages green. Go 1.26 toolchain: use what `go version` gives on auto-vps,
   or install the required toolchain into the worktree's GOTOOLCHAIN cache, never system-wide.

Done when: one PR to `main` of Automancer-Ltd/CLIProxyAPI is merged with the strategy, pinning,
holds, docs and tests, and the plan file cursor says what L2 needs.

Scope: this repository only. Do **not** touch m6, the live hub config, BWS, or any other estate
tool. Do not sign in to any provider. Never commit auth files, keys or tokens. Never `rm` a glob,
variable or relative path. One PR for the slice; push when you expect green. No second review is
required: check your own diff, merge, report.

Report format (final message): what shipped (PR URL, merge SHA), the exact config keys an operator
sets, what the strategy does when reset data is missing, open questions for L2, anything you
deliberately left out.

## L1 outcome (2026-10-26)

Operator detail lives in `docs/estate-hub.md`. What shipped:

- `routing.strategy: "consume-first"` (`sdk/cliproxy/auth/selector_consume_first.go`). It ranks on the
  quota headers each credential already records from its own responses. No new polling.
- Holds: auth JSON `"hold_until": "<RFC3339>"`, live via `PATCH /v8/management/credentials/fields`.
  `disabled` remains the open-ended hold.
- Pins: request header `X-CPA-Account: <account or group>`. Groups are the auth JSON `"group"`
  field (comma-separated). Refusals: 400 unknown pin, 429 + `Retry-After` held or at limit, 503 disabled.
- `GET /v8/management/credentials` entries gain `consume_first` (`reset_at`, `used_percent`, `spent`),
  `hold_until` and `group`.

Decisions:

- Rank on the longest (weekly) window, as cswap does. The 5-hour window only marks an account spent.
  The 95% spent line and the "window not started" rule are cxswap's. No 5-minute reset margin: the hub
  picks per new conversation, so nothing ping-pongs. Spent accounts are a last resort, not refused.
- Pin by header, not a per-API-key map. `access.api-keys` is a plain list in the v8 schema, and both
  Claude Code (`ANTHROPIC_CUSTOM_HEADERS`) and Codex (`http_headers`) can send a header. Per-key pins
  wait until a client cannot send headers.
- Holds and pins are enforced in the per-request eligibility filter, which every strategy, the
  scheduler fast path and mixed-provider routing already call. Hold expiry needs no event.
- The deprecated v0 management API is untouched (AGENTS.md), so v0 `PUT /routing/strategy` rejects
  `consume-first`. Set it in the YAML or through `/v8/management/config`.
- The fork has no CI: GitHub Actions has never run on it (fork workflows stay off until someone
  enables them in the Actions tab). L1 therefore ran the full `go test ./...` once locally. The fork
  also inherited upstream's `auto-retarget-main-pr-to-dev` workflow, which would move every PR against
  `main` onto upstream's `dev` branch once Actions are on. L1 disabled that one workflow. Undo:
  `gh workflow enable auto-retarget-main-pr-to-dev -R Automancer-Ltd/CLIProxyAPI`.

Findings to verify once m6 has logins (inferred, not seen on a live response):

- Claude utilization is read from `anthropic-ratelimit-unified-5h-utilization` / `-7d-utilization` as
  a 0-1 fraction, which is how Claude Code reads it. The repo's own fixtures only show `-7d-status`
  and `-7d-reset`. Check one account's `quota.signals` in the credential listing. If utilization is a
  percent, every Claude account looks spent and the order still follows the weekly reset.
- The quota snapshot is in memory only. After a hub restart every account ranks "unknown" (ID order)
  until it serves one request.
- Figures refresh only when an account serves traffic. That is accurate while the hub is the only
  user of each account.

What L2 (dashboard) needs:

- Source: `GET /v8/management/credentials` with the management key. Per account it has provider,
  label, email, `disabled`, `status`, `unavailable`, `next_retry_after`, `hold_until`, `group`,
  `priority`, `quota.signals` + `quota.observed_at`, `consume_first`, `recent_requests`.
- Router order: sort as described in `docs/estate-hub.md` ("Reading the state").
- Actions: hold or clear with `PATCH /v8/management/credentials/fields {name, hold_until}`; disable
  with `PATCH /v8/management/credentials/status`. Strategy is in `GET /v8/management/config`.
- Not available: a single "active account" (the hub binds per conversation; `recent_requests` shows
  which accounts are serving), Claude plan names, and fresh figures for idle accounts.
- Repo detail: upstream's `.gitignore` ignores `docs/*`, so a new doc is added with `git add -f`.
- Open question for L2: refresh idle accounts by calling the providers' usage endpoints through
  `/v8/management/requests/api-call` or the quota plugins. That would also close the restart gap.

## L2 outcome (2026-10-26)

Operator detail is in `docs/estate-hub.md` ("Usage refresher", "Dashboard"). What shipped:

- Dashboard at `/hub`, one embedded HTML file (`internal/api/hub_dashboard.html`), management key in
  local storage like the stock panel. Reads `GET /v8/management/credentials/usage` (new: every file
  credential plus config-file API keys, with `usage` and `cooldowns`, and the strategy). Hold,
  release, disable/enable, name (`note`) and groups go through the existing L1 endpoints.
- Usage refresher: `routing.usage-refresh.interval` (off by default) and `.providers`. Polls idle
  accounts of Claude, Codex, Kimi, Grok (`xai`), Antigravity and Muse (`meta`, 3-hour floor).
  Claude and Codex readings become quota headers, so consume-first ranks idle accounts. The last
  reading persists in `<auth-dir>/usage-snapshot.cache`, closing the restart gap L1 noted.

Not verified against a live account (only fixtures taken from CodexBar's source, commit 5de59e1):
every provider's usage endpoint as called with the hub's own token. Check once on m6 with the
refresher on: each account's `usage.error` on `/hub` should be empty. Most likely to need a fix:
Kimi (CodexBar also sends `X-Msh-*` device headers), Antigravity (summary endpoint only, no
`fetchAvailableModels` fallback) and Grok (`subscriptionTier` may be an enum, not a display name).
Claude's header utilization scale (L1's open question) also still needs one live response: the
refresher writes a 0-1 fraction, matching how L1 reads it.

Left out: usage for API-key upstreams (deepseek, commandcode, zai, opencode-go, local), Claude
plan names (every account reports the same tier; name accounts with `note` on the page), Codex
banked resets, CodexBar's predicted "will last" pace, and a record of which client pinned
which account (the hub keeps none; groups and pin targets are shown).

## L3 outcome (2026-10-26)

Operator detail is in `docs/estate-hub.md` ("Usage refresher", "Dashboard"). Product context for
the page is in `PRODUCT.md` (inferred from the brief, not confirmed by Waseem). What shipped:

- Redesign of `/hub`, phone first: "Spend next" (per provider, the next account's tightest window or
  balance, with a pace mark), "Running low", "Banked resets", then every account as one row that opens
  to its windows, facts and actions. Salmon paper and serif headings in light mode, slate in dark;
  bars show what is left, not what is used. The phone page went from about 6,600 px to 3,400 px tall
  on the demo seed.
- Pace ("will it last") is computed in the page from each window's used percent, length and reset,
  CodexBar's formula; sysdash's `+N spare` / `on pace` labels plus `runs out <time>`.
- Codex banked resets: the Codex poll also reads `/wham/rate-limit-reset-credits`; a busy Codex
  account is polled at least hourly so the next-to-spend account still reports them.
- Config-file API keys are polled by base-URL host: DeepSeek balance, CommandCode (5-hour, weekly,
  USD left of the monthly grant), GLM coding plan (5-hour, weekly, monthly MCP calls), OpenCode Go
  (5-hour, weekly, monthly). New `usage` fields `balance` and `banked_resets`. No new config keys;
  `usage-refresh.providers` accepts the four new names.

Not verified against a live account: every new endpoint (shapes from CodexBar's source, commit
5de59e1). After m6 runs this build, check on `/hub` that DeepSeek, CommandCode, GLM and OpenCode Go
show figures with no "Poll failed" mark, and that a Codex account with a banked reset lists it.
Seen in passing, not changed: editing an account's name or groups drops its quota headers until the
next refresher tick (within a minute) restores them from the snapshot.

## L4 outcome (2026-10-28): one graphical table

Waseem rejected v3 on 28 Oct ("I want one table, shown graphically, with proper logos for each
service, showing a bar, allowance left, in spend-first order, resets shown alongside ... closer to
what sysdash shows"). Lane bb thread thr_8zinmqbui3 rebuilt `/hub` as one list; operator detail is in
`docs/estate-hub.md` ("Dashboard"), his words are principle 5 in `PRODUCT.md`. Same data source and
actions, no new config keys. Logos: lobe-icons 1.101.0 (MIT) inlined as an SVG sprite; "local" (the
estate gateway) has no brand and gets a drawn server mark. One independent design critique (Opus,
screenshots only) ran; its accepted fixes are in. Rejected: swapping the Codex, OpenCode and
CommandCode marks (they are those brands' own logos) and a Next tag on single-account providers.

Banked resets were never missing from the hub. The 26 Oct v3 screenshot was taken two minutes after
the deploy, before the refresher's first Codex poll; v3 rendered against 28 Oct data lists all of
them. The hub holds 5 of the 7 that `allowances` counts: the other 2 belong to Codex Team GDP, which
was deliberately not signed in on the hub (26 Oct). Banked resets persist in the usage snapshot, so a
restart keeps them.

## Device matrix (Waseem, 26 Oct 2026: "any agent from any device" by Tue 28 / Wed 29 Oct)

Last verified: 28 Oct 2026 (m6, mbp, omarchy, wasnewpc). On mbp Waseem is signed in to Muse, agy, Devin, Grok and Kimi:
`devin auth status` and `grok models` report it, Muse and Kimi hold their credential files, agy is his word.

Work devices only: wasnewpc, omarchy (same GPU box, Linux boot; one boot up at a time), mbp, m6. naz and the
Beelink HA box are excluded. Cell values: **hub** = points at m6 and works; **login** = CLI-only tool, Waseem logs
in on that device; **install** = not installed yet; **check** = installed, hub support unverified. Read with
`command -v <tool>` on each device; auto-vps is the reference set (all fourteen installed, 26 Oct 2026).

| Harness | m6 | wasnewpc | mbp | omarchy | Route |
|---|---|---|---|---|---|
| Claude Code | hub | hub | hub | hub | hub (`ANTHROPIC_BASE_URL` + `ANTHROPIC_AUTH_TOKEN`) |
| Codex | hub | hub | hub | hub | hub (`model_providers.cliproxy`) |
| OMP | hub | hub | hub | hub | hub provider in `~/.omp/agent/models.json` (non-Claude models) |
| pi | hub | hub | hub | hub | hub provider in `~/.pi/agent/models.json` |
| opencode | hub | hub | hub (fixed 28 Oct: npm postinstall had not run, so the binary did not start) | hub | hub provider in `~/.config/opencode/opencode.json` |
| Muse CLI | login (launcher installed 26 Oct) | login (Windows launcher installed 28 Oct, 1.4.14) | signed in (1.4.14) | login? (installed) | own login; Muse models via hub for the tools above |
| agy (Antigravity) | login (installed 26 Oct) | login? (installed) | signed in (Waseem, 28 Oct; 1.4.9) | login (binary copied from auto-vps 26 Oct) | own login |
| Devin CLI | login (installed 26 Oct) | login (installed 28 Oct, 3000.11.228) | signed in (installed 28 Oct, 3000.11.228) | login (binary copied from auto-vps 26 Oct) | own login; Devin models via hub for the tools above |
| Grok CLI | hub (default) | hub (default; npm, 28 Oct) | hub (beside Waseem's own grok.com login, which stays default) | hub (default) | hub models in `~/.grok/config.toml` (see below) |
| Kimi CLI | hub (default) | hub (default; 2.4.0, 28 Oct) | hub (beside Waseem's own Kimi login, which stays default; CLI is 0.20.3) | hub (default) | hub provider in `~/.kimi-code/config.toml` (see below) |
| dsh (DeepSeek) | installed, key needed | installed, key needed | installed, key needed | installed (npm; mise cannot resolve its rc version), key needed | API key from BWS, or hub `deepseek-flash` through OMP/pi |
| commandcode | installed, key needed | installed, key needed | installed, key needed | installed (mise), key needed | API key from BWS, or hub `commandcode/*` through OMP/pi |
| cursor-agent / agent | install | install | installed | installed | own login (Cursor); no hub path |

Grok CLI and Kimi CLI both take a custom OpenAI-compatible endpoint, checked 28 Oct 2026 against Grok's
`~/.grok/README.md` ("Custom Models") and Kimi's docs (`providers` in config.toml). Each device carries two hub
models per tool: Grok `[model.hub-grok-build]` (`grok-build-0.1`) and `[model.hub-grok-4-7]` (`grok-4.7`), Kimi
`[providers.hub]` (`type = "openai"`) with `hub/kimi-k3` and `hub/kimi-k3.1-pro`. `base_url` is the hub's `/v1`
and the key is the same hub access key as Claude Code's. Where the tool is not signed in, the hub model is the
default; where Waseem is signed in (mbp), his own login stays the default and the hub models are picked with
`grok -m hub-grok-build` or `kimi -m hub/kimi-k3`. All four answered a one-word prompt on m6, omarchy, mbp and wasnewpc.
Apply with `ssh <host> 'python3 -I -' < docs/estate-hub-devices/hub-grok-kimi.py` (macOS, Linux) or run
`docs/estate-hub-devices/hub-grok-kimi.ps1` with `pwsh -File` on wasnewpc; both are idempotent and never print the key.
On wasnewpc, run installers with `pwsh`, not `powershell`: the ssh shell is PowerShell 7, and Windows PowerShell 5.1
started from it cannot load `Get-FileHash`, so Devin's installer fails its checksum step.

"login?" means installed but whether it is signed in on that device is unverified. Omarchy done 26 Oct 22:30: Claude Code, Codex, OMP, pi, opencode on the hub (all answered or list 25 hub models); Herdr there is the Arch package 0.9.1-2 in /usr/bin (update with the package manager, not `herdr update`); auto-vps saved in its machines. The GPU box is one machine: wasnewpc is unreachable while omarchy is booted and vice versa.

## auto-vps cut-over (t8306)

Waseem's ruling (28 Oct 2026, through the chair): the auto-vps cut-over runs last, with him watching and
not mid-session, after the home lanes finish. Nothing in it restarts bb; the account-pin release reloads
only that plugin. Confirm with the chair before step 5 all the same. Every step is verified before the next.

What each step installs is a draft PR, unmerged until its step (an hourly sweep merges green non-draft PRs):

| Piece | PR |
|---|---|
| `estate-hub-route`, `estate-hub-key`, codex-seat hub pins | Automancer-Ltd/automancer-bin#104 |
| allowances / sysdash read the hub | Automancer-Ltd/automancer-ops#1335 |
| account-pin plugin pins through the hub | Automancer-Ltd/bb-plugin-account-pin#16 |
| Gandalf's Executor hub usage tool | Automancer-Ltd/automancer-ops#1336 |
| ROUTES.md and capacity.md | Automancer-Ltd/automancer-skills#133 |

Already done on 28 Oct: hub groups (Claude `1`-`10` = the old cswap slots, Codex `pro`, `pro-gmail`,
`pro-personal` = the old cxswap names) and notes (the allowances names) on every Claude and Codex login;
`~/.config/estate-hub/env` (mode 600) on auto-vps; `~/.codex/config.toml` carries the `cliproxy` provider
with its `model_provider` line commented out (the route is off). Backups of both harness configs from
before the 18:23 trial: `~/.claude/settings.json.pre-hub-20261028`, `~/.codex/config.toml.pre-hub-20261028`.

**Flip-back at any point:** `estate-hub-route off` (step 1 installs it; before that,
`/opt/automancer/worktrees/bin-t8306-hub/estate-hub-route off`). New Claude Code and Codex sessions use
the local fallback login; running ones keep the route they started with.

0. **Pre-flight, read-only.** `estate-hub-route status` says the hub answers; `/hub` shows no poll errors;
   save `bb account-pin list --json` to scratch (172 Codex pins, no Claude pins on 28 Oct).
1. **bin.** Merge #104. `umask 022; git -C /opt/automancer/bin pull --ff-only`; check `stat -c %a` is 755 on
   `estate-hub-route`, `estate-hub-key`, `codex-seat`; `ln -s /opt/automancer/bin/estate-hub-{route,key}
   ~/.local/bin/`. Verify: `estate-hub-route status` reads local/local and the hub answers. codex-seat's
   change is inert while the route is off (its test proves the local path is unchanged).
2. **pi, OMP, opencode** (adds a provider called `hub`; nothing defaults to it). Copy m6's `hub` provider
   block (25 non-Claude models) into `~/.pi/agent/models.json` and `~/.omp/agent/models.yml` with
   `apiKey: "!/home/ubuntu/.local/bin/estate-hub-key"`, and into `~/.config/opencode/opencode.jsonc` under
   `provider.hub` (that file is mode 600; opencode takes the key literally). Verify: `pi --list-models | grep
   hub`; one pong each through `pi --provider hub`, `omp`, `opencode run -m hub/gpt-6.1-terra`; the hub log
   (`ssh m6 tail ~/Library/Logs/hub.log`) shows them.
3. **Claude Code and Codex.** `estate-hub-route on`. Verify one pong each: `claude -p --model
   claude-opus-5-5`, `codex exec -m gpt-6.1-terra`; then pins: `ANTHROPIC_CUSTOM_HEADERS="X-CPA-Account: 3"`
   lands on group 3, `CODEX_HUB_ACCOUNT=pro-personal` on pro-personal (hub log `session-affinity ... auth=`
   lines; the per-account counters mix in other devices' traffic, so read the log). Codex needs
   `supports_websockets = true`: over plain HTTP streaming it hangs against the hub (seen 28 Oct).
4. **allowances.** Known differences, accepted: Codex Team GDP and its banked resets drop out (not on the
   hub); Claude 5h shows only for accounts used in the current window; CommandCode stays STALE until the
   hub's CommandCode key passes its usage endpoint (HTTP 401 today). Merge #1335, `git -C /opt/automancer/ops pull --ff-only`. Verify `allowances` and
   `allowances --json` show hub figures with the hub as rotation owner; the bb allowances panel renders.
5. **bb pins.** First clear the 23 Codex threads pinned to `team` (the held-out GDP Team seat, not on the
   hub): `bb account-pin list --json`, then `bb account-pin clear <thread>` for each idle one (a running
   one refuses; it is refused at its next turn after the release, with the re-pin command). Merge #16 and
   run its `scripts/release.sh` (reloads that plugin only, prints
   the rollback path). Verify: `bb account-pin list` reads hub usage; spawn two throwaway lanes,
   `claude-code` titled `[pin:3]` and `codex` titled `[pin:pro-personal]`, each answering one word; the hub
   log shows those accounts. Archive them.
6. **Docs and Gandalf.** Merge #133 (skills sync carries it). Gandalf's tool, per #1336's body: merge and pull,
   enable the `estate-hub-usage` user unit (127.0.0.1:8319), run `scripts/hub-svc-cutover.sh` dry then
   `--apply` (adds `hub-svc.automancer.uk` to the estate-gateway Access app, tunnel and DNS), create and
   deploy the `hub-readonly` Executor app. **Waseem's hands:** connecting its account means typing the
   estate-gateway Access service token into Executor's connect form, as for local-gateway and autodash.
   Verify with `executor-tool call hub-readonly.usage` (provider codex).
7. **Fallback logins and rotation off.** Keep one local login per harness, Automancer's own and least used
   this week, and separate OAuth grants from the hub's (the hub signed in fresh on m6):
   Claude slot 1 (`waseem@automancer.uk`, Claude Auto Max 20): `cswap switch 1`; Codex `pro-personal`
   (`waseem@automancer.uk`, Codex Pro Auto): `cxswap switch pro-personal` (check the verb with `cxswap
   --help`). The GDP team seats that cswap and cxswap hold out were not chosen: they belong to a client
   organisation and Waseem held them out. Then stop and disable `cswap-auto.service`,
   `cswap-auto-refresh.timer`, `cswap-auto-upgrade.path` and `cxswap-auto.service` (`systemctl --user`;
   the bin AGENTS.md asks for operator approval before touching cxswap-auto: Waseem watching is that).
   Leave the binaries; put `RETIRED-<date>.md` beside `~/.local/share/uv/tools/claude-swap` and a line in
   `/opt/automancer/bin/README.md`, and file a tracker task dated seven days out to remove them.
8. **Muse tunnel.** Repoint pi's `muse` provider to the hub's Muse models first and verify one pong; then
   stop and disable `muse-uk-proxy.service` and `muse-uk-tunnel.service`. The `muse` CLI on auto-vps uses
   the same CONNECT proxy for its own login: run `muse exec` once after; if Meta geo-gates it, keep the
   two units and record that.
9. **Callers to repoint or retire** (each read before changing; most are ops PRs):
   Gandalf's voice bridge (`~/.local/share/cos-voice-bridge/bridge.mjs`, chief-of-staff `src/codex-voice.ts`)
   runs `cxswap run active` and a ChatGPT login of its own (the hub cannot carry realtime voice): pin it with
   `COS_VOICE_ACCOUNT=pro-personal`, the fallback login. Ops: `guardian/checks.d/codex-shared-account.sh`,
   `scripts/codex-shared-account-gate.mjs`, `codex-account-pool.mjs`, `codex-account-rollouts.mjs` (Codex
   lane admission and forecasts read cxswap); `scripts/auth-token-expiry.py`, `automancer-auth-health.sh`,
   `auth-liveness-probe.sh`, `guardian-probe.sh`, `guardian-respond.sh` (local login health, which will
   alert on the retired logins); `automancer-nightly-reaper.sh` and the worktree-housekeeping drop-in (stale
   Codex process sweep; comments only). `~/.claude/hooks/fable-subagent-guard.py` mentions cswap in a
   comment. `bb-plugin-codex-voice` (bb's voice plugin) reads cxswap accounts too.
10. **Close.** The brief's checks: Claude Code, Codex, pi, OMP and opencode on auto-vps answer through the
    hub (hub log); `allowances` prints hub figures; a pinned bb lane lands on its account; Gandalf's tool
    returns the list; `systemctl --user list-units 'cswap*' 'cxswap*' 'muse-uk*'` shows nothing active.
    Update the device matrix and close t8306.

## Cursor

- 2026-10-26: hub installed on m6 (brew `cliproxyapi` 8.0.40, bound to 127.0.0.1:8317, Tailscale
  Serve on :8317, keys in BWS `AUT_HUB_ACCESS_KEY` / `AUT_HUB_MANAGEMENT_KEY`, project `personal`).
  Logins pending (Waseem does them in the browser). L1 dispatched.
- 2026-10-26: L1 merged to `main` (consume-first, holds, pins, credential listing fields, docs,
  tests). m6 still runs upstream brew 8.0.40, so none of it is live until the migration (t8306)
  installs this fork's build. Next: L2 brief (t8304) from "What L2 needs" above.
  L1 dispatched.
- 2026-10-26 20:20: signed in on the hub: 10 Claude accounts (cswap slots 1-10; GDP Team file marked
  `disabled`), 3 Codex Pro (GDP personal, automancer.uk, gmail; Team left out), Grok Build, Antigravity,
  Kimi, Muse. Devin pending (its login needs an interactive paste on m6). API-key upstreams added under
  `openai-compatibility`: deepseek, commandcode, zai (GLM coding plan), opencode-go (needs the
  `x-opencode-session` header), local (the estate gateway router at automancer-vps:8100). All answered a
  one-word prompt through the hub except Grok (402, weekly pool spent; expected). Keys were empty for the
  first hour because the config script read them from the same stdin as its own source: fixed; wrong or
  missing key now returns 401. m6's own Claude Code and Codex point at the hub and work; wasnewpc being
  pointed; mbp asleep. Management API answers on `/v8/management/credentials` with the management key.
- 2026-10-26 20:20: L1 build (estate-8acd42bb, cross-compiled on auto-vps, darwin/arm64) is live on m6 as
  launch agent `uk.automancer.hub` (`~/.local/hub/cliproxyapi -config ~/.local/hub/config.yaml`, log
  `~/Library/Logs/hub.log`), `routing.strategy: consume-first`; the Homebrew service is stopped. Config now
  lives at `~/.local/hub/config.yaml`, not the brew path. wasnewpc's Claude Code and Codex point at the hub
  and answered. L2 dispatched.
- 2026-10-26 20:50: Devin signed in (18 logins). OMP on m6 and wasnewpc, and opencode on wasnewpc, carry a `hub` provider (25 non-Claude models: GPT via the Codex subs, Antigravity Gemini, Grok, Kimi, Muse, DeepSeek, GLM, CommandCode, OpenCode Go, Devin, local GPU). Claude models stay Claude-Code-only through the hub (Waseem's rule). ROUTES.md now allows Terra for Codex lanes (skills commit fae7cf5). Migration item t8306 carries the Gandalf usage read via estate-gateway and the remaining harnesses.
- 2026-10-26: L2 merged (dashboard `/hub`, `GET /v8/management/credentials/usage`, usage refresher
  with persisted snapshot). m6 runs the L1 build, so none of L2 is live until m6's binary is rebuilt
  from `main` and `routing.usage-refresh.interval` is set in `~/.local/hub/config.yaml`. Then open
  `/hub` and check each account's poll error.
- 2026-10-26 21:25: L2 build (estate-156d2b1b) live on m6; `routing.usage-refresh.interval: 10m` set; `/hub` renders real figures for Claude (5h, weekly, Fable-only), Codex, Kimi, Grok, Antigravity, Muse within one poll; Devin and the API-key providers show no usage (as designed). Screenshots: /home/ubuntu/Notes/Reports/assets/26-10-26-hub-dashboard-{desktop,phone}.png (taken with a raw-CDP script, /opt/automancer/var/scratch/t3eval/hubshot.mjs, key via env). Seen defects: the provider summary table's label column overlaps on long names (commandcode); CodexBar parity (banked resets, will-it-last) still open; Waseem keeps CodexBar until the page is at least as good.
- 2026-10-26 21:40: mbp (MacBook) pointed at the hub: Claude Code and Codex answered; OMP and pi carry the `hub` provider (merged beside its existing local, maple, mesh providers). Herdr on mbp already listed auto-vps. L3 dispatched.
- 2026-10-26: L3 (t8318) redesigned `/hub` and added banked resets, pace, and API-key provider usage.
  None of it is live until m6's binary is rebuilt from `main`. Then open `/hub` on the phone and check
  each API-key provider's poll error.
- 2026-10-26 22:10: pi, opencode, dsh and commandcode installed by npm where missing on m6, mbp, wasnewpc (packages: @earendil-works/pi-coding-agent, opencode-ai, @deepseek-ai/dsh, command-code); pi and opencode carry the hub provider (25 models) on m6 and wasnewpc; mbp opencode lists 0 hub models, under investigation. Device matrix updated.
- 2026-10-26 22:30: omarchy column closed for the hub-served tools; agy, kimi, devin Linux binaries copied from auto-vps (same x86_64), commandcode via mise, dsh via npm. Waseem installs by mise on omarchy where it can.
- 2026-10-26 22:35: m6 has every harness installed (kimi 2.3.3, devin, agy 1.4.7, muse 1.4.13 via the Meta launcher, grok via npm); installers inspected first, kept at /opt/automancer/var/scratch/t3eval/installers/. Logins on m6 for Muse, agy, Devin, Kimi and Grok are Waseem's.
- 2026-10-26 22:55: L3 build (estate-278f7e18, PR #3 d6abbc05) live on m6. First real run: 23 credentials, no poll errors; CommandCode (USD left of monthly grant, 5h, week), OpenCode Go (5h, week, month), GLM (5h, week) and DeepSeek balance all show. Codex banked resets not visible yet on the page (allowances on auto-vps shows 7); re-check after the next poll. Screenshots: Notes/Reports/assets/26-10-26-hub-dashboard-v3-{desktop,phone}.png. t8318 closed.
- 2026-10-28: device matrix gaps. Grok CLI and Kimi CLI take a custom endpoint and now carry hub models on m6, omarchy and mbp (all answered). mbp: opencode fixed (its npm postinstall had not run, so the binary never started; the provider config was fine) and lists the 25 hub models; Devin CLI installed; Kimi CLI was already installed and signed in (0.20.3). wasnewpc was off (the GPU box was booted as omarchy), so its Grok, Kimi and Devin installs are still open.
- 2026-10-28 (evening): wasnewpc closed. Grok (npm), Kimi 2.4.0 and Devin 3000.11.228 installed; Grok and Kimi default to the hub and all four hub models answered; none of the three is signed in.
- 2026-10-28 (evening): Muse on wasnewpc. Meta's Windows launcher (`https://api.meta.ai/muse-launcher.ps1`, read before use; the release manifest carries `x86_windows`) sits in `%LOCALAPPDATA%\muse\` with its builds, and `~\.local\bin\muse.cmd` runs it under `pwsh`. Installed with `MUSE_LAUNCHER_INSTALL=1 MUSE_LOGIN=0`, no sign-in; `muse --version` reports 1.4.14 from pwsh and cmd. Login: `muse login`.
- 2026-10-28: hub credentials are in the nightly encrypted backup (ops PR #1332, merged): `estate-extra-stage`'s `hub` part streams m6's `~/.cli-proxy-api` (auth files, `usage-snapshot.cache`) and `~/.local/hub/config.yaml` over ssh into age; the estate-extra tiers carry it to naz and B2. Restore steps: ops `runbooks/vps-ops.md` "Restore the estate hub's credentials (m6)". A dry-run restore matched all 20 files byte for byte. A new state file on m6 outside those two paths needs adding to that part.
- 2026-10-28: t8314. Both flaky interrupt tests were symptoms of one handler race that upstream fixed the same day (9717e325, issue #7065: a late interrupt after a terminal event got a fake `response.incomplete` or was dropped). Cherry-picked here; `TestResponsesInterruptStopsHTTPUpstream` also waits on a channel for the upstream cancel instead of reading a flag once (fork PR #4; the test half went upstream as router-for-me/CLIProxyAPI#7086 to `dev`). m6 needs a rebuild from `main` to carry it.
- 2026-10-28 18:23-18:36: auto-vps Claude Code and Codex trialled on the hub (Fable, Opus, Sonnet, Terra answered; Claude and Codex pins landed on the named accounts; an unknown pin got 400). The chair stopped the lane because bb was unreliable (it was already slow at 18:00, before any change) and ran `estate-hub-route off`; the cut-over waits for Waseem ("auto-vps cut-over" above). Hub groups and notes set. PRs open as drafts.
- 2026-10-28 19:28: L4 (one graphical table, `estate-2fe4a080`) live on m6; `/hub` 200 on the tailnet, traffic serving. Previous binary kept as `~/.local/hub/cliproxyapi.prev-estate-278f7e18` (roll back: move it over `cliproxyapi`, then `launchctl kickstart -k gui/$(id -u)/uk.automancer.hub`). Seen on deploy: the first spawn after swapping the binary failed with `OS_REASON_CODESIGNING`; launchd's KeepAlive retry started it within seconds (`codesign -v` passes). Screenshots: repo `docs/estate-hub-screens/v4-*.png` (account names and emails replaced, the fork is public); real ones in `Notes/Reports/assets/26-10-28-hub-v4-*.png`.
