# Phase 0 Research — Packycode 提供商支持

## 目标
将 Packycode（兼容 OpenAI Codex CLI 的第三方提供商）通过 CLIProxyAPI 进行无感转接，保留 Claude Code 兼容入口与用户体验。

## 需澄清问题与决策（NEEDS CLARIFICATION → 决策）

1) TC-1 多密钥轮询
- 问题：`packycode.credentials.openai-api-key` 是否需要支持多密钥轮询？
- 决策（Decision）：不支持，先行单密钥，后续如有强需求再扩展为 `openai-api-keys`（数组）与轮询策略。
- 理由（Rationale）：当前目标以“直通 Packycode”与简化配置为主，降低首次集成复杂度。
- 备选（Alternatives）：
  - A. 立即支持多密钥与轮询（增加实现与文档复杂度）
  - B. 仅单密钥（所选）

2) TC-2 端到端（E2E）自动化
- 问题：是否需要强制 E2E 脚本覆盖 Packycode 启用/禁用与回退？
- 决策（Decision）：不强制；先补充合约测试与集成测试。E2E 可选。
- 理由（Rationale）：当前以功能正确性与管理接口契约为主，E2E 成本较高、收益相对次要。
- 备选（Alternatives）：
  - A. 必须 E2E（增加时长与维护成本）
  - B. 合约/集成优先（所选）

3) TC-3 wire-api 取值
- 问题：`wire-api` 固定为 "responses" 还是可扩展？
- 决策（Decision）：固定为 "responses"。
- 理由（Rationale）：与用户输入示例对齐；避免早期引入不必要的变体。
- 备选（Alternatives）：
  - A. 可扩展枚举（可能无明确消费方）
  - B. 固定 responses（所选）

## 其他研究与约束归纳

- 配置风格：遵循项目 `config.yaml` 的 kebab-case 习惯（如 requires-openai-auth、disable-response-storage）。
- 隐私默认：`privacy.disable-response-storage=true`，日志仅记录必要元信息（不含用户内容）。
- 优先级策略：当存在 Codex CLI 的 Packycode 配置与 `config.yaml` 的 `packycode` 字段时，优先使用 `config.yaml`；其后为环境变量；最后才参考 Codex CLI 配置。
- 错误处理：上游不可用时，输出清晰可读错误，允许快速停用（降级/切回）。

## 结论
- 所有 NEEDS CLARIFICATION 已决：TC-1 选单密钥；TC-2 选合约/集成优先；TC-3 固定 responses。
- 进入 Phase 1 设计与契约生成。
