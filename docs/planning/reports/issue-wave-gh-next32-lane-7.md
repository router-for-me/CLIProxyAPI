# Issue Wave Next32 - Lane 7 Report

Scope: `router-for-me/CLIProxyAPIPlus` issues `#69 #43 #37 #30 #26`
Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/wt/cpb-wave-c7-docs-next`

## Per-Issue Status

### #69
- GitHub: `OPEN` - `[BUG] Vision requests fail for ZAI (glm) and Copilot models with missing header / invalid parameter errors`
- Status: `blocked`
- Code/Test surface:
  - `pkg/llmproxy/executor/github_copilot_executor.go`
  - `pkg/llmproxy/executor/github_copilot_executor_test.go`
  - `pkg/llmproxy/executor/openai_models_fetcher_test.go`
- Evidence command:
  - `rg -n "Copilot-Vision-Request|detectVisionContent|api.z.ai|/api/coding/paas/v4/models" pkg/llmproxy/executor/github_copilot_executor.go pkg/llmproxy/executor/github_copilot_executor_test.go pkg/llmproxy/executor/openai_models_fetcher_test.go`
- Evidence output:
  - `github_copilot_executor.go:164: httpReq.Header.Set("Copilot-Vision-Request", "true")`
  - `github_copilot_executor.go:298: httpReq.Header.Set("Copilot-Vision-Request", "true")`
  - `github_copilot_executor_test.go:317: if !detectVisionContent(body) {`
  - `openai_models_fetcher_test.go:28: want: "https://api.z.ai/api/coding/paas/v4/models"`
- Notes:
  - Copilot vision-header handling is implemented, but no deterministic local proof was found for the specific ZAI vision payload-parameter error path described in the issue.

### #43
- GitHub: `OPEN` - `[Bug] Models from Codex (openai) are not accessible when Copilot is added`
- Status: `done`
- Code/Test surface:
  - `pkg/llmproxy/api/server.go`
  - `pkg/llmproxy/api/handlers/management/config_basic.go`
  - `pkg/llmproxy/api/handlers/management/auth_files.go`
- Evidence command:
  - `rg -n "force-model-prefix|PutForceModelPrefix|GetForceModelPrefix|Prefix\\s+\\*string|PatchAuthFileFields" pkg/llmproxy/api/server.go pkg/llmproxy/api/handlers/management/config_basic.go pkg/llmproxy/api/handlers/management/auth_files.go`
- Evidence output:
  - `config_basic.go:280: func (h *Handler) GetForceModelPrefix(c *gin.Context) {`
  - `config_basic.go:283: func (h *Handler) PutForceModelPrefix(c *gin.Context) {`
  - `server.go:626: mgmt.GET("/force-model-prefix", s.mgmt.GetForceModelPrefix)`
  - `server.go:627: mgmt.PUT("/force-model-prefix", s.mgmt.PutForceModelPrefix)`
  - `auth_files.go:916: // PatchAuthFileFields updates editable fields (prefix, proxy_url, priority) of an auth file.`
- Notes:
  - Existing implementation provides model-prefix controls (`force-model-prefix` and per-auth `prefix`) matching the issue's suggested disambiguation path.

### #37
- GitHub: `OPEN` - `GitHub Copilot models seem to be hardcoded`
- Status: `blocked`
- Code/Test surface:
  - `pkg/llmproxy/registry/model_definitions.go`
- Evidence command:
  - `sed -n '171,230p' pkg/llmproxy/registry/model_definitions.go`
- Evidence output:
  - `func GetGitHubCopilotModels() []*ModelInfo {`
  - `gpt4oEntries := []struct { ... }{ ... }`
  - `models := []*ModelInfo{ ... ID: "gpt-4.1" ... }`
  - `models = append(models, []*ModelInfo{ ... ID: "gpt-5" ... })`
- Notes:
  - Copilot models are enumerated in static code, not fetched dynamically from upstream.

### #30
- GitHub: `OPEN` - `kiro命令登录没有端口`
- Status: `blocked`
- Code/Test surface:
  - `pkg/llmproxy/cmd/kiro_login.go`
  - `pkg/llmproxy/api/handlers/management/auth_files.go`
  - `cmd/server/main.go`
- Evidence command:
  - `rg -n "kiroCallbackPort|startCallbackForwarder\\(|--kiro-aws-authcode|--kiro-aws-login|--kiro-import" pkg/llmproxy/api/handlers/management/auth_files.go pkg/llmproxy/cmd/kiro_login.go cmd/server/main.go`
- Evidence output:
  - `auth_files.go:2623: const kiroCallbackPort = 9876`
  - `auth_files.go:2766: if _, errStart := startCallbackForwarder(kiroCallbackPort, "kiro", targetURL); errStart != nil {`
  - `kiro_login.go:102: ... use --kiro-aws-authcode.`
  - `kiro_login.go:161: ... try: --kiro-aws-login (device code flow)`
- Notes:
  - Callback port and fallback flows exist in code, but deterministic proof that the reported "no port shown" runtime behavior is resolved in the stated container environment was not established.

### #26
- GitHub: `OPEN` - `I did not find the Kiro entry in the Web UI`
- Status: `done`
- Code/Test surface:
  - `pkg/llmproxy/api/server.go`
  - `pkg/llmproxy/api/handlers/management/auth_files.go`
  - `pkg/llmproxy/cmd/setup.go`
- Evidence command:
  - `rg -n "Kiro|kiro|Auth Files|auth files|/management.html|Provider: \\\"kiro\\\"" pkg/llmproxy/api/server.go pkg/llmproxy/api/handlers/management/auth_files.go pkg/llmproxy/cmd/setup.go`
- Evidence output:
  - `server.go:323: s.engine.GET("/management.html", s.serveManagementControlPanel)`
  - `server.go:683: mgmt.GET("/kiro-auth-url", s.mgmt.RequestKiroToken)`
  - `auth_files.go:2711: Provider: "kiro",`
  - `auth_files.go:2864: Provider: "kiro",`
  - `setup.go:118: {label: "Kiro OAuth login", run: DoKiroLogin},`
- Notes:
  - Kiro management and auth entrypoints are present, and Kiro auth records are created with provider type `kiro`.

## Focused Checks

- `gh api repos/router-for-me/CLIProxyAPIPlus/issues/69 --jq '"#\(.number) [\(.state|ascii_upcase)] \(.title) | \(.html_url)"'`
  - `#69 [OPEN] [BUG] Vision requests fail for ZAI (glm) and Copilot models with missing header / invalid parameter errors | https://github.com/router-for-me/CLIProxyAPIPlus/issues/69`
- `gh api repos/router-for-me/CLIProxyAPIPlus/issues/43 --jq '"#\(.number) [\(.state|ascii_upcase)] \(.title) | \(.html_url)"'`
  - `#43 [OPEN] [Bug] Models from Codex (openai) are not accessible when Copilot is added | https://github.com/router-for-me/CLIProxyAPIPlus/issues/43`
- `gh api repos/router-for-me/CLIProxyAPIPlus/issues/37 --jq '"#\(.number) [\(.state|ascii_upcase)] \(.title) | \(.html_url)"'`
  - `#37 [OPEN] GitHub Copilot models seem to be hardcoded | https://github.com/router-for-me/CLIProxyAPIPlus/issues/37`
- `gh api repos/router-for-me/CLIProxyAPIPlus/issues/30 --jq '"#\(.number) [\(.state|ascii_upcase)] \(.title) | \(.html_url)"'`
  - `#30 [OPEN] kiro命令登录没有端口 | https://github.com/router-for-me/CLIProxyAPIPlus/issues/30`
- `gh api repos/router-for-me/CLIProxyAPIPlus/issues/26 --jq '"#\(.number) [\(.state|ascii_upcase)] \(.title) | \(.html_url)"'`
  - `#26 [OPEN] I did not find the Kiro entry in the Web UI | https://github.com/router-for-me/CLIProxyAPIPlus/issues/26`

## Blockers

- `#69`: only partial proof (Copilot header path); no deterministic proof of ZAI vision-parameter fix.
- `#37`: implementation remains static/hardcoded model list.
- `#30`: environment-specific login/port symptom not deterministically proven resolved from code-only evidence.
