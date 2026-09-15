# Codex response steering

Enable the experimental full-duplex transport with:

```yaml
codex-response-steering: true
```

The default is false. The client must use the Responses WebSocket endpoint and
the selected Codex credential must support upstream WebSockets.

The transport forwards `response.steer` without applying response-create
defaults or translating its payload. Upstream acknowledgements, pending events,
and failures are preserved. The connection remains readable after a response
ends, allowing automatic successors and client tool results on the same socket.

## Boundaries

- Each connection stays bound to its selected account and model. Reconnect and
  restore context to change models or select a different account.
- Initial rejections before the first response is created use the existing
  authentication/quota cooldown and failover policy. Once a response is visible
  downstream, there is no reconnect, cross-account failover, or replay. Only
  upstream acknowledgements establish ownership of steering input.
- Disabling the selected credential rejects subsequent client frames and closes
  the connection. A new client connection may select another enabled account.
- Subsequent creates reuse the executor's request preparation, but do not
  re-enter per-request plugin interception. This mode is intended for native
  Codex requests without per-request plugins.
- Stream bootstrap buffering is bypassed after duplex handoff. Initial terminal
  failures are returned to the conductor as errors, with their status, retry
  delay, and credential scope preserved.
- A transport failure closes the connection without cooling an otherwise
  healthy shared credential.
- Rejections before a later create is established consume that create's pending
  metadata. Failures identified as the current response leave queued creates
  intact. Invalid reasoning replay is cleared only for the failing scope.
- If a failure has no response ID while both an active response and a pending
  create exist, the connection closes after forwarding the failure. The proxy
  does not guess which request owns it or replay either request.
- Normal per-response accounting and steering control frames are distinct:
  acknowledgements do not represent a successful completed response.

## Tests

The mocked-upstream tests require no subscriptions or external API access:

```sh
go test -race -count=1 ./sdk/api/handlers/openai
go test -race -count=1 -run 'TestCodex.*Duplex' ./internal/runtime/executor
go build -o test-output ./cmd/server
```

Coverage includes steering during generation, multiple submissions, automatic
successors, tool-result pending states, credential disablement, and disconnects
without replay. Initial rejection tests cover authentication and quota errors,
error metadata, stream closure, account cooldown, and failover to a healthy
account without waiting for the rejected socket to close. These tests do not
claim GUI behavior, model quality, or Fast performance equivalence.

Protocol reference: https://developers.openai.com/api/docs/guides/steering
