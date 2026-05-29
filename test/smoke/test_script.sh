#!/bin/bash

# Change directory to where the script is located (test/smoke)
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
cd "$DIR"

mkdir -p logs

# Cleanup existing proxy if it was left running
pkill -f "test_proxy.go" 2>/dev/null || true
pkill -f "cli-proxy-api --config test_config.yaml" 2>/dev/null || true
sleep 1

# 1. Start the proxy in the background
echo "Starting proxy on port 9091..."
go run test_proxy.go > logs/proxy_output.log 2>&1 &
PROXY_PID=$!
sleep 2

# 2. Start the proxy API
echo "Starting CLI Proxy API on port 8318..."
go build -o cli-proxy-api ../../cmd/server
./cli-proxy-api --config test_config.yaml > logs/api_output.log 2>&1 &
API_PID=$!
sleep 3

# 3. Make the first request
echo "Making Request 1..."
curl -s --location 'localhost:8318/v1/chat/completions' \
--header 'Content-Type: application/json' \
--data '{
    "model": "gemini-2.5-flash-lite",
    "messages": [
        {
            "role": "user",
            "content": "hi, tell me abt urself"
        }
    ]
}' > /dev/null

sleep 2

# 4. Make the second request
echo "Making Request 2..."
curl -s --location 'localhost:8318/v1/chat/completions' \
--header 'Content-Type: application/json' \
--data '{
    "model": "gemini-2.5-flash-lite",
    "messages": [
        {
            "role": "user",
            "content": "hi again"
        }
    ]
}' > /dev/null

sleep 2

# 5. Clean up
echo "Shutting down servers..."
kill $API_PID 2>/dev/null || true
kill $PROXY_PID 2>/dev/null || true
pkill -f "test_proxy.go" 2>/dev/null || true
pkill -f "cli-proxy-api --config test_config.yaml" 2>/dev/null || true

# Clean up compiled binary inside test/smoke
rm -f cli-proxy-api

# 6. Show the proxy logs
echo "=== PROXY LOGS ==="
cat logs/proxy_output.log
