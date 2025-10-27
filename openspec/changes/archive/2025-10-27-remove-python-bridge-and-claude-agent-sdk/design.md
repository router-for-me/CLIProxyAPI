# Design: remove-python-bridge-and-claude-agent-sdk

## Overview
将 zhipu 的执行路径统一为单一的直连实现（OpenAI 兼容），完全移除 Python Bridge 与 Claude Agent SDK 依赖。

## Key Changes
- 移除 Bridge 相关代码与配置：
  - 删除 Bridge URL 解析、健康检查与进程管理逻辑；
  - 删除 `claude-agent-sdk-for-python` 配置块与默认值；
  - 移除所有 `CLAUDE_AGENT_SDK_*` 环境变量引用；
- ZhipuExecutor：
  - 仅保留直连上游执行（非流/流式）与服务端清洗、分片策略；
- Service 生命周期：
  - 移除 Bridge 启动/停止调用；

## Compatibility
- 破坏性变更：依赖 Bridge 的部署需迁移至直连配置（`zhipu-api-key` 或兼容端点）。

