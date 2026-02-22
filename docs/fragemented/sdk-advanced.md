# SDK Advanced: Executors & Translators

This guide explains how to extend the embedded proxy with custom providers and schemas using the SDK. You will:
- Implement a provider executor that talks to your upstream API
- Register request/response translators for schema conversion
- Register models so they appear in `/v1/models`

The examples use Go 1.24+ and the v6 module path.

## Concepts

- Provider executor: a runtime component implementing `auth.ProviderExecutor` that performs outbound calls for a given provider key (e.g., `gemini`, `claude`, `codex`). Executors can also implement `RequestPreparer` to inject credentials on raw HTTP requests.
- Translator registry: schema conversion functions routed by `sdk/translator`. The built‑in handlers translate between OpenAI/Gemini/Claude/Codex formats; you can register new ones.
- Model registry: publishes the list of available models per client/provider to power `/v1/models` and routing hints.

## 1) Implement a Provider Executor

Create a type that satisfies `auth.ProviderExecutor`.

```go
package myprov

import (
  "context"
  "net/http"

  coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
  clipexec "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type Executor struct{}

func (Executor) Identifier() string { return "myprov" }

// Optional: mutate outbound HTTP requests with credentials
func (Executor) PrepareRequest(req *http.Request, a *coreauth.Auth) error {
  // Example: req.Header.Set("Authorization", "Bearer "+a.APIKey)
  return nil
}

func (Executor) Execute(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
  // Build HTTP request based on req.Payload (already translated into provider format)
  // Use per‑auth transport if provided: transport := a.RoundTripper // via RoundTripperProvider
  // Perform call and return provider JSON payload
  return clipexec.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (Executor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (<-chan clipexec.StreamChunk, error) {
  ch := make(chan clipexec.StreamChunk, 1)
  go func() { defer close(ch); ch <- clipexec.StreamChunk{Payload: []byte("data: {\"done\":true}\n\n")} }()
  return ch, nil
}

func (Executor) Refresh(ctx context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
  // Optionally refresh tokens and return updated auth
  return a, nil
}
```

Register the executor with the core manager before starting the service:

```go
core := coreauth.NewManager(coreauth.NewFileStore(cfg.AuthDir), nil, nil)
core.RegisterExecutor(myprov.Executor{})
svc, _ := cliproxy.NewBuilder().WithConfig(cfg).WithConfigPath(cfgPath).WithCoreAuthManager(core).Build()
```

If your auth entries use provider `"myprov"`, the manager routes requests to your executor.

## 2) Register Translators

The handlers accept OpenAI/Gemini/Claude/Codex inputs. To support a new provider format, register translation functions in `sdk/translator`’s default registry.

Direction matters:
- Request: register from inbound schema to provider schema
- Response: register from provider schema back to inbound schema

Example: Convert OpenAI Chat → MyProv Chat and back.

```go
package myprov

import (
  "context"
  sdktr "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

const (
  FOpenAI = sdktr.Format("openai.chat")
  FMyProv = sdktr.Format("myprov.chat")
)

func init() {
  sdktr.Register(FOpenAI, FMyProv,
    // Request transform (model, rawJSON, stream)
    func(model string, raw []byte, stream bool) []byte { return convertOpenAIToMyProv(model, raw, stream) },
    // Response transform (stream & non‑stream)
    sdktr.ResponseTransform{
      Stream: func(ctx context.Context, model string, originalReq, translatedReq, raw []byte, param *any) []string {
        return convertStreamMyProvToOpenAI(model, originalReq, translatedReq, raw)
      },
      NonStream: func(ctx context.Context, model string, originalReq, translatedReq, raw []byte, param *any) string {
        return convertMyProvToOpenAI(model, originalReq, translatedReq, raw)
      },
    },
  )
}
```

When the OpenAI handler receives a request that should route to `myprov`, the pipeline uses the registered transforms automatically.

## 3) Register Models

Expose models under `/v1/models` by registering them in the global model registry using the auth ID (client ID) and provider name.

```go
models := []*cliproxy.ModelInfo{
  { ID: "myprov-pro-1", Object: "model", Type: "myprov", DisplayName: "MyProv Pro 1" },
}
cliproxy.GlobalModelRegistry().RegisterClient(authID, "myprov", models)
```

The embedded server calls this automatically for built‑in providers; for custom providers, register during startup (e.g., after loading auths) or upon auth registration hooks.

## Credentials & Transports

- Use `Manager.SetRoundTripperProvider` to inject per‑auth `*http.Transport` (e.g., proxy):
  ```go
  core.SetRoundTripperProvider(myProvider) // returns transport per auth
  ```
- For raw HTTP flows, implement `PrepareRequest` and/or call `Manager.InjectCredentials(req, authID)` to set headers.

## Testing Tips

- Enable request logging: Management API GET/PUT `/v0/management/request-log`
- Toggle debug logs: Management API GET/PUT `/v0/management/debug`
- Hot reload changes in `config.yaml` and `auths/` are picked up automatically by the watcher

