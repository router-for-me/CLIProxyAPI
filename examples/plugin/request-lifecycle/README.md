# Request Lifecycle Plugin

This Go dynamic-library plugin demonstrates request admission, active termination, and exactly-once terminal lifecycle handling. It requires a host that supports plugin RPC schema version 2 or newer.

It declares two optional capabilities:

- `request_interceptor`: acquires a concurrency slot in `request.intercept_before` and can terminate the request before any upstream executor runs.
- `request_lifecycle_plugin`: releases the slot in `request.complete` for successful, failed, rejected, and canceled requests.

The host passes the same `RequestID` to request interception, response interception, stream interception, and the terminal `RequestCompletion` event.

## Behavior

- Allows at most `max_concurrency` requests in flight.
- Returns a custom `429` JSON response with `Retry-After: 1` when the limit is reached.
- Returns a custom `403` JSON response when the raw request body contains `reject_keyword`.
- Does not send terminated requests to an upstream model.
- Releases only request IDs that were previously admitted, so rejected requests and duplicate terminal events do not underflow the counter.

## Configuration

```yaml
plugins:
  enabled: true
  configs:
    request-lifecycle:
      enabled: true
      priority: 100
      max_concurrency: 2
      reject_keyword: "blocked"
```

Set `reject_keyword` to an empty string to disable keyword rejection.

## Build

From the repository root on macOS:

```bash
mkdir -p plugins/darwin/$(go env GOARCH)
go build -buildmode=c-shared \
  -o plugins/darwin/$(go env GOARCH)/request-lifecycle.dylib \
  ./examples/plugin/request-lifecycle/go
rm -f plugins/darwin/$(go env GOARCH)/request-lifecycle.h
```

Use `.so` on Linux or FreeBSD and `.dll` on Windows.

The output filename is the plugin ID, so the example artifact must be named `request-lifecycle` for the configuration above.

## Relevant RPC Methods

```text
plugin.register
plugin.reconfigure
request.intercept_before
request.intercept_after
request.complete
```

`request.complete` is an observational callback. The host schedules it asynchronously so a blocked plugin cannot delay response delivery, logs callback errors, and uses a context detached from downstream cancellation so a canceled request can still release its slot.
