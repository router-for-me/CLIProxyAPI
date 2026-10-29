# Native plugin on-demand token refresh

## Declare the policy

`pluginapi.AuthData.RefreshPolicy` is an optional typed policy. Omission on initial parse keeps the existing host behavior. Set `"on-demand"` to refresh only when an authenticated request needs the credential.

```go
policy := pluginapi.RefreshOnDemand
return pluginapi.AuthParseResponse{
	Handled: true,
	Auth: pluginapi.AuthData{
		Provider:      "example",
		RefreshPolicy: &policy,
		StorageJSON:   raw,
		Metadata: map[string]any{
			"access_token":  accessToken,
			"refresh_token": refreshToken,
			"expired":       expiresAt.Format(time.RFC3339),
		},
	},
}, nil
```

The native JSON field is `RefreshPolicy`. Its accepted values are `"default"` and `"on-demand"`. Unknown values fail parsing. Omitted policy in a refresh result or model `AuthUpdate` inherits the current policy. An explicit `"default"` removes the opt-in.

Host RPC schema 9 introduces this contract. `pluginabi.SchemaVersionOnDemandRefresh` names the minimum supported schema. ABI version remains 1. A plugin that requires request-only rotation must reject a host that advertises a schema below 9. Older plugins remain loadable and retain their existing behavior.

## Return real credentials

Expose standard `access_token`, `refresh_token`, and real expiry. The host accepts the existing `accessToken`, `refreshToken`, and expiry aliases, then canonicalizes opted-in refresh results. A JWT's `exp` claim takes precedence.

A refresh response can contain raw storage, metadata, or both. Supplied token and expiry values must agree. Partial output overlays the current record. Omitted refresh tokens inherit. A changed bearer needs renewed expiry or a JWT `exp` claim. The refreshed access token must be usable beyond the 60-second request margin, including when the token text stays unchanged.

For OpenAI-compatible execution, the host updates an inherited `api_key` attribute from the canonical access token. Initial opted-in credentials and refresh results must not supply an `api_key` that contradicts that token. The plugin must not keep a separate refresh lock or token owner.

## Host behavior

The host admits expired, opted-in, refreshable credentials only through request selection. Disabled credentials, rejected bearers, terminal auth failures, and active quota blocks remain ineligible. `SelectAuth` alone never admits an expired credential or refreshes it.

The host refreshes when access expiry is at or before the current time plus 60 seconds. It does not falsify the stored expiry. Buffered execution, remote token counts, stream opening, images, authenticated HTTP, quota fetch and reset, and billing token resolution use the committed snapshot. Refresh does not replay emitted stream content or repeat a quota reset.

Background rotation skips opted-in credentials, including jobs queued before a policy change. Model discovery and inspection retain their existing behavior and do not request freshness. SDK embedders bind the same manager with `pluginhost.Host.SetAuthManager` for authenticated quota operations.

## Persistence and failures

On-demand rotation requires a physical file-backed plugin credential and a file token store. Missing stores, runtime-only records, virtual plugin credentials, configuration API keys, and Home-owned credentials are not supported for managed rotation.

The host saves the normal refresh-aware merge before publishing the rotated pair. Save failure returns the actual error and blocks execution. The host retains the rotated result in memory and retries only the save on later demand after the refresh retry deadline. It does not consume the old refresh token again to retry persistence. Force refresh also respects a pending save.

Changing the registration epoch, credential version, expiry, or file source does not release a consumed grant. A temporarily unreadable file can remove the registration, but restoring the old or pending grant keeps it unsettled. The host saves a retained candidate only against its original source. A source change can therefore keep the credential unavailable until a distinct login replaces the grant.

A successful provider rotation with an unusable or contradictory result quarantines the credential in the same refresh slot. Later demand, force refresh, a policy change, and reload cannot reuse the potentially consumed grant. A distinct replacement with usable access and refresh credentials releases that hold. Refresh endpoint errors remain retryable under the existing backoff.

A transient failure from voluntary force refresh does not block an otherwise fresh, unrejected credential. Due-demand failures and unsettled rotations still stop authenticated dispatch. A managed executor route change during preparation stops dispatch before the captured executor can run.

Managed refresh errors expose the sanitized upstream summary to HTTP clients. SDK callers retain the original cause through `errors.Is` and `errors.As`, including its HTTP status.

The host persists `_cliproxy_refresh_policy` in plugin storage. A current plugin parser must handle the file to restore that policy after restart. Built-in metadata does not opt into this behavior.

Canceling a request stops that request's wait, not the owner of a native rotation. A late successful native result is saved. An in-process native call that never returns cannot be safely killed, and no second rotation starts for its unsettled identity.

A successfully saved pair survives a fresh process restart. A crash before any successful save can lose the retained pair. Atomic rename is not a power-loss durability guarantee. Remote store restart equivalence and durable crash journals are outside this contract.
