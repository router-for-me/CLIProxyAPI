# vision-fallback

`vision-fallback` is a Go dynamic-library request interceptor for image-bearing requests. After authentication selects an upstream model, it checks the model registry's declared input modalities. If the model is explicitly text-only (or matches `force-text-only-models`), image containers are described once by the configured multimodal `vision-model`, then replaced with an untrusted text report before the original model runs.

The plugin supports OpenAI Chat and Responses/Codex, Claude Messages, and Gemini `generateContent` payloads. It accepts data URLs, base64 data, and HTTP(S) URLs without downloading remote URLs. Provider-scoped file IDs, non-HTTP(S) URIs, malformed references, request-size limits, and image-count limits are rejected without logging image content.

```yaml
plugins:
  enabled: true
  configs:
    vision-fallback:
      enabled: true
      priority: 100
      vision-model: "gemini-2.5-flash"
      force-text-only-models: []
      force-multimodal-models: []
      unknown-model-policy: "bypass"
      max-images: 8
      max-request-bytes: 33554432
      vision-max-tokens: 1200
```

The nested callback always uses one non-streaming OpenAI Chat request containing only the current image container's text and images. It forwards `host_callback_id`, so the originating plugin is skipped for the nested call and cannot recurse. Results are cached per request ID and image/configuration digest; `request.complete` clears that cache.

Build from the repository root with `make -C examples/plugin build`, or build this module directly with `go build -buildmode=c-shared -o vision-fallback.dll .` (use the platform's shared-library suffix).
