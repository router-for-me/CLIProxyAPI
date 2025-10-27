# Change Proposal: remove-python-bridge-and-claude-agent-sdk

## Summary
彻底移除 Python Bridge（Zhipu Python Agent Bridge）与 Claude Agent SDK 相关的所有规范与实现路径。目标是将 provider="zhipu" 的路由统一为无需 Python 侧桥接的实现：
- 移除 `claude-agent-sdk-for-python` 配置键与全部语义；
- 删除或归档与 Python Bridge 相关的规范能力与引用；
- 调整 zhipu 执行路径为纯 Go 执行器或兼容直连路径，不再依赖 Bridge。

## Motivation
- 简化系统架构，去除跨进程/语言桥接的复杂度与运维成本；
- 规避 Bridge 健康检查、SSE 转发与 URL 选择等边缘问题；
- 降低部署耦合度，便于配置与测试。

## Scope
- 规范：`python-agent-bridge-routing` 与历史变更中涉及 Bridge 的能力要求标记为移除；
- 配置：移除 `claude-agent-sdk-for-python` 配置项及其默认值、环境变量引用；
- 代码：删除 Bridge 启停、URL 选择与健康检查逻辑；Zhipu 执行器统一直连上游；
- 测试：删除或重写依赖 Bridge 的测试；
- 文档：删除/更新 README、示例配置与注释。

## Out of Scope
- 与 zhipu 无关的其他 provider 不做无关调整。

## Risks
- 依赖 Bridge 的现有部署将无法继续工作，需要迁移到直连配置（如 `zhipu-api-key` 或 `claude-api-key` 指向官方兼容端点）。
- 删除配置键属于破坏性变更，需要清晰的迁移指南。

## Rollback Strategy
- 若回滚，恢复 `claude-agent-sdk-for-python` 键与 bridge 执行路径；保留本次删除提交可逆。

