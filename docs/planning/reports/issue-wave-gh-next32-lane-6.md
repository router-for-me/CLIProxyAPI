# Issue Wave Next32 - Lane 6 Report

Scope: `router-for-me/CLIProxyAPIPlus` issues `#83 #81 #79 #78 #72`
Worktree: `cliproxyapi-plusplus-wave-cpb-6`

## Per-Issue Status

### #83
- Status: `blocked`
- Mapping:
  - Code investigation command: `rg -n "event stream fatal|context deadline exceeded|Timeout" pkg/llmproxy/executor pkg/llmproxy/translator`
  - Repro/validation command: `gh issue view 83 --repo router-for-me/CLIProxyAPIPlus --json number,state,title,url --jq '.number,.state,.title,.url'`
- Evidence:
  - Output (`gh issue view 83 ...`):
    - `83`
    - `OPEN`
    - `kiro请求偶尔报错event stream fatal`
    - `https://github.com/router-for-me/CLIProxyAPIPlus/issues/83`
  - Block reason: no deterministic in-repo reproducer payload/trace attached for bounded low-risk patching.

### #81
- Status: `blocked`
- Mapping:
  - Code investigation command: `rg -n "config path .* is a directory|CloudFallbackToNestedConfig|NonCloudFallbackToNestedConfigWhenDefaultIsDir" cmd/server/config_path_test.go pkg/llmproxy/config/config.go`
  - Targeted test/vet commands:
    - `go test ./cmd/server -run 'TestResolveDefaultConfigPath_(CloudFallbackToNestedConfig|NonCloudFallbackToNestedConfigWhenDefaultIsDir)$'`
    - `go test ./pkg/llmproxy/config -run 'TestLoadConfigOptional_DirectoryPath$'`
    - `go vet ./cmd/server`
- Evidence:
  - Output (`rg -n ...`):
    - `cmd/server/config_path_test.go:59:func TestResolveDefaultConfigPath_CloudFallbackToNestedConfig(t *testing.T) {`
    - `cmd/server/config_path_test.go:84:func TestResolveDefaultConfigPath_NonCloudFallbackToNestedConfigWhenDefaultIsDir(t *testing.T) {`
    - `pkg/llmproxy/config/config.go:694: "failed to read config file: %w (config path %q is a directory; pass a YAML file path such as /CLIProxyAPI/config.yaml)",`
  - Output (`go test`/`go vet` attempts): toolchain-blocked.
    - `FAIL github.com/router-for-me/CLIProxyAPI/v6/cmd/server [setup failed]`
    - `... package internal/abi is not in std (.../go1.26.0.darwin-arm64/src/internal/abi)`
    - `go: go.mod requires go >= 1.26.0 (running go 1.23.4; GOTOOLCHAIN=local)`

### #79
- Status: `blocked`
- Mapping:
  - Investigation command: `gh issue view 79 --repo router-for-me/CLIProxyAPIPlus --json number,state,title,url,body`
  - Impact-scan command: `rg -n "provider|oauth|auth|model" pkg/llmproxy cmd`
- Evidence:
  - Output (`gh issue view 79 --repo ... --json number,state,title,url --jq '.number,.state,.title,.url'`):
    - `79`
    - `OPEN`
    - `[建议] 技术大佬考虑可以有机会新增一堆逆向平台`
    - `https://github.com/router-for-me/CLIProxyAPIPlus/issues/79`
  - Block reason: broad multi-provider feature request, not a bounded low-risk lane fix.

### #78
- Status: `blocked`
- Mapping:
  - Investigation command: `gh issue view 78 --repo router-for-me/CLIProxyAPIPlus --json number,state,title,url,body`
  - Targeted test/vet commands:
    - `go test ./pkg/llmproxy/translator/openai/claude -run 'TestConvertOpenAIResponseToClaude_(StreamingToolCalls|ToolCalls)$'`
    - `go vet ./pkg/llmproxy/translator/openai/claude`
