# Install

`cliproxyapi++` can run as a container, standalone binary, or embedded SDK.

## Audience Guidance

- Choose Docker for most production and shared-team use.
- Choose binary for lightweight host installs.
- Choose SDK embedding when you need in-process integration in Go.

## Option A: Docker (Recommended)

```bash
docker pull KooshaPari/cliproxyapi-plusplus:latest
```

Minimal run command:

```bash
docker run -d --name cliproxyapi-plusplus \
  -p 8317:8317 \
  -v "$PWD/config.yaml:/CLIProxyAPI/config.yaml" \
  -v "$PWD/auths:/root/.cli-proxy-api" \
  -v "$PWD/logs:/CLIProxyAPI/logs" \
  KooshaPari/cliproxyapi-plusplus:latest
```

Validate:

```bash
curl -sS http://localhost:8317/health
```

ARM64 note (`#147` scope):

- Prefer Docker image manifests that include `linux/arm64`.
- If your host pulls the wrong image variant, force the platform explicitly:

```bash
docker run --platform linux/arm64 -d --name cliproxyapi-plusplus \
  -p 8317:8317 \
  -v "$PWD/config.yaml:/CLIProxyAPI/config.yaml" \
  -v "$PWD/auths:/root/.cli-proxy-api" \
  -v "$PWD/logs:/CLIProxyAPI/logs" \
  KooshaPari/cliproxyapi-plusplus:latest
```

- Verify architecture inside the running container:

```bash
docker exec cliproxyapi-plusplus uname -m
```

Expected output for ARM hosts: `aarch64`.

## Option B: Standalone Binary

Releases:

- https://github.com/KooshaPari/cliproxyapi-plusplus/releases

Example download and run (adjust artifact name for your OS/arch):

```bash
curl -fL \
  https://github.com/KooshaPari/cliproxyapi-plusplus/releases/latest/download/cliproxyapi++-darwin-amd64 \
  -o cliproxyapi++
chmod +x cliproxyapi++
./cliproxyapi++ --config ./config.yaml
```

## Option C: Build From Source

```bash
git clone https://github.com/KooshaPari/cliproxyapi-plusplus.git
cd cliproxyapi-plusplus
go build ./cmd/cliproxyapi
./cliproxyapi --config ./config.example.yaml
```

## Local Dev Refresh Workflow (process-compose)

Use this for deterministic local startup while keeping config/auth reload handled by the built-in watcher.

```bash
cp config.example.yaml config.yaml
process-compose -f examples/process-compose.dev.yaml up
```

Then edit `config.yaml` or files under `auth-dir`; the running process reloads changes automatically.

Deterministic local refresh sequence:

```bash
# 1) Confirm process is healthy.
curl -sS http://localhost:8317/health

# 2) Force watcher-visible change after config edits.
touch config.yaml

# 3) Verify model inventory reloaded.
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer YOUR_CLIENT_KEY" | jq '.data | length'

# 4) Run one canary request for the changed provider/model.
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer YOUR_CLIENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini/flash","messages":[{"role":"user","content":"reload check"}],"stream":false}' | jq '.error // .choices[0].finish_reason'
```

## Option D: Go SDK / Embedding

```bash
go get github.com/KooshaPari/cliproxyapi-plusplus/sdk/cliproxy
```

Related SDK docs:

- [SDK usage](./sdk-usage.md)
- [SDK advanced](./sdk-advanced.md)
- [SDK watcher](./sdk-watcher.md)

## Install-Time Checklist

- Confirm `config.yaml` is readable by the process/container user.
- Confirm `auth-dir` is writable if tokens refresh at runtime.
- Confirm port `8317` is reachable from intended clients only.
- Confirm at least one provider credential is configured.

## Common Install Failures

- Container starts then exits: invalid config path or parse error.
- `failed to read config file ... is a directory`: pass a file path (for example `/CLIProxyAPI/config.yaml`), not a directory.
- `bind: address already in use`: port conflict; change host port mapping.
- Requests always `401`: missing or incorrect `api-keys` for client auth.
- Management API unavailable: `remote-management.secret-key` unset.
