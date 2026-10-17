# Codex earned reset aware routing

This local companion checks each CLIProxyAPI Codex OAuth credential through the
official Codex app-server account API. It prefers accounts whose earned reset
credits expire within seven days, ordering by earliest expiry, then by weekly
usage. CLIProxyAPI's normal priority and cooldown handling still decides which
credential can serve a request. The companion does not create artificial model
requests to spend usage.

Run `python3 reset_router.py --once --dry-run` to inspect the proposed changes.
Run `python3 reset_router.py --once` to apply priorities once, or omit `--once`
to check every 15 minutes. It needs `codex` on `PATH`, local CLIProxyAPI at
`127.0.0.1:8317`, the management key, and local Codex OAuth credential files.
Use `--auth-dir`, `--management-key`, and `--state-file` for other locations.

An earned reset is attempted only when its expiry is within 24 hours, the
weekly window renews after that expiry, and weekly usage is at least 90%.
During the last hour, the threshold drops to 10%, since the reset would soon
expire. The provider may return `nothingToReset`; in that case no credit is
spent. After successful redemption, the companion clears CLIProxyAPI's local
cooldown through `reset-quota`. It records an idempotency key before every
attempt and reuses it after a timeout.

If the account API omits individual credit expiry details, the companion
cannot make an expiry-aware decision for that account. If this process is not
running, CLIProxyAPI keeps its last assigned priorities. This program changes
Codex credential priorities, so manual priorities for those credentials will
be overwritten while it runs. Claude credentials are touched only when a
separate verified-offer file is present.

## Claude limit-reset offers

Anthropic exposes earned limit-reset offers in Claude web/Desktop under
Settings > Usage. There is no documented account API for reading or redeeming
them. To prioritize an account with a verified offer, provide a local JSON file
at `~/.local/state/cliproxy-reset-router/claude-offers.json` (or use
`--claude-offers-file`):

```json
{"offers": [{"email": "you@example.com", "kind": "full", "expires_on": "2026-10-22"}]}
```

The companion gives that Claude credential priority 100 through the listed
calendar date, then restores priority 0. It cannot redeem the Claude offer;
open Claude Settings > Usage and select **Reset for free** yourself when the
available capacity makes it worthwhile. It never clicks that button for a test.
