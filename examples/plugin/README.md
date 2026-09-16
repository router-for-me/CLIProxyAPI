# Standard Dynamic Library Plugin Examples

This directory contains standard dynamic library plugin examples for the CLIProxyAPI C ABI.

## Layout

- `simple/`: full provider-native skeleton that declares every supported capability.
- `model/`: model capability only.
- `auth/`: auth provider capability only.
- `frontend-auth/`: frontend auth provider capability only.
- `frontend-auth-exclusive/`: frontend auth provider that becomes the only request authentication provider when selected.
- `executor/`: executor capability only.
- `protocol-format/`: minimal executor focused on input/output format declarations.
- `request-translator/`: request translation capability only.
- `request-normalizer/`: request normalization capability only.
- `codex-service-tier/`: Go-only request normalizer that sets Codex `gpt-5.5` requests to the priority service tier when enabled.
- `request-lifecycle/`: Go-only request admission example with concurrency control, active HTTP termination, and terminal callbacks.
- `scheduler/`: Go-only scheduler that can select a configured auth ID, delegate to a built-in scheduler, or deny picks.
- `claude-web-search-router/`: ModelRouter + executor for Claude Code built-in `web_search` (antigravity / codex / xai / Tavily). See `claude-web-search-router/README.md`.
- `response-translator/`: response translation capability only.
- `response-normalizer/`: response normalization capability only.
- `thinking/`: thinking applier capability only.
- `usage/`: usage observer capability only.
- `cli/`: command-line capability only.
- `management-api/`: Management API and resource capability only.
- `host-callback/`: minimal plugin resource that demonstrates host callbacks.
- `host-callback-auth-files/`: Go-only plugin resource that calls host auth file callbacks.
- `host-model-callback/`: Go-only plugin resource that calls the host model execution callbacks.

Most standard capability examples contain `go/`, `c/`, and `rust/` subdirectories. Specialized examples may provide only the implementation language they need.

## Codex Service Tier

`codex-service-tier` declares the request normalization capability. When `fast` is `true`, it sets `service_tier` to `priority` for requests where `req.ToFormat` is `codex` and `req.Model` is `gpt-5.5`.

```yaml
plugins:
  configs:
    codex-service-tier:
      enabled: true
      priority: 1
      fast: false
```

## Request Lifecycle

`request-lifecycle` combines `request_interceptor` with `request_lifecycle_plugin`. It acquires a concurrency slot before auth selection, can return a custom `403` or `429` response without contacting an upstream model, and releases admitted slots from `request.complete` on success, failure, rejection, or cancellation.

```yaml
plugins:
  configs:
    request-lifecycle:
      enabled: true
      priority: 100
      max_concurrency: 2
      reject_keyword: "blocked"
```

See `request-lifecycle/README.md` for build instructions and lifecycle semantics.

## Host Auth Files Callback

`host-callback-auth-files` declares the Management API capability and exposes a browser resource named `Host Auth Files`. The resource demonstrates `host.auth.list`, `host.auth.get` (physical JSON file), `host.auth.get_runtime`, and `host.auth.save`.

```yaml
plugins:
  configs:
    host-callback-auth-files:
      enabled: true
      priority: 1
```

See `host-callback-auth-files/README.md` for URL examples.

## Host Model Callback

`host-model-callback` declares the Management API capability and exposes a browser resource named `Host Model Callback`. The resource calls `host.model.execute` for non-streaming requests and `host.model.execute_stream` plus `host.model.stream_read` for streaming requests. It demonstrates explicit stream close with `host.model.stream_close` and an `implicit_close=true` option for RPC-scope host cleanup.

When the resource forwards its `host_callback_id`, CPA identifies the plugin that initiated the host model callback and skips that same plugin's interceptors for the nested execution. This makes host model callbacks non-recursive for the caller while allowing other plugins to intercept the nested request.

```yaml
plugins:
  configs:
    host-model-callback:
      enabled: true
      priority: 1
```

The default example model is `gpt-5.5`, but the request succeeds only when the current CPA model and auth configuration can route that model.

## Scheduler

`scheduler` declares the scheduler capability. It can select a configured auth ID from the candidate list, delegate to the built-in `fill-first` or `round-robin` scheduler, or reject picks when `deny` is `true`.

```yaml
plugins:
  configs:
    scheduler:
      enabled: true
      priority: 1
      auth_id: ""
      delegate: ""
      deny: false
```

`auth_id` selects a matching candidate when `delegate` is empty. `delegate` accepts `""`, `fill-first`, or `round-robin`; other non-empty values leave the pick unhandled. `deny` returns a scheduler error.

## Plugin Executor Error Handling and HTTP Status

When a plugin executor encounters an upstream failure (such as `401 Unauthorized` for invalid credentials, `403 Forbidden` for model permission/quota limits, or `429 Too Many Requests` for rate limits), it should report the HTTP status code in the error envelope:

- In the JSON RPC error envelope, set the `http_status` field inside the `error` object (`pluginabi.Error.HTTPStatus`).
- If `http_status` is omitted or `0`, CPA defaults to returning HTTP `500 Internal Server Error` (`server_error` / `internal_server_error`), which clients typically treat as a temporary gateway outage and retry with backoff.
- When `http_status` is set, CPA maps the status into client-visible error responses:
  - `401` -> HTTP 401 with `type: "authentication_error"`, `code: "invalid_api_key"`
  - `403` -> HTTP 403 with `type: "permission_error"`, `code: "insufficient_quota"`
  - `429` -> HTTP 429 with `type: "rate_limit_error"`, `code: "rate_limit_exceeded"`
  - `404` -> HTTP 404 with `type: "invalid_request_error"`, `code: "model_not_found"`
  - `>=500` -> HTTP 5xx with `type: "server_error"`, `code: "internal_server_error"`
- **Important**: Both non-streaming (`executor.execute`) and streaming (`executor.execute_stream`) call sites must include `http_status` so failures are classified consistently.
- Because native dynamic library plugins communicate across the C ABI via serialized JSON buffers, the status code must be encoded in the serialized JSON envelope (e.g. using `pluginabi.NewErrorEnvelope` or a custom envelope struct with an `http_status` field). Returning an unmarshaled Go error does not traverse the C ABI boundary.

Go plugins can import `github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi` and use `pluginabi.NewErrorEnvelope(code, message, httpStatus)`:

```go
// Recommended: construct an error envelope directly using sdk/pluginabi
rawEnvelope, errMarshal := pluginabi.NewErrorEnvelope("insufficient_quota", "plan limit reached", http.StatusForbidden)
```

Alternatively, plugins defining a custom envelope struct can declare an `HTTPStatus` field (`json:"http_status,omitempty"`):

```go
type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

func errorEnvelope(code, message string, httpStatus ...int) []byte {
	status := 0
	if len(httpStatus) > 0 {
		status = httpStatus[0]
	}
	raw, _ := json.Marshal(envelope{
		OK: false,
		Error: &envelopeError{
			Code:       code,
			Message:    message,
			HTTPStatus: status,
		},
	})
	return raw
}
```

## Build All Examples

```bash
make -C examples/plugin list
make -C examples/plugin build
```

Artifacts are written to `examples/plugin/bin`.

## Notes

`protocol-format` uses a minimal executor because format declarations belong to executor capabilities.

`host-callback` uses a minimal plugin resource because host callbacks are invoked from plugin methods and are not standalone capabilities.

Menu resources returned by `management.register` through the `resources` field are exposed by CPA under `/v0/resource/plugins/<pluginID>/...`. Authenticated plugin Management API routes remain under `/v0/management/...`.
