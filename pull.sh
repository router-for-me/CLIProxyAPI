#!/usr/bin/env bash

set -euo pipefail

IMAGE_REFERENCE="${1:-}"
COMPOSE_FILE="${CPA_PRODUCTION_COMPOSE_FILE:-docker-compose.production.yml}"
SERVICE_NAME="cli-proxy-api"
CONTAINER_NAME="cli-proxy-api"
ROLLBACK_IMAGE="cli-proxy-api-plus:rollback-previous"

if docker compose version >/dev/null 2>&1; then
  COMPOSE_COMMAND=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE_COMMAND=(docker-compose)
else
  echo "Error: neither docker compose nor docker-compose is available."
  exit 1
fi

if [[ -z "${IMAGE_REFERENCE}" ]]; then
  echo "Usage: ./pull.sh ghcr.io/austinhmh/cli-proxy-api-plus:sha-<40-hex-commit>"
  echo "   or: ./pull.sh ghcr.io/austinhmh/cli-proxy-api-plus@sha256:<digest>"
  exit 1
fi

if [[ ! "${IMAGE_REFERENCE}" =~ :sha-[0-9a-f]{40}$ ]] && [[ ! "${IMAGE_REFERENCE}" =~ @sha256:[0-9a-f]{64}$ ]]; then
  echo "Error: only immutable full-commit tags or image digests are accepted."
  exit 1
fi

if [[ ! -f "${COMPOSE_FILE}" ]]; then
  echo "Error: production compose file not found: ${COMPOSE_FILE}"
  exit 1
fi

if [[ "${IMAGE_REFERENCE}" == ghcr.io/* ]]; then
  GHCR_TOKEN="${GHCR_TOKEN:-${GH_TOKEN:-}}"
  GHCR_USERNAME="${GHCR_USERNAME:-austinhmh}"
  if [[ -n "${GHCR_TOKEN}" ]]; then
    printf '%s' "${GHCR_TOKEN}" | docker login ghcr.io --username "${GHCR_USERNAME}" --password-stdin >/dev/null
  fi
fi

CURRENT_IMAGE_ID="$(docker inspect --format '{{.Image}}' "${CONTAINER_NAME}" 2>/dev/null || true)"
if [[ -z "${CURRENT_IMAGE_ID}" ]]; then
  echo "Error: current ${CONTAINER_NAME} container was not found; refusing deployment without a rollback image."
  exit 1
fi

if [[ -z "${MANAGEMENT_PASSWORD:-}" ]]; then
  while IFS= read -r environment_entry; do
    case "${environment_entry}" in
      MANAGEMENT_PASSWORD=*)
        MANAGEMENT_PASSWORD="${environment_entry#MANAGEMENT_PASSWORD=}"
        break
        ;;
    esac
  done < <(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "${CONTAINER_NAME}")
fi
export MANAGEMENT_PASSWORD="${MANAGEMENT_PASSWORD:-}"
MANAGEMENT_KEY="${MANAGEMENT_KEY:-${MANAGEMENT_PASSWORD:-}}"

docker image tag "${CURRENT_IMAGE_ID}" "${ROLLBACK_IMAGE}"

echo "Pulling CI-built image: ${IMAGE_REFERENCE}"
docker pull "${IMAGE_REFERENCE}"
CANDIDATE_IMAGE_ID="$(docker image inspect --format '{{.Id}}' "${IMAGE_REFERENCE}")"

replace_running_container() {
  local target_image="$1"
  # Existing production container may not belong to this compose project.
  # Stop/remove the named container first so compose can recreate it cleanly.
  if docker inspect "${CONTAINER_NAME}" >/dev/null 2>&1; then
    docker stop "${CONTAINER_NAME}" >/dev/null
    docker rm "${CONTAINER_NAME}" >/dev/null
  fi
  export CLI_PROXY_IMAGE="${target_image}"
  "${COMPOSE_COMMAND[@]}" -f "${COMPOSE_FILE}" up -d --no-build --force-recreate "${SERVICE_NAME}"
}

rollback() {
  echo "Candidate verification failed; restoring ${ROLLBACK_IMAGE}."
  replace_running_container "${ROLLBACK_IMAGE}"
  echo "Rollback completed."
}

if ! replace_running_container "${IMAGE_REFERENCE}"; then
  rollback
  exit 1
fi

healthy=false
for attempt in $(seq 1 60); do
  if curl -fsS --max-time 2 http://127.0.0.1:8317/healthz >/dev/null; then
    healthy=true
    break
  fi
  sleep 2
done

if [[ "${healthy}" != "true" ]]; then
  rollback
  exit 1
fi

RUNNING_IMAGE_ID="$(docker inspect --format '{{.Image}}' "${CONTAINER_NAME}")"
if [[ "${RUNNING_IMAGE_ID}" != "${CANDIDATE_IMAGE_ID}" ]]; then
  echo "Running image mismatch: ${RUNNING_IMAGE_ID} != ${CANDIDATE_IMAGE_ID}"
  rollback
  exit 1
fi

EXPECTED_COMMIT=""
if [[ "${IMAGE_REFERENCE}" =~ :sha-([0-9a-f]{40})$ ]]; then
  EXPECTED_COMMIT="${BASH_REMATCH[1]}"
fi

if [[ -n "${EXPECTED_COMMIT}" ]]; then
  if [[ -z "${MANAGEMENT_KEY}" ]]; then
    echo "Error: MANAGEMENT_KEY or the current container's MANAGEMENT_PASSWORD is required to verify the build commit."
    rollback
    exit 1
  fi

  BUILD_COMMIT=""
  while IFS=':' read -r header_name header_value; do
    header_name="${header_name//$'\r'/}"
    header_value="${header_value//$'\r'/}"
    if [[ "${header_name,,}" == "x-cpa-commit" ]]; then
      BUILD_COMMIT="${header_value# }"
      break
    fi
  done < <(curl -fsS -D - -o /dev/null --max-time 10 -H "Authorization: Bearer ${MANAGEMENT_KEY}" http://127.0.0.1:8317/v0/management/config)

  if [[ "${BUILD_COMMIT}" != "${EXPECTED_COMMIT}" ]]; then
    echo "Build commit mismatch: ${BUILD_COMMIT:-missing} != ${EXPECTED_COMMIT}"
    rollback
    exit 1
  fi
fi

echo "Deployment verified."
echo "Image: ${IMAGE_REFERENCE}"
echo "Image ID: ${CANDIDATE_IMAGE_ID}"
echo "Rollback image: ${ROLLBACK_IMAGE}"