- Evidence:
  - Output (`gh issue view 78 --repo ... --json number,state,title,url --jq '.number,.state,.title,.url'`):
    - `78`
    - `OPEN`
    - `Issue with removed parameters - Sequential Thinking Tool Failure (nextThoughtNeeded undefined)`
    - `https://github.com/router-for-me/CLIProxyAPIPlus/issues/78`
  - Block reason: requires reproducible request/response capture to pinpoint where parameter loss occurs; go validation currently blocked by toolchain.

### #72
- Status: `blocked`
- Mapping:
  - Code investigation command: `rg -n "skipping Claude built-in web_search|TestConvertClaudeToolsToKiro_SkipsBuiltInWebSearchInMixedTools" pkg/llmproxy/translator/kiro/claude/kiro_claude_request.go pkg/llmproxy/translator/kiro/claude/kiro_claude_request_test.go`
  - Targeted test/vet commands:
    - `go test ./pkg/llmproxy/translator/kiro/claude -run 'TestConvertClaudeToolsToKiro_SkipsBuiltInWebSearchInMixedTools$'`
    - `go vet ./pkg/llmproxy/translator/kiro/claude`
- Evidence:
  - Output (`rg -n ...`):
    - `pkg/llmproxy/translator/kiro/claude/kiro_claude_request.go:542: log.Infof("kiro: skipping Claude built-in web_search tool in mixed-tool request (type=%s)", toolType)`
    - `pkg/llmproxy/translator/kiro/claude/kiro_claude_request_test.go:140:func TestConvertClaudeToolsToKiro_SkipsBuiltInWebSearchInMixedTools(t *testing.T) {`
  - Output (`go test` attempt): toolchain-blocked.
    - `FAIL github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/translator/kiro/claude [setup failed]`
    - `... package internal/chacha8rand is not in std (.../go1.26.0.darwin-arm64/src/internal/chacha8rand)`

## Focused Checks

- `task quality:fmt:check`
- `QUALITY_PACKAGES='./pkg/llmproxy/api ./sdk/api/handlers/openai' task quality:quick`

## Blockers

- Go 1.26 toolchain in this worktree is not runnable for package-level `go test`/`go vet` (`golang.org/toolchain@v0.0.1-go1.26.0.darwin-arm64` missing std/internal packages during setup).

## Wave2 Entries

### 2026-02-23 - #179 OpenAI-MLX/vLLM-MLX support
- Status: `done`
- Mapping:
  - Source issue: `router-for-me/CLIProxyAPIPlus#179`
  - Implemented fix: OpenAI-compatible model discovery now honors `models_endpoint` auth attribute (emitted from `models-endpoint` config), including absolute URL and absolute path overrides.
  - Why this is low risk: fallback/default `/v1/models` behavior is unchanged; only explicit override handling is added.
- Files:
  - `pkg/llmproxy/executor/openai_models_fetcher.go`
  - `pkg/llmproxy/executor/openai_models_fetcher_test.go`
  - `pkg/llmproxy/runtime/executor/openai_models_fetcher.go`
  - `pkg/llmproxy/runtime/executor/openai_models_fetcher_test.go`
- Tests:
  - `go test pkg/llmproxy/executor/openai_models_fetcher.go pkg/llmproxy/executor/proxy_helpers.go pkg/llmproxy/executor/openai_models_fetcher_test.go`
  - `go test pkg/llmproxy/runtime/executor/openai_models_fetcher.go pkg/llmproxy/runtime/executor/proxy_helpers.go pkg/llmproxy/runtime/executor/openai_models_fetcher_test.go`
- Verification notes:
  - Added regression coverage for `models_endpoint` path override and absolute URL override in both mirrored executor test suites.
- Blockers:
  - Package-level `go test ./pkg/llmproxy/executor` and `go test ./pkg/llmproxy/runtime/executor` are currently blocked by unrelated compile errors in existing lane files (`kiro_executor.go`, `codex_websockets_executor.go`).
