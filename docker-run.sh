#!/usr/bin/env bash
#
# docker-run.sh - Runtime-only Docker Build and Run Script
#
# This script builds a Docker image from a pre-compiled binary and runs it
# with config.yaml mounted from the host.

set -euo pipefail

# Configuration
IMAGE_NAME="cliproxyapi-runtime"
CONTAINER_NAME="cliproxyapi"
BINARY_NAME="CLIProxyAPI"
CONFIG_FILE="config.yaml"
PORT="8317"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "=================================="
echo "CLIProxyAPI Runtime Docker Script"
echo "=================================="
echo ""

# --- Step 1: Check if binary exists ---
if [ ! -f "./${BINARY_NAME}" ]; then
    echo -e "${RED}Error: Binary '${BINARY_NAME}' not found in current directory.${NC}"
    echo ""
    echo "Please build the binary first using one of these methods:"
    echo ""
    echo "1. Build for Linux (if you're on Linux):"
    echo "   CGO_ENABLED=0 GOOS=linux go build -ldflags='-s -w' -o ./CLIProxyAPI ./cmd/server/"
    echo ""
    echo "2. Build for Linux (if you're on macOS/Windows):"
    echo "   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o ./CLIProxyAPI ./cmd/server/"
    echo ""
    exit 1
fi

echo -e "${GREEN}✓ Binary found: ${BINARY_NAME}${NC}"

# --- Step 2: Check if config.yaml exists ---
if [ ! -f "./${CONFIG_FILE}" ]; then
    echo -e "${YELLOW}Warning: ${CONFIG_FILE} not found in current directory.${NC}"
    
    if [ -f "./config.example.yaml" ]; then
        read -r -p "Do you want to copy config.example.yaml to config.yaml? [Y/n]: " response
        response=${response:-Y}
        if [[ "$response" =~ ^[Yy]$ ]]; then
            cp config.example.yaml config.yaml
            echo -e "${GREEN}✓ Created ${CONFIG_FILE} from config.example.yaml${NC}"
            echo -e "${YELLOW}Please edit ${CONFIG_FILE} before continuing.${NC}"
            read -r -p "Press Enter to continue after editing config.yaml..."
        else
            echo -e "${RED}Cannot continue without ${CONFIG_FILE}${NC}"
            exit 1
        fi
    else
        echo -e "${RED}Error: Neither ${CONFIG_FILE} nor config.example.yaml found.${NC}"
        exit 1
    fi
fi

echo -e "${GREEN}✓ Config file found: ${CONFIG_FILE}${NC}"

# --- Step 3: Build Docker image ---
echo ""
echo "Building Docker image..."
if docker build -f Dockerfile.runtime -t "${IMAGE_NAME}:latest" .; then
    echo -e "${GREEN}✓ Docker image built successfully${NC}"
else
    echo -e "${RED}Error: Failed to build Docker image${NC}"
    exit 1
fi

# --- Step 4: Stop and remove existing container if running ---
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo ""
    echo "Stopping and removing existing container..."
    docker stop "${CONTAINER_NAME}" >/dev/null 2>&1 || true
    docker rm "${CONTAINER_NAME}" >/dev/null 2>&1 || true
    echo -e "${GREEN}✓ Existing container removed${NC}"
fi

# --- Step 5: Run the container ---
echo ""
echo "Starting container..."
echo ""

# Get absolute path for mounting
CONFIG_DIR="$(cd "$(dirname "${CONFIG_FILE}")" && pwd)"

# Create auths directory if it doesn't exist
mkdir -p "${CONFIG_DIR}/auths"

docker run -d \
    --name "${CONTAINER_NAME}" \
    -p "${PORT}:${PORT}" \
    -v "${CONFIG_DIR}/${CONFIG_FILE}:/config/config.yaml:ro" \
    -v "${CONFIG_DIR}/auths:/data/auths" \
    --restart unless-stopped \
    "${IMAGE_NAME}:latest"

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ Container started successfully${NC}"
    echo ""
    echo "Container Information:"
    echo "  Name: ${CONTAINER_NAME}"
    echo "  Port: ${PORT}"
    echo "  Config: ${CONFIG_DIR}/${CONFIG_FILE} -> /config/config.yaml"
    echo "  Auths:  ${CONFIG_DIR}/auths -> /data/auths"
    echo ""
    echo -e "${YELLOW}Note: Make sure your config.yaml has 'auth-dir: /data/auths'${NC}"
    echo ""
    echo "Useful commands:"
    echo "  View logs:    docker logs -f ${CONTAINER_NAME}"
    echo "  Stop:         docker stop ${CONTAINER_NAME}"
    echo "  Restart:      docker restart ${CONTAINER_NAME}"
    echo "  Remove:       docker rm -f ${CONTAINER_NAME}"
    echo ""
    echo "Showing last 20 lines of logs..."
    echo "=================================="
    sleep 2
    docker logs --tail 20 "${CONTAINER_NAME}"
else
    echo -e "${RED}Error: Failed to start container${NC}"
    exit 1
fi

