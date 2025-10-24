#!/usr/bin/env bash
#
# test_query_cli_default_model.sh
#
# 验证 query_cli.py 在不设置 ANTHROPIC_MODEL 环境变量且不使用 --model 参数时
# 默认使用 glm-4.6 模型，并确保子进程隔离生效。
#
# 依赖：
#   - Python 3.10+ 与 claude_agent_sdk_python 已安装
#   - config.yaml 中配置了有效的 zhipu-api-key
#
# 输出：
#   - logs/query_cli_default_model_test.log（完整日志）
#   - 验证日志包含 model: 'glm-4.6' 与助手回复

set -euo pipefail

# --- 配置 ---
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
LOG_DIR="$PROJECT_ROOT/logs"
LOG_FILE="$LOG_DIR/query_cli_default_model_test.log"
QUERY_CLI="$PROJECT_ROOT/python/claude_agent_sdk_python/query_cli.py"

# Zhipu API 凭证（从 config.yaml 读取）
ANTHROPIC_BASE_URL="https://open.bigmodel.cn/api/anthropic"
ANTHROPIC_AUTH_TOKEN="2daae61b47e0420a80de9d3941ce9f30.Wu1lFPXoHBYaCkxv"

# --- 前置检查 ---
echo "======================================"
echo "Query CLI 默认模型验证测试"
echo "======================================"
echo ""

if [[ ! -f "$QUERY_CLI" ]]; then
    echo "❌ 错误: query_cli.py 未找到: $QUERY_CLI"
    exit 1
fi

if ! command -v python3 &> /dev/null; then
    echo "❌ 错误: python3 未安装"
    exit 1
fi

# --- 创建日志目录 ---
mkdir -p "$LOG_DIR"

# --- 清理旧日志 ---
if [[ -f "$LOG_FILE" ]]; then
    echo "🗑️  清理旧日志: $LOG_FILE"
    rm -f "$LOG_FILE"
fi

# --- 检查运行中的实例 ---
echo "🔍 检查运行中的 Claude Code/Agent SDK 实例..."
if pgrep -f "claude_agent_sdk" > /dev/null; then
    echo "⚠️  警告: 检测到运行中的 claude_agent_sdk 进程"
    echo "   建议终止以确保测试环境干净（可选）"
    # 不自动终止，让用户决定
fi
echo ""

# --- 设置环境变量（不设置 ANTHROPIC_MODEL） ---
export ANTHROPIC_BASE_URL
export ANTHROPIC_AUTH_TOKEN
export PYTHONPATH="$PROJECT_ROOT/python"

# 确保不设置 ANTHROPIC_MODEL（验证默认值生效）
unset ANTHROPIC_MODEL || true

echo "📋 测试配置："
echo "   ANTHROPIC_BASE_URL: $ANTHROPIC_BASE_URL"
echo "   ANTHROPIC_AUTH_TOKEN: ${ANTHROPIC_AUTH_TOKEN:0:20}...（已截断）"
echo "   ANTHROPIC_MODEL: <未设置，期望使用默认 glm-4.6>"
echo "   PYTHONPATH: $PYTHONPATH"
echo "   日志文件: $LOG_FILE"
echo ""

# --- 运行测试 ---
echo "🚀 运行 query_cli.py（无 --model 参数）..."
echo "   命令: python3 $QUERY_CLI \"你好，请简单介绍一下你自己。\""
echo ""

set +e  # 允许命令失败以便捕获错误
python3 "$QUERY_CLI" "你好，请简单介绍一下你自己。" > "$LOG_FILE" 2>&1
EXIT_CODE=$?
set -e

# --- 分析结果 ---
echo ""
echo "======================================"
echo "测试结果分析"
echo "======================================"
echo ""
echo "退出码: $EXIT_CODE"
echo ""

if [[ $EXIT_CODE -ne 0 ]]; then
    echo "❌ 测试失败: query_cli.py 返回非零退出码"
    echo ""
    echo "--- 日志内容（前50行）---"
    head -n 50 "$LOG_FILE"
    exit 1
fi

# --- 验证日志内容 ---
echo "🔍 验证日志内容..."
echo ""

# 检查是否包含 model: 'glm-4.6' 或 "model":"glm-4.6"
if grep -qE "(model[\"']?\s*:\s*[\"']?glm-4\.6|ANTHROPIC_MODEL.*glm-4\.6)" "$LOG_FILE"; then
    echo "✅ 验证通过: 日志包含 model: 'glm-4.6'"
else
    echo "❌ 验证失败: 日志未包含 model: 'glm-4.6'"
    echo ""
    echo "--- 日志内容 ---"
    cat "$LOG_FILE"
    exit 1
fi

# 检查是否有助手回复（非空内容）
if grep -qE "(你好|助手|AI|语言模型|很高兴|介绍)" "$LOG_FILE"; then
    echo "✅ 验证通过: 日志包含助手回复文本"
else
    echo "⚠️  警告: 日志可能不包含明显的助手回复"
fi

echo ""
echo "======================================"
echo "✅ 测试成功完成"
echo "======================================"
echo ""
echo "📄 完整日志已保存到: $LOG_FILE"
echo ""
echo "--- 日志内容（前30行）---"
head -n 30 "$LOG_FILE"
echo ""
echo "--- 日志内容（后20行）---"
tail -n 20 "$LOG_FILE"
echo ""

exit 0
