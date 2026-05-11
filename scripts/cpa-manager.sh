#!/bin/bash

set -e

INSTALL_DIR="/opt/cpa-manager"
DATA_DIR="$INSTALL_DIR/data"
SCRIPT_NAME=$(basename "$0")

show_help() {
    echo "用法: bash $SCRIPT_NAME [命令]"
    echo ""
    echo "命令:"
    echo "  install    一键安装 CPA-Manager"
    echo "  fix        修复/重启 CPA-Manager"
    echo "  setup      配置 CLIProxyAPI 并联动启动 CPA-Manager"
    echo "  help       显示帮助信息"
    echo ""
    echo "示例:"
    echo "  bash $SCRIPT_NAME install"
    echo "  bash $SCRIPT_NAME fix"
    echo "  bash $SCRIPT_NAME setup"
}

cmd_install() {
    ARCH=$(dpkg --print-architecture)

    # 映射 amd64/arm64
    if [ "$ARCH" = "amd64" ]; then
        BIN_ARCH="amd64"
    elif [ "$ARCH" = "arm64" ]; then
        BIN_ARCH="arm64"
    else
        echo "不支持的架构: $ARCH"
        exit 1
    fi

    echo "=== 1. 安装依赖 ==="
    sudo apt update
    sudo apt install -y curl wget jq

    echo "=== 2. 开启 Swap（2C2G 建议开启）==="
    if ! swapon --show | grep -q '/swapfile'; then
        sudo fallocate -l 2G /swapfile
        sudo chmod 600 /swapfile
        sudo mkswap /swapfile
        sudo swapon /swapfile
        echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
    fi

    echo "=== 3. 创建目录 ==="
    sudo mkdir -p $INSTALL_DIR $DATA_DIR

    echo "=== 4. 获取最新版本并下载 ==="
    LATEST_URL=$(curl -s https://api.github.com/repos/seakee/CPA-Manager/releases/latest | grep "browser_download_url.*linux_${BIN_ARCH}.tar.gz" | cut -d '"' -f 4)

    if [ -z "$LATEST_URL" ]; then
        echo "获取下载链接失败"
        exit 1
    fi

    echo "下载: $LATEST_URL"
    cd /tmp
    wget -q --show-progress "$LATEST_URL" -O cpa-manager.tar.gz

    echo "=== 5. 解压安装 ==="
    tar -xzf cpa-manager.tar.gz -C $INSTALL_DIR --strip-components=1
    sudo chown -R root:root $INSTALL_DIR

    echo "=== 6. 创建 systemd 服务 ==="
    sudo tee /etc/systemd/system/cpa-manager.service > /dev/null <<'EOF'
[Unit]
Description=CPA-Manager
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/cpa-manager
ExecStart=/opt/cpa-manager/cpa-manager
Restart=always
RestartSec=5

# 2C2G 资源限制
LimitAS=512M
LimitRSS=256M

Environment="USAGE_DATA_DIR=/opt/cpa-manager/data"
Environment="HTTP_ADDR=0.0.0.0:18317"

[Install]
WantedBy=multi-user.target
EOF

    echo "=== 7. 启动服务 ==="
    sudo systemctl daemon-reload
    sudo systemctl enable cpa-manager
    sudo systemctl start cpa-manager

    echo "=== 8. 检查状态 ==="
    sleep 2
    sudo systemctl status cpa-manager --no-pager

    echo ""
    echo "=========================================="
    echo "部署完成！访问: http://$(curl -s ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}'):18317/management.html"
    echo "=========================================="
}

cmd_fix() {
    echo "=== 1. 停止当前 CPA-Manager 服务 ==="
    sudo systemctl stop cpa-manager 2>/dev/null || true

    echo "=== 2. 重写 systemd 服务配置 ==="
    sudo tee /etc/systemd/system/cpa-manager.service > /dev/null <<'EOF'
[Unit]
Description=CPA-Manager
After=network.target

[Service]
Type=simple
ExecStart=/opt/cpa-manager/cpa-manager
WorkingDirectory=/opt/cpa-manager
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

    echo "=== 3. 重新加载 systemd 并启动 ==="
    sudo systemctl daemon-reload
    sudo systemctl enable cpa-manager
    sudo systemctl start cpa-manager

    echo "=== 4. 等待服务启动 ==="
    sleep 3

    echo "=== 5. 检查 CPA-Manager 状态 ==="
    sudo systemctl status cpa-manager --no-pager

    echo ""
    echo "=== 6. 检查 CPA 上游服务 (8317端口) ==="
    ss -tlnp | grep 8317 || echo "⚠️ 8317 端口未监听，CPA 上游可能未启动"

    echo ""
    echo "=== 7. CPA-Manager 日志（最近20行）==="
    sudo journalctl -u cpa-manager -n 20 --no-pager || true

    echo ""
    echo "=========================================="
    CPA_IP=$(curl -s ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}')
    echo "访问地址: http://${CPA_IP}:18317/management.html"
    echo "=========================================="
}

cmd_setup() {
    echo "=== 1. 修改 CLIProxyAPI 配置 ==="
    cd /root/cliproxyapi

    # 生成随机密钥
    MGMT_KEY=$(openssl rand -base64 32)
    echo "Management Key: $MGMT_KEY"

    # 替换配置
    sed -i 's/secret-key: ""/secret-key: "'$MGMT_KEY'"/' config.yaml
    sed -i 's/allow-remote: false/allow-remote: true/' config.yaml

    # 确认修改
    grep -E "secret-key:|allow-remote:" config.yaml

    echo ""
    echo "=== 2. 重启 CLIProxyAPI ==="
    pkill cli-proxy-api 2>/dev/null || true
    sleep 1
    nohup ./cli-proxy-api > /var/log/cliproxyapi.log 2>&1 &

    sleep 2
    ss -tlnp | grep 8317

    echo ""
    echo "=== 3. 启动 CPA-Manager ==="
    sudo systemctl restart cpa-manager
    sleep 2
    sudo systemctl status cpa-manager --no-pager

    echo ""
    echo "=========================================="
    echo "部署完成！"
    echo ""
    echo "登录 CPA-Manager："
    echo "  地址: http://$(curl -s ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}'):18317/management.html"
    echo ""
    echo "  字段             值"
    echo "  ───────────────────────────────────────"
    echo "  CPA URL          http://127.0.0.1:8317"
    echo "  Management Key   $MGMT_KEY"
    echo ""
    echo "⚠️  请妥善保存 Management Key！"
    echo "=========================================="
}

# 主入口
case "${1:-}" in
    install)
        cmd_install
        ;;
    fix)
        cmd_fix
        ;;
    setup)
        cmd_setup
        ;;
    help|--help|-h)
        show_help
        ;;
    *)
        echo "未知命令: ${1:-}" >&2
        echo ""
        show_help
        exit 1
        ;;
esac
