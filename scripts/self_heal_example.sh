#!/usr/bin/env bash
set -euo pipefail

COMPOSE_CMD="${CLI_PROXY_COMPOSE_CMD:-docker compose}"
SERVICE_NAME="${CLI_PROXY_SERVICE_NAME:-cli-proxy-api}"

$COMPOSE_CMD ps "$SERVICE_NAME" >/dev/null 2>&1 || true
$COMPOSE_CMD up -d "$SERVICE_NAME"

echo "SELF_HEAL_ATTEMPTED: $SERVICE_NAME"
