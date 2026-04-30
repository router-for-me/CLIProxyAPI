#!/usr/bin/env bash

# Build local image with docker build (no buildx required),
# then start services via docker compose.

set -euo pipefail

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker command not found."
  exit 1
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "Error: docker compose plugin not available."
  exit 1
fi

IMAGE_TAG="${CLI_PROXY_IMAGE:-cli-proxy-api:local}"

if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  VERSION="$(git describe --tags --always --dirty)"
  COMMIT="$(git rev-parse --short HEAD)"
else
  VERSION="dev"
  COMMIT="none"
fi
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "Building local image with:"
echo "  Image Tag:  ${IMAGE_TAG}"
echo "  Version:    ${VERSION}"
echo "  Commit:     ${COMMIT}"
echo "  Build Date: ${BUILD_DATE}"
echo "----------------------------------------"

docker build \
  -t "${IMAGE_TAG}" \
  --build-arg VERSION="${VERSION}" \
  --build-arg COMMIT="${COMMIT}" \
  --build-arg BUILD_DATE="${BUILD_DATE}" \
  --build-arg GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}" \
  --build-arg GOSUMDB="${GOSUMDB:-sum.golang.org}" \
  --build-arg GOPRIVATE="${GOPRIVATE:-}" \
  .

echo "Starting services from local image..."
CLI_PROXY_IMAGE="${IMAGE_TAG}" CLI_PROXY_PULL_POLICY="never" docker compose up -d --remove-orphans --no-build --pull never

echo "Done."
echo "Use 'docker compose logs -f' to view logs."
