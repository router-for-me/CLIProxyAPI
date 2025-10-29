# Proposal: Packycode ↔ Codex 运行时互斥开关

## Summary
- 当 `config.yaml` 中 `packycode.enabled=true` 时，禁用 `provider=codex` 的认证与路由；当 `false` 时恢复此前被该开关禁用的 Codex。
- 在服务启动与配置热重载时都应应用该开关，确保模型可见性与执行路径一致（对外 `provider=packycode` 暴露，内部复用 Codex 执行器）。

## Motivation
- 明确 Packycode 作为 OpenAI/GPT 兼容上游时，与 Codex 互斥使用，避免并行导致路由歧义与冲突。

## Scope
- 配置：`config.yaml` → `packycode.enabled` 布尔开关。
- 服务：启动与热重载回调应用禁用/恢复逻辑。
- 认证：对 `provider=codex` 的条目标记 `Attributes["toggle_by"]="packycode"` 以便可逆恢复。
- 模型：随开关变更同步注册/反注册，以反映 `/v1/models` 可见性。

## Acceptance Criteria
- 启用 Packycode 后，现有 Codex 认证被禁用且模型不可见；关闭 Packycode 后，仅恢复因该开关禁用的 Codex 认证与模型。
- 行为在启动与热重载生效；单元测试覆盖通过。

## Backward Compatibility
- 保持 `provider=packycode` 对外暴露、内部使用 Codex 执行器不变；仅在互斥维度新增约束。

## Why
通过将 Packycode 作为 OpenAI/GPT 兼容上游时的唯一入口，避免与 Codex 并行导致的路由歧义、模型重复暴露与运维困惑。提供显式的、可逆的互斥开关，使部署在不同阶段（灰度/回退）具备清晰可控的切换路径。

## References

## What Changes
- 新增运行时开关：依据 `packycode.enabled` 对 `provider=codex` 认证进行禁用/恢复；保留 `provider=packycode` 对外暴露并内部复用 Codex 执行器。
- 启动与热重载两处应用开关；模型注册表随之同步。
- 为可逆恢复引入 `toggle_by=packycode` 的认证属性标记。
- 实现入口：sdk/cliproxy/service.go:628, sdk/cliproxy/service.go:655, sdk/cliproxy/service.go:851
- 配置结构：internal/config/config.go:79
- 测试：sdk/cliproxy/service_packycode_codex_toggle_test.go:1
