#!/bin/bash
# 切换到脚本所在目录
cd "$(dirname "$0")" || exit

echo "正在编译 cliproxy 的最新版本..."
go build -ldflags "-X main.Version=$(git describe --tags --always 2>/dev/null || echo dev) -X main.Commit=$(git rev-parse --short HEAD 2>/dev/null || echo none) -X main.BuildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o cliproxy ./cmd/server

if [ $? -eq 0 ]; then
    echo "✅ 编译成功! 生成了可执行文件: ./cliproxy"
else
    echo "❌ 编译失败! 请检查上述报错信息。"
fi

echo "--------------------------------"
echo "按任意键关闭此窗口..."
read -n 1 -s
