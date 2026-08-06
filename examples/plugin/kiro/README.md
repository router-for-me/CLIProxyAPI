# Kiro Provider Plugin

This Go plugin adds an API-key-only Kiro provider to CLIProxyAPI. It uses the
Kiro CLI runtime protocol and exposes Claude as its native protocol, so Claude
Code can use it through `/v1/messages`.

Codex CLI uses the OpenAI Responses API. CLIProxyAPI translates those requests
to the plugin's Claude-native protocol. Complete OpenAI Chat Completions and
Responses compatibility also depends on the translator work tracked in
[issue #4815](https://github.com/router-for-me/CLIProxyAPI/issues/4815). If a
build does not contain that work, Claude Code can still work while Codex CLI
may fail or return incomplete formatting or usage information.

## Before you start

You need:

- a Kiro API key and the region for that account;
- a CLIProxyAPI client key, such as `local-dev-key`;
- Go 1.26 or later to build the plugin; and
- Codex CLI or Claude Code for the client workflow you want to test.

The two keys have different purposes:

- `KIRO_API_KEY` authenticates CLIProxyAPI to Kiro. Only the server needs it.
- `local-dev-key` authenticates Codex CLI or Claude Code to CLIProxyAPI. It is
  configured under `api-keys` in `config.yaml`.

## 1. Build and install the plugin

For a CLIProxyAPI process running directly on the current machine:

```bash
make -C examples/plugin build-kiro

plugin_os="$(go env GOOS)"
plugin_arch="$(go env GOARCH)"
case "$plugin_os" in
  darwin) plugin_ext="dylib" ;;
  windows) plugin_ext="dll" ;;
  *) plugin_ext="so" ;;
esac

mkdir -p "plugins/$plugin_os/$plugin_arch"
cp "examples/plugin/bin/kiro-go.$plugin_ext" "plugins/$plugin_os/$plugin_arch/"
```

The plugin must be built for the operating system and architecture where
CLIProxyAPI runs, not necessarily where the source directory is located.

For a Podman deployment on an Apple Silicon Mac, CLIProxyAPI runs inside a
Linux ARM64 virtual machine. Build the Linux ARM64 plugin with Podman:

```bash
mkdir -p plugins/linux/arm64

podman run --rm \
  -v "$PWD:/src" \
  -w /src/examples/plugin/kiro/go \
  docker.io/library/golang:1.26 \
  sh -c 'CGO_ENABLED=1 go build -buildmode=c-shared -o /src/plugins/linux/arm64/kiro-go.so . && rm -f /src/plugins/linux/arm64/kiro-go.h'
```

The image name is an OCI registry reference. This command uses Podman and does
not require a Docker daemon.

## 2. Configure CLIProxyAPI

Enable the plugin and add a client-facing proxy key in `config.yaml`:

```yaml
api-keys:
  - "local-dev-key"

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

`"*"` exposes every model discovered for each Kiro account. To limit the
models, replace it with exact IDs returned for your account:

```yaml
      models:
        - "claude-sonnet-4.5"
        - "claude-haiku-4.5"
```

Model discovery results are cached for five minutes. If a refresh fails, the
plugin keeps the last successful result. On a cold-start failure it falls back
to the configured models; when `"*"` is configured, the fallback is Claude
Sonnet 4.5 and Claude Haiku 4.5.

## 3. Add the Kiro credential

Create `.env` in the directory from which CLIProxyAPI starts:

```dotenv
KIRO_API_KEY=replace-with-your-kiro-key
```

CLIProxyAPI automatically loads that file at startup. `.env` is ignored by
this repository and should not be committed.

If CLIProxyAPI runs in Podman, the host's shell variables and `.env` file are
not automatically available inside the container. Pass the variable with
`--env-file .env`, `--env KIRO_API_KEY`, or a Podman secret in the container's
normal launch configuration.

Create the auth record in CLIProxyAPI's configured `auth-dir`. For a native
process, the default path is `~/.cli-proxy-api/kiro-pro.json`. For the
repository's Podman bind mount, create `auths/kiro-pro.json` on the host so it
appears under `/root/.cli-proxy-api` in the container:

```json
{
  "type": "kiro",
  "label": "kiro-pro",
  "region": "us-east-1",
  "api_key_env": "KIRO_API_KEY"
}
```

Protect the auth record and restart CLIProxyAPI. Use the path for your launch
method:

```bash
chmod 600 ~/.cli-proxy-api/kiro-pro.json
# Podman bind-mount path:
chmod 600 auths/kiro-pro.json
```

`api_key_env` stores only the environment-variable name. The plugin resolves
the value when it sends a request and never writes the resolved key back to the
auth JSON. An inline `api_key` field is also accepted, but an environment
variable or Podman secret is preferred.

## 4. Verify the server first

Set the client-facing proxy key in your current shell:

```bash
export CLIPROXY_API_KEY='local-dev-key'
```

Check the server and list the models available through it:

```bash
curl -fsS http://127.0.0.1:8317/healthz

curl -fsS http://127.0.0.1:8317/v1/models \
  -H "Authorization: Bearer $CLIPROXY_API_KEY"
```

Use an exact Kiro model ID from this response in the client examples below.
If `jq` is installed, this prints only Kiro-owned model IDs:

```bash
curl -fsS http://127.0.0.1:8317/v1/models \
  -H "Authorization: Bearer $CLIPROXY_API_KEY" \
  | jq -r '.data[] | select(.owned_by == "kiro") | .id'
```

## Use with Claude Code

Claude Code speaks the plugin's native Anthropic Messages protocol, so this is
the shortest path.

### Configure the session

Run these commands in the shell that will start Claude Code:

```bash
export ANTHROPIC_BASE_URL='http://127.0.0.1:8317'
export ANTHROPIC_AUTH_TOKEN="$CLIPROXY_API_KEY"
export ANTHROPIC_MODEL='claude-sonnet-4.5'
export ANTHROPIC_DEFAULT_HAIKU_MODEL='claude-haiku-4.5'
export CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1
```

Important details:

- Do not add `/v1` to `ANTHROPIC_BASE_URL`; Claude Code adds
  `/v1/messages` itself.
- `ANTHROPIC_AUTH_TOKEN` is the CLIProxyAPI client key, not the Kiro key.
- Set `ANTHROPIC_MODEL` and `ANTHROPIC_DEFAULT_HAIKU_MODEL` only to models
  returned by `/v1/models` for the account.
- Gateway model discovery requires Claude Code 2.1.129 or later. It adds the
  proxy's models to the `/model` picker.

For persistent configuration, place the same variables in the `env` object of
`~/.claude/settings.json` or a gitignored `.claude/settings.local.json`. Do not
put credentials in a committed `.claude/settings.json`.

See Anthropic's
[gateway connection guide](https://code.claude.com/docs/en/llm-gateway-connect)
for Claude Code's configuration precedence.

### Run a test

Start an interactive session:

```bash
claude --model "$ANTHROPIC_MODEL"
```

Or run one non-interactive request:

```bash
claude -p --model "$ANTHROPIC_MODEL" \
  "Find the main entry point and name its file."
```

Inside Claude Code, run `/status`. `Anthropic base URL` should show
`http://127.0.0.1:8317`, and the credential source should be
`ANTHROPIC_AUTH_TOKEN`. CLIProxyAPI logs should show `/v1/messages`, a Kiro auth
record, and an upstream request to `https://runtime.<region>.kiro.dev/`.

## Use with Codex CLI

Codex CLI sends Responses API requests, so use a custom model provider with
`wire_api = "responses"`.

### Configure a Codex profile

Create `~/.codex/kiro.config.toml`:

```toml
model = "claude-sonnet-4.5"
model_provider = "cliproxy_kiro"

[model_providers.cliproxy_kiro]
name = "CLIProxyAPI Kiro"
base_url = "http://127.0.0.1:8317/v1"
wire_api = "responses"
env_key = "CLIPROXY_API_KEY"
```

Keep `CLIPROXY_API_KEY` exported in the shell that starts Codex:

```bash
export CLIPROXY_API_KEY='local-dev-key'
```

Use a user profile rather than a project `.codex/config.toml`. Current Codex
versions ignore provider and authentication settings in project configuration
files. Also do not set `requires_openai_auth = true`: that would make Codex use
OpenAI authentication and ignore `env_key`.

See OpenAI's
[custom model provider documentation](https://developers.openai.com/codex/config-advanced/#custom-model-providers)
for the full provider configuration reference.

### Run a test

Start an interactive session:

```bash
codex --profile kiro
```

Or run one read-only, non-interactive request:

```bash
codex exec --profile kiro \
  "Find the main entry point and name its file."
```

Override the profile's model for one session with an exact model returned by
`/v1/models`:

```bash
codex --profile kiro --model claude-haiku-4.5
```

CLIProxyAPI logs should show `/v1/responses`, provider `kiro`, and an upstream
request to `https://runtime.<region>.kiro.dev/`.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| `connection refused` or `/healthz` fails | Confirm CLIProxyAPI is running on port 8317. With Podman, publish the port and check `podman ps`. |
| The plugin does not load | Confirm `plugins.enabled` and `kiro-go.enabled` are true, and that the plugin binary matches the server's OS and architecture. Check startup logs for `kiro-go`. |
| `401` from CLIProxyAPI | The client must send a value listed under `api-keys`. Check `CLIPROXY_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, or the Codex provider's `env_key`. |
| Kiro returns `401` or `403` | Check that `KIRO_API_KEY` is visible inside the CLIProxyAPI process or Podman container and that the auth record uses the correct region. |
| `auth_unavailable: no auth available` | Confirm the model appears in `/v1/models`, the auth JSON has `"type":"kiro"`, and the plugin model allow-list includes the exact model ID. Restart after changing credentials. |
| Claude Code opens its normal login or uses a subscription | Start it from the shell containing the gateway variables, then check `/status`. `ANTHROPIC_AUTH_TOKEN` takes precedence over a saved login. |
| Models do not appear in Claude Code's `/model` picker | Check `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`, update Claude Code if needed, and ensure `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC` is not set because it disables gateway discovery. |
| Codex uses OpenAI auth or the wrong endpoint | Put the provider in `~/.codex/kiro.config.toml`, select it with `--profile kiro`, keep `requires_openai_auth` unset, and verify the base URL ends in `/v1`. |
| Claude Code works but Codex formatting or usage is incomplete | The build is missing the OpenAI-to-Claude translator compatibility work tracked in issue #4815. |

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
- Assistant-prefill requests are rejected because Kiro's current message must
  be a user message.
- Kiro client compatibility versions are configurable because upstream header
  expectations may change.

See `THIRD_PARTY_NOTICES.md` for implementation provenance.
