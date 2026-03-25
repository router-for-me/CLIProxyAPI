#!/bin/bash
# Test script for Cursor proxy integration
# Usage:
#   ./test_cursor.sh login    - Login to Cursor (opens browser)
#   ./test_cursor.sh start    - Build and start the server
#   ./test_cursor.sh test     - Run API tests against running server
#   ./test_cursor.sh all      - Login + Start + Test (full flow)

set -e

export PATH="/opt/homebrew/bin:$PATH"
export GOROOT="/opt/homebrew/Cellar/go/1.26.1/libexec"

PROJECT_DIR="/Volumes/Personal/cursor-cli-proxy/CLIProxyAPIPlus"
BINARY="$PROJECT_DIR/cliproxy-test"
API_KEY="quotio-local-D6ABC285-3085-44B4-B872-BD269888811F"
BASE_URL="http://127.0.0.1:8317"
CONFIG="$PROJECT_DIR/config-cursor-test.yaml"
PID_FILE="/tmp/cliproxy-test.pid"

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; }

# --- Build ---
build() {
    info "Building CLIProxyAPIPlus..."
    cd "$PROJECT_DIR"
    go build -o "$BINARY" ./cmd/server/
    info "Build successful: $BINARY"
}

# --- Create test config ---
create_config() {
    cat > "$CONFIG" << 'EOF'
host: '127.0.0.1'
port: 8317
auth-dir: '~/.cli-proxy-api'
api-keys:
  - 'quotio-local-D6ABC285-3085-44B4-B872-BD269888811F'
debug: true
EOF
    info "Test config created: $CONFIG"
}

# --- Login ---
do_login() {
    build
    create_config
    info "Starting Cursor login (will open browser)..."
    "$BINARY" --config "$CONFIG" --cursor-login
}

# --- Start server ---
start_server() {
    # Kill any existing instance
    stop_server 2>/dev/null || true

    build
    create_config

    info "Starting server on port 8317..."
    "$BINARY" --config "$CONFIG" &
    SERVER_PID=$!
    echo "$SERVER_PID" > "$PID_FILE"
    info "Server started (PID: $SERVER_PID)"

    # Wait for server to be ready
    info "Waiting for server to be ready..."
    for i in $(seq 1 15); do
        if curl -s "$BASE_URL/v1/models" -H "Authorization: Bearer $API_KEY" > /dev/null 2>&1; then
            info "Server is ready!"
            return 0
        fi
        sleep 1
    done
    error "Server failed to start within 15 seconds"
    return 1
}

# --- Stop server ---
stop_server() {
    if [ -f "$PID_FILE" ]; then
        PID=$(cat "$PID_FILE")
        if kill -0 "$PID" 2>/dev/null; then
            info "Stopping server (PID: $PID)..."
            kill "$PID"
            rm -f "$PID_FILE"
        fi
    fi
    # Also kill any stale process on port 8317
    lsof -ti:8317 2>/dev/null | xargs kill 2>/dev/null || true
}

