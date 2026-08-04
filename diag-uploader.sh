#!/bin/bash
# Log uploader diagnostic script
# Usage: bash diag-uploader.sh

set -euo pipefail
CONTAINER="cli-proxy-api-uploader"
WORK_DIR="/CLIProxyAPI/logs/.log-uploader-work"
STATE_FILE="$WORK_DIR/state.json"
AUDIT_FILE="$WORK_DIR/audit.jsonl"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

ok()   { echo -e "${GREEN}[OK]${NC} $1"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
fail() { echo -e "${RED}[FAIL]${NC} $1"; }
sep()  { echo "────────────────────────────────────────────────"; }

echo "========================================"
echo "  Log Uploader Diagnostics"
echo "  $(date '+%Y-%m-%d %H:%M:%S')"
echo "========================================"
echo

# 1. Container status
sep
echo "1. Container Status"
sep
if docker ps --filter "name=$CONTAINER" --format "{{.Names}}" | grep -q "$CONTAINER"; then
    STATUS=$(docker ps --filter "name=$CONTAINER" --format "{{.Status}}")
    ok "Container is running: $STATUS"
else
    if docker ps -a --filter "name=$CONTAINER" --format "{{.Names}}" | grep -q "$CONTAINER"; then
        STATUS=$(docker ps -a --filter "name=$CONTAINER" --format "{{.Status}}")
        fail "Container exists but not running: $STATUS"
    else
        fail "Container does not exist. Run: docker compose up -d log-uploader"
    fi
    echo
    echo "Stopping diagnostics."
    exit 1
fi
echo

# 2. Recent logs (errors only)
sep
echo "2. Recent Logs (last 50 lines, errors highlighted)"
sep
RECENT_LOGS=$(docker logs "$CONTAINER" --tail 50 2>&1)
echo "$RECENT_LOGS" | tail -5
echo "... (showing last 5 of 50 lines)"
echo

ERROR_COUNT=$(echo "$RECENT_LOGS" | grep -c "\[error\]" || true)
if [ "$ERROR_COUNT" -gt 0 ]; then
    fail "Found $ERROR_COUNT error(s) in recent logs:"
    echo "$RECENT_LOGS" | grep "\[error\]"
else
    ok "No errors in recent logs"
fi
echo

# 3. State.json validation
sep
echo "3. State.json Validation"
sep

# Check for -p2/-p3 bad keys
BAD_KEYS=$(docker exec "$CONTAINER" sh -c "grep -c '\-p[0-9]' $STATE_FILE" 2>/dev/null || true)
[ -n "$BAD_KEYS" ] || BAD_KEYS=0
if [ "$BAD_KEYS" -gt 0 ]; then
    fail "Found $BAD_KEYS lines with -p2/-p3 split keys in state.json (blocks uploader)"
    echo "   Fix: remove these keys from hours/objects sections"
    docker exec "$CONTAINER" sh -c "grep -n '\-p[0-9]' $STATE_FILE" | head -10
else
    ok "No -p2/-p3 split keys found"
fi

# Check hours count
HOURS_COUNT=$(docker exec "$CONTAINER" sh -c "grep -c '\"status\": \"sealed\"' $STATE_FILE" 2>/dev/null || true)
[ -n "$HOURS_COUNT" ] || HOURS_COUNT=0
echo "   Sealed hours: $HOURS_COUNT"

# Check prepared_hours (pending batches)
PREPARED=$(docker exec "$CONTAINER" sh -c "grep -A1 'prepared_hours' $STATE_FILE | grep -c '{' " 2>/dev/null || true)
[ -n "$PREPARED" ] || PREPARED=0
if [ "$PREPARED" -gt 0 ]; then
    warn "Found $PREPARED prepared (pending) hour(s) - may indicate stuck batch"
fi
echo

# 4. Audit log analysis
sep
echo "4. Audit Log Analysis"
sep

LAST_AUDIT=$(docker exec "$CONTAINER" sh -c "tail -1 $AUDIT_FILE" 2>/dev/null || echo "")
if [ -z "$LAST_AUDIT" ]; then
    fail "audit.jsonl is empty or missing"
else
    LAST_STATUS=$(echo "$LAST_AUDIT" | grep -o '"status":"[^"]*"' | head -1)
    LAST_HOUR=$(echo "$LAST_AUDIT" | grep -o '"hour":"[^"]*"' | head -1)
    LAST_TIME=$(echo "$LAST_AUDIT" | grep -o '"timestamp":"[^"]*"' | head -1)
    echo "   Last record: $LAST_TIME"
    echo "   Last hour:   $LAST_HOUR"
    echo "   Last status: $LAST_STATUS"

    # Check for failed records in last 20
    FAILED=$(docker exec "$CONTAINER" sh -c "tail -40 $AUDIT_FILE" 2>/dev/null | grep -c '"status":"failed"' || true)
    LATE=$(docker exec "$CONTAINER" sh -c "tail -40 $AUDIT_FILE" 2>/dev/null | grep -c '"status":"late_logs_retained"' || true)

    if [ "$FAILED" -gt 0 ]; then
        fail "Found $FAILED failed record(s) in recent audit:"
        docker exec "$CONTAINER" sh -c "tail -40 $AUDIT_FILE" | grep '"status":"failed"' | grep -o '"error":"[^"]*"' | head -3
    fi

    if [ "$LATE" -gt 0 ]; then
        warn "Found $LATE late_logs_retained record(s) - orphaned source logs exist"
        docker exec "$CONTAINER" sh -c "tail -40 $AUDIT_FILE" | grep '"status":"late_logs_retained"' | grep -o '"hour":"[^"]*"' | head -3
    fi

    if [ "$FAILED" -eq 0 ] && [ "$LATE" -eq 0 ]; then
        ok "Recent audit records look healthy"
    fi
fi
echo

# 5. Time gap analysis
sep
echo "5. Upload Timeline (last 10 successful uploads)"
sep
docker exec "$CONTAINER" sh -c "grep '\"status\":\"uploaded\"' $AUDIT_FILE" 2>/dev/null | tail -10 | while IFS= read -r line; do
    HOUR=$(echo "$line" | grep -o '"hour":"[^"]*"' | head -1)
    TIME=$(echo "$line" | grep -o '"timestamp":"[^"]*"' | head -1)
    SIZE=$(echo "$line" | grep -o '"compressed_bytes":[0-9]*' | head -1)
    echo "   $HOUR  $TIME  $SIZE"
done
echo

# 6. Source log count
sep
echo "6. Local Source Logs"
sep
# Try both possible paths
for LOGS_ROOT in "/CLIProxyAPI/auths/logs/keys" "/CLIProxyAPI/logs/auths/logs/keys"; do
    COUNT=$(docker exec "$CONTAINER" sh -c "find $LOGS_ROOT -name '*.log' -type f 2>/dev/null | wc -l" || echo "0")
    if [ "$COUNT" != "0" ]; then
        SIZE=$(docker exec "$CONTAINER" sh -c "du -sh $LOGS_ROOT 2>/dev/null | cut -f1" || echo "unknown")
        warn "Found $COUNT source logs ($SIZE) at $LOGS_ROOT"
        if [ "$COUNT" -gt 500 ]; then
            fail "Large backlog detected - uploader may be stuck or falling behind"
        fi
        break
    fi
done

# Also check the configured logs-root from log-uploader.yaml
CONFIGURED_ROOT=$(docker exec "$CONTAINER" sh -c "grep 'logs-root' /CLIProxyAPI/log-uploader.yaml 2>/dev/null | awk '{print \$2}'" || echo "")
if [ -n "$CONFIGURED_ROOT" ]; then
    # Resolve relative path against container workdir
    FULL_ROOT="/CLIProxyAPI/$CONFIGURED_ROOT"
    COUNT2=$(docker exec "$CONTAINER" sh -c "find $FULL_ROOT -name '*.log' -type f 2>/dev/null | wc -l" || echo "0")
    if [ "$COUNT2" != "0" ]; then
        SIZE2=$(docker exec "$CONTAINER" sh -c "du -sh $FULL_ROOT 2>/dev/null | cut -f1" || echo "unknown")
        warn "Configured logs-root $FULL_ROOT: $COUNT2 files ($SIZE2)"
    fi
fi
echo

# 7. TOS connectivity
sep
echo "7. TOS Connectivity Check"
sep
TOS_CHECK=$(docker exec "$CONTAINER" sh -c "wget -q --spider --timeout=5 https://llm-d1.tos-cn-beijing.volces.com 2>&1 && echo 'reachable' || echo 'unreachable'" 2>/dev/null || echo "check failed")
if echo "$TOS_CHECK" | grep -q "reachable"; then
    ok "TOS endpoint is reachable"
else
    warn "TOS endpoint check: $TOS_CHECK"
fi
echo

# 8. Summary
sep
echo "8. Summary & Recommendations"
sep
echo "   Current time: $(date '+%Y-%m-%d %H:%M:%S')"
echo "   Container:    running"

if [ "$ERROR_COUNT" -gt 0 ]; then
    echo "   Issues found:"
    echo "$RECENT_LOGS" | grep "\[error\]" | sed 's/^/     /'
fi

echo
echo "========================================"
echo "  Diagnostics Complete"
echo "========================================"
