# Browser login with OpenID Connect

CLIProxyAPI can accept an OpenID Connect browser session for the v8 Management API.
The Management API key remains available for automation and for the legacy v0 API.

Management login uses `/v8/management/auth/oidc/login` and
`/v8/management/auth/oidc/callback`; session discovery and logout use
`/v8/management/auth/session` and `/v8/management/auth/logout`. These routes
authenticate administrators to this instance. The existing `/v8/management/oauth/*`
routes acquire provider credentials for upstream API access and remain separate.

Set these process environment variables to enable browser login:

| Variable | Value |
| --- | --- |
| `MANAGEMENT_OIDC_ISSUER` | HTTPS issuer URL with OpenID Provider Discovery enabled. |
| `MANAGEMENT_OIDC_CLIENT_ID` | Client ID registered with the identity provider. |
| `MANAGEMENT_OIDC_CLIENT_SECRET` | Optional client secret; leave empty for a public PKCE client. |
| `MANAGEMENT_OIDC_REDIRECT_URL` | Registered HTTPS callback URL ending in `/v8/management/auth/oidc/callback`. |
| `MANAGEMENT_OIDC_ALLOWED_SUBJECTS` | Comma-separated exact `sub` claim values permitted to manage this instance. |

OIDC is disabled when `MANAGEMENT_OIDC_ISSUER` is unset. Invalid OIDC configuration
is logged and disables OIDC; a configured Management API key continues to work.
With valid OIDC configuration, the Management API routes are enabled even when no
management key is configured. OIDC browser sessions may be used remotely; the
`management.allow-remote` setting continues to control management-key access.

The flow uses authorization code, PKCE S256, browser-bound state, and nonce. The
server verifies the ID token signature, issuer, audience, expiry, nonce, and exact
subject allowlist before creating a session. It does not retain identity-provider
access or refresh tokens. Forwarded identity headers do not authenticate.

The callback URL and Management Center UI must share an origin and be served over
HTTPS. Browser sessions use secure, HttpOnly, host-only cookies. Cookie-authenticated
v8 requests require `X-Management-CSRF: 1`, must have a matching `Origin` when that
header is present, and reject cross-site fetches. Explicit management-key requests
continue to use the existing key-authentication rules. OIDC cookies do not grant
access to `/v0/management`.

Sessions and login transactions are held in memory and capped at 1024 entries each.
Login transactions expire after five minutes. Starting another login replaces that
browser's previous transaction; when the pending store is full, new logins evict the
oldest pending transaction. A displaced login must be restarted. This keeps abandoned
attempts from blocking all new logins until expiry, but does not provide rate limiting
against a sustained flood.

Sessions expire at the earlier of one hour and ID-token expiry; restarting the process revokes all sessions. Use one
instance or sticky routing. Logout revokes the local browser session and does not
sign the user out of the identity provider.
