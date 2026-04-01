#!/bin/bash

# ============================================================
# CLIProxyAPI 管理脚本
#
# 用法:
#   ./start.sh              # 编译并启动（默认行为）
#   ./start.sh start        # 同上
#   ./start.sh build        # 仅编译
#   ./start.sh stop         # 停止服务
#   ./start.sh restart      # 先停止再编译并启动
#   ./start.sh status       # 查看服务运行状态
#   ./start.sh log          # 实时查看日志
#   ./start.sh help         # 显示帮助信息
#
# 环境变量（可选）:
#   PORT=18318              监听端口，默认 18318
# ============================================================

set -euo pipefail

# ---- 可配置项 ----
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY_NAME="cli-proxy-new"
CONFIG_FILE="config.yaml"
PORT="${PORT:-18318}"

# ---- 颜色输出 ----
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${CYAN}[INFO]${NC} $*"; }
ok()    { echo -e "${GREEN}[OK]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
fail()  { echo -e "${RED}[FAIL]${NC} $*"; exit 1; }

# ============================================================
# 编译：自动检测 Go 环境，编译最新代码
# ============================================================
do_build() {
    info "开始编译 $BINARY_NAME ..."

    # 检测 go 命令（支持多种安装路径）
    local go_bin
    go_bin="$(command -v go 2>/dev/null)" || true
    if [ -z "$go_bin" ]; then
        # 尝试常见路径
        for candidate in \
            /opt/homebrew/Caskroom/miniforge/base/go/bin/darwin_arm64/go \
            /usr/local/go/bin/go \
            "$HOME/go/bin/go"; do
            if [ -x "$candidate" ]; then
                go_bin="$candidate"
                break
            fi
        done
    fi
    [ -n "$go_bin" ] || fail "未找到 go 命令，请确认 Go 已安装"
    info "使用 Go: $go_bin ($("$go_bin" version 2>/dev/null || true))"

    # 执行编译
    cd "$SCRIPT_DIR"
    if "$go_bin" build -o "$BINARY_NAME" ./cmd/server; then
        ok "编译成功: $SCRIPT_DIR/$BINARY_NAME"
    else
        fail "编译失败，请检查代码"
    fi
}

# ============================================================
# 获取占用端口的 PID
# ============================================================
get_pid() {
    lsof -ti:"$PORT" 2>/dev/null || true
}

# ============================================================
# 停止服务：先 SIGTERM 优雅关闭，2秒后 SIGKILL 强杀
# ============================================================
do_stop() {
    local pid
    pid="$(get_pid)"
    if [ -z "$pid" ]; then
        info "端口 $PORT 无进程运行"
        return 0
    fi

    info "停止进程 (PID: $pid) ..."
    kill -15 "$pid" 2>/dev/null || true

    # 等待进程退出，最多等 5 秒
    local i=0
    while [ $i -lt 5 ]; do
        [ -z "$(get_pid)" ] && { ok "进程已停止"; return 0; }
        sleep 1
        i=$((i + 1))
    done

    # 超时后强杀
    warn "优雅关闭超时，强制终止 ..."
    kill -9 "$pid" 2>/dev/null || true
    sleep 1
    [ -z "$(get_pid)" ] && { ok "进程已终止"; return 0; }
    fail "无法停止进程，请手动处理: kill -9 $pid"
}

# ============================================================
# 启动服务：编译 → 停旧进程 → 启动 → 健康检查
# ============================================================
do_start() {
    # 1. 自动编译最新代码
    do_build

    # 2. 检查配置文件
    if [ ! -f "$SCRIPT_DIR/$CONFIG_FILE" ]; then
        fail "配置文件 $CONFIG_FILE 不存在，请从 config.example.yaml 创建"
    fi

    # 3. 创建日志目录
    mkdir -p "$SCRIPT_DIR/logs"

    # 4. 停止旧进程（如果端口被占用）
    local old_pid
    old_pid="$(get_pid)"
    if [ -n "$old_pid" ]; then
        warn "端口 $PORT 已被占用，先停止旧进程"
        do_stop
    fi

    # 5. 启动服务（后台运行，日志写入 app.log）
    info "启动服务 (端口: $PORT) ..."
    cd "$SCRIPT_DIR"
    nohup ./"$BINARY_NAME" -config "$CONFIG_FILE" > logs/app.log 2>&1 &
    local pid=$!

    # 6. 健康检查：等待最多 10 秒
    info "等待服务就绪 ..."
    local i=0
    while [ $i -lt 10 ]; do
        if [ -n "$(get_pid)" ]; then
            # 端口已被监听，确认启动成功
            ok "服务启动成功!"
            echo ""
            echo "  PID:        $pid"
            echo "  端口:       $PORT"
            echo "  API:        http://localhost:$PORT"
            echo "  管理 API:   http://localhost:$PORT/v0/management"
            echo "  日志:       tail -f $SCRIPT_DIR/logs/app.log"
            echo ""
            return 0
        fi
        sleep 1
        i=$((i + 1))
    done

    # 启动失败，打印最后几行日志辅助排查
    fail "服务启动失败，最近日志:\n$(tail -20 "$SCRIPT_DIR/logs/app.log" 2>/dev/null || echo '日志为空')"
}

# ============================================================
# 查看服务状态
# ============================================================
do_status() {
    local pid
    pid="$(get_pid)"
    if [ -n "$pid" ]; then
        ok "服务运行中 (PID: $pid, 端口: $PORT)"
    else
        info "服务未运行 (端口 $PORT 无监听)"
    fi
}

# ============================================================
# 实时查看日志
# ============================================================
do_log() {
    local logfile="$SCRIPT_DIR/logs/app.log"
    if [ ! -f "$logfile" ]; then
        fail "日志文件不存在: $logfile"
    fi
    info "实时日志 (Ctrl+C 退出):"
    tail -f "$logfile"
}

# ============================================================
# 显示帮助信息
# ============================================================
do_help() {
    echo ""
    echo "CLIProxyAPI 管理脚本"
    echo ""
    echo "用法: $0 [命令]"
    echo ""
    echo "命令:"
    echo "  (无参数)    编译并启动服务"
    echo "  start       编译并启动服务"
    echo "  build       仅编译，不启动"
    echo "  stop        停止服务"
    echo "  restart     停止 → 编译 → 启动"
    echo "  status      查看服务运行状态"
    echo "  log         实时查看日志"
    echo "  help        显示此帮助信息"
    echo ""
    echo "环境变量:"
    echo "  PORT=18318  指定监听端口（默认 18318）"
    echo ""
}

# ============================================================
# 主入口：解析命令行参数
# ============================================================
case "${1:-start}" in
    start)   do_start   ;;
    build)   do_build   ;;
    stop)    do_stop    ;;
    restart) do_stop; do_start ;;
    status)  do_status  ;;
    log)     do_log     ;;
    help|-h|--help) do_help ;;
    *)       fail "未知命令: $1\n运行 '$0 help' 查看可用命令" ;;
esac
