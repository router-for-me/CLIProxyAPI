#!/usr/bin/env bash
#
# build-binary.sh - Build a static binary for Docker
#

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo "=================================="
echo "Building Static Binary for Docker"
echo "=================================="
echo ""

# Detect architecture
HOST_ARCH=$(uname -m)
echo "Host architecture: ${HOST_ARCH}"

# Map to Go GOARCH
if [[ "${HOST_ARCH}" == "x86_64" ]]; then
    GOARCH="amd64"
elif [[ "${HOST_ARCH}" == "aarch64" ]] || [[ "${HOST_ARCH}" == "arm64" ]]; then
    GOARCH="arm64"
else
    echo -e "${RED}Unsupported architecture: ${HOST_ARCH}${NC}"
    exit 1
fi

echo "Building for: linux/${GOARCH}"
echo ""

# Clean old binary
rm -f ./CLIProxyAPI

# Build with correct flags for static linking
echo "Building static binary..."
CGO_ENABLED=0 GOOS=linux GOARCH=${GOARCH} \
    go build \
    -a \
    -trimpath \
    -ldflags='-s -w -extldflags "-static"' \
    -o ./CLIProxyAPI \
    ./cmd/server/

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ Binary built successfully${NC}"
    echo ""
    
    # Show binary info
    ls -lh ./CLIProxyAPI
    echo ""
    
    # Check if it's static
    if command -v ldd &> /dev/null; then
        echo "Checking if binary is static:"
        if ldd ./CLIProxyAPI 2>&1 | grep -q "not a dynamic executable"; then
            echo -e "${GREEN}✓ Binary is statically linked${NC}"
        else
            echo -e "${YELLOW}Warning: Binary may have dynamic dependencies:${NC}"
            ldd ./CLIProxyAPI
        fi
    fi
    
    if command -v file &> /dev/null; then
        echo ""
        echo "Binary details:"
        file ./CLIProxyAPI
    fi
    
    echo ""
    echo -e "${GREEN}You can now run: ./docker-run.sh${NC}"
else
    echo -e "${RED}✗ Build failed${NC}"
    exit 1
fi

