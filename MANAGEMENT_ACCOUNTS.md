# Management accounts

## Login and bootstrap

Open `/accounts.html` on the existing management service origin. The initial username is `admin`.
On the first successful account-store initialization, a random password is written to:

```
<config.yaml directory>/.management-auth/management-admin-initial-password.txt
```

The directory is created with mode `0700`; the password file and account database
(`management-accounts.json`) use `0600`. Read the password locally on the server;
do not send it in a URL or paste it into logs. Change it using the account page after
logging in, then remove the bootstrap password file. It is not recreated on normal restarts.
Back up the account database securely with the service configuration. Do not delete the
account database to reset a password; an existing administrator can reset passwords.

The existing remote-management availability/allow-remote settings still apply. Use HTTPS
(or an SSH tunnel) when accessing the password login remotely. Existing management keys
remain full administrator credentials for backward compatibility and recovery. Do not
share those keys with users.

## Permissions

- `admin`: all existing management operations plus user creation, password reset and
  enable/disable. The fixed admin account cannot be disabled or demoted.
- `user`: own profile/password, global non-protected configuration, API keys, provider
  credentials, OAuth login/refresh, shared account-pool operations, global logs and
  ordinary plugin management.
- Users cannot access billing/quota, account administration, usage administration,
  or the unrestricted management `api-call` proxy.
- Configuration exports hide billing/quota and administrative security settings.
  User configuration saves preserve protected values and reject changes to them.
  Authentication-directory and pprof settings are protected to prevent indirect
  credential-store access. Credential lists hide quota metadata.
- Plugin config reads redact billing/quota. User PUT preserves omitted protected
  settings; PUT/PATCH reject protected additions, changes, null deletion and array
  replacement. Plugins with saved billing/quota settings require admin for lifecycle
  operations (enable/disable, delete or reinstall); generic config edits cannot change
  their enable flags or the global plugin enable/directory settings.
- Read-only provider request success/failure counters are available to users for the dashboard and provider pages; these contain no billing or quota settings.
- The UI hides billing/quota navigation for users; the API enforces the actual permissions.

**These are trusted global operators, not isolated tenants.** Users can see and modify
shared provider credentials and logs. Custom plugins execute trusted code; application
RBAC is not a sandbox for hostile plugin code. Possession of provider credentials can
also allow direct upstream access outside this application's RBAC boundary.

## Sessions and API calls

Account sessions expire after 12 hours and are held in memory; a service restart requires
logging in again. Password changes/resets, disabling an account and logout revoke the
relevant sessions. Passwords use bcrypt; session tokens are random and hashed server-side.
Failed logins are rate limited using the connected peer IP, not arbitrary forwarding headers.

The account page stores the session token (never the password) in browser localStorage,
including compatibility entries used by the existing management UI. All same-origin
scripts/plugins therefore must be trusted. Use the account-page logout to revoke the session.

Model calls continue to use existing model API keys, not account passwords or management
session tokens. Disabling a management account does not revoke shared model API keys.

## Verification

```
go test ./internal/managementauth ./internal/api/handlers/management ./internal/api
node --test internal/api/account_portal_test.cjs internal/api/billing_page_test.cjs
node --check internal/api/account_bridge.js
```

Account APIs are under `/v0/management/accounts`: public `POST /login`; authenticated
`GET /me`, `POST /logout`, `PUT /password`; admin-only `GET/POST /users` and
`PATCH /users/:username`.

## Real-browser regression

`internal/api/account_browser_e2e.cjs` uses Playwright Chromium against a running
service. Install Playwright and Chromium in a separate test directory, then run with
`NODE_PATH` pointing to its `node_modules`. Set `CPA_TEST_BASE_URL`, `CPA_TEST_USER`
and `CPA_TEST_PASSWORD_FILE` (a private `0600` file). The test signs in with a user
account, verifies desktop/mobile navigation, session restore, allowed pages,
account management navigation, forbidden APIs and logout revocation. It does not
modify passwords, credentials or global configuration. Optional screenshots go to
`CPA_TEST_ARTIFACTS`; keep that directory private. Remove the password file afterward.

The upstream console temporarily navigates to `#/login` while restoring its session.
The account bridge must not mistake that intermediate route for logout: it hides the
legacy form while `isLoggedIn` is present, and revokes the session only on actual logout.
