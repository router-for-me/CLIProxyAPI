# Implementation Plan: Packycode 提供商支持（代理至 Claude Code）

**Branch**: `001-packycode-packycode-openai` | **Date**: 2025-10-20 | **Spec**: /home/adam/projects/CLIProxyAPI/specs/001-packycode-packycode-openai/spec.md
**Input**: Feature specification from `/specs/001-packycode-packycode-openai/spec.md`

## Summary

- 目标：在 `config.yaml` 增加独立 `packycode` 字段，完整承载 Packycode 配置，用于将 Packycode（OpenAI Codex CLI 兼容提供商）的服务无感转接到 Claude Code 兼容入口。
- 技术路径（概览）：
  - 配置面：新增 `packycode` 配置块（enabled、base-url、requires-openai-auth、wire-api、privacy、defaults、credentials）。
  - 路由面：当 `packycode.enabled=true` 时，将 Claude Code 兼容入口的调用按策略转发至 Packycode 上游，并保持响应格式兼容。
  - 模型注册：提供 CLI 标志 `--packycode` 主动注册 OpenAI/GPT 模型（如 `gpt-5`、`gpt-5-*`、`gpt-5-codex-*`、`codex-mini-latest`）至全局 ModelRegistry（provider=`codex`）；同时在服务启动与配置热重载回调中兜底注册，确保 `/v1/models` 可见。
  - 管理面：提供管理接口契约（GET/PUT/PATCH /v0/management/packycode）以便远程查看与更新配置。
  - 隐私与可观测性：默认不持久化用户内容，仅记录必要元信息；关键事件可审计。

## Technical Context

**Language/Version**: Go 1.24.0  
**Primary Dependencies**: gin（HTTP 服务）、yaml.v3（配置）、oauth2、logrus 等  
**Storage**: 无持久化新增要求（沿用现有配置与内存/文件机制）  
**Testing**: go test；需补充合约与集成测试（NEEDS CLARIFICATION：是否新增端到端脚本）  
**Target Platform**: Linux server（亦兼容容器运行）  
**Project Type**: 单一后端服务（Go）  
**Performance Goals**: 95% 请求 2 秒内开始呈现可见结果（来自规格）  
**Constraints**: 默认不持久化用户内容；上游不可用时可快速降级并输出清晰错误  
**Scale/Scope**: CLI 场景下的并发/吞吐量（NEEDS CLARIFICATION：是否需要多密钥轮询支持）

NEEDS CLARIFICATION 列表（将于 Phase 0 研究并定稿）：
- TC-1：`packycode.credentials.openai-api-key` 是否支持多密钥轮询？（默认：不支持，先行单密钥）
- TC-2：是否需要端到端（E2E）自动化脚本覆盖 Packycode 启用/禁用与回退场景？（默认：补充集成测试用例，不强制E2E脚本）
- TC-3：`wire-api` 固定为 "responses" 还是支持拓展枚举？（默认：固定 responses）

## Constitution Check

- 宪法文件路径：/home/adam/projects/CLIProxyAPI/.specify/memory/constitution.md 为空模板，未定义强制 Gate。  
- Gate 评估：无显式限制与冲突，暂视为 PASS。Phase 1 设计后复检仍 PASS。

## Project Structure

### Documentation (this feature)

```
specs/001-packycode-packycode-openai/
├── plan.md              # 本文件（/speckit.plan 输出）
├── research.md          # Phase 0 输出（本次生成）
├── data-model.md        # Phase 1 输出（本次生成）
├── quickstart.md        # Phase 1 输出（本次生成）
├── contracts/           # Phase 1 输出（本次生成）
└── tasks.md             # Phase 2 输出（由 /speckit.tasks 生成；本次不生成）
```

### Source Code（repository root 实际结构）

```
cmd/
  └── server/
      └── main.go
internal/
  └── ...
sdk/
  ├── api/handlers/
  │   ├── claude/
  │   └── openai/
  ├── access/
  ├── auth/
  ├── translator/
  └── config/
auths/
examples/
docs/
```

**Structure Decision**: 采用现有 Go 单服务结构；新增 Packycode 功能将以内聚的配置解析、请求路由和错误处理为主，不新增顶层项目或模块层级。

## Complexity Tracking

（当前无宪法违反项，无需登记。）

---
