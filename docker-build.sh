#!/usr/bin/env bash
#
# docker-build.sh - Linux/macOS one-shot Docker deploy script
#
# Builds (optional) and starts:
#   - cli-proxy-api      (proxy + Management UI)
#   - log-uploader       (hourly archive upload to TOS)
#
# log-qa is optional (compose profile "log-qa") and is NOT started by default,
# so it does not compete with log-uploader for CPU/disk during backlog drain.
#

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${ROOT_DIR}"

if [[ "${1:-}" != "" ]]; then
  echo "Error: unknown option '${1}'."
  echo "Usage: ./docker-build.sh"
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker is not installed or not on PATH."
  exit 1
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "Error: docker compose is not available. Install Docker Compose v2."
  exit 1
fi

ensure_file_from_example() {
  local target="$1"
  local example="$2"
  if [[ -f "${target}" ]]; then
    return 0
  fi
  if [[ ! -f "${example}" ]]; then
    echo "Error: missing ${example}; cannot create ${target}."
    exit 1
  fi
  cp "${example}" "${target}"
  echo "[prep] created ${target} from ${example}"
}

# --- Step 0: prepare runtime files ---
echo "--- Preparing config files ---"
ensure_file_from_example "config.yaml" "config.example.yaml"
ensure_file_from_example "log-uploader.yaml" "log-uploader.example.yaml"
ensure_file_from_example "log-qa.yaml" "log-qa.example.yaml"

if [[ ! -f ".env" ]]; then
  cat > .env <<'EOF'
# Optional environment for docker compose
# VOLC_TOS_ACCESS_KEY_ID=
# VOLC_TOS_SECRET_ACCESS_KEY=
# TOS_ACCESS_KEY_ID=
# TOS_SECRET_ACCESS_KEY=
EOF
  echo "[prep] created empty .env (fill TOS keys before enabling uploader upload)"
fi

# plugins/ is bind-mounted into the API container so store-installed
# plugin binaries survive container recreate.
mkdir -p logs auths plugins

echo
echo "Please select an option:"
echo "1) Run using Pre-built Image (Recommended)"
echo "2) Build from Source and Run (For Developers)"
read -r -p "Enter choice [1-2]: " choice

start_services() {
  local mode="$1"
  # Explicit service list: do not start log-qa (compose profile "log-qa").
  local services=(cli-proxy-api log-uploader)
  if [[ "${mode}" == "prebuilt" ]]; then
    docker compose up -d --remove-orphans --no-build "${services[@]}"
  else
    docker compose up -d --remove-orphans --pull never "${services[@]}"
  fi
  # If an older deploy left log-qa running, stop it so it does not keep using CPU/IO.
  if docker ps --filter "name=cli-proxy-api-log-qa" --format "{{.Names}}" 2>/dev/null | grep -q "cli-proxy-api-log-qa"; then
    echo "[prep] stopping existing log-qa container (disabled by default)"
    docker stop cli-proxy-api-log-qa >/dev/null || true
    docker update --restart=no cli-proxy-api-log-qa >/dev/null 2>&1 || true
  fi
}

print_status() {
  echo
  echo "========================================"
  echo "  Deploy complete"
  echo "========================================"
  echo "Services started:"
  echo "  - cli-proxy-api         proxy + Management UI"
  echo "  - log-uploader          hourly upload to TOS"
  echo "Services not started:"
  echo "  - log-qa                optional; start only when needed"
  echo
  echo "Useful commands:"
  echo "  docker compose ps"
  echo "  docker compose logs -f log-uploader"
  echo "  docker compose logs -f cli-proxy-api"
  echo
  echo "Start log-qa later (optional):"
  echo "  docker compose --profile log-qa up -d log-qa"
  echo "  docker compose --profile log-qa logs -f log-qa"
  echo "  docker compose --profile log-qa stop log-qa"
  echo
  echo "Management UI:  http://<server-ip>:8317/management.html"
  echo "Log QA panel:   only has data after log-qa has been run"
  echo
  echo "QA reports dir (host): ./logs/log-qa/reports/"
  echo "Plugins dir (host):    ./plugins  (mounted at /CLIProxyAPI/plugins)"
  echo "One-shot QA (no daemon): docker compose run --rm --profile log-qa log-qa ./log-qa -config /CLIProxyAPI/log-qa.yaml -once"
  echo
  echo "Plugins: set plugins.enabled=true and plugins.configs.<id>.enabled=true in config.yaml,"
  echo "         then install/update the plugin from Management after deploy."
  echo
  docker compose ps
}

case "$choice" in
  1)
    echo "--- Running with Pre-built Image ---"
    echo "Note: starts cli-proxy-api + log-uploader only (log-qa profile off)."
    start_services prebuilt
    print_status
    ;;
  2)
    echo "--- Building from Source and Running ---"

    VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
    COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
    BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

    echo "Building with the following info:"
    echo "  Version: ${VERSION}"
    echo "  Commit: ${COMMIT}"
    echo "  Build Date: ${BUILD_DATE}"
    echo "----------------------------------------"

    export CLI_PROXY_IMAGE="cli-proxy-api:local"
    export DOCKER_BUILDKIT=1

    echo "Building the Docker image (includes CLIProxyAPI, log-uploader, log-qa binaries)..."
    docker compose build \
      --build-arg VERSION="${VERSION}" \
      --build-arg COMMIT="${COMMIT}" \
      --build-arg BUILD_DATE="${BUILD_DATE}"

    echo "Starting cli-proxy-api + log-uploader (log-qa not started)..."
    start_services local
    print_status
    ;;
  *)
    echo "Invalid choice. Please enter 1 or 2."
    exit 1
    ;;
esac
