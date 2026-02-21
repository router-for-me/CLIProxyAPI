# Getting Started

This guide gets a local `cliproxyapi++` instance running and verifies end-to-end request flow.

## Audience

- Use this if you need a quick local or dev-server setup.
- If you need deployment hardening, continue to [Install](/install) and [Troubleshooting](/troubleshooting).

## Prerequisites

- Docker + Docker Compose, or Go 1.26+ for local builds.
- `curl` for API checks.
- `jq` (optional, for readable JSON output).

## 1. Prepare Working Directory

```bash
mkdir -p ~/cliproxy && cd ~/cliproxy
curl -fsSL -o config.yaml \
  https://raw.githubusercontent.com/KooshaPari/cliproxyapi-plusplus/main/config.example.yaml
mkdir -p auths logs
```

## 2. Configure the Minimum Required Settings

In `config.yaml`, set at least:

```yaml
port: 8317
auth-dir: "./auths"
api-keys:
  - "dev-local-key"
routing:
  strategy: "round-robin"
```

Notes:

- `api-keys` protects `/v1/*` endpoints (client-facing auth).
- `auth-dir` is where provider credentials are loaded from.

## 3. Add One Provider Credential

Example (`claude-api-key`) in `config.yaml`:

```yaml
claude-api-key:
  - api-key: "sk-ant-your-key"
```

You can also configure other provider blocks from `config.example.yaml`.

## 4. Start With Docker

```bash
cat > docker-compose.yml << 'EOF_COMPOSE'
services:
  cliproxy:
    image: KooshaPari/cliproxyapi-plusplus:latest
    container_name: cliproxyapi-plusplus
    ports:
      - "8317:8317"
    volumes:
      - ./config.yaml:/CLIProxyAPI/config.yaml
      - ./auths:/root/.cli-proxy-api
      - ./logs:/CLIProxyAPI/logs
    restart: unless-stopped
EOF_COMPOSE

docker compose up -d
```

## 5. Verify the Service

```bash
# Health
curl -sS http://localhost:8317/health

# Public model list (requires API key)
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer dev-local-key" | jq '.data[:5]'
```

## 6. Send a Chat Request

```bash
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer dev-local-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-3-5-sonnet",
    "messages": [
      {"role": "user", "content": "Say hello from cliproxyapi++"}
    ],
    "stream": false
  }'
```

Example response shape:

```json
{
  "id": "chatcmpl-...",
  "object": "chat.completion",
  "model": "claude-3-5-sonnet",
  "choices": [
    {
      "index": 0,
      "message": { "role": "assistant", "content": "Hello..." },
      "finish_reason": "stop"
    }
  ]
}
```

## Common First-Run Failures

- `401 Unauthorized`: missing/invalid `Authorization` header for `/v1/*`.
- `404` on management routes: `remote-management.secret-key` is empty (management disabled).
- `429` upstream: credential is throttled; rotate credentials or add provider capacity.
- Model not listed in `/v1/models`: provider/auth not configured or filtered by prefix rules.

## Next Steps

- [Install](/install)
- [Provider Usage](/provider-usage)
- [Routing and Models Reference](/routing-reference)
- [API Index](/api/)
