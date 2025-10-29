## 1. Implementation
- [x] 1.1 添加注册测试（MiniMax）：当 `provider=claude` 且 `base_url=https://api.minimaxi.com/anthropic` 时，仅注册 `MiniMax-M2`，不注册任何 `claude-*`。
- [x] 1.2 添加路由测试：当请求 `MiniMax-M2` 时仅调用 `minimax` 执行器。
- [x] 1.3 `sdk/cliproxy/service.go::registerModelsForAuth`：实现 MiniMax 的检测与仅登记策略。
- [x] 1.4 启发式 Provider：`internal/util/provider.go` 保持 `minimax-*` → `minimax`。
- [x] 1.5 预注册执行器：`sdk/cliproxy/builder.go` 预注册 MiniMax 兼容执行器，缺凭据时报 `auth_not_found`。

## 2. Validation
- [x] 2.1 `go test ./sdk/cliproxy -run TestRegisterModelsForAuth_ClaudeBaseURL_MiniMaxAnthropic_RegistersOnlyM2 -v`
- [x] 2.2 `go test ./sdk/api/handlers -run TestRouting_MiniMax_UsesMinimaxExecutor -v`
- [x] 2.3 `go test ./sdk/api/handlers/openai -run TestOpenAIModels_ContainsZhipuAndMiniMaxModels -v`（包含 MiniMax-M2）
- [x] 2.4 `go test ./sdk/api/handlers/claude -run TestClaudeModels_ContainsZhipuAndMiniMaxModels -v`（包含 MiniMax-M2）
- [ ] 2.5 `openspec validate 2025-10-28-add-minimax-m2-capability --strict`（保存完整输出到 `openspec/changes/2025-10-28-add-minimax-m2-capability/validate.log`）
