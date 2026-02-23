# Issue Wave CPB-0781-0830 Lane F Report

- Lane: `F (cliproxyapi-plusplus)`
- Window: `CPB-0821` to `CPB-0830`
- Scope: triage-only report (no code edits)

## Triage Items

### CPB-0821
- Title: `gemini oauth in droid cli: unknown provider`
- Candidate paths:
  - `pkg/llmproxy/translator`
  - `pkg/llmproxy/executor`
  - `docs/troubleshooting.md`
- Verification command: `rg -n "CPB-0821|CPB-0821" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0822
- Title: `认证文件管理 主动触发同步`
- Candidate paths:
  - `pkg/llmproxy/translator`
  - `pkg/llmproxy/executor`
  - `pkg/llmproxy/runtime/executor`
- Verification command: `rg -n "CPB-0822|CPB-0822" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0823
- Title: `Kimi K2 Thinking`
- Candidate paths:
  - `docs/operations`
  - `docs/troubleshooting.md`
  - `pkg/llmproxy/api/handlers/management`
- Verification command: `rg -n "CPB-0823|CPB-0823" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0824
- Title: `nano banana 水印的能解决？我使用CLIProxyAPI 6.1`
- Candidate paths:
  - `pkg/llmproxy/translator`
  - `pkg/llmproxy/executor`
  - `pkg/llmproxy/runtime/executor`
- Verification command: `rg -n "CPB-0824|CPB-0824" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0825
- Title: `ai studio 不能用`
- Candidate paths:
  - `pkg/llmproxy/translator`
  - `pkg/llmproxy/executor`
  - `docs/troubleshooting.md`
- Verification command: `rg -n "CPB-0825|CPB-0825" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0826
- Title: `Feature: scoped auto model (provider + pattern)`
- Candidate paths:
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`
  - `docs/planning/README.md`
- Verification command: `rg -n "CPB-0826|CPB-0826" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0827
- Title: `wss 链接失败`
- Candidate paths:
  - `pkg/llmproxy/translator/gemini/openai/chat-completions`
  - `pkg/llmproxy/translator/antigravity/openai/responses`
  - `pkg/llmproxy/executor`
- Verification command: `rg -n "CPB-0827|CPB-0827" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0828
- Title: `应该给GPT-5.1添加-none后缀适配以保持一致性`
- Candidate paths:
  - `cmd`
  - `sdk/cliproxy`
  - `pkg/llmproxy/api/handlers/management`
- Verification command: `rg -n "CPB-0828|CPB-0828" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0829
- Title: `不支持 candidate_count 功能，设置需要多版本回复的时候，只会输出1条`
- Candidate paths:
  - `docs/operations/release-governance.md`
  - `docs/troubleshooting.md`
  - `pkg/llmproxy/config`
- Verification command: `rg -n "CPB-0829|CPB-0829" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

### CPB-0830
- Title: `gpt-5.1模型添加`
- Candidate paths:
  - `pkg/llmproxy/registry/model_registry.go`
  - `docs/operations/release-governance.md`
  - `docs/provider-quickstarts.md`
- Verification command: `rg -n "CPB-0830|CPB-0830" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`

## Verification

- `rg -n "CPB-0821|CPB-0830" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md`
- `rg -n "quickstart|troubleshooting|stream|tool|reasoning|provider" docs/provider-quickstarts.md docs/troubleshooting.md`
- `go test ./pkg/llmproxy/translator/... -run "TestConvert|TestTranslate" -count=1`
