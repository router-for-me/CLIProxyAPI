#!/usr/bin/env bash
#
# build.sh - Linux/macOS Build Script
#
# This script automates the process of building and running the Docker container
# with version information dynamically injected at build time.

set -euo pipefail

if [[ "${1:-}" != "" ]]; then
  echo "Error: unknown option '${1}'."
  echo "Usage: ./docker-build.sh"
  exit 1
fi

resolve_repository_url() {
  local remote_url normalized
  remote_url="$(git remote get-url origin 2>/dev/null || true)"
  if [[ -z "$remote_url" ]]; then
    remote_url="$(git config --get remote.origin.url 2>/dev/null || true)"
  fi
  if [[ -z "$remote_url" ]]; then
    echo "unknown"
    return
  fi

  case "$remote_url" in
    git@github.com:*)
      normalized="https://github.com/${remote_url#git@github.com:}"
      ;;
    ssh://git@github.com/*)
      normalized="https://github.com/${remote_url#ssh://git@github.com/}"
      ;;
    http://github.com/*)
      normalized="https://${remote_url#http://}"
      ;;
    *)
      normalized="$remote_url"
      ;;
  esac

  normalized="${normalized%.git}"
  echo "$normalized"
}

default_remote_image="ghcr.io/quqi1599/cliproxyapi:latest"
selected_remote_image="${CLI_PROXY_IMAGE:-$default_remote_image}"

# --- Step 1: Choose Environment ---
echo "Please select an option:"
echo "1) Run using Pre-built Image (Recommended)"
echo "2) Build from Source and Run (For Developers)"
read -r -p "Enter choice [1-2]: " choice

# --- Step 2: Execute based on choice ---
case "$choice" in
  1)
    echo "--- Running with Pre-built Image ---"
    echo "Using remote image: ${selected_remote_image}"
    echo "Tip: set CLI_PROXY_IMAGE=ghcr.io/quqi1599/cliproxyapi:fork-v7.10.43 to pin a release."
    docker compose pull
    docker compose up -d --remove-orphans --no-build
    echo "Services are starting from remote image."
    echo "Run 'docker compose logs -f' to see the logs."
    ;;
  2)
    echo "--- Building from Source and Running ---"

    # Get Version Information
    VERSION="$(git describe --tags --always --dirty)"
    COMMIT="$(git rev-parse --short HEAD)"
    BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    REPOSITORY_URL="$(resolve_repository_url)"

    echo "Building with the following info:"
    echo "  Version: ${VERSION}"
    echo "  Commit: ${COMMIT}"
    echo "  Build Date: ${BUILD_DATE}"
    echo "  Repository URL: ${REPOSITORY_URL}"
    echo "----------------------------------------"

    # Build and start the services with a local-only image tag
    export CLI_PROXY_IMAGE="cli-proxy-api:local"

    echo "Building the Docker image..."
    docker compose build \
      --build-arg VERSION="${VERSION}" \
      --build-arg COMMIT="${COMMIT}" \
      --build-arg BUILD_DATE="${BUILD_DATE}" \
      --build-arg REPOSITORY_URL="${REPOSITORY_URL}"

    echo "Starting the services..."
    docker compose up -d --remove-orphans --pull never

    echo "Build complete. Services are starting."
    echo "Run 'docker compose logs -f' to see the logs."
    ;;
  *)
    echo "Invalid choice. Please enter 1 or 2."
    exit 1
    ;;
esac
