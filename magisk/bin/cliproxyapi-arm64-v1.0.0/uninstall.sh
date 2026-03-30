#!/system/bin/sh
# CLIProxyAPI Magisk Module - Uninstall Script
# This script runs when the module is uninstalled

MODDIR="${0%/*}"

PID_FILE="$MODDIR/cliproxyapi.pid"

if [ -f "$PID_FILE" ]; then
    PID=$(cat "$PID_FILE")
    if kill -0 "$PID" 2>/dev/null; then
        kill "$PID" 2>/dev/null
        sleep 1
        if kill -0 "$PID" 2>/dev/null; then
            kill -9 "$PID" 2>/dev/null
        fi
    fi
    rm -f "$PID_FILE"
fi

echo "[CLIProxyAPI] Module uninstalled, service stopped"
