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
| L1 routing | `consume-first` routing strategy + account pinning, merged to `main` of Automancer-Ltd/CLIProxyAPI | bb thread, Claude Code, Opus 5.5, high | dispatched 2026-10-26 as bb thread thr_cu7cfdji97 (tracker t8305; L2 t8304; migration t8306) |
| L2 dashboard | web dashboard replacing sysdash/codexbar | after L1 | not started |

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

## Cursor

- 2026-10-26: hub installed on m6 (brew `cliproxyapi` 8.0.40, bound to 127.0.0.1:8317, Tailscale
  Serve on :8317, keys in BWS `AUT_HUB_ACCESS_KEY` / `AUT_HUB_MANAGEMENT_KEY`, project `personal`).
  Logins pending (Waseem does them in the browser). L1 dispatched.
