# Task: Fix Thinking Mode for GPT-OSS

**Status**: [ ] Todo

## Context
GPT-OSS models currently get stuck in planning loops when thinking mode is enabled. The `antigravity_executor.go` has a logic block that specifically disables thinking config for `gpt-oss` models:

```go
		// TODO: Fix GPT-OSS thinking mode - model gets stuck in infinite planning loops
		// GPT-OSS models have issues with thinking mode - they repeatedly generate
		// the same plan without executing actions. Temporarily disable thinking.
		if strings.HasPrefix(modelName, "gpt-oss") {
			delete(genConfig, "thinkingConfig")
		}
```

## Sub-Tasks
- [ ] Remove the explicit suppression of thinking config for `gpt-oss` models in `internal/runtime/executor/antigravity_executor.go`.
- [ ] Modify `normalizeAntigravityThinking` to ensuring a safe minimum budget for GPT-OSS if needed, or rely on `util` normalization.
- [ ] Add a unit test or verification script to confirm `thinkingConfig` is preserved for GPT-OSS models in the payload generation.

## Verification
1. Create a unit test `internal/runtime/executor/antigravity_executor_test.go` (if not exists) or similar.
2. Test `geminiToAntigravity` function with a GPT-OSS model and thinking config.
3. Assert that `thinkingConfig` is present in the output JSON.
