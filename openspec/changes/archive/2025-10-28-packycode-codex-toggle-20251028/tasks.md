# Tasks: Packycode ↔ Codex 互斥开关

1. 配置与验证
   - 保持 `packycode.enabled` 字段与现有校验语义：enabled=false 跳过，true 校验必填（base-url、credentials.openai-api-key 当 requires-openai-auth=true）。

2. 服务开关逻辑
   - 启动后调用 `enforceCodexToggle(cfg)`；在配置热重载回调中再次调用。
   - 规则：enabled=true → 禁用所有 `provider=codex`（标记 `toggle_by=packycode`）；enabled=false → 仅恢复上述标记的 codex 认证。

3. 模型可见性同步
   - 开关执行后调用 `registerModelsForAuth` 或 `ensurePackycodeModelsRegistered`/反注册以同步 `/v1/models` 可见性。

4. 测试
   - 单测：`TestEnforceCodexToggle_WithPackycode` 覆盖启用/禁用与可逆恢复。

5. 文档（可选）
   - README 与管理接口文档补充开关行为说明（如项目要求）。

6. 验证
   - 运行 `openspec validate packycode-codex-toggle-20251028 --strict` 并保存 `openspec show <id> --json --deltas-only` 作为证据。

