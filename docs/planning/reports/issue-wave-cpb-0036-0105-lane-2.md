# Issue Wave CPB-0036..0105 Lane 2 Report

## Scope
- Lane: `2`
- Worktree: `cliproxyapi-plusplus-wave-cpb-2`
- Item window handled in this run: `CPB-0046..CPB-0055`
- Required dispositions: `implemented | planned | blocked | deferred`

## Quick Wins Implemented
1. `CPB-0054`: Added provider-agnostic OpenAI-compat model discovery endpoint override (`models-endpoint`) with tests.
2. `CPB-0051`: Expanded provider quickstart with explicit multi-account OpenAI-compat pattern and models-endpoint example.
3. `CPB-0053`: Added explicit incognito troubleshooting/remediation guidance to auth runbook.

## Per-Item Triage

### CPB-0046 — Define non-subprocess integration path for "Gemini3无法生图"
- Disposition: `planned`
- Evidence:
  - Board item remains `proposed` with integration-contract scope: `docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md:436`
  - Search found no non-planning implementation artifacts for a Go bindings + HTTP fallback contract (`rg -n "capability negotiation|http fallback|go bindings|non-subprocess" ...` => `no non-subprocess integration contract artifacts found outside planning docs`).
- Lane action: No safe narrow patch; requires dedicated contract design and API surface work.

### CPB-0047 — Add QA scenarios for Kiro enterprise 403 instability
- Disposition: `planned`
- Evidence:
  - Board item remains `proposed`: `docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md:445`
  - Targeted test search returned no explicit Kiro 403 parity coverage (`rg -n "403|StatusForbidden|forbidden" pkg/llmproxy/executor/kiro_executor*_test.go pkg/llmproxy/runtime/executor/kiro_executor*_test.go` => `no kiro 403 parity tests found`).
- Lane action: No safe quick win without introducing a broader QA matrix.

### CPB-0048 — Refactor `-kiro-aws-login` lockout path
- Disposition: `blocked`
- Evidence:
  - Prior lane evidence marks root cause as upstream/account policy and not locally fixable in isolation: `docs/planning/reports/issue-wave-gh-35-lane-7.md:49`
  - Existing local mitigation is guidance-level fallback, not a full refactor: `pkg/llmproxy/cmd/kiro_login.go:101`
- Lane action: Left as blocked on upstream/provider behavior and larger auth-flow redesign scope.

### CPB-0049 — Rollout safety for Copilot premium amplification with amp
- Disposition: `implemented`
- Evidence:
  - Historical fix explicitly closes issue #113 (`git show d468eec6`): adds initiator/billing guard and request-shape fixes.
  - Current code includes `X-Initiator` derivation and assistant-content flattening safeguards: `pkg/llmproxy/executor/github_copilot_executor.go:492`, `pkg/llmproxy/executor/github_copilot_executor.go:554`.
- Lane action: Confirmed implemented; no additional safe delta required in this pass.

### CPB-0050 — Standardize Antigravity auth failure metadata/naming
- Disposition: `implemented`
- Evidence:
  - Callback bind/access remediation helper and deterministic CLI hint exist: `sdk/auth/antigravity.go:216`
  - Regression tests validate callback-port guidance: `sdk/auth/antigravity_error_test.go:9`
  - Prior lane marked issue #111 as done with callback-port remediation: `docs/planning/reports/issue-wave-gh-35-lane-7.md:60`
- Lane action: Confirmed implemented in current tree.

### CPB-0051 — Multi-account quickstart/docs refresh
- Disposition: `implemented`
- Evidence:
  - Added multi-account OpenAI-compat quickstart block with explicit `models-endpoint`: `docs/provider-quickstarts.md:179`
  - Added Kiro login behavior guidance around incognito for account separation: `docs/provider-quickstarts.md:124`
  - Added `config.example.yaml` discoverability for `models-endpoint`: `config.example.yaml:257`
