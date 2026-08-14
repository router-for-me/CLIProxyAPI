# Stateful Stream Interceptor Plugin

This Go dynamic-library plugin demonstrates the stateful response stream interceptor lifecycle introduced by plugin RPC schema version 4.

It declares:

- `response_stream_interceptor`: intercepts response stream initialization and payload chunks.
- `response_stream_interceptor_stateful`: opts into a stable `StreamID`, an end callback, and host-managed plugin pinning for the stream lifetime.

## Lifecycle

The host serializes calls for each stream:

1. `ChunkIndex == -1` (`StreamChunkHeaderInitIndex`) initializes plugin state. The request includes the stable `StreamID` and heavy request fields.
2. `ChunkIndex >= 0` processes payload chunks. After every stateful interceptor initializes successfully, the host may omit `OriginalRequest`, `RequestBody`, and `HistoryChunks`.
3. `ChunkIndex == -2` (`StreamChunkEndIndex`) releases state. The host sends this callback with a context detached from downstream cancellation.

This example keeps an independent payload count for each `StreamID`. It adds `X-Stateful-Stream: initialized` during header initialization, forwards the first `max_chunks` payloads, drops later payloads, and deletes stream state at the end callback.

`DropChunk` affects payload delivery only. Initialization and end callbacks still run for every eligible stateful interceptor.

## Configuration

```yaml
plugins:
  enabled: true
  configs:
    stateful-stream-interceptor:
      enabled: true
      priority: 100
      max_chunks: 3
```

`max_chunks` must be greater than zero.

## Build

From the repository root on macOS:

```bash
mkdir -p plugins/darwin/$(go env GOARCH)
go build -buildmode=c-shared \
  -o plugins/darwin/$(go env GOARCH)/stateful-stream-interceptor.dylib \
  ./examples/plugin/stateful-stream-interceptor/go
rm -f plugins/darwin/$(go env GOARCH)/stateful-stream-interceptor.h
```

Use `.so` on Linux or FreeBSD and `.dll` on Windows.

The output filename is the plugin ID, so the artifact must be named `stateful-stream-interceptor` for the configuration above.

## Relevant RPC Methods

```text
plugin.register
plugin.reconfigure
response.intercept_stream_chunk
```

Stateful stream interception requires host and plugin schema version 4 or newer. A schema 3 plugin registration still uses the payload request-body omission contract, but is treated as a non-stateful stream interceptor even if it sends the stateful capability field.
