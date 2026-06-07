#!/usr/bin/env bash

set -euo pipefail

SERVICE_NAME="${CLI_PROXY_SERVICE:-cli-proxy-api}"
COMPOSE_FILE="${CLI_PROXY_COMPOSE_FILE:-docker-compose.yml}"
PORT="${CLI_PROXY_PORT:-8317}"
HEALTH_PATH="${CLI_PROXY_HEALTH_PATH:-/healthz}"
DEFAULT_IMAGE_REPO="${CLI_PROXY_IMAGE_REPO:-ghcr.io/quqi1599/cliproxyapi}"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/deploy-ghcr-release.sh fork/v7.10.48

Environment overrides:
  CLI_PROXY_IMAGE            Full image reference, for example ghcr.io/quqi1599/cliproxyapi:fork-v7.10.48
  CLI_PROXY_SERVICE          Compose service name (default: cli-proxy-api)
  CLI_PROXY_COMPOSE_FILE     Compose file path (default: docker-compose.yml)
  CLI_PROXY_PORT             Local health port (default: 8317)
  CLI_PROXY_HEALTH_PATH      Local health path (default: /healthz)
  CLI_PROXY_IMAGE_REPO       Image repository (default: ghcr.io/quqi1599/cliproxyapi)
EOF
}

require_command() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "Missing required command: $cmd" >&2
    exit 1
  fi
}

timestamp_utc() {
  date -u +%Y-%m-%dT%H:%M:%SZ
}

resolve_image() {
  local release_ref="$1"
  local docker_tag=""

  if [[ -n "${CLI_PROXY_IMAGE:-}" ]]; then
    echo "${CLI_PROXY_IMAGE}"
    return
  fi

  case "$release_ref" in
    fork/v*)
      docker_tag="${release_ref//\//-}"
      ;;
    fork-v*)
      docker_tag="$release_ref"
      ;;
    v*)
      docker_tag="fork-${release_ref}"
      ;;
    *)
      echo "Unsupported release ref: $release_ref" >&2
      exit 1
      ;;
  esac

  echo "${DEFAULT_IMAGE_REPO}:${docker_tag}"
}

rewrite_compose_image_if_needed() {
  local image="$1"
  if grep -q 'CLI_PROXY_IMAGE' "$COMPOSE_FILE"; then
    export CLI_PROXY_IMAGE="$image"
    return
  fi

  perl -0pi -e 's~(^\s*image:\s*).*$~${1}'"$image"'~m' "$COMPOSE_FILE"
}

extract_event_time() {
  local event_file="$1"
  local status="$2"
  local container_id="$3"
  if [[ -z "$container_id" ]]; then
    return
  fi
  awk -v wanted_status="$status" -v wanted_id="$container_id" '
    $2 == wanted_status && $4 == wanted_id {
      print $1
      exit
    }
  ' "$event_file"
}

format_events_timestamp() {
  local ts_nano="$1"
  if [[ -z "$ts_nano" ]]; then
    return
  fi
  python3 - "$ts_nano" <<'PY'
import datetime
import sys

ts = sys.argv[1].strip()
if not ts:
    raise SystemExit(0)
seconds = int(ts) / 1_000_000_000
dt = datetime.datetime.fromtimestamp(seconds, datetime.timezone.utc)
print(dt.strftime("%Y-%m-%dT%H:%M:%SZ"))
PY
}

require_command docker
require_command curl
require_command perl
require_command python3
require_command ss

