#!/bin/sh

echo "--- Starting CLIProxyAPI Startup Script ---"

# 如果没有 config.yaml，则从示例文件复制
if [ ! -f "config.yaml" ]; then
    echo "[INFO] Creating config.yaml from config.example.yaml..."
    cp config.example.yaml config.yaml
else
    echo "[INFO] config.yaml already exists."
fi

# 1. 关键修复：将 Railway 分配的 $PORT 注入到 config.yaml 中
if [ -n "$PORT" ]; then
    echo "[INFO] Railway detected PORT: $PORT. Updating config.yaml..."
    sed -i "s/^port: [0-9]*/port: $PORT/g" config.yaml
    echo "[INFO] config.yaml updated with port $PORT."
else
    echo "[WARN] No PORT environment variable found, using default port in config.yaml"
fi

# 2. 安全修复：从环境变量读取管理密钥并注入到 config.yaml
# 这样您的密钥就不会暴露在 GitHub 仓库中
if [ -n "$MANAGEMENT_PASSWORD" ]; then
    echo "[INFO] MANAGEMENT_PASSWORD detected. Updating config.yaml..."
    # 假设配置文件中有 password: "xxx" 这种格式
    # 如果没有，我们直接在文件末尾追加（CLIProxyAPI 通常支持这种方式）
    if grep -q "^password:" config.yaml; then
        sed -i "s/^password: .*/password: \"$MANAGEMENT_PASSWORD\"/g" config.yaml
    else
        echo "password: \"$MANAGEMENT_PASSWORD\"" >> config.yaml
    fi
    echo "[INFO] Management password updated from environment variable."
else
    echo "[WARN] No MANAGEMENT_PASSWORD environment variable found."
fi

# 3. 启用管理接口（如果尚未启用）
if ! grep -q "^management-enabled: true" config.yaml; then
    echo "management-enabled: true" >> config.yaml
    echo "[INFO] Management interface enabled."
fi

# 打印当前的端口配置以供调试（不打印密码）
echo "[DEBUG] Current port setting in config.yaml:"
grep "^port:" config.yaml

# 确保 auth 目录存在
mkdir -p auth

# 启动程序
echo "[INFO] Launching CLIProxyAPI binary..."
exec ./CLIProxyAPI
