# ARM64 Docker Provider Quickstart

Scope: CP2K-0034 (`#147` follow-up).

This quickstart is for ARM64 hosts running `cliproxyapi++` with an OpenAI-compatible provider sanity flow.

## 1. Setup

```bash
docker pull KooshaPari/cliproxyapi-plusplus:latest
mkdir -p auths logs
cp config.example.yaml config.yaml
```

Run ARM64 explicitly:

```bash
docker run --platform linux/arm64 -d --name cliproxyapi-plusplus \
  -p 8317:8317 \
  -v "$PWD/config.yaml:/CLIProxyAPI/config.yaml" \
  -v "$PWD/auths:/root/.cli-proxy-api" \
  -v "$PWD/logs:/CLIProxyAPI/logs" \
  KooshaPari/cliproxyapi-plusplus:latest
```

Check architecture:

```bash
docker exec cliproxyapi-plusplus uname -m
```

Expected: `aarch64`.

## 2. Auth and Config

Set at least one client API key and one provider/auth block in `config.yaml`, then verify server health:

```bash
curl -sS http://localhost:8317/health | jq
```

## 3. Model Visibility Check

```bash
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer <client-api-key>" | jq '.data[:10]'
```

Confirm the target model/prefix is visible before generation tests.

## 4. Sanity Checks (Non-Stream then Stream)

Non-stream:

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <client-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"reply with ok"}],"stream":false}' | jq
```

Stream:

```bash
curl -N -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <client-api-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"reply with ok"}],"stream":true}'
```

If non-stream passes and stream fails, check proxy buffering and SSE timeout settings first.
