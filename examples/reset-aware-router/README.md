# Renewal and reset aware routing companion

This optional local companion updates CLIProxyAPI credential priorities. Its
order is: a usable account with a verified reset credit expiring within five
hours; accounts with usable general weekly quota, ordered by the earliest
weekly renewal; then accounts with no observed general weekly window but with
usable five-hour quota. Within a group, the earlier five-hour renewal goes
first, followed by more unused five-hour capacity. Renewal times are
compared to the nearest minute to ignore second-level reporting jitter.
This changes request routing; it does not automatically redeem a reset credit
or guarantee that traffic will consume the balance. CLIProxyAPI still checks
model support and cooldowns before serving a request.

Codex weekly and five-hour limits and earned reset credits come from the Codex
app-server account API. Accounts are promoted only when that API explicitly
allows ordinary usage. An exhausted observed five-hour window is not promoted.
A Codex credential in a CLIProxyAPI quota cooldown is still
inspected for an eligible earned reset, while its previous priority is kept
until it recovers. SIWC-backed credentials are excluded because their request
token differs from the outer Codex token; if an active one is present, Codex
priorities are left unchanged. Claude weekly and five-hour limits come from
recent quota headers already observed by CLIProxyAPI and exposed through its
v8 management API. A Fable-only weekly window is not treated as a general
Claude weekly window for other models. Accounts without a general weekly
window can still be prioritized using their observed five-hour quota.
Claude observations older than three days, expired or exhausted windows, and
credentials with an error status are not promoted. The companion does not send artificial model
requests or repeatedly probe Claude's quota endpoint. If any active Codex
account cannot be read or its routing data is incomplete, that pass keeps the
previous Codex ordering.

CLIProxyAPIHome mode is unsupported: the server disables its management
endpoints in that mode, so this companion cannot change Home's routing order.

Run `python3 reset_router.py --once --dry-run --priority-only` to inspect the
proposed priority changes. Run `python3 reset_router.py --once --priority-only`
to apply priorities without attempting a manual reset. Omit `--once` to check
every 15 minutes. It needs `codex` on `PATH`, local CLIProxyAPI at
`127.0.0.1:8317`, the management key, and local OAuth credential files. Set
`CLIPROXY_MANAGEMENT_KEY` to the key value, or create a private file containing
it and pass `--management-key-file PATH`. The default file path is
`~/.cli-proxy-api/management-key`; CLIProxyAPI does not create this file.
Use `--auth-dir` and `--state-file` for other locations.

The existing Codex earned reset policy is separate from priority selection.
Without `--priority-only`, it attempts redemption only when a credit expires
within 24 hours, the weekly window renews later, and weekly usage is at least
90%. During the final hour, that threshold becomes 10%. The provider may
return `nothingToReset`; no credit is spent in that case. After confirmed
redemption, the companion clears CLIProxyAPI's local cooldown. It records an
idempotency key before each attempt and reuses it after an uncertain outcome.
If cooldown clearing fails after a confirmed redemption, it records the
pending operation and retries it on later runs. `--priority-only` never
redeems a credit; it can still finish a previously pending local cooldown clear.
An error while checking or redeeming one Codex credential is logged and does
not prevent later credentials from being checked in that pass.

## Claude limit reset offers

Anthropic exposes earned limit reset offers in Claude web/Desktop under
Settings > Usage. There is no documented account API for reading or redeeming
them. To record a verified offer, provide a local JSON file
at `~/.local/state/cliproxy-reset-router/claude-offers.json` (or use
`--claude-offers-file`):

```json
{"offers": [{"name": "claude-account.json", "kind": "full", "expires_on": "2026-10-22"}]}
```

The offer expiry is normally a secondary routing signal. If Claude shows an
exact expiry time, use `"expires_at": "2026-10-22T18:00:00+03:00"` instead
of `expires_on`. An active offer with a known expiry within five hours takes
priority over other Claude accounts with earlier weekly renewals, provided
its observed quota windows are usable or unknown. A date-only `expires_on`
never triggers the five-hour override because the exact expiry time is unknown.
When neither weekly nor five-hour usage is observed, a currently valid offer
can still promote the account after accounts with usable observed quota. You
can use `email` instead of `name` when exactly one loaded
Claude credential has that email. Ambiguous emails are skipped; use the exact
credential name in that case. This companion never redeems a Claude offer;
open Claude Settings > Usage and select **Reset for free** yourself when
appropriate.

This example changes credential priorities through the v8 management API. A
stopped companion leaves its last assigned priorities in place.
