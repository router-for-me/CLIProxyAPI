# Issue Wave CPB-0036..0105 Lane 2 Report

## Scope
- Lane: 2
- Worktree: `cliproxyapi-plusplus` (agent-equivalent execution, no external workers available)
- Target items: `CPB-0046` .. `CPB-0055`
- Date: 2026-02-22

## Per-Item Triage and Status

### CPB-0046 Gemini3 cannot generate images / image path non-subprocess
- Status: `blocked`
- Triage: No deterministic image-generation regression fixture or deterministic provider contract was available in-repo.
- Next action: Add a synthetic Gemini image-generation fixture + add integration e2e before touching translator/transport.

### CPB-0047 Enterprise Kiro 403 instability
- Status: `blocked`
- Triage: Requires provider/account behavior matrix and telemetry proof across multiple 403 payload variants.
- Next action: Capture stable 4xx samples and add provider-level retry/telemetry tests.

### CPB-0048 -kiro-aws-login login ban / blocking
- Status: `blocked`
- Triage: This flow crosses auth UI/login, session caps, and external policy behavior; no safe local-only patch.
- Next action: Add regression fixture at integration layer before code changes.

### CPB-0049 Amp usage inflation + `amp`
- Status: `blocked`
- Triage: No reproducible workload that proves current over-amplification shape for targeted fix.
- Next action: Add replayable `amp` traffic fixture and validate `request-retry`/cooling behavior.

### CPB-0050 Antigravity auth failure naming metadata
- Status: `blocked`
- Triage: Changes are cross-repo/config-standardization in scope and need coordination with management docs.
- Next action: Create shared metadata naming ADR before repo-local patch.

### CPB-0051 Multi-account management quickstart
- Status: `blocked`
- Triage: No accepted UX contract for account lifecycle orchestration in current worktree.
- Next action: Add explicit account-management acceptance spec and CLI command matrix first.

### CPB-0052 `auth file changed (WRITE)` logging noise
- Status: `blocked`
- Triage: Requires broader logging noise policy and backpressure changes in auth writers.
- Next action: Add log-level/verbosity matrix then refactor emit points.

### CPB-0053 `incognito` parameter invalid
- Status: `blocked`
- Triage: Needs broader login argument parity validation and behavior matrix.
- Next action: Add cross-command CLI acceptance coverage before changing argument parser.

### CPB-0054 OpenAI-compatible `/v1/models` hardcoded path
- Status: `implemented`
- Result:
  - Added shared model-list endpoint resolution for OpenAI-style clients, including:
    - `models_url` override from auth attributes.
    - automatic `/models` resolution for versioned base URLs.
- Validation run:
  - `go test ./pkg/llmproxy/executor ./pkg/llmproxy/runtime/executor -run 'Test.*FetchOpenAIModels.*' -count=1`
- Touched files:
  - `pkg/llmproxy/executor/openai_models_fetcher.go`
  - `pkg/llmproxy/runtime/executor/openai_models_fetcher.go`

### CPB-0055 `ADD TRAE IDE support` DX follow-up
- Status: `blocked`
- Triage: Requires explicit CLI path support contract and likely external runtime integration.
- Next action: Add support matrix and command spec in issue design doc first.

## Validation Commands

- `go test ./pkg/llmproxy/executor ./pkg/llmproxy/runtime/executor ./pkg/llmproxy/logging ./pkg/llmproxy/translator/gemini/openai/chat-completions ./pkg/llmproxy/translator/codex/openai/chat-completions ./cmd/server -run 'TestUseGitHubCopilotResponsesEndpoint|TestApplyClaude|TestEnforceLogDirSizeLimit|TestOpenAIModels|TestResponseFormat|TestConvertOpenAIRequestToGemini' -count=1`
- Result: all passing for referenced packages.
