# Banked usage resets

Codex can keep answering after a subscription window reaches 100% by charging purchased credits. Automatic banked resets restore that window first. Expiring resets can also be used while quota remains.

```yaml
quota-exceeded:
  banked-reset:
    mode: off             # off (default), manual, auto
    use-before: 2h        # default 2h; 0s disables expiry use
    providers: [codex]    # automatic use only; omit to include Claude
```

`off` preserves existing behavior. `manual` enables managed redemption without automatic spending. `auto` checks before generation requests, after an initial 429, and every minute while the service runs. Accounts without credits are checked at most once a minute. When credits exist, Codex usage is read before generation so a successful paid-credit response is not the exhaustion signal.

The earliest-expiring usable reset is selected. Exhaustion takes precedence over expiry. An expired reset is never used; a reset without an expiry is eligible for exhaustion only. No minimum quota gain is required for expiry use. Provider errors back off from 30 seconds to 16 minutes, including near expiry. After a confirmed redemption, checks pause for one minute while provider usage settles.

Claude supports the `cedar_ember` usage grant API. Eligibility, paused grants, start/end times, `usable_now`, `resets_left`, provider cooldown, and `use_requires_limit` are respected. A grant requiring exhaustion cannot be redeemed early, even near expiry. An absent experiment block means no supported grants. API-key credentials and Home-dispatched credentials are excluded.

## Management API

All routes use the existing management authentication under `/v8/management`:

- `GET /credentials/banked-resets?auth_index=INDEX`: read-only list, total count, per-credit expiry, usability and exhaustion. Available even in `off` mode. This never spends a reset.
- `POST /credentials/banked-resets/redeem`: body `{"auth_index":"INDEX","credit_id":"ID"}`. Omit `credit_id` to select the earliest usable reset. Requires `manual` or `auto` mode.
- `POST /credentials/banked-resets/check`: run the configured automatic checks once; no spending in `off` or `manual` mode.

The current Management Center already provides Codex and Claude reset buttons and expiry details. In managed modes their provider redemption calls through `/requests/api-call` enter the same backend lock and ledger. No frontend upgrade is required beyond a Management Center version containing those controls. In `off` mode its existing direct-call behavior remains unchanged.

## Spending safeguards

A lock is shared by credentials for the same provider account. Each redemption attempt has a deterministic request UUID; a multi-use Claude grant includes its remaining count in that identity. A confirmed first-attempt no-spend response permits a new request ID later. A refusal after an ambiguous POST keeps the original intent. Intent is saved in credential metadata before POST. On restart, recovery searches every login file for the same account and completes the intent on its originating file. Ambiguous results retain that intent, so retries and restarts reuse the same request ID instead of choosing another credit. A different requested credit is refused while an earlier redemption is unresolved. Keep the auth store durable and use only one active gateway for a given set of OAuth credentials; this is not a distributed lock between independent gateways.

Only `reset`/`already_redeemed` (Codex) or `reset`/`already_used` (Claude) clear the proxy cooldown. An HTTP 200 alone does not. Cooldown release uses the existing `ResetQuota` operation, including its account-wide model cooldown reset. Unknown outcomes retain the intent and back off. A successful operation is saved in `banked_reset_operation`; structured logs record each requested and confirmed redemption with provider, auth index, reason and request ID, without tokens or upstream error bodies. Keep these logs for the full redemption history; metadata stores the latest operation only.

These are undocumented provider APIs. Listing or parsing failures leave normal routing available and never manufacture an available reset.

## Verification

Use mocked provider responses for spending tests:

```sh
go test -race ./sdk/cliproxy/auth ./internal/config ./internal/api/handlers/management -run Banked -count=1
go test ./...
go build -o test-output ./cmd/server && rm test-output
```

For a live check, call only the GET listing route. Do not redeem a real credit as a test.

API contracts were checked against [codex-reset](https://github.com/aaamosh/codex-reset), [DonovanMontoya/CLIProxyAPI#3](https://github.com/DonovanMontoya/CLIProxyAPI/pull/3), [ntindle/CLIProxyAPI#14](https://github.com/ntindle/CLIProxyAPI/pull/14), and the current Management Center's Claude grant implementation. Related request: [CLIProxyAPI#6253](https://github.com/router-for-me/CLIProxyAPI/issues/6253).
