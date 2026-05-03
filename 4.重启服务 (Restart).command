#!/bin/bash
# 切换到脚本所在目录
cd "$(dirname "$0")" || exit

echo "【第一步】正在检查并停止运行中的 cliproxy 进程..."
pkill cliproxy
if [ $? -eq 0 ]; then
    echo "✅ 成功停止旧的进程。"
else
    echo "⚠️ 没有找到正在后台运行的 cliproxy 进程，或者它已经被停止。"
fi
echo ""

echo "【第二步】正在编译 cliproxy 最新的代码..."
go build -ldflags "-X main.Version=$(git describe --tags --always 2>/dev/null || echo dev) -X main.Commit=$(git rev-parse --short HEAD 2>/dev/null || echo none) -X main.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o cliproxy ./cmd/server
if [ $? -eq 0 ]; then
    echo "✅ 编译成功! 生成了最新的可执行文件: ./cliproxy"
else
    echo "❌ 编译失败! 请检查报错信息。"
    echo "按任意键退出..."
    read -n 1 -s
    exit 1
fi
echo ""

echo "【第三步】正在后台启动新的 cliproxy 进程..."
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

nohup ./cliproxy -config ./config.yaml -no-browser > ~/.cli-proxy-api/logs/cliproxy-stdout.log 2>&1 &
PID=$!
echo "✅ cliproxy 新版本启动成功，进程ID为 $PID"
echo "📄 日志文件在 ~/.cli-proxy-api/logs/cliproxy-stdout.log"

echo "--------------------------------"
echo "🎉 重启完成！按任意键关闭此窗口..."
read -n 1 -s