if [[ $# -gt 1 ]]; then
  usage
  exit 1
fi

RELEASE_REF="${1:-${CLI_PROXY_RELEASE:-}}"
if [[ -z "$RELEASE_REF" && -z "${CLI_PROXY_IMAGE:-}" ]]; then
  usage
  exit 1
fi

if [[ ! -f "$COMPOSE_FILE" ]]; then
  echo "Compose file not found: $COMPOSE_FILE" >&2
  exit 1
fi

IMAGE_REF="$(resolve_image "$RELEASE_REF")"
COMPOSE_ARGS=(docker compose -f "$COMPOSE_FILE")
BACKUP_FILE="${COMPOSE_FILE}.bak-deploy-$(date -u +%Y%m%dT%H%M%SZ)"
HEALTH_URL="http://127.0.0.1:${PORT}${HEALTH_PATH}"

cp "$COMPOSE_FILE" "$BACKUP_FILE"
rewrite_compose_image_if_needed "$IMAGE_REF"

OLD_CONTAINER_ID="$("${COMPOSE_ARGS[@]}" ps -q "$SERVICE_NAME" || true)"
EVENTS_FILE="$(mktemp)"
EVENTS_PID=""
cleanup() {
  if [[ -n "$EVENTS_PID" ]] && kill -0 "$EVENTS_PID" >/dev/null 2>&1; then
    kill "$EVENTS_PID" >/dev/null 2>&1 || true
    wait "$EVENTS_PID" 2>/dev/null || true
  fi
  rm -f "$EVENTS_FILE"
}
trap cleanup EXIT

docker events \
  --since "$(timestamp_utc)" \
  --filter "type=container" \
  --format '{{.TimeNano}} {{.Status}} {{.Actor.Attributes.name}} {{.ID}}' >"$EVENTS_FILE" 2>/dev/null &
EVENTS_PID="$!"

PULL_STARTED_AT="$(timestamp_utc)"
"${COMPOSE_ARGS[@]}" pull "$SERVICE_NAME"
PULL_FINISHED_AT="$(timestamp_utc)"

UP_INVOKED_AT="$(timestamp_utc)"
"${COMPOSE_ARGS[@]}" up -d --no-build "$SERVICE_NAME"
NEW_CONTAINER_ID="$("${COMPOSE_ARGS[@]}" ps -q "$SERVICE_NAME")"
NEW_CONTAINER_STARTED_AT="$(docker inspect -f '{{.State.StartedAt}}' "$NEW_CONTAINER_ID" 2>/dev/null || true)"

PORT_LISTEN_AT=""
HEALTHZ_OK_AT=""
FIRST_SUCCESS_REQUEST_AT=""
FIRST_FAILED_REQUEST_AT=""
LAST_FAILED_REQUEST_AT=""
CONNECTION_REFUSED_REQUEST_COUNT=0
HEALTH_BODY=""

for _ in $(seq 1 150); do
  NOW="$(timestamp_utc)"

  if [[ -z "$PORT_LISTEN_AT" ]] && ss -lnt "( sport = :${PORT} )" | grep -q LISTEN; then
    PORT_LISTEN_AT="$NOW"
  fi

  if HEALTH_BODY="$(curl -fsS "$HEALTH_URL" 2>&1)"; then
    HEALTHZ_OK_AT="$NOW"
    FIRST_SUCCESS_REQUEST_AT="$NOW"
    break
  fi

  if [[ -z "$FIRST_FAILED_REQUEST_AT" ]]; then
    FIRST_FAILED_REQUEST_AT="$NOW"
  fi
  LAST_FAILED_REQUEST_AT="$NOW"

  if [[ "$HEALTH_BODY" == *"Connection refused"* || "$HEALTH_BODY" == *"connection refused"* ]]; then
    CONNECTION_REFUSED_REQUEST_COUNT=$((CONNECTION_REFUSED_REQUEST_COUNT + 1))
  fi

  sleep 0.2
done

sleep 1

if [[ -z "$HEALTHZ_OK_AT" ]]; then
  echo "Deployment failed: health check never recovered for $HEALTH_URL" >&2
  exit 1
fi

OLD_CONTAINER_STOP_AT="$(format_events_timestamp "$(extract_event_time "$EVENTS_FILE" die "$OLD_CONTAINER_ID")")"
NEW_CONTAINER_START_EVENT_AT="$(format_events_timestamp "$(extract_event_time "$EVENTS_FILE" start "$NEW_CONTAINER_ID")")"

cat <<EOF
release_ref=${RELEASE_REF:-custom-image}
image_ref=${IMAGE_REF}
compose_file=${COMPOSE_FILE}
compose_backup=${BACKUP_FILE}
service_name=${SERVICE_NAME}
old_container_id=${OLD_CONTAINER_ID}
old_container_stop_at=${OLD_CONTAINER_STOP_AT}
pull_started_at=${PULL_STARTED_AT}
pull_finished_at=${PULL_FINISHED_AT}
up_invoked_at=${UP_INVOKED_AT}
new_container_id=${NEW_CONTAINER_ID}
new_container_start_event_at=${NEW_CONTAINER_START_EVENT_AT}
new_container_started_at=${NEW_CONTAINER_STARTED_AT}
port_${PORT}_listen_at=${PORT_LISTEN_AT}
healthz_ok_at=${HEALTHZ_OK_AT}
first_success_request_at=${FIRST_SUCCESS_REQUEST_AT}
first_failed_request_at=${FIRST_FAILED_REQUEST_AT}
last_failed_request_at=${LAST_FAILED_REQUEST_AT}
connection_refused_request_count=${CONNECTION_REFUSED_REQUEST_COUNT}
healthz_body=${HEALTH_BODY}
EOF

"${COMPOSE_ARGS[@]}" ps "$SERVICE_NAME"
docker logs --tail 20 "$SERVICE_NAME" 2>&1
