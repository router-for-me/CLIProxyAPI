# JS Handler Plugin

A CLIProxyAPI plugin that executes external JavaScript scripts to intercept and modify requests, responses, and streaming chunks using the Goja VM engine.

## Features

- **Request Interception** (`on_before_request`): Modify request payloads and headers before upstream delivery.
- **Response Interception** (`on_after_nonstream_response`): Modify non-streaming response bodies and headers.
- **Stream Chunk Interception** (`on_after_stream_response`): Modify individual streaming chunks with read-only `history_chunks` context.
- **Hot Reload**: Scripts are automatically reloaded when modified on disk.
- **Execution Timeout**: Configurable timeout prevents infinite loops.
- **Graceful Degradation**: Original data is preserved on JS execution errors.

## Configuration

```yaml
plugins:
  enabled: true
  dir: "plugins-dir"
  configs:
    jshandler:
      enabled: true
      script_paths:
        - /path/to/custom_handler.js
        - ./relative_handler.js
      timeout: 1s
```

### Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | boolean | `true` | Enable or disable the plugin |
| `script_paths` | array | `[]` | JS script file paths (absolute or relative to plugin directory) |
| `timeout` | string | `1s` | Execution timeout per JS hook call |

## JS Script API

Scripts can export these global functions:

### `on_before_request(ctx)`

Called before the request is sent upstream.

**ctx structure:**
```javascript
{
    "id": "request-id",
    "body": "...",        // Request body string
    "headers": {},        // Request headers
    "url": "",
    "model": "gpt-4",
    "protocol": "openai"
}
```

### `on_after_nonstream_response(ctx)`

Called after a non-streaming response is received from upstream.

**ctx structure (non-streaming):**
```javascript
{
    "id": "request-id",
    "body": "...",        // Full response body
    "req": { "body": "...", "headers": {}, "url": "" },
    "protocol": "openai",
    "headers": {},
    "chunk": null,
    "history_chunks": null
}
```

### `on_after_stream_response(ctx)`

Called after each streaming response chunk is received from upstream.

**ctx structure:**
```javascript
{
    "id": "request-id",
    "body": null,
    "req": { "body": "...", "headers": {}, "url": "" },
    "protocol": "openai",
    "headers": {},
    "chunk": "...",              // Current writable chunk
    "history_chunks": ["..."]    // Read-only frozen array
}
```

### Return Value

Return the modified `ctx` object, or a plain string to replace the body/chunk.

## Built-in Scripts

The `scripts/` directory contains built-in scripts loaded automatically:

- `copilot_handler.js`: Fixes tool-call `finish_reason` for GitHub Copilot compatibility.

## Building

```bash
make build
```

The Makefile chooses the plugin extension from the target platform:

| GOOS | Output |
|------|--------|
| `linux` / `freebsd` | `jshandler.so` |
| `darwin` | `jshandler.dylib` |
| `windows` | `jshandler.dll` |

You can override the target and output directory:

```bash
make build GOOS=darwin GOARCH=arm64 BUILD_DIR=/path/to/plugins/darwin/arm64
```
