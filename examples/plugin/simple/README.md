# Example Standard Dynamic Library Plugin

This is the full mixed-capability skeleton. For single-capability examples, see `../README.md`.

This directory is the reference skeleton for the current standard dynamic library plugin ABI. The ABI is language-neutral: the host loads a native dynamic library, calls `cliproxy_plugin_init`, and then exchanges JSON envelopes through a stable C function table.

This directory contains complete Go, C, and Rust implementations of the same mixed-capability sample. The Go sample uses `-buildmode=c-shared`; the C sample uses CMake; the Rust sample uses a `cdylib` crate.

## Entry Point

Every plugin must export:

```c
int cliproxy_plugin_init(const cliproxy_host_api* host, cliproxy_plugin_api* plugin);
```

The plugin fills `cliproxy_plugin_api` with:

```c
int call(char* method, uint8_t* request, size_t request_len, cliproxy_buffer* response);
void free_buffer(void* ptr, size_t len);
void shutdown(void);
```

The host provides `cliproxy_host_api` with:

```c
int call(void* host_ctx, char* method, uint8_t* request, size_t request_len, cliproxy_buffer* response);
void free_buffer(void* ptr, size_t len);
```

The C ABI never passes Go interfaces, Go slices, Go maps, Go channels, `context.Context`, or Go errors.

## JSON Envelope

Successful responses use:

```json
{
  "ok": true,
  "result": {}
}
```

Errors use:

```json
{
  "ok": false,
  "error": {
    "code": "invalid_request",
    "message": "request is invalid"
  }
}
```

Raw byte fields are encoded as base64 by JSON.

## Capabilities

`plugin.register` and `plugin.reconfigure` return metadata and capability flags. This sample declares the full provider-native surface:

- model provider
- model registrar
- auth provider
- frontend auth provider
- executor
- request and response transforms
- thinking applier
- usage observer
- command-line plugin
- Management API plugin

Executor plugins must declare `executor_input_formats` and `executor_output_formats` in their capability block. The host passes requests through directly when the client protocol is declared by the executor. Otherwise, the host translates the inbound request into one declared input format and translates the executor response back to the client protocol. This example declares `chat-completions` for both lists, so non-chat-completions protocols are translated by the host. The host also accepts the existing internal aliases `openai`, `openai-response`, and `claude` for Chat Completions, Responses, and Anthropic protocols.

The host keeps the existing precedence rules: native logic wins, plugins fill gaps, and higher-priority plugins run before lower-priority plugins.

## Layout

- `go/`: full mixed-capability Go implementation.
- `c/`: full mixed-capability C implementation with no external dependencies.
- `rust/`: full mixed-capability Rust implementation with no external dependencies.

All three implementations parse incoming JSON requests for the methods where request content matters. Auth methods persist the raw request payload as `StorageJSON`; request and response transforms echo the inbound `Body`; Thinking decodes `Body` and appends `plugin_example_thinking`; executor methods use request fields such as `Model`, `Format`, and `Payload`; Usage keeps an in-process count.

## Build

Build from the repository root.

Build all plugin examples, including all three `simple` variants:

```bash
make -C examples/plugin build
```

Artifacts are written to `examples/plugin/bin` as `simple-go`, `simple-c`, and `simple-rust` with the current platform dynamic-library extension.

Manual Go build on macOS:

```bash
mkdir -p plugins/darwin/$(go env GOARCH)
go build -buildmode=c-shared -o plugins/darwin/$(go env GOARCH)/simple-go.dylib ./examples/plugin/simple/go
rm -f plugins/darwin/$(go env GOARCH)/simple-go.h
```

Manual C build on macOS:

```bash
mkdir -p plugins/darwin/$(go env GOARCH)
cmake -S examples/plugin/simple/c -B /tmp/cliproxy-simple-c-build -DCMAKE_LIBRARY_OUTPUT_DIRECTORY=$PWD/plugins/darwin/$(go env GOARCH)
cmake --build /tmp/cliproxy-simple-c-build
```

Manual Rust build on macOS:

```bash
mkdir -p plugins/darwin/$(go env GOARCH)
cd examples/plugin/simple/rust
CARGO_TARGET_DIR=/tmp/cliproxy-simple-rust-target cargo build --release --locked
cp /tmp/cliproxy-simple-rust-target/release/libcliproxy_simple_rust.dylib ../../../../plugins/darwin/$(go env GOARCH)/simple-rust.dylib
```

For Linux, FreeBSD, or Windows, keep the same source directory and use the platform extension selected by `examples/plugin/Makefile`.

The plugin ID is the dynamic library basename without the platform extension. Makefile-built artifacts map to `plugins.configs.simple-go`, `plugins.configs.simple-c`, and `plugins.configs.simple-rust`.

## Discovery

The host searches:

```text
plugins/<GOOS>/<GOARCH>-<variant>
plugins/<GOOS>/<GOARCH>
plugins
```

Accepted extensions are:

- `.so` on Linux and FreeBSD
- `.dylib` on macOS
- `.dll` on Windows

Plugin IDs must match:

```text
[A-Za-z0-9][A-Za-z0-9._-]{0,127}
```

## Configuration

Dynamic plugins are disabled by default.

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    simple-go:
      enabled: true
      priority: 1
      config1: true
      config2: "string"
      config3: 3
      mode: "safe"
```

`plugins.configs.<pluginID>` is passed to `plugin.register` or `plugin.reconfigure` as normalized YAML bytes inside the JSON request.

## Host HTTP Bridge

Plugins can call host functionality through `host.call`. The HTTP bridge method is:

```text
host.http.do
```

The host still performs the real HTTP request, so proxy handling, transport policy, auth context, and request logging stay under host control.

## Management API

The native plugin management endpoints are:

```text
GET /v0/management/plugins
DELETE /v0/management/plugins/{pluginID}
PATCH /v0/management/plugins/{pluginID}/enabled
GET /v0/management/plugins/{pluginID}/config
PUT /v0/management/plugins/{pluginID}/config
PATCH /v0/management/plugins/{pluginID}/config
```

Plugin-owned Management API routes are registered through the `routes` field of `management.register` and handled through `management.handle`.

Browser-navigable menu resources are registered through the `resources` field of `management.register`. CPA exposes those resources under `/v0/resource/plugins/<pluginID>/...`; for example, a plugin with ID `example` and resource path `/status` is served as `/v0/resource/plugins/example/status`.

## Trust Boundary

Standard dynamic library plugins are trusted in-process code. Panic recovery can protect host-managed calls, but it cannot prevent a plugin from exiting the process, corrupting memory, mutating global process state, or leaking secrets. Install only plugins you trust as much as the service binary.

## Verification

Current platform sample builds:

```bash
make -C examples/plugin list
make -C examples/plugin build
find examples/plugin/bin -maxdepth 1 -type f | wc -l
make -C examples/plugin clean
```

After changing Go code in this repository, also run:

```bash
go build -o test-output ./cmd/server && rm test-output
```
