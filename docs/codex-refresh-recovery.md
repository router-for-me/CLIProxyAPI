# Codex refresh recovery

A lost refresh response does not establish whether the provider consumed the refresh token. CLIProxyAPI records a claim **before** sending a Codex or local SIWC refresh request. Failures proven to occur before any request bytes can be written (request creation, DNS resolution, TCP/proxy dial, HTTP proxy `CONNECT`, and origin TLS handshake prior to connection handoff) release the pending claim so the grant can be retried later; on macOS, maintenance wakes lacking interactive power capabilities also defer renewal before claiming. Ambiguous post-connection transport failures, unreadable or invalid successful responses, and HTTP responses without a qualified no-effect contract keep the claim and stop replay. A 429 or 5xx status alone is not such a contract. Redirects cannot replay refresh POSTs.

## Recovery

- An unresolved outcome is reported as `refresh_outcome_unknown`, with its failure stage. An unexpired access token remains usable; the refresh grant requires recovery.
- Scheduled and forced refreshes cannot resubmit a claimed grant. There is no lease expiry or time-based reset.
- A successful owner saves a receipt before returning its new credentials. Late callers reuse unexpired receipts and follow subsequent recorded rotations rather than resubmitting old tokens. An expired receipt requires recovery, including when a successful response returned the same refresh token; it is never reported as successful renewal.
- Adopt credentials from the successful refresh owner, or sign in again to obtain a **new** grant. Re-importing the same token, deleting an auth record, or restarting the server is not recovery.
- If a process stops between recording its claim and saving its result, the outcome remains uncertain. A failed journal write also fails closed; do not delete the claim to retry.

## Persistence and deployment

The journal must accompany credentials across backups, migrations, and restores:

| Credential storage | Authoritative journal |
| --- | --- |
| Files | `.codex-refresh/` under the auth directory |
| PostgreSQL | `<auth-table>_refresh_journal` in the configured schema |
| Git | Immutable `refs/tags/cliproxy-refresh/` references in the same remote |
| S3-compatible storage | `codex-refresh/` under the configured object prefix |

Receipts contain credentials. Protect them with the same access controls as auth files. Do not publish the Git repository, bucket, database, or journal directory. Claims and receipts are retained rather than expiring automatically. Revoke the associated grants before administratively removing their journal history. Restoring older credentials without their complete journal history is unsafe.

On Unix, file claims synchronize their canonical directory ancestry before an exchange; those directories must permit synchronization. Windows preserves claims across process restarts but does not support directory fsync, so this is not a power-loss durability guarantee.

PostgreSQL initializes the journal before refresh workers start, using a transaction-scoped advisory lock to coordinate concurrent initialization. It requires permission to create the journal table and acquire that lock. Git requires permission to create the journal tags; all journal pushes are non-force. S3 backends must implement atomic `If-None-Match: *` writes. A capability check rejects backends that ignore that condition. Storage failure never falls back to refreshing against a local mirror.

All processes sharing credentials must use the same authoritative journal and this safety protocol. Older deployments or external clients refreshing the same grant cannot be coordinated by a journal they do not participate in.

Direct SDK users can attach a `codex.RefreshJournal` using `WithRefreshJournal` or `SetRefreshJournal`. Without a configured auth directory or explicit journal, protection is process-local only; a caller persisting credentials across restarts must also provide a durable journal.

CLIProxyAPIHome owns its separate outer-token refresh implementation. These changes cover refreshes performed in this repository (including local SIWC refreshes in Home mode); the Home service requires its own corresponding update.
