#!/bin/sh
# Railway deployment startup script
# This script creates a config.yaml that uses Railway's PORT environment variable

# Default port if PORT not set
PORT="${PORT:-8317}"

# Create config.yaml with the dynamic port
cat > /CLIProxyAPI/config.yaml << EOF
# Auto-generated config for Railway deployment
host: ""
port: ${PORT}

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

echo "Starting CLIProxyAPI on port ${PORT}..."
exec ./CLIProxyAPI
