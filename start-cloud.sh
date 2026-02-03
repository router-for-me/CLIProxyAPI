#!/bin/sh
# Cloud deployment startup script for CLIProxyAPI
# Works with Railway, Zeabur, and other container platforms

set -e

# Use PORT environment variable or default to 8080
LISTEN_PORT="${PORT:-8080}"

echo "=== Cloud Deployment Environment ==="
echo "PORT: ${LISTEN_PORT}"
echo "API_KEY set: $([ -n "${API_KEY}" ] && echo 'yes' || echo 'no')"
echo "MANAGEMENT_PASSWORD set: $([ -n "${MANAGEMENT_PASSWORD}" ] && echo 'yes' || echo 'no')"
echo "DEBUG: ${DEBUG:-false}"
echo "PROXY_URL set: $([ -n "${PROXY_URL}" ] && echo 'yes' || echo 'no')"
echo "===================================="

# Validate required environment variables
if [ -z "${API_KEY}" ]; then
  echo "ERROR: API_KEY environment variable is required"
  echo "Please set it in Railway dashboard: Variables -> Add Variable"
  exit 1
fi

if [ -z "${MANAGEMENT_PASSWORD}" ]; then
  echo "WARNING: MANAGEMENT_PASSWORD not set, management panel will be disabled"
  echo "To enable management panel, set MANAGEMENT_PASSWORD in Railway dashboard"
fi

# Create config.yaml with environment variables
cat > /CLIProxyAPI/config.yaml << EOF
# Auto-generated config for cloud deployment
# Generated at: $(date)

host: ""
port: ${LISTEN_PORT}

# API keys for client authentication
api-keys:
  - "${API_KEY}"

# Debug mode
debug: ${DEBUG:-false}

# Auth directory for OAuth credentials
auth-dir: "/CLIProxyAPI/.cli-proxy-api"

# Management API settings
remote-management:
  allow-remote: ${ALLOW_REMOTE_MANAGEMENT:-true}
  secret-key: "${MANAGEMENT_PASSWORD:-}"
  disable-control-panel: false
  panel-github-repository: "https://github.com/router-for-me/Cli-Proxy-API-Management-Center"

# Proxy settings (optional)
proxy-url: "${PROXY_URL:-}"

# Request retry settings
request-retry: ${REQUEST_RETRY:-3}
max-retry-interval: ${MAX_RETRY_INTERVAL:-30}

# Quota exceeded behavior
quota-exceeded:
  switch-project: ${SWITCH_PROJECT:-true}
  switch-preview-model: ${SWITCH_PREVIEW_MODEL:-true}

# Routing strategy
routing:
  strategy: "${ROUTING_STRATEGY:-round-robin}"

# WebSocket authentication
ws-auth: ${WS_AUTH:-false}

# Usage statistics
usage-statistics-enabled: ${USAGE_STATS:-true}

# Logging
logging-to-file: ${LOGGING_TO_FILE:-false}
logs-max-total-size-mb: ${LOGS_MAX_SIZE_MB:-0}

# Commercial mode (reduce memory overhead)
commercial-mode: ${COMMERCIAL_MODE:-false}

# Model prefix enforcement
force-model-prefix: ${FORCE_MODEL_PREFIX:-false}
EOF

echo ""
echo "Generated config.yaml with:"
echo "  - Port: ${LISTEN_PORT}"
echo "  - API Key: ${API_KEY:0:8}***"
echo "  - Management: $([ -n "${MANAGEMENT_PASSWORD}" ] && echo 'enabled' || echo 'disabled')"
echo ""
echo "Starting CLIProxyAPI..."
exec ./CLIProxyAPI
