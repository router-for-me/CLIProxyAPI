#!/bin/sh
# Cloud deployment startup script for CLIProxyAPI
# Works with Railway, Zeabur, and other container platforms

set -e

# Use PORT environment variable or default to 8080
LISTEN_PORT="${PORT:-8080}"

echo "=== Cloud Deployment Environment ==="
echo "PORT env: ${PORT}"
echo "Using port: ${LISTEN_PORT}"
echo "API_KEY set: $([ -n "${API_KEY}" ] && echo 'yes' || echo 'no')"
echo "MANAGEMENT_PASSWORD set: $([ -n "${MANAGEMENT_PASSWORD}" ] && echo 'yes' || echo 'no')"
echo "===================================="

# Create config.yaml with the dynamic port
cat > /CLIProxyAPI/config.yaml << EOF
# Auto-generated config for cloud deployment
host: ""
port: ${LISTEN_PORT}

# API keys (set via environment variables)
api-keys:
  - "${API_KEY:-default-change-me}"

# Debug mode
debug: ${DEBUG:-false}

# Auth directory
auth-dir: "/CLIProxyAPI/.cli-proxy-api"

# Management API settings
remote-management:
  allow-remote: true
  secret-key: "${MANAGEMENT_PASSWORD:-}"
  disable-control-panel: false

# Request retry
request-retry: 3

# Quota exceeded behavior
quota-exceeded:
  switch-project: true
  switch-preview-model: true

# Routing strategy
routing:
  strategy: "round-robin"
EOF

echo "Generated config.yaml with port ${LISTEN_PORT}"
echo "Starting CLIProxyAPI..."
exec ./CLIProxyAPI
