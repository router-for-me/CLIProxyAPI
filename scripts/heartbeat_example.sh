#!/usr/bin/env bash
set -euo pipefail

HEALTH_URL="${CLI_PROXY_HEALTH_URL:-http://127.0.0.1:8317/health}"
SELF_HEAL_SCRIPT="${CLI_PROXY_SELF_HEAL_SCRIPT:-./scripts/self_heal_example.sh}"
NOTIFY="${CLI_PROXY_NOTIFY:-1}"

notify() {
  [ "$NOTIFY" = "1" ] || return 0
  local title="$1"
  local body="$2"
  echo "NOTIFY: $title - $body"
}

if curl -fsS "$HEALTH_URL" >/dev/null 2>&1; then
  echo "HEARTBEAT_OK"
  exit 0
fi

before_msg="health check failed: $HEALTH_URL"
echo "$before_msg"

if bash "$SELF_HEAL_SCRIPT"; then
  if curl -fsS "$HEALTH_URL" >/dev/null 2>&1; then
    notify "CLIProxyAPI recovered" "self-heal succeeded after heartbeat failure"
    echo "RECOVERED: self-heal succeeded"
    exit 0
  fi
fi

notify "CLIProxyAPI self-heal failed" "service still unhealthy after recovery attempt"
echo "FAILED: self-heal did not restore health"
exit 1
