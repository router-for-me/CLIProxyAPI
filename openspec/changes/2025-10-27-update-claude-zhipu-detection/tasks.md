## 1. Implementation
- [x] 1.1 添加测试（Zhipu）：当 `provider=claude` 且 `base_url=https://open.bigmodel.cn/api/anthropic` 时，仅注册 `glm-4.6`，不注册任何 `claude-*`。
- [x] 1.2 添加测试（MiniMax）：当 `provider=claude` 且 `base_url=https://api.minimaxi.com/anthropic` 时，仅注册 `MiniMax-M2`，不注册任何 `claude-*`，并将 Provider 识别为 `MiniMax`。
- [x] 1.3 修改 `sdk/cliproxy/service.go::registerModelsForAuth`，实现上述 Zhipu 与 MiniMax 的检测与注册策略。
- [x] 1.4 添加 provider 标识测试：验证上述两种侦测路径下 `util.GetProviderName` 分别返回 `zhipu` 与 `minimax`。
- [x] 1.5 更新执行器映射：对于任何 Claude API（`provider=claude`）下的官方/智谱/MiniMax 兼容端点，默认使用 Claude 执行器（非回退）。涉及 `ensureExecutorsForAuth` 与 `internal/util/provider.go`。

## 2. Validation
- [x] 2.1 运行最小相关测试（Zhipu）：`go test ./sdk/cliproxy -run TestRegisterModelsForAuth_ClaudeBaseURL_ZhipuAnthropic_RegistersOnlyGLM46 -v`
- [x] 2.2 运行最小相关测试（MiniMax）：`go test ./sdk/cliproxy -run TestRegisterModelsForAuth_ClaudeBaseURL_MiniMaxAnthropic_RegistersOnlyM2 -v`
- [x] 2.3 运行最小相关测试（Provider 标识）：
      - `go test ./sdk/cliproxy -run TestRegisterModelsForAuth_ClaudeBaseURL_ZhipuAnthropic_ProviderZhipu -v`
      - `go test ./sdk/cliproxy -run TestRegisterModelsForAuth_ClaudeBaseURL_MiniMaxAnthropic_ProviderMinimax -v`
- [x] 2.4 执行器映射验证：增加用例确保 minimax/zhipu 模型在仅注册 Claude 执行器时可正常调用（不依赖“回退”路径）。

## 3. Rollback
- [ ] 3.1 如需回滚，移除检测分支，恢复 `provider=claude` 固定注册 `registry.GetClaudeModels()`。
- [ ] 3.2 如已引入 MiniMax 模型定义，按需回滚删除或在注册时屏蔽引用。
- [ ] 3.3 如需回滚执行器策略，恢复到“优先兼容端点 provider，无法匹配时回退 Claude 执行器”的旧逻辑。
