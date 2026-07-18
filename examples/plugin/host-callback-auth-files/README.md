# Host Callback Auth Files Plugin

This Go-only plugin demonstrates how a plugin-owned browser resource can call the host auth file callbacks:

- `host.auth.list`
- `host.auth.get`
- `host.auth.get_runtime`
- `host.auth.save`

## Purpose and Scope

The plugin registers a Management API resource named `Host Auth Files` at `/status`. CPA exposes it under:

```text
/v0/resource/plugins/host-callback-auth-files/status
```

The resource reads URL query parameters, calls the host auth callbacks, and renders the result in HTML. It does not implement executor, translator, auth provider, or scheduler capabilities.

## Build

From this directory:

```bash
cd go
go build -buildmode=c-shared -o host-callback-auth-files.dylib .
rm -f host-callback-auth-files.dylib host-callback-auth-files.h
```

Use the platform extension expected by your target system:

- `.dylib` on macOS
- `.so` on Linux
- `.dll` on Windows

## Configuration

Build the dynamic library and place it under the configured plugin directory with a basename that matches the plugin ID. For example, `plugins/host-callback-auth-files.dylib` maps to `plugins.configs.host-callback-auth-files`.

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    host-callback-auth-files:
      enabled: true
      priority: 1
```

This plugin does not define plugin-specific configuration fields.

## Resource URL Examples

List all auth files:

```text
http://localhost:8080/v0/resource/plugins/host-callback-auth-files/status?op=list
```

Read physical JSON by auth index:

```text
http://localhost:8080/v0/resource/plugins/host-callback-auth-files/status?op=get&auth_index=<AUTH_INDEX>
```

Read runtime info by auth index:

```text
http://localhost:8080/v0/resource/plugins/host-callback-auth-files/status?op=runtime&auth_index=<AUTH_INDEX>
```

Save physical JSON:

```text
http://localhost:8080/v0/resource/plugins/host-callback-auth-files/status?op=save&name=example-auth.json&json=%7B%22type%22%3A%22gemini%22%2C%22email%22%3A%22demo%40example.com%22%2C%22api_key%22%3A%22demo-key%22%7D
```

## Parameters

- `op`: one of `list`, `get`, `runtime`, `save`. Default is `list`.
- `auth_index`: required for `get` and `runtime`.
- `name`: required for `save`. Must end with `.json`.
- `json`: required for `save`. Must be valid JSON.

## Notes

- `host.auth.get` returns the physical auth file JSON.
- `host.auth.get_runtime` returns runtime credential metadata.
- `host.auth.save` writes the JSON to the auth directory and upserts the runtime auth record.
