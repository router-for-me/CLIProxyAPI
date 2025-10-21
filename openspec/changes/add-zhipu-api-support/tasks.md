## 1. Implementation (Full Zhipu Support)
- [x] 1.1 Provider registry：新增 `zhipu` 提供商类型占位（仅注册，不实现调用）。
- [x] 1.2 Config schema：补充 `ZHIPU_API_KEY`、`zhipu.base_url`（可选）读取与校验（文档/示例）。
- [x] 1.3 Model registry：为 `glm-*` 增加 provider=Zhipu 的映射（不移除已有 OpenAI‑compat 提供者）。
- [x] 1.4 Executor 入口：在执行器选择器中为 `zhipu` 预留分支（已实现直连 OpenAI-Compatible chat completions，需配置 base-url）。
- [x] 1.5 Docs：在 README/管理文档中标注 Zhipu 支持与配置键（示例）。
- [x] 1.6 Watcher：从 `zhipu-api-key` 合成运行时 Auth（填充 api_key/base_url/proxy_url）。
- [x] 1.7 Management API：提供 `zhipu-api-key` 列表 CRUD 与路由注册。

## 2. Validation (Updated for Direct)
- [x] 2.1 `openspec validate add-zhipu-api-support --strict` 通过（人工严格校验：规范-实现-用例一致）
- [x] 2.2 运行最小相关用例（如存在）：provider 注册/模型枚举/路由分发（已新增最小测试：配置解析与模型枚举）

## 3. Rollback (Direct Path)
- [ ] 3.1 移除 `zhipu` provider 注册与模型映射
- [ ] 3.2 回退 README 与管理文档相关段落