# --- Test: List models ---
test_models() {
    info "Testing GET /v1/models (looking for cursor models)..."
    RESPONSE=$(curl -s "$BASE_URL/v1/models" \
        -H "Authorization: Bearer $API_KEY")

    CURSOR_MODELS=$(echo "$RESPONSE" | python3 -c "
import json, sys
try:
    data = json.load(sys.stdin)
    models = [m['id'] for m in data.get('data', []) if m.get('owned_by') == 'cursor' or m.get('type') == 'cursor']
    if models:
        print('\n'.join(models))
    else:
        print('NONE')
except:
    print('ERROR')
" 2>/dev/null || echo "PARSE_ERROR")

    if [ "$CURSOR_MODELS" = "NONE" ] || [ "$CURSOR_MODELS" = "ERROR" ] || [ "$CURSOR_MODELS" = "PARSE_ERROR" ]; then
        warn "No cursor models found. Have you run '--cursor-login' first?"
        echo "  Response preview: $(echo "$RESPONSE" | head -c 200)"
        return 1
    else
        info "Found cursor models:"
        echo "$CURSOR_MODELS" | while read -r model; do
            echo "  - $model"
        done
        return 0
    fi
}

# --- Test: Chat completion (streaming) ---
test_chat_stream() {
    local model="${1:-cursor-small}"
    info "Testing POST /v1/chat/completions (stream, model=$model)..."

    RESPONSE=$(curl -s --max-time 30 "$BASE_URL/v1/chat/completions" \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -d "{
            \"model\": \"$model\",
            \"messages\": [{\"role\": \"user\", \"content\": \"Say hello in exactly 3 words.\"}],
            \"stream\": true
        }" 2>&1)

    # Check if we got SSE data
    if echo "$RESPONSE" | grep -q "data:"; then
        # Extract content from SSE chunks
        CONTENT=$(echo "$RESPONSE" | grep "^data: " | grep -v "\[DONE\]" | while read -r line; do
            echo "${line#data: }" | python3 -c "
import json, sys
try:
    chunk = json.load(sys.stdin)
    delta = chunk.get('choices', [{}])[0].get('delta', {})
    content = delta.get('content', '')
    if content:
        sys.stdout.write(content)
except:
    pass
" 2>/dev/null
        done)

        if [ -n "$CONTENT" ]; then
            info "Stream response received:"
            echo "  Content: $CONTENT"
            return 0
        else
            warn "Got SSE chunks but no content extracted"
            echo "  Raw (first 500 chars): $(echo "$RESPONSE" | head -c 500)"
            return 1
        fi
    else
        error "No SSE data received"
        echo "  Response: $(echo "$RESPONSE" | head -c 300)"
        return 1
    fi
}

# --- Test: Chat completion (non-streaming) ---
test_chat_nonstream() {
    local model="${1:-cursor-small}"
    info "Testing POST /v1/chat/completions (non-stream, model=$model)..."

    RESPONSE=$(curl -s --max-time 30 "$BASE_URL/v1/chat/completions" \
        -H "Authorization: Bearer $API_KEY" \
        -H "Content-Type: application/json" \
        -d "{
            \"model\": \"$model\",
            \"messages\": [{\"role\": \"user\", \"content\": \"What is 2+2? Answer with just the number.\"}],
            \"stream\": false
        }" 2>&1)

    CONTENT=$(echo "$RESPONSE" | python3 -c "
import json, sys
try:
    data = json.load(sys.stdin)
    content = data['choices'][0]['message']['content']
    print(content)
except Exception as e:
    print(f'ERROR: {e}')
" 2>/dev/null || echo "PARSE_ERROR")

    if echo "$CONTENT" | grep -q "ERROR\|PARSE_ERROR"; then
        error "Non-streaming request failed"
        echo "  Response: $(echo "$RESPONSE" | head -c 300)"
        return 1
    else
        info "Non-stream response received:"
        echo "  Content: $CONTENT"
        return 0
    fi
}

# --- Run all tests ---
run_tests() {
    local passed=0
    local failed=0

    echo ""
    echo "========================================="
    echo "  Cursor Proxy Integration Tests"
    echo "========================================="
    echo ""

    # Test 1: Models
    if test_models; then
        ((passed++))
    else
        ((failed++))
    fi
    echo ""

    # Test 2: Streaming chat
    if test_chat_stream "cursor-small"; then
        ((passed++))
    else
        ((failed++))
    fi
    echo ""

    # Test 3: Non-streaming chat
    if test_chat_nonstream "cursor-small"; then
        ((passed++))
    else
        ((failed++))
    fi
    echo ""

    echo "========================================="
    echo "  Results: ${passed} passed, ${failed} failed"
    echo "========================================="

    [ "$failed" -eq 0 ]
}

# --- Cleanup ---
cleanup() {
    stop_server
    rm -f "$BINARY" "$CONFIG"
    info "Cleaned up."
}

# --- Main ---
case "${1:-help}" in
    login)
        do_login
        ;;
    start)
        start_server
        info "Server running. Use './test_cursor.sh test' to run tests."
        info "Use './test_cursor.sh stop' to stop."
        ;;
    stop)
        stop_server
        ;;
    test)
        run_tests
        ;;
    all)
        info "=== Full flow: login -> start -> test ==="
        echo ""
        info "Step 1: Login to Cursor"
        do_login
        echo ""
        info "Step 2: Start server"
        start_server
        echo ""
        info "Step 3: Run tests"
        sleep 2
        run_tests
        echo ""
        info "Step 4: Cleanup"
        stop_server
        ;;
    clean)
        cleanup
        ;;
    *)
        echo "Usage: $0 {login|start|stop|test|all|clean}"
        echo ""
        echo "  login  - Authenticate with Cursor (opens browser)"
        echo "  start  - Build and start the proxy server"
        echo "  stop   - Stop the running server"
        echo "  test   - Run API tests against running server"
        echo "  all    - Full flow: login + start + test"
        echo "  clean  - Stop server and remove artifacts"
        ;;
esac
