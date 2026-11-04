# Management API

Administrators read and change runtime config via `/v0/management/*` using the management secret key; changes are written back to config.yaml.

## Sub-features

- `mgmt-auth` request without key → 401.
- `mgmt-config` `GET /v0/management/config` returns JSON config (includes `api-keys`).
- `mgmt-write` `PUT /v0/management/debug {"value":true}` → 200, GET reflects it, config.yaml has `debug: true`.

## How to get to it (user POV)

- Management Center web panel (`/management.html`, disabled in verification), `-tui` mode, or curl with `X-Management-Key` / `Authorization: Bearer`.

## Driving it with cpa-verify.sh

Preconditions: doctor OK (doctor already proves the key works).

- **Reject.** `scripts/cpa-verify.sh drive management` → `mgmt-nokey.status` `401`.
- **Read.** `mgmt-config.status` `200`, body has `"api-keys"`.
- **Write + persist.** `mgmt-debug-set` `200`, `mgmt-debug-get` body contains `true`, harness greps `$CPA_RUN/config.yaml` for `debug: true` and saves `config-after.yaml`.

## Gotchas

- Empty `secret-key` disables the whole API (404s), not "open access".
- Non-localhost access also needs `allow-remote: true`.
- Writes rewrite config.yaml with expanded defaults; never point the harness at a real config.
- Config bodies contain API keys; the harness redacts this run's keys — redact manually if you curl by hand.
