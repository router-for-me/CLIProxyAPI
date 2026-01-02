#!/usr/bin/env bash
#
# build.sh - Linux/macOS Build Script
#
# This script automates the process of building and running the Docker container
# with version information dynamically injected at build time.

# Hidden feature: Preserve usage statistics across rebuilds
# Usage: ./docker-build.sh --with-usage
# First run prompts for management API key, saved to temp/stats/.api_secret

set -euo pipefail

STATS_DIR="temp/stats"
STATS_FILE="${STATS_DIR}/.usage_backup.json"
SECRET_FILE="${STATS_DIR}/.api_secret"
WITH_USAGE=false

get_port() {
  if [[ -f "config.yaml" ]]; then
    grep -E "^port:" config.yaml | sed -E 's/^port: *["'"'"']?([0-9]+)["'"'"']?.*$/\1/'
  else
    echo "8317"
  fi
}

export_stats_api_secret() {
  if [[ -f "${SECRET_FILE}" ]]; then
    API_SECRET=$(cat "${SECRET_FILE}")
  else
    if [[ ! -d "${STATS_DIR}" ]]; then
      mkdir -p "${STATS_DIR}"
    fi
    echo "First time using --with-usage. Management API key required."
    read -r -p "Enter management key: " -s API_SECRET
    echo
    echo "${API_SECRET}" > "${SECRET_FILE}"
    chmod 600 "${SECRET_FILE}"
  fi
}

check_container_running() {
  local port
  port=$(get_port)

  if ! curl -s -o /dev/null -w "%{http_code}" "http://localhost:${port}/" | grep -q "200"; then
    echo "Error: cli-proxy-api service is not responding at localhost:${port}"
    echo "Please start the container first or use without --with-usage flag."
    exit 1
  fi
}

export_stats() {
  local port
  port=$(get_port)

  if [[ ! -d "${STATS_DIR}" ]]; then
    mkdir -p "${STATS_DIR}"
  fi
  check_container_running
  echo "Exporting usage statistics..."
  EXPORT_RESPONSE=$(curl -s -w "\n%{http_code}" -H "X-Management-Key: ${API_SECRET}" \
    "http://localhost:${port}/v0/management/usage/export")
  HTTP_CODE=$(echo "${EXPORT_RESPONSE}" | tail -n1)
  RESPONSE_BODY=$(echo "${EXPORT_RESPONSE}" | sed '$d')

  if [[ "${HTTP_CODE}" != "200" ]]; then
    echo "Export failed (HTTP ${HTTP_CODE}): ${RESPONSE_BODY}"
    exit 1
  fi

  echo "${RESPONSE_BODY}" > "${STATS_FILE}"
  echo "Statistics exported to ${STATS_FILE}"
}

import_stats() {
  local port
  port=$(get_port)

  echo "Importing usage statistics..."
  IMPORT_RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "X-Management-Key: ${API_SECRET}" \
    -H "Content-Type: application/json" \
    -d @"${STATS_FILE}" \
    "http://localhost:${port}/v0/management/usage/import")
  IMPORT_CODE=$(echo "${IMPORT_RESPONSE}" | tail -n1)
  IMPORT_BODY=$(echo "${IMPORT_RESPONSE}" | sed '$d')

  if [[ "${IMPORT_CODE}" == "200" ]]; then
    echo "Statistics imported successfully"
  else
    echo "Import failed (HTTP ${IMPORT_CODE}): ${IMPORT_BODY}"
  fi

  rm -f "${STATS_FILE}"
}

wait_for_service() {
  local port
  port=$(get_port)

  echo "Waiting for service to be ready..."
  for i in {1..30}; do
    if curl -s -o /dev/null -w "%{http_code}" "http://localhost:${port}/" | grep -q "200"; then
      break
    fi
    sleep 1
  done
  sleep 2
}

if [[ "${1:-}" == "--with-usage" ]]; then
  WITH_USAGE=true
  export_stats_api_secret
fi

# --- Step 1: Choose Environment ---
echo "Please select an option:"
echo "1) Run using Pre-built Image (Recommended)"
echo "2) Build from Source and Run (For Developers)"
read -r -p "Enter choice [1-2]: " choice

# --- Step 2: Execute based on choice ---
case "$choice" in
  1)
    echo "--- Running with Pre-built Image ---"
    if [[ "${WITH_USAGE}" == "true" ]]; then
      export_stats
    fi
    docker compose up -d --remove-orphans --no-build
    if [[ "${WITH_USAGE}" == "true" ]]; then
      wait_for_service
      import_stats
    fi
    echo "Services are starting from remote image."
    echo "Run 'docker compose logs -f' to see the logs."
    ;;
  2)
    echo "--- Building from Source and Running ---"

    # Get Version Information
    VERSION="$(git describe --tags --always --dirty)"
    COMMIT="$(git rev-parse --short HEAD)"
    BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

    echo "Building with the following info:"
    echo "  Version: ${VERSION}"
    echo "  Commit: ${COMMIT}"
    echo "  Build Date: ${BUILD_DATE}"
    echo "----------------------------------------"

    # Build and start the services with a local-only image tag
    export CLI_PROXY_IMAGE="cli-proxy-api:local"

    if [[ "${WITH_USAGE}" == "true" ]]; then
      export_stats
    fi

    echo "Building the Docker image..."
    docker compose build \
      --build-arg VERSION="${VERSION}" \
      --build-arg COMMIT="${COMMIT}" \
      --build-arg BUILD_DATE="${BUILD_DATE}"

    echo "Starting the services..."
    docker compose up -d --remove-orphans --pull never

    if [[ "${WITH_USAGE}" == "true" ]]; then
      wait_for_service
      import_stats
    fi

    echo "Build complete. Services are starting."
    echo "Run 'docker compose logs -f' to see the logs."
    ;;
  *)
    echo "Invalid choice. Please enter 1 or 2."
    exit 1
    ;;
esac
