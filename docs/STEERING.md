# Codex response steering

Enable the experimental full-duplex transport with:

```yaml
codex:
  response-steering: true
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
  authentication/quota cooldown and failover policy. Follow-up input remains
  queued until the first `response.created` is delivered, so bootstrap failover
  cannot consume steering or creates on the rejected account. Once a response
  is visible downstream, there is no reconnect, cross-account failover, or replay. Only
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
- Explicit creates wait while steering is awaiting acknowledgement or an
  automatic successor. A tool-result continuation can be submitted early; it is
  released when upstream reports the required-input boundary. Steering waits
  for already-submitted explicit creates to receive their creation events.
- Automatic successors use their parent's response settings. The connection
  retains 16 recent lightweight settings snapshots; in-flight steering pins its
  parent independently. If an automatic successor refers to unavailable parent
  settings, the socket fails rather than applying another request's metadata.
- Rejections before a later create is established consume that create's pending
  metadata. Failures identified as the current response leave queued creates
  intact. Invalid reasoning replay is cleared only for the failing scope.
- After a response has started on the opted-in Codex duplex route, top-level
  error payloads are forwarded without closing the downstream connection, so
  clients can correct a rejected create. Initial errors, executor stream errors,
  and non-duplex routes retain terminal handling.
- Malformed JSON and unsupported client event types receive a local 400 error
  without closing the established socket or forwarding the invalid frame. The
  client can send corrected steering or a create on the same connection.
- Later authentication, permission, and rate/quota failures (401/403/429) retain
  their status, headers, cooldown and quota scope through the auth manager. The
  original failure is forwarded before closing; the started response is not
  retried. Ordinary request-shape errors remain recoverable.
- If a failure has no response ID while both an active response and a pending
  create exist, or a steering submission is awaiting acknowledgement, the
  connection closes after forwarding the failure. The proxy
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
account without waiting for the rejected socket to close. Follow-up preservation,
bootstrap cancellation, and corrected frames after local validation errors are
covered through the real conductor and downstream handler. These tests do not
claim GUI behavior, model quality, or Fast performance equivalence.

Protocol reference: https://developers.openai.com/api/docs/guides/steering