- Lane action: Implemented as safe docs quick win.

### CPB-0052 — Harden repeated "auth file changed (WRITE)" logging
- Disposition: `planned`
- Evidence:
  - Current watcher path still logs every auth write as info-level incremental processing: `pkg/llmproxy/watcher/events.go:135`, `pkg/llmproxy/watcher/events.go:143`, `pkg/llmproxy/watcher/events.go:152`
  - Board item remains proposed: `docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md:490`
- Lane action: Deferred code change in this pass to avoid risky watcher behavior regressions without a dedicated noise-threshold spec.

### CPB-0053 — Operationalize ineffective incognito login parameter
- Disposition: `implemented`
- Evidence:
  - Existing command/help path already encodes default-incognito + `--no-incognito` caveat: `pkg/llmproxy/cmd/kiro_login.go:35`
  - Runtime/auth path logs and applies incognito mode explicitly: `pkg/llmproxy/auth/kiro/sso_oidc.go:431`
  - Added runbook symptom/remediation entry for ignored account selection: `docs/operations/auth-refresh-failure-symptom-fix.md:13`
- Lane action: Implemented operationalization via runbook and existing runtime behavior confirmation.

### CPB-0054 — Remove hardcoded `/v1/models` in OpenAI-compat model discovery
- Disposition: `implemented`
- Evidence:
  - Added `models-endpoint` to OpenAI-compat config schema: `pkg/llmproxy/config/config.go:606`
  - Propagated optional endpoint into synthesized auth attributes: `pkg/llmproxy/auth/synthesizer/config.go:274`
  - Fetcher now honors configurable endpoint with default fallback: `pkg/llmproxy/executor/openai_models_fetcher.go:31`
  - Added regression tests for default and custom endpoints: `pkg/llmproxy/executor/openai_models_fetcher_test.go:13`
- Lane action: Implemented as safe code + test quick win.

### CPB-0055 — DX polish for TRAE IDE support
- Disposition: `deferred`
- Evidence:
  - Board item remains proposed: `docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.md:517`
  - No TRAE-specific implementation/docs artifacts found outside planning docs (`rg -n -i "\\btrae\\b" ...` => `no TRAE-specific implementation/docs matches found`).
- Lane action: Deferred pending concrete TRAE integration requirements and acceptance criteria.

## Focused Go Tests (Touched Areas)
- `go test ./pkg/llmproxy/executor -run TestFetchOpenAIModels_Uses -count=1`
  - `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/executor 9.882s`
- `go test ./pkg/llmproxy/runtime/executor -run TestFetchOpenAIModels_Uses -count=1`
  - `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/runtime/executor 14.259s`
- `go test ./pkg/llmproxy/auth/synthesizer -run TestConfigSynthesizer_SynthesizeOpenAICompat -count=1`
  - `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/synthesizer 6.406s`
- `go test ./pkg/llmproxy/watcher/synthesizer -run TestConfigSynthesizer_SynthesizeOpenAICompat -count=1`
  - `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/watcher/synthesizer 7.986s`

## Files Changed In This Lane Pass
- `pkg/llmproxy/config/config.go`
- `pkg/llmproxy/auth/synthesizer/config.go`
- `pkg/llmproxy/watcher/synthesizer/config.go`
- `pkg/llmproxy/auth/synthesizer/config_test.go`
- `pkg/llmproxy/watcher/synthesizer/config_test.go`
- `pkg/llmproxy/executor/openai_models_fetcher.go`
- `pkg/llmproxy/runtime/executor/openai_models_fetcher.go`
- `pkg/llmproxy/executor/openai_models_fetcher_test.go`
- `pkg/llmproxy/runtime/executor/openai_models_fetcher_test.go`
- `docs/provider-quickstarts.md`
- `docs/operations/auth-refresh-failure-symptom-fix.md`
- `config.example.yaml`
- `docs/planning/reports/issue-wave-cpb-0036-0105-lane-2.md`
