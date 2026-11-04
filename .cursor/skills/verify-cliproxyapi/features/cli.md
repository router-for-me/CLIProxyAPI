# CLI flags

The `cli-proxy-api` binary is started with flags for config path, OAuth logins, TUI, LAN discovery and Vertex import.

## Sub-features

- `cli-help` `-h` lists flags including `-config`.
- `cli-logins` `-codex-login`, `-claude-login`, … — SKIPPED (need browser OAuth).
- `cli-discover` `-discover`/`-discover-json` — LAN mDNS; not asserted.

## How to get to it (user POV)

- `./cli-proxy-api -h`, `./cli-proxy-api -config config.yaml`, `./cli-proxy-api -tui -standalone`.

## Driving it with cpa-verify.sh

Preconditions: launched (binary built at `$CPA_RUN/cli-proxy-api`).

- **Help.** `scripts/cpa-verify.sh drive cli` → `help.out` contains `-config`; exit code in `help.exit` (Go's flag package exits 0 or 2 for `-h`).

## Gotchas

- `-tui` needs a PTY (use tmux) and a running server or `-standalone`.
- Login flags open a browser unless `-no-browser`; never run them against the user's auth-dir.
