#!/system/bin/sh
# CLIProxyAPI Magisk Module - Service Script
# Magisk 在 late_start 服务阶段执行此脚本
# 支持手动服务管理命令: start|stop|restart|status

MODDIR="${0%/*}"
CLI_PROXY="$MODDIR/cli-proxy-api"
PID_FILE="$MODDIR/cliproxyapi.pid"
LOG_FILE="$MODDIR/logs/service.log"

mkdir -p "$MODDIR/auths"
mkdir -p "$MODDIR/logs"
mkdir -p "$MODDIR/config_backup"

get_pid() {
    if [ -f "$PID_FILE" ]; then
        cat "$PID_FILE"
    fi
}

is_running() {
    local pid=$(get_pid)
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        return 0
    fi
    return 1
}

do_start() {
    if is_running; then
        echo "[CLIProxyAPI] Service is already running (PID: $(get_pid))"
        return 0
    fi

    if [ ! -f "$CLI_PROXY" ]; then
        echo "[CLIProxyAPI] Error: cli-proxy-api not found"
        return 1
    fi

    chmod 755 "$CLI_PROXY"
    cd "$MODDIR"
    export HOME="$MODDIR"
    export CLI_PROXY_API_NO_BROWSER="true"

    "$CLI_PROXY" >> "$LOG_FILE" 2>&1 &
    local pid=$!
    echo "$pid" > "$PID_FILE"

    sleep 1
    if is_running; then
        echo "[CLIProxyAPI] Service started (PID: $pid)"
    else
        echo "[CLIProxyAPI] Service failed to start"
        rm -f "$PID_FILE"
        return 1
    fi
}

do_stop() {
    local pid=$(get_pid)
    if [ -z "$pid" ]; then
        echo "[CLIProxyAPI] Service is not running"
        return 0
    fi

    if kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null
        sleep 1
        if kill -0 "$pid" 2>/dev/null; then
            kill -9 "$pid" 2>/dev/null
            sleep 1
        fi
    fi

    rm -f "$PID_FILE"
    echo "[CLIProxyAPI] Service stopped"
}

do_status() {
    if is_running; then
        echo "[CLIProxyAPI] Service is running (PID: $(get_pid))"
        return 0
    else
        echo "[CLIProxyAPI] Service is not running"
        return 1
    fi
}

CMD="$1"

case "$CMD" in
    start)
        while [ "$(getprop sys.boot_completed)" != "1" ]; do
            sleep 1
        done
        sleep 3
        do_start
        ;;
    stop)
        do_stop
        ;;
    restart)
        do_stop
        sleep 1
        do_start
        ;;
    status)
        do_status
        ;;
    *)
        if [ -z "$CMD" ]; then
            while [ "$(getprop sys.boot_completed)" != "1" ]; do
                sleep 1
            done
            sleep 3
            do_start
        else
            echo "Usage: $0 {start|stop|restart|status}"
            exit 1
        fi
        ;;
esac
