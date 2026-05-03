#!/bin/bash
# 切换到脚本所在目录
cd "$(dirname "$0")" || exit

if [ ! -f "config.yaml" ]; then
    if [ -f "config.example.yaml" ]; then
        echo "未找到 config.yaml，正在从 config.example.yaml 复制..."
        cp config.example.yaml config.yaml
    else
        echo "❌ 错误: 未找到 config.yaml 也没有找到 config.example.yaml!"
        echo "按任意键退出..."
        read -n 1 -s
        exit 1
    fi
fi

# 确保日志目录存在
mkdir -p ~/.cli-proxy-api/logs

echo "正在后台启动 cliproxy..."
nohup ./cliproxy -config ./config.yaml -no-browser > ~/.cli-proxy-api/logs/cliproxy-stdout.log 2>&1 &

PID=$!
echo "✅ cliproxy 启动成功，进程ID为 $PID"
echo "📄 日志文件在 ~/.cli-proxy-api/logs/cliproxy-stdout.log"

echo "--------------------------------"
echo "按任意键关闭此窗口..."
read -n 1 -s
