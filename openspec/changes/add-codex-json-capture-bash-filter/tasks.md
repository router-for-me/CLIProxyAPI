## 1. Implementation
- [ ] 1.1 Feature flag 校验：确保受 `request-log` 门控（只读）
- [ ] 1.2 Provider/Model 断言：codex|packycode + gpt-5-codex-{low,medium,high}
- [ ] 1.3 过滤器实现：抽取 instructions、tools[bash|shell]、input[function_call bash|shell]
- [ ] 1.4 Gin Context 写入：API_JSON_CAPTURE* 与 API_PROVIDER/API_MODEL_ID
- [ ] 1.5 非流式日志拼接：在 API_REQUEST 段后追加 CAPTURE 分节

## 2. Validation
- [ ] 2.1 单测：命中条件时 Context 出现 API_JSON_CAPTURE
- [ ] 2.2 单测：非命中（provider 或 model 不符）不产生捕获
- [ ] 2.3 单测：非 bash/shell 工具与调用不被包含

## 3. Safety
- [ ] 3.1 不持久化原始 Body；仅输出过滤后的最小 JSON
- [ ] 3.2 继承现有 header 脱敏策略；不记录认证密钥
- [ ] 3.3 性能：仅在 RequestLog=true 时解析；避免额外分配

