# python-agent-bridge Specification

## Purpose
定义 Python Agent Bridge（PAB）能力的范围与职责：专注于配置项（如 `claude-agent-sdk-for-python`）、诊断与可观测性要求、以及回退策略与本地验证约定（Query CLI）。路由与端点兼容性等流量转发语义归属 `python-agent-bridge-routing` 规范，避免重复定义。
## Requirements
### Requirement: Claude Agent SDK for Python config (key: claude-agent-sdk-for-python)
- Config MUST include (under key `claude-agent-sdk-for-python`):
  - enabled (bool, default true)
  - baseURL (string, default http://127.0.0.1:35331)
  - env map for exporting Zhipu credentials to PAB process when managed as sidecar (optional)

#### Scenario: Rollback
When Claude Agent SDK for Python is disabled (claude-agent-sdk-for-python.enabled=false)
Then provider="zhipu" MUST fallback to legacy Go ZhipuExecutor.

