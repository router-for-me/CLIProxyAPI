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
