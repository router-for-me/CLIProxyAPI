# Upstream authentication errors

When every eligible local OAuth credential is unavailable after a terminal
unauthorized refresh failure, CLIProxyAPI returns HTTP 503 with
`error.code: "upstream_authentication_required"`:

```json
{
  "error": {
    "type": "server_error",
    "code": "upstream_authentication_required",
    "message": "All eligible upstream credentials require reauthentication; ask the proxy operator to sign in again"
  }
}
```

The message can include a redacted diagnostic from the last upstream failure.
Clients should match the code, not the message text. On this code, stop automatic
retries for the current operation and ask the proxy operator to reauthenticate
or replace the affected upstream credentials. Retry after that intervention.
Changing the client's proxy API key does not repair upstream OAuth credentials.

The HTTP status remains 503 because the proxy cannot serve the requested model.
The code supplies the non-retryable client contract; a client that retries every
5xx without inspecting the code will continue to retry. No `Retry-After` hint is
generated for this condition. The same code is returned for initial streaming
and non-streaming Responses failures.

The classification requires the existing terminal refresh state, no remaining
valid access token or future recovery deadline, and every eligible candidate to
satisfy those conditions.
A last unauthorized error alone is insufficient. Mixed pools with temporary
refresh failures or quota cooldowns retain their existing error behavior, and a
healthy eligible credential can still serve the request. Successful credential
refresh or replacement clears the condition.

SDK callers can use `auth.IsUpstreamAuthenticationRequired(err)`. Existing
`errors.As(err, &authErr)` consumers still see `auth_unavailable`, HTTP 503, and
`Retryable: false`, and the underlying diagnostic remains available through
error unwrapping. The local scheduler still refreshes a stale selection snapshot
before returning the error. This does not infer terminal state for a remote Home
scheduler or an external plugin from its generic unavailable response.
