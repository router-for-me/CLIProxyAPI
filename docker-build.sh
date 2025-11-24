#!/usr/bin/env bash
#
# docker-build.sh - Build script for CLIProxyAPI
#
# This script builds the Docker image with version information.
# Use docker-compose.yml in Portainer for deployment.

set -euo pipefail

echo "=== CLIProxyAPI Docker Build ==="
echo ""

# Get Version Information
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo 'dev')"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "Build info:"
echo "  Version:    ${VERSION}"
echo "  Commit:     ${COMMIT}"
echo "  Build Date: ${BUILD_DATE}"
echo "----------------------------------------"

# Build the Docker image
echo "Building Docker image..."
docker build \
  --build-arg VERSION="${VERSION}" \
  --build-arg COMMIT="${COMMIT}" \
  --build-arg BUILD_DATE="${BUILD_DATE}" \
  -t cli-proxy-api:local \
  -f Dockerfile \
  .

echo ""
echo "========================================="
echo "Build complete!"
echo "Image: cli-proxy-api:local"
echo "========================================="
echo ""
echo "Next steps:"
echo "  1. Go to Portainer"
echo "  2. Update your stack (recreate container)"
echo "  3. The new binary will be used automatically"
echo ""
echo "Or run locally with:"
echo "  docker compose up -d"
