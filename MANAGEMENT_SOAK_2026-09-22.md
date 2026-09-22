# Management Playwright soak — 2026-09-22

## Executed result

This was an actual foreground 2-hour Chromium run, not a scheduled/background-only promise.

| Item | Result |
| --- | --- |
| Start (UTC) | 2026-09-22 02:54:48.597 |
| Finish (UTC) | 2026-09-22 04:54:59.706 |
| Measured duration | **7,211 seconds — 2h 0m 11s** |
| Iterations | 481 |
| Scenario checks | **2,424 passed, 0 failed** |
| Observed browser requests | 9,259 |
| Browser response events | 8,817: 8,784 HTTP 200; 25 expected role-denial 403; 8 guard-generated 409 |
| Unhandled JS errors | 0 |
| Unexpected HTTP / transport failures | 0 / 0 |
| Fault injection | 78 connection resets across 13 scenarios; all 13 recovered |
| Guard self-tests | 8 unsafe requests intercepted before the server |
| Service | active; PID 33391 throughout; NRestarts=0 |
| Preflight / postflight | 75/75 checks passed in each |

Request-event totals include injected aborts and page lifecycle activity and are not a claim that every request received a 200 response. Auxiliary role/session API probes are separate from browser request-event counters.

## Coverage

Four persistent contexts retained their authenticated sessions for the whole run:
`admin` and `user`, each at 1440×900 and 390×900. Responsive checks also exercised
320px and 1024px widths. Extra isolated contexts tested repeated cross-tab logout
without invalidating the four long-lived sessions.

| Scenario group | Checks |
| --- | ---: |
| Real sidebar navigation: dashboard, Quick Start, credentials, OAuth, providers, config, logs, system | 1,284 |
| Credential filtering, model-list dialog and list refresh | 160 |
| Parallel account portal, admin user list preservation | 160 |
| Billing direct navigation and role behavior | 160 |
| Quota direct navigation and role behavior | 160 |
| Browser back/forward | 160 |
| Theme media preferences | 120 |
| Responsive resizing | 80 |
| API role boundaries, live session and heap sampling | 104 |
| Login including isolated churn contexts | 13 |
| Two-reset-per-endpoint automatic recovery | 13 |
| Fresh-context cross-tab logout and old-token rejection | 9 |
| Default-deny guard probes | 1 group of 8 requests |

Fault scenarios reset each of `/auth-files`, `/oauth-excluded-models` and
`/oauth-model-alias` twice. Every context/role was exercised. No manual browser
refresh was needed to recover from the injected failures.

The pre/postflight suites additionally checked legacy login routing, expiry 401/403,
late profile responses, and intercepted configuration/password/user-create submissions.
No such submission was allowed to persist.

## State protection and audit

- The browser guard is default-deny: only explicitly enumerated read APIs, known
  UI assets, and test-session login/logout POSTs can reach the service.
- This also blocks side-effecting GETs: OAuth start, callback, login-status polling,
  unknown plugin management routes and plugin resource handlers.
- Service workers and unapproved WebSockets cannot bypass the guard.
- Helper requests use allowlisted reads or test-session logout only.
- Configuration/account/credential hashes and service state were sampled 122 times.
- Request-log audit recorded **zero non-session writes or unsafe GETs from the test host**.
- Test contexts were closed and their sessions revoked. Temporary password and raw
  configuration-baseline copies were removed after verification.

### Concurrent changes were preserved, not rolled back

The live platform was not frozen. Of the five originally tracked files, config.yaml
and two credential files changed; the account database did not. One new credential
file also appeared. Evidence distinguishes these from test activity:

- **03:34:21 UTC:** normal service auto-refresh logged `auto-refresh scheduler due auths: 1`
  and a successful Claude refresh. One credential hash changed at the next audit sample.
- Another client performed OAuth initiation/polling and a successful callback at
  **03:56:48**, followed by credential-status PATCHes at **03:58:34 / 03:58:36**.
- Another client successfully saved `/config.yaml` at **04:12:01**. The semantic
  top-level configuration difference was `api-keys`; values are not included here.
- External activity also included `/api-call` 403 responses. These were not browser
  soak requests and do not establish an upstream credential failure.

No external edits, refreshed tokens or newly authorized credentials were overwritten.

## Memory observations

104 page samples recorded JavaScript heap usage between **9.19 and 23.85 MiB**.
Aggregate Chromium RSS medians were approximately 1,682 MiB in the first ten samples,
1,804 MiB around the middle, and 1,765 MiB near the end; peak aggregate RSS was
1,862 MiB. These sums include shared-process RSS, not unique PSS. The measurements
show no sustained unbounded growth over this run, but are not a proof that no memory
leak exists under other workloads.

## Changes made this turn

No new platform-function defect was reproduced in the formal soak, so production
application code and platform configuration were not changed or redeployed for this run.
The work extended the test tooling:

- `internal/api/management_readonly_guard.cjs`: strict read allowlist and guard tests.
- `internal/api/management_readonly_e2e.cjs`: uses the stricter guard.
- `internal/api/management_soak_e2e.cjs`: persistent-session soak, fault injection,
  actual sidebar/history/filter/modal interactions, metrics and failure evidence.
- `internal/api/management_soak_audit.py`: file hashes, service liveness and request audit.

Short pilot runs corrected test-driver issues (Response.status property access,
opening an off-canvas mobile sidebar, and classifying the expected user quota-aware
403). Those were test-harness defects, not claimed as platform bug fixes. A 210-second
pilot passed before the formal 7,211-second run.

## Scope limits

This is Chromium with desktop/mobile-sized viewports, not a Safari/Firefox/device-farm
result. Real configuration saves, password changes, credential updates, OAuth authorization,
upstream quota calls, plugin installation and enable/disable operations were deliberately
not performed. The current console did not expose a plugin menu; plugin-list reads and
installation blocking were checked, not successful plugin installation. The existing user
billing/quota restrictions were preserved.

## Evidence and re-run

Private machine-local evidence: `/tmp/cpa-soak-20260922/`:

- `preflight.json`, `postflight.json`
- `run1/status.json`, `run1/events.jsonl`, `run1/console.log`
- `run1/state-audit.jsonl`, `run1/mutation-audit.jsonl`, `run1/audit-final.json`

Credentials are supplied through private files, never embedded in scripts or reports:

```sh
NODE_PATH=/path/to/playwright/node_modules \
CPA_TEST_BASE_URL=http://your-service:8317 \
CPA_TEST_ADMIN_PASSWORD_FILE=/private/admin-password \
CPA_TEST_USER_PASSWORD_FILE=/private/user-password \
CPA_SOAK_DIR=/private/soak-results \
CPA_SOAK_SECONDS=7200 \
node internal/api/management_soak_e2e.cjs
```

Use 0600 password files and a 0700 artifact directory. The audit companion needs a
JSON baseline with a `files` mapping of absolute file paths to SHA-256 values, and
is run with `--root`, `--baseline`, and `--output`. Keep it active for the entire run;
stop it with SIGTERM after the browser exits so it writes its final snapshot.
