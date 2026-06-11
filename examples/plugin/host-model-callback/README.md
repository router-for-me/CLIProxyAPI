# Host Model Callback Plugin

This Go-only plugin demonstrates how a plugin-owned browser resource can call the host model execution callbacks instead of sending any external HTTP request itself.

## Purpose and Scope

The plugin registers a Management API resource named `Host Model Callback` at `/status`. CPA exposes it under:

```text
/v0/resource/plugins/host-model-callback/status
```

The resource examples are query-based. The resource reads URL query parameters, builds an OpenAI-compatible chat request, and calls:

- `host.model.execute` for non-streaming model execution.
- `host.model.execute_stream`, `host.model.stream_read`, and `host.model.stream_close` for streaming execution.

This example is intentionally limited to host model callbacks. It does not implement an executor, translator, normalizer, auth provider, scheduler, or any direct outbound HTTP client.

## Build

From this directory:

```bash
cd go
go build -buildmode=c-shared -o host-model-callback.dylib .
rm -f host-model-callback.dylib host-model-callback.h
```

Use the platform extension expected by your target system:

- `.dylib` on macOS
- `.so` on Linux
- `.dll` on Windows

## Configuration

Build the dynamic library and place it under the configured plugin directory with a basename that matches the plugin ID. For example, `plugins/host-model-callback.dylib` maps to `plugins.configs.host-model-callback`.

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    host-model-callback:
      enabled: true
      priority: 1
```

This plugin does not define plugin-specific configuration fields.

## Resource URL Examples

Non-streaming request with defaults:

```text
http://localhost:8080/v0/resource/plugins/host-model-callback/status
```

Non-streaming request with explicit protocol and prompt:

```text
http://localhost:8080/v0/resource/plugins/host-model-callback/status?entry_protocol=openai&exit_protocol=openai&model=gpt-5.5&prompt=Say%20hello%20in%20one%20sentence
```

Streaming request with explicit close:

```text
http://localhost:8080/v0/resource/plugins/host-model-callback/status?stream=true&model=gpt-5.5&prompt=Write%20three%20short%20tokens
```

Streaming request that relies on RPC-scope implicit close:

```text
http://localhost:8080/v0/resource/plugins/host-model-callback/status?stream=true&implicit_close=true
```

The default model ID is `gpt-5.5` to match the current nearby Codex example documentation and code. It is only an example model identifier; the request succeeds only when your CPA configuration can route that model.

## Parameters

- `entry_protocol`: inbound client protocol passed to the host model execution path. The default is `openai`.
- `exit_protocol`: target provider protocol passed to the host model execution path. The default is `openai`.
- `model`: model identifier passed in the host model execution request. The default is `gpt-5.5`; availability depends on the configured model registry and auth records.
- `stream`: boolean flag. The default is `false`; set `stream=true` to use `host.model.execute_stream`.
- `prompt`: text used to build the default OpenAI-compatible request body.
- `body`: optional JSON string in the URL query used as the raw model request body. When `body` is provided, it replaces the generated body.
- `alt`: optional alternate route or mode suffix passed through the host model request.
- `implicit_close`: streaming-only boolean flag. The default is `false`.

The generated default body is OpenAI-compatible:

```json
{
  "model": "gpt-5.5",
  "stream": false,
  "messages": [
    {
      "role": "user",
      "content": "Summarize host model callbacks in one short sentence."
    }
  ]
}
```

For example, a URL-encoded `body` query value can provide the raw OpenAI-compatible request:

```text
http://localhost:8080/v0/resource/plugins/host-model-callback/status?body=%7B%22model%22%3A%22gpt-5.5%22%2C%22stream%22%3Afalse%2C%22messages%22%3A%5B%7B%22role%22%3A%22user%22%2C%22content%22%3A%22Say%20hello%20in%20one%20sentence%22%7D%5D%7D
```

## Stream Close Semantics

By default, streaming mode explicitly closes the host-owned stream with `host.model.stream_close` through a deferred close call. This is the preferred pattern for plugins because it releases stream resources as soon as the plugin has finished reading.

When `implicit_close=true` is set, the plugin intentionally skips the explicit close call. CPA injects `host_callback_id` into the `management.handle` request, and this example forwards that callback ID to `host.model.execute_stream` so the host can close the stream when the `management.handle` RPC callback scope returns. This mode exists only to demonstrate host cleanup behavior; normal plugin code should explicitly close streams it opens.

## Billing and Usage

The callback uses the existing CPA model executor path. Usage collection, request accounting, and billing metadata are handled by the same executor and usage reporter path as normal proxied requests. The callback layer does not bill twice and does not create an additional usage record by itself.

## Error Handling and Troubleshooting

The page displays the model status, response headers, body, stream chunks, close mode, and any callback error returned by the host envelope.

Common issues:

- `host model executor is unavailable`: the host model executor path is not initialized for this plugin callback context.
- `unsupported model` or provider-specific routing errors: the `model` value is not routable with the current CPA model/auth configuration.
- `host.model.execute requires stream=false`: non-stream execution was called with a streaming request.
- `host.model.execute_stream requires stream=true`: streaming execution was called without `stream=true`.
- Empty or partial stream output: inspect the page error section and host logs; upstream stream errors are returned through `host.model.stream_read`.
