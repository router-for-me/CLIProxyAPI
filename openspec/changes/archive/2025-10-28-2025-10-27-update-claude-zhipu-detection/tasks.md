## 1. Implementation
- [x] 1.1 添加测试（Zhipu）：当 `provider=claude` 且 `base_url=https://open.bigmodel.cn/api/anthropic` 时，仅注册 `glm-4.6`，不注册任何 `claude-*`。
- [x] 1.2 添加测试（MiniMax）：当 `provider=claude` 且 `base_url=https://api.minimaxi.com/anthropic` 时，仅注册 `MiniMax-M2`，不注册任何 `claude-*`，并将 Provider 识别为 `MiniMax`。
- [x] 1.3 修改 `sdk/cliproxy/service.go::registerModelsForAuth`（阶段一临时）：实现 Zhipu 与 MiniMax 的检测与注册策略。
- [x] 1.4 添加 provider 标识测试：验证上述两种侦测路径下 `util.GetProviderName` 分别返回 `zhipu` 与 `minimax`。
- [x] 1.5 架构重构：引入 AnthropicCompatExecutor 基类，抽离 Claude 执行器通用逻辑（已落地，minimax/zhipu 执行器改为基类实例化）。
- [x] 1.6 新增执行器：GlmAnthropicExecutor（identifier=`zhipu`）与 MiniMaxAnthropicExecutor（identifier=`minimax`）。
- [x] 1.7 更新注册与路由：
      - `registerModelsForAuth` 将 Zhipu/MiniMax 模型注册回归各自 provider（`zhipu`/`minimax`）。
      - `ensureExecutorsForAuth` 对 `provider=claude` + 兼容端点合成运行时 auth（provider 改写为 `zhipu`/`minimax`），并注册对应执行器。
- [x] 1.8 更新 provider 选择：`internal/util/provider.go` 严格按前缀映射（`glm-*`→`zhipu`，`minimax-*`→`minimax`，`claude-*`→`claude`），移除“默认追加 claude”。

## 2. Validation
- [x] 2.1 运行最小相关测试（Zhipu）：`go test ./sdk/cliproxy -run TestRegisterModelsForAuth_ClaudeBaseURL_ZhipuAnthropic_RegistersOnlyGLM46 -v`
- [x] 2.2 运行最小相关测试（MiniMax）：`go test ./sdk/cliproxy -run TestRegisterModelsForAuth_ClaudeBaseURL_MiniMaxAnthropic_RegistersOnlyM2 -v`
- [x] 2.3 运行最小相关测试（Provider 标识）：
      - `go test ./sdk/cliproxy -run TestRegisterModelsForAuth_ClaudeBaseURL_ZhipuAnthropic_ProviderZhipu -v`
      - `go test ./sdk/cliproxy -run TestRegisterModelsForAuth_ClaudeBaseURL_MiniMaxAnthropic_ProviderMinimax -v`
- [x] 2.4 执行器映射验证（重构后）：
      - Zhipu：`glm-4.6` 仅通过 `provider=zhipu` 执行器调用成功；
      - MiniMax：`MiniMax-M2` 仅通过 `provider=minimax` 执行器调用成功；
      - Claude 官方：`claude-*` 仅通过 `provider=claude` 执行器调用成功。

## 3. Rollback
- [ ] 3.1 如需回滚，移除检测分支，恢复 `provider=claude` 固定注册 `registry.GetClaudeModels()`。
- [ ] 3.2 如已引入 MiniMax 模型定义，按需回滚删除或在注册时屏蔽引用。
- [ ] 3.3 如需回滚执行器策略，临时恢复到“兼容端点模型注册到 claude + provider 决策追加 claude”的逻辑（不推荐）。
