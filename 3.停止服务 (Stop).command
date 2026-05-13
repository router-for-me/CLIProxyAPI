#!/bin/bash
echo "正在检查并停止运行中的 cliproxy 进程..."

pkill cliproxy

if [ $? -eq 0 ]; then
    echo "✅ 成功停止所有后台的 cliproxy 进程。"
else
    echo "⚠️ 没有找到正在后台运行的 cliproxy 进程。"
fi

echo "--------------------------------"
echo "按任意键关闭此窗口..."
read -n 1 -s
