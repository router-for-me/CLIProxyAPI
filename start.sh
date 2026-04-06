#!/bin/sh

# 如果没有 config.yaml，则从示例文件复制
if [ ! -f "config.yaml" ]; then
    echo "Creating config.yaml from config.example.yaml..."
    cp config.example.yaml config.yaml
fi

# 关键修复：将 Railway 分配的 $PORT 注入到 config.yaml 中
# CLIProxyAPI 默认监听 8317，我们需要将其改为 Railway 分配的端口
if [ -n "$PORT" ]; then
    echo "Railway detected PORT: $PORT. Updating config.yaml..."
    # 使用 sed 替换端口配置，假设 yaml 中有 port: 8317 这种格式
    sed -i "s/port: [0-9]*/port: $PORT/g" config.yaml
else
    echo "No PORT environment variable found, using default port in config.yaml"
fi

# 启动程序
echo "Starting CLIProxyAPI..."
exec ./CLIProxyAPI
