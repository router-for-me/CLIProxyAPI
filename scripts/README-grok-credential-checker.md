# Grok Credential Checker

Independent Python operations script for CLIProxyAPI. It inspects Grok/XAI OAuth
credentials through the Management API, disables accounts that hit free-tier
exhaustion, and keeps a configurable active pool (default **500**).

Python **standard library only** — no extra package manager dependency.

## Features

- One-shot (`--once`, default) and daemon (`--daemon`) modes
- Dry-run by default; mutations require explicit `--apply`
- Isolated single-account check (`--isolate NAME_OR_INDEX`)
- JSON action report (`--json`)
- Ownership tracking: `managed` / `external` / `manual_override`
- Automatic disable on explicit free-usage exhaustion, auth invalid, or verification
- Automatic re-enable **only** for script-owned quota/standby accounts after reset
- Configurable free-tier policy limit (default **1,000,000** tokens)
- Rolling **24h** reset fallback when provider reset / Retry-After is absent
- Bounded work for large inventories (batch 100, concurrency ≤ 32, ≤ 50 probes/cycle)
- Process lock, atomic state file, action journal, and `--rollback-run`

## Security

- Prefer environment variables for secrets: `CLIPROXY_MANAGEMENT_KEY`
- Do not put management keys in shell history, unit files without credentials, or git
- Logs, JSON reports, state files, and exceptions redact bearer tokens and common secret fields
- The script never moves, deletes, or rewrites files under `auths/`

## Prerequisites

- Python 3.9+ (3.10+ recommended)
- Reachable CLIProxyAPI Management API (`/v0/management`)
- Management key with permission to list auth files and patch status

## Quick start (dry-run first)

```bash
export CLIPROXY_MANAGEMENT_URL="http://127.0.0.1:8317"
export CLIPROXY_MANAGEMENT_KEY="your-management-key"

# 1) Inspect planned actions only
python3 scripts/grok_credential_checker.py \
  --once --dry-run --json \
  --state-file /var/lib/cliproxy/grok_credential_checker_state.json \
  --target-active 500 \
  --concurrency 32 \
  --batch-size 100

# 2) Optional: isolate one account
python3 scripts/grok_credential_checker.py \
  --once --dry-run --json \
  --isolate 'account-name.json'

# 3) Apply one cycle after reviewing the JSON report
python3 scripts/grok_credential_checker.py \
  --once --apply --json \
  --state-file /var/lib/cliproxy/grok_credential_checker_state.json

# 4) Daemon (five-minute interval example)
python3 scripts/grok_credential_checker.py \
  --daemon --apply \
  --interval-seconds 300 \
  --target-active 500 \
  --concurrency 32 \
  --batch-size 100 \
  --state-file /var/lib/cliproxy/grok_credential_checker_state.json
```

## Important flags

| Flag | Default | Meaning |
|------|---------|---------|
| `--once` | on | Single cycle |
| `--daemon` | off | Loop forever |
| `--interval-seconds` | 300 | Daemon sleep between cycles |
| `--apply` | off | Perform PATCH enable/disable |
| `--dry-run` | on | Report only (default unless `--apply`) |
| `--target-active` | 500 | Desired healthy enabled pool size |
| `--quota-limit-tokens` | 1000000 | Policy limit (not invented usage) |
| `--reset-window-hours` | 24 | Fallback cooldown for free-usage exhaustion |
| `--concurrency` | 32 | Max concurrent upstream checks (capped at 32) |
| `--batch-size` | 100 | Inventory check batch size |
| `--max-probes-per-cycle` | 50 | Isolated fallback probe cap |
| `--state-file` | `./grok_credential_checker_state.json` | Durable ownership/journal |
| `--rollback-run RUN_ID` | — | Undo one journaled run when safe |
| `--round-robin` | off | Stable RR ranking without usage metrics |
| `--json` | off | Machine-readable report on stdout |

Environment:

- `CLIPROXY_MANAGEMENT_URL`
- `CLIPROXY_MANAGEMENT_KEY` (or `--management-key-env`)

## Behaviour summary

1. `GET /v0/management/auth-files` — inventory
2. Keep only XAI/Grok OAuth records with stable `name` + `auth_index`
3. Classify using runtime fields (`next_retry_after`, `status`, `status_message`, …)
4. Optionally call billing via `POST /v0/management/api-call` with `auth_index`
5. Bounded isolated probe only when the pool is short and state is inconclusive
6. Policy:
   - Explicit free-usage exhaustion / invalid / verification → disable immediately
   - Generic 429, 5xx, timeout, unknown schema → **no mass disable**
7. Pool reconcile toward `--target-active`
8. `PATCH /v0/management/auth-files/status` only when live state differs and `--apply` is set

### Ownership rules

| Ownership | Meaning | Auto-enable? |
|-----------|---------|--------------|
| `external` | Never mutated by this script (or disabled before first manage) | No |
| `managed` | Last successful mutation was by this script | Yes, if quota/standby eligible |
| `manual_override` | Live disabled bit drifted from last script mutation | No |

### Safe mode

If the state file is missing or corrupt on load:

- Report inventory and planned **disables** as usual in dry-run
- **Never enable** currently disabled credentials
- Fix or remove the bad state file, then re-run dry-run

### Rollback

Every applied cycle stores a journal under the state file (`runs.<run_id>`).

```bash
python3 scripts/grok_credential_checker.py \
  --rollback-run <RUN_ID> --apply \
  --state-file /var/lib/cliproxy/grok_credential_checker_state.json
```

Rollback restores only actions from that run whose live state still matches the
script's post-run value. Later manual changes are skipped.

## systemd examples

See `scripts/systemd/`:

- `grok-credential-checker.service`
- `grok-credential-checker.timer` (optional periodic oneshot)

Copy units, adjust paths/user, store the management key in an environment file
with mode `0600`, then:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now grok-credential-checker.timer
# or: sudo systemctl enable --now grok-credential-checker.service  # long-running daemon
```

## Tests

```bash
python3 -m compileall scripts/grok_credential_checker.py
python3 -m unittest discover -s scripts/tests -p 'test_grok_credential_checker.py'
python3 scripts/grok_credential_checker.py --help
```

## Recovery

1. Stop the daemon/timer — Management API state stays as last applied
2. Inspect state file and JSON dry-run output
3. Use `--rollback-run` if a bad apply cycle needs reversal
4. If state is untrusted, delete/move it (safe mode) and re-baseline with dry-run

## Notes

- The **1M token** value is a configurable policy default. Decisions use verified
  provider/runtime exhaustion signals; billing dollar fields are never converted
  into invented token usage. When the provider embeds
  `tokens (actual/limit): N/M` in free-usage messages, those values are stored
  with unit `tokens`.
- This script does not change CLIProxyAPI conductor cooldown internals.
- Prefer dry-run on production before the first `--apply`.
