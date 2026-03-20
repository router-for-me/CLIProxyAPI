#!/bin/sh
set -eu

APP_DIR="/CLIProxyAPI"
CONFIG_PATH="${CONFIG_PATH:-$APP_DIR/config.yaml}"
EXAMPLE_PATH="$APP_DIR/config.example.yaml"

if [ -n "${CONFIG_YAML_B64:-}" ]; then
  if ! command -v base64 >/dev/null 2>&1; then
    echo "CONFIG_YAML_B64 is set, but base64 is not available" >&2
    exit 1
  fi
  printf '%s' "$CONFIG_YAML_B64" | base64 -d > "$CONFIG_PATH"
elif [ -n "${CONFIG_YAML:-}" ]; then
  printf '%s\n' "$CONFIG_YAML" > "$CONFIG_PATH"
elif [ ! -f "$CONFIG_PATH" ] && [ -f "$EXAMPLE_PATH" ]; then
  echo "No config.yaml found; bootstrapping from config.example.yaml" >&2
  cp "$EXAMPLE_PATH" "$CONFIG_PATH"
fi

if [ -n "${PORT:-}" ] && [ -f "$CONFIG_PATH" ]; then
  if grep -Eq '^[[:space:]]*port:[[:space:]]*[0-9]+' "$CONFIG_PATH"; then
    sed -i "s/^[[:space:]]*port:[[:space:]]*[0-9][0-9]*/port: ${PORT}/" "$CONFIG_PATH"
  else
    printf '\nport: %s\n' "$PORT" >> "$CONFIG_PATH"
  fi
fi

exec "$@"
