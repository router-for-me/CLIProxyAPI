# Management browser regression — 2026-09-21

## Result

- Playwright Chromium against the running service: **75 passed, 0 failed, 0 JavaScript page errors**.
- Roles: existing `admin` and `user`; viewports: desktop 1440×900 and mobile 390×900.
- Separate transient-network test: two connection resets for each of the credential-list,
  OAuth exclusion and OAuth alias endpoints; the page recovered without manual refresh.
- Account portal, bridge and bounded-retry Node tests passed, as did Go tests for
  `internal/managementauth`, `internal/api/handlers/management` and `internal/api`.

## Covered behavior

- Anonymous legacy-login entry; account login; session restore/reload; authenticated
  legacy-login route; absence of the old password form; visible account-header link.
- Quick Start, credential files, OAuth page, providers, configuration, logs and system info.
- Billing and quota direct navigation, admin-positive/user-negative API authorization,
  allowed read APIs and user configuration redaction.
- Account page role controls and return to the main console.
- Configuration save, password change and user creation submissions intercepted in
  Playwright before contacting the server. These exercise UI submission/error paths,
  **not successful production writes**.
- Cross-tab logout, server-side session rejection after logout, expired `/me` 401/403,
  delayed profile responses after logout and narrow-screen behavior.

## Fixed regressions

1. Other open tabs did not follow account logout or identity changes.
2. Portal expiry detection depended on error wording rather than HTTP status.
3. Late account/profile responses could write stale state after logout or identity replacement.
4. Closing a page immediately after logout could cancel its logout request; it now uses keepalive.
5. Same-token storage notifications could unnecessarily clear an open admin user list.

## Non-destructive safeguards and audit

`management_readonly_e2e.cjs` intercepts browser management writes except temporary
login/logout operations, and intercepts OAuth initiation GETs. Helper API requests
only read state or revoke test sessions. Five UI writes were intercepted: two config
saves, two password changes, and one attempted user creation. No test account was created.

A before/after hash audit covered config.yaml, the account database and three credential
files. Account/credential files were unchanged. config.yaml changed concurrently:
server logs show **another client**, not the test host, successfully called
`PATCH /v0/management/billing/api-tokens/legacy-2` at **11:59:04 UTC**. That external
change was preserved, not rolled back. Request-log audit found no non-session writes
from the test host. Temporary test-password file was removed afterward.

## Re-run

Install Playwright/Chromium outside the repository. Supply passwords through private
0600 files, not command-line arguments or committed test data:

```sh
NODE_PATH=/path/to/playwright/node_modules \
CPA_TEST_BASE_URL=http://your-service:8317 \
CPA_TEST_ADMIN_PASSWORD_FILE=/private/admin-password \
CPA_TEST_USER_PASSWORD_FILE=/private/user-password \
CPA_TEST_REPORT=/private/browser-report.json \
node internal/api/management_readonly_e2e.cjs
```

The independent network-recovery suite is `internal/api/account_network_e2e.cjs`;
it uses `CPA_TEST_USER=admin` and `CPA_TEST_PASSWORD_FILE` in addition to the base URL
and Playwright module path. Treat test reports and screenshots as private data.
