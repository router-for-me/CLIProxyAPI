#!/usr/bin/env bash
# Check what's inside the container

echo "=== Checking Container Contents ==="
echo ""

# Stop the failing container first
docker stop cliproxyapi 2>/dev/null
docker rm cliproxyapi 2>/dev/null

# Run a temporary container with shell to inspect
echo "Starting temporary container for inspection..."
docker run --rm --name cliproxyapi-inspect \
    --entrypoint /bin/sh \
    cliproxyapi-runtime:latest \
    -c "
echo '1. Contents of /app:';
ls -lah /app/;
echo '';
echo '2. File type of binary:';
file /app/CLIProxyAPI 2>&1 || echo 'File command not available';
echo '';
echo '3. Architecture:';
uname -m;
echo '';
echo '4. Try to execute:';
/app/CLIProxyAPI --version 2>&1 || echo 'Failed to execute';
"

