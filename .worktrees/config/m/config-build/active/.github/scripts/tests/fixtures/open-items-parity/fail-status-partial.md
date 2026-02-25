# Open Items Validation

- Issue #258 `Support variant fallback for reasoning_effort in codex models`
  - Status: partial
  - This block also says implemented in free text, but status should govern.
  - implemented keyword should not override status mapping.

## Evidence
- `pkg/llmproxy/translator/codex/openai/chat-completions/codex_openai_request.go:56`
