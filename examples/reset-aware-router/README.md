# Renewal and reset aware routing companion

This optional local companion updates CLIProxyAPI credential priorities. For
accounts with usable weekly quota, it prefers the earliest upcoming weekly
renewal, so unused capacity is spent before that window closes. Renewal times
are compared to the nearest minute to ignore second-level reporting jitter.
An earned manual reset expiry breaks ties, followed by unused weekly quota.
CLIProxyAPI still checks model support and cooldowns before serving a request.

Codex weekly limits and earned reset credits come from the Codex app-server
account API. Claude weekly limits come from recent quota headers already
observed by CLIProxyAPI and exposed through its v8 management API. Claude
observations older than three days, expired windows, and credentials with an
error status are not promoted. The companion does not send artificial model
requests or repeatedly probe Claude's quota endpoint. If any active Codex
account cannot be read, that pass keeps the previous Codex ordering.

Run `python3 reset_router.py --once --dry-run --priority-only` to inspect the
proposed priority changes. Run `python3 reset_router.py --once --priority-only`
to apply priorities without attempting a manual reset. Omit `--once` to check
every 15 minutes. It needs `codex` on `PATH`, local CLIProxyAPI at
`127.0.0.1:8317`, the management key, and local OAuth credential files. Use
`--auth-dir`, `--management-key`, and `--state-file` for other locations.

The existing Codex earned reset policy is separate from priority selection.
Without `--priority-only`, it attempts redemption only when a credit expires
within 24 hours, the weekly window renews later, and weekly usage is at least
90%. During the final hour, that threshold becomes 10%. The provider may
return `nothingToReset`; no credit is spent in that case. After confirmed
redemption, the companion clears CLIProxyAPI's local cooldown. It records an
idempotency key before each attempt and reuses it after an uncertain outcome.

## Claude limit reset offers

Anthropic exposes earned limit reset offers in Claude web/Desktop under
Settings > Usage. There is no documented account API for reading or redeeming
them. To record a verified offer, provide a local JSON file
at `~/.local/state/cliproxy-reset-router/claude-offers.json` (or use
`--claude-offers-file`):

```json
{"offers": [{"email": "you@example.com", "kind": "full", "expires_on": "2026-10-22"}]}
```

The offer expiry is a secondary routing signal. When no usable weekly window
is observed, a currently valid offer can promote the account until its
calendar expiry. This companion never redeems a Claude offer; open Claude
Settings > Usage and select **Reset for free** yourself when appropriate.

This example changes credential priorities through the v8 management API. A
stopped companion leaves its last assigned priorities in place.
