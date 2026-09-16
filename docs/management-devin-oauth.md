# Devin OAuth for WebUI

Devin uses the existing management OAuth callback/status contract. No session token or PKCE verifier is returned to the frontend.

1. Request `GET /v0/management/devin-auth-url?is_webui=true` with the management key. The response is `{"status":"ok","url":"https://app.devin.ai/auth/cli/continue?...","state":"..."}`.
2. Open `url` in a browser and complete Devin authorization.
3. For a remote server, copy the complete final callback URL from the browser address bar (even if the loopback page cannot be reached), then submit it with the management key:

   ```http
   POST /v0/management/oauth-callback
   Content-Type: application/json
   Authorization: Bearer <management-key>

   {"provider":"devin","redirect_url":"<complete callback URL>"}
   ```

   Alternatively, submit `{"provider":"devin","state":"...","code":"..."}`. The `cognition` provider alias is also accepted. Keep the state from the original login attempt; do not mix concurrent attempts.
4. Poll `GET /v0/management/get-auth-status?state=...` with the management key. `status` is `wait`, `ok` after credentials are saved, or `error` with an `error` message. A successful callback submission alone does not mean token exchange has completed.
5. Cancel a pending attempt with `DELETE /v0/management/oauth-session?state=...`.

The generated redirect uses the server's configured port at `http://127.0.0.1:<port>/callback` (Devin's authorization page strictly validates that the redirect URI is `http://127.0.0.1:<port>/callback`). When the browser can reach that loopback server (local deployment or an appropriate tunnel), the public callback route accepts the redirect automatically. Remote deployments do not require opening an extra callback port: use step 3 instead. This is a browser authorization-code flow, not a device-code flow.

Authorization must finish within five minutes. Credentials use the same `devin-*.json` format as `--devin-login`, including normalized session tokens, OAuth auth kind, and best-effort profile/plan metadata. Account/quota enrichment failures do not invalidate an otherwise valid session token.

The WebUI source is maintained separately; add `devin` to its OAuth provider list and wire its login card to these existing callback, polling, and cancellation APIs.
