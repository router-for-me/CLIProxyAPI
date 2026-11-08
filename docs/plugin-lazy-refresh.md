# Native plugin lazy token refresh

Schema version 11 adds an optional `RefreshPolicy` to `pluginapi.AuthData`.
The native C ABI remains version 1. Older plugins retain their existing
refresh behavior. A plugin that requires lazy refresh must declare
`schema_version: 11` in its registration response so older hosts reject it
instead of silently ignoring the policy.

Return the policy from `ParseAuth` or a successful login response:

```go
pluginapi.AuthData{
	Provider: "my-provider",
	RefreshPolicy: &pluginapi.AuthRefreshPolicy{
		Mode:                pluginapi.AuthRefreshLazy,
		ExpiryMarginSeconds: 60,
	},
	Metadata: map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"expires_at":    expiresAt,
	},
	StorageJSON: storageJSON,
}
```

The corresponding JSON field is:

```json
{
  "RefreshPolicy": {
    "mode": "lazy",
    "expiry_margin_seconds": 60
  }
}
```

`mode` accepts `lazy` and `scheduled`. Omission preserves existing behavior.
The margin is inclusive and accepts zero through 3600 seconds. Zero refreshes
at expiry. Invalid policies fail auth conversion. Return `scheduled` explicitly
to turn off a previously configured lazy policy. Partial refresh and model
updates that omit the policy preserve it.

## Request lifecycle

The host does not schedule automatic refresh for lazy credentials. Explicit
manual refresh and existing 401 recovery remain available. Keep accurate access
token, refresh token, and expiry metadata.

An expired credential with a usable refresh grant can enter selection. The host
refreshes it before invoking the executor if its access token is missing,
expired, or within the configured margin. Disabled credentials, rejected grants,
and unrelated model or credential cooldowns remain blocked.

The freshness step covers buffered execution, images, streaming, token counts,
auth-bound HTTP preparation and requests, and native quota fetch/reset operations.
Subscription and billing data supplied by `QuotaProvider` use the same step.
Quota requests must identify a registered auth by `AuthID` or `AuthIndex`.
For lazy credentials, the host replaces stale caller-supplied credential fields
with the current snapshot and builds transport from that snapshot.

The host invokes `RefreshAuth` under the same per-auth lock used for 401 recovery.
Concurrent requests recheck the latest credential after waiting and reuse a
successful rotation. A transient refresh failure returns to the caller without
inference and is reused during the host's refresh failure backoff. Later demand
retries the refresh. Terminal grant rejection requires new credentials.
Streaming refresh completes before the inference stream opens.
Public errors retain the refresh failure's status, code, and retry hint.
The host redacts credential values, bearer tokens, URLs, and OAuth secrets from
the message and does not expose the underlying raw error.

## Persistence

Return the new token pair and accurate expiry in `Metadata`, `StorageJSON`, or
both. When both contain a credential field, `Metadata` takes precedence.
The host overlays the resulting metadata onto plugin storage before saving.
The host persists the policy in the reserved `host_refresh_policy` property
and restores it while parsing the saved file after restart.

An opted-in rotation must reach the configured store before inference.
If saving fails, the request fails and the host retains the rotated material
under the refresh lock. The next request retries persistence without submitting
the consumed old refresh grant. Replacing or removing the credential invalidates
that pending update. If the process exits before storage recovers, the failed
rotation is not restart-safe. SDK managers without a store remain in-memory.

This policy applies to host-managed local credentials. Home retains its separate
credential ownership and dispatch lifecycle.
With a configured store, virtual records expanded from multi-auth files,
runtime-only records, and config API-key records cannot opt into lazy rotation.
Their persistence belongs to another source owner. The host rejects those
credentials before rotation instead of treating a skipped save as durable.
