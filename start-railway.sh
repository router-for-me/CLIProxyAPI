#!/bin/sh
# Railway deployment startup script
# This script creates a config.yaml that uses Railway's PORT environment variable

set -e

# Railway injects PORT, use it or default to 8080
LISTEN_PORT="${PORT:-8080}"

echo "=== Railway Environment Debug ==="
echo "PORT env: ${PORT}"
echo "Using port: ${LISTEN_PORT}"
echo "API_KEY set: $([ -n \"${API_KEY}\" ] && echo 'yes' || echo 'no')"
echo "MANAGEMENT_PASSWORD set: $([ -n \"${MANAGEMENT_PASSWORD}\" ] && echo 'yes' || echo 'no')"
echo "================================="

# Create config.yaml with the dynamic port
# NOTE: The app reads MANAGEMENT_PASSWORD directly from environment (preferred)
# The secret-key in config is only a fallback
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
# NOTE: App prefers MANAGEMENT_PASSWORD env var over this secret-key
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
