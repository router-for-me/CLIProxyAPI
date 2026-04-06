#!/bin/sh

echo "--- Starting CLIProxyAPI Startup Script ---"

# 如果没有 config.yaml，则从示例文件复制
if [ ! -f "config.yaml" ]; then
    echo "[INFO] Creating config.yaml from config.example.yaml..."
    cp config.example.yaml config.yaml
else
    echo "[INFO] config.yaml already exists."
fi

# 关键修复：将 Railway 分配的 $PORT 注入到 config.yaml 中
if [ -n "$PORT" ]; then
    echo "[INFO] Railway detected PORT: $PORT. Updating config.yaml..."
    # 更加精确的替换逻辑，确保只替换第 6 行左右的 server port
    sed -i "s/^port: [0-9]*/port: $PORT/g" config.yaml
    echo "[INFO] config.yaml updated with port $PORT."
else
    echo "[WARN] No PORT environment variable found, using default port in config.yaml"
fi

# 打印当前的端口配置以供调试
echo "[DEBUG] Current port setting in config.yaml:"
grep "^port:" config.yaml

# 确保 auth 目录存在（如果配置中需要）
mkdir -p auth

# 启动程序
echo "[INFO] Launching CLIProxyAPI binary..."
exec ./CLIProxyAPI
