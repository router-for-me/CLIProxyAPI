# Kiro Provider Plugin

This Go plugin adds an API-key-only Kiro provider to CLIProxyAPI. It uses the
Kiro CLI runtime protocol and exposes Claude as its native protocol. Claude
`/v1/messages` requests are supported directly. Complete non-streaming OpenAI
Chat Completions and Responses API compatibility additionally requires the
restricted translator work tracked in
[issue #4815](https://github.com/router-for-me/CLIProxyAPI/issues/4815).

The implementation deliberately does not include Kiro OAuth, device login,
refresh tokens, account selection, or its own HTTP client. CLIProxyAPI owns
credential selection, session affinity, cooldowns, protocol translation, proxy
configuration, and outbound request logging.

## Build

Build for the current platform:

```bash
make -C examples/plugin build-kiro
```

On Linux, the extension is `.so`. The plugin must be built for the operating
system and architecture where CLIProxyAPI runs. For a Podman deployment on an
Apple Silicon Mac, build the Linux ARM64 plugin inside the Podman VM:

```bash
podman run --rm \
  -v "$PWD:/src" \
  -w /src/examples/plugin/kiro/go \
  docker.io/library/golang:1.26 \
  sh -c 'CGO_ENABLED=1 go build -buildmode=c-shared -o /src/plugins/linux/arm64/kiro-go.so . && rm -f /src/plugins/linux/arm64/kiro-go.h'
```

The image name uses an OCI registry reference; the command is executed by
Podman and does not require a Docker daemon.

## Configure the plugin

Dynamic plugins are disabled by default. Add this to `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    kiro-go:
      enabled: true
      priority: 1
      models:
        - "*"
```

The plugin discovers models separately for every Kiro account. Use `"*"` to
expose every model returned for that account, or list specific model IDs to use
the configured list as an allow-list:

```yaml
      models:
        - "claude-sonnet-4.5"
        - "claude-haiku-4.5"
```

Successful results are cached for five minutes. If a refresh fails, the plugin
keeps the last successful result. On a cold-start failure it falls back to the
configured models; when `"*"` is configured, the cold-start fallback is Claude
Sonnet 4.5 and Claude Haiku 4.5.

## Add an auth record

Create `auths/kiro-pro.json`:

```json
{
  "type": "kiro",
  "label": "kiro-pro",
  "region": "us-east-1",
  "api_key_env": "KIRO_API_KEY"
}
```

Set restrictive permissions:

```bash
chmod 600 auths/kiro-pro.json
```

Pass `KIRO_API_KEY` to the CLIProxyAPI process or Podman container. The plugin
resolves the environment variable only when it sends a request. The resolved
key is not written back to the auth JSON.

For a temporary local test:

```bash
export KIRO_API_KEY='replace-me'
```

An `api_key` field is also accepted for installations that already protect the
auth directory, but `api_key_env` or a Podman secret is preferred.

## Request flow

1. The auth parser recognizes an auth JSON whose `type` is `kiro`.
2. During auth registration, the plugin calls Kiro's model-list service and
   returns the discovered, allow-listed models for that account.
3. CLIProxyAPI selects a compatible Kiro auth using its normal routing and
   affinity logic.
4. The host translates the incoming request to Claude format when necessary.
5. The plugin converts Claude messages, tools, tool results, images, system
   instructions, and inference settings into Kiro conversation-state JSON.
6. The plugin asks the host HTTP bridge to POST to
   `https://runtime.<region>.kiro.dev/` with API-key headers.
7. The plugin validates and decodes the AWS EventStream response.
8. It returns Claude JSON or emits Claude SSE. CLIProxyAPI translates that back
   to the original client protocol when necessary.

## Initial test cases

Run these after providing a real key:

1. List models and confirm the account's discovered models appear. If an
   explicit allow-list is configured, confirm only its intersection appears.
2. Send a non-streaming `/v1/messages` text request.
3. Send the same request with `stream: true` and verify Anthropic SSE ordering.
4. After issue #4815 is implemented, call `/v1/chat/completions` to verify
   host-side OpenAI translation.
5. After issue #4815 is implemented, call `/v1/responses` to verify Responses
   API translation.
6. Exercise one client tool call and return its `tool_result`.
7. Send a small base64 image if the selected account model supports images.
8. Use an invalid key and verify a 401/403 does not leak the key.
9. Use a model outside the account entitlement and verify the upstream error is
   surfaced without disabling unrelated credentials.
10. Cancel a streaming request and verify the upstream stream closes.

## Limitations

- The Kiro subscription API key is documented, but the raw runtime protocol is
  not a public model API and may change.
- Complete non-streaming OpenAI-compatible endpoint support depends on the
  maintainer-owned translator changes tracked in issue #4815.
- Model discovery uses Kiro's internal `ListAvailableModels` service rather
  than a documented public model API and may change.
- Discovery is refreshed when the host registers an auth. The five-minute
  cache avoids repeated calls but does not run its own background refresh.
- The host model registry has no credit-multiplier field, so discovery maps
  names, descriptions, token limits, and modalities but does not expose Kiro's
  per-model credit multiplier.
- `/v1/messages/count_tokens` returns an explicitly marked local estimate.
- Web search server tools are not forwarded in the initial implementation.
- Kiro client compatibility versions are configurable because upstream header
  expectations may change.

See `THIRD_PARTY_NOTICES.md` for implementation provenance.
