#!/bin/sh
# Railway deployment startup script
# This script creates a config.yaml that uses Railway's PORT environment variable

set -e

# Railway injects PORT, use it or default to 8080
LISTEN_PORT="${PORT:-8080}"

echo "Railway PORT env: ${PORT}"
echo "Using port: ${LISTEN_PORT}"

# Create config.yaml with the dynamic port
cat > /CLIProxyAPI/config.yaml << EOF
# Auto-generated config for Railway deployment
host: ""
port: ${LISTEN_PORT}

# API keys (set via RAILWAY environment variables)
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
