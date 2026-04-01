#!/bin/bash
# CLIProxyAPI - 服务器构建部署脚本
#
# 用法:
#   ./server-build.sh   # 编译 + 部署 + 重启

set -e

log_info()  { echo "[INFO] $1"; }
log_error() { echo "[ERROR] $1"; }

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DEPLOY_DIR="/home/iec/deploy"
BUILD_TMP="/tmp/cliproxyapi-build-$$"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)

echo "========================================"
echo "  CLIProxyAPI 构建脚本"
echo "  服务器: $(hostname)"
echo "  版本: $TIMESTAMP"
echo "========================================"
echo ""

mkdir -p "$BUILD_TMP"
trap "rm -rf $BUILD_TMP" EXIT

# 1. 编译
log_info "1. 编译 CLIProxyAPI..."
cd "$SCRIPT_DIR"
CGO_ENABLED=0 go build -o "$BUILD_TMP/cliproxyapi" ./cmd/server/
log_info "   编译完成"

# 2. 部署二进制（带时间戳 + 软链）
log_info "2. 部署二进制..."
cp "$BUILD_TMP/cliproxyapi" "$DEPLOY_DIR/bin/cliproxyapi.$TIMESTAMP"
ln -sfn "cliproxyapi.$TIMESTAMP" "$DEPLOY_DIR/bin/cliproxyapi"
log_info "   ✓ cliproxyapi -> cliproxyapi.$TIMESTAMP"

# 3. 清理旧版本（保留最近 10 个）
log_info "3. 清理旧版本二进制（保留最近 10 个）..."
ls -t "$DEPLOY_DIR/bin/cliproxyapi."* 2>/dev/null | tail -n +11 | xargs -r rm -f
log_info "   ✓ 清理完成"

# 4. 重启服务
log_info "4. 重启 cliproxyapi 服务..."
sudo systemctl restart cliproxyapi
sleep 2

for i in {1..15}; do
    if curl -sf http://127.0.0.1:8317/health > /dev/null 2>&1; then
        log_info "   ✓ 健康检查通过"
        break
    fi
    if [ $i -eq 15 ]; then
        log_error "   ✗ 健康检查失败"
        sudo systemctl status cliproxyapi --no-pager
        exit 1
    fi
    sleep 2
done

echo ""
log_info "========================================"
log_info "  部署完成！当前版本: $TIMESTAMP"
log_info "========================================"
echo ""
echo "查看服务状态:  sudo systemctl status cliproxyapi"
echo "查看日志:      tail -f /home/iec/CLIProxyAPI/auths/logs/main.log"
echo "快速回滚:      ln -sfn cliproxyapi.<旧版本号> ~/deploy/bin/cliproxyapi && sudo systemctl restart cliproxyapi"
echo ""
