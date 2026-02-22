# Issue Wave CPB-0036..0105 Lane 3 Report

## Scope
<<<<<<< HEAD
- Lane: 3
- Worktree: `cliproxyapi-plusplus`
- Target items: `CPB-0056` .. `CPB-0065`
- Date: 2026-02-22

## Per-Item Triage and Status

### CPB-0056 Kiro currently has no authentication available
- Status: `blocked`
- Triage: Requires end-to-end auth health contract and expected UX for no-auth conditions.
- Next action: Add acceptance test fixture for null-auth behavior and CLI diagnostics.

### CPB-0057 GitHub Copilot model-call failure flow
- Status: `blocked`
- Triage: Requires broader extraction of the thegent-managed flow and safe CLI command boundary.
- Next action: Add architecture decision + explicit flow tests before code changes.

### CPB-0058 Veo-style image generation + process-compose/HMR refresh
- Status: `blocked`
- Triage: No process-compose profile exists in this worktree; no deterministic HMR refresh harness.
- Next action: Add process-compose profile and scripted reload checks.

### CPB-0059 Token collisions with builderId / email/profile_arn empty
- Status: `blocked`
- Triage: Needs provider fixture and migration-safe token collision detection logic.
- Next action: Add token-store conflict fixture and collision-safe merge path.

### CPB-0060 Amazon Q `ValidationException` handling compatibility
- Status: `blocked`
- Triage: Upstream payload compatibility is not yet reproducible from local fixtures.
- Next action: Add failing regression payload + normalization lock.

### CPB-0061 Kiro config entry in UI guidance
- Status: `blocked`
- Triage: Requires coordination with product/UI/management-plane surfaces not owned by this codepath.
- Next action: Add documentation + UI contract with upstream team.

### CPB-0062 Cursor issue hardening
- Status: `blocked`
- Triage: Needs deterministic 4xx/stream error matrix before modifying translation logic.
- Next action: Add cursor regression fixture in executor integration tests.

### CPB-0063 Configurable HTTP timeout for extended thinking
- Status: `blocked`
- Triage: No configurable timeout knob is wired through the targeted request path.
- Next action: Add config schema + executor wiring + timeout override tests.

### CPB-0064 event stream fatal / stream protocol fragility
- Status: `blocked`
- Triage: Observed only via external stream sessions; no unit fixture today.
- Next action: Add stream stress/integrity fixture to capture protocol drop conditions.

### CPB-0065 failed to read config path is a directory
- Status: `partial`
- Result:
  - Existing config loader already returns empty config for optional mode and surfaces OS directory-read error for strict mode.
  - No local safe patch applied because behavior is acceptable for known startup flow, but error message surfacing still needs user-facing guidance in docs.
- Touched docs:
  - `docs/troubleshooting.md`
  - `docs/provider-operations.md`
- Next action: Add user-facing quick triage entry and end-to-end test that validates `--config` pointing at directory emits deterministic remediation text.

## Validation Commands

- `go test ./cmd/server ./pkg/llmproxy/executor ./pkg/llmproxy/runtime/executor ./pkg/llmproxy/logging ./pkg/llmproxy/translator/gemini/openai/chat-completions ./pkg/llmproxy/translator/codex/openai/chat-completions -count=1`
- Result: all targeted package tests passing.
=======
- Lane: `3`
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb-3`
- Window handled in this lane: `CPB-0056..CPB-0065`
- Constraint followed: no commits; only lane-scoped changes.

## Per-Item Triage + Status

### CPB-0056 - Kiro "no authentication available" docs/quickstart
- Status: `done (quick win)`
- What changed:
  - Added explicit Kiro bootstrap commands (`--kiro-login`, `--kiro-aws-authcode`, `--kiro-import`) and a troubleshooting block for `auth_unavailable`.
- Evidence:
  - `docs/provider-quickstarts.md:114`
  - `docs/provider-quickstarts.md:143`
  - `docs/troubleshooting.md:35`

### CPB-0057 - Copilot model-call-failure flow into first-class CLI commands
- Status: `partial (docs-only quick win; larger CLI extraction deferred)`
- Triage:
  - Core CLI surface already has `--github-copilot-login`; full flow extraction/integration hardening is broader than safe lane quick wins.
- What changed:
  - Added explicit bootstrap/auth command in provider quickstart.
- Evidence:
  - `docs/provider-quickstarts.md:85`
  - Existing flag surface observed in `cmd/server/main.go` (`--github-copilot-login`).

### CPB-0058 - process-compose/HMR refresh workflow
- Status: `done (quick win)`
- What changed:
  - Added a minimal process-compose profile for deterministic local startup.
  - Added install docs section describing local process-compose workflow with built-in watcher reload behavior.
- Evidence:
  - `examples/process-compose.dev.yaml`
  - `docs/install.md:81`
  - `docs/install.md:87`

### CPB-0059 - Kiro/BuilderID token collision + refresh lifecycle safety
- Status: `done (quick win)`
- What changed:
  - Hardened Kiro synthesized auth ID generation: when `profile_arn` is empty, include `refresh_token` in stable ID seed to reduce collisions across Builder ID credentials.
  - Added targeted tests in both synthesizer paths.
- Evidence:
  - `pkg/llmproxy/watcher/synthesizer/config.go:604`
  - `pkg/llmproxy/auth/synthesizer/config.go:601`
  - `pkg/llmproxy/watcher/synthesizer/config_test.go`
  - `pkg/llmproxy/auth/synthesizer/config_test.go`

### CPB-0060 - Amazon Q ValidationException metadata/origin standardization
- Status: `triaged (docs guidance quick win; broader cross-repo standardization deferred)`
- Triage:
  - Full cross-repo naming/metadata standardization is larger-scope.
- What changed:
  - Added troubleshooting row with endpoint/origin preference checks and remediation guidance.
- Evidence:
  - `docs/troubleshooting.md` (Amazon Q ValidationException row)

### CPB-0061 - Kiro config entry discoverability/compat gaps
- Status: `partial (docs quick win)`
- What changed:
  - Extended quickstarts with concrete Kiro and Cursor setup paths to improve config-entry discoverability.
- Evidence:
  - `docs/provider-quickstarts.md:114`
  - `docs/provider-quickstarts.md:199`

### CPB-0062 - Cursor issue hardening
- Status: `partial (docs quick win; deeper behavior hardening deferred)`
- Triage:
  - Runtime hardening exists in synthesizer warnings/defaults; further defensive fallback expansion should be handled in a dedicated runtime lane.
- What changed:
  - Added explicit Cursor troubleshooting row and quickstart.
- Evidence:
  - `docs/troubleshooting.md` (Cursor row)
  - `docs/provider-quickstarts.md:199`

### CPB-0063 - Configurable timeout for extended thinking
- Status: `partial (operational docs quick win)`
- Triage:
  - Full observability + alerting/runbook expansion is larger than safe quick edits.
- What changed:
  - Added timeout-specific troubleshooting and keepalive config guidance for long reasoning windows.
- Evidence:
  - `docs/troubleshooting.md` (Extended-thinking timeout row)
  - `docs/troubleshooting.md` (keepalive YAML snippet)

### CPB-0064 - event stream fatal provider-agnostic handling
- Status: `partial (ops/docs quick win; translation refactor deferred)`
- Triage:
  - Provider-agnostic translation refactor is non-trivial and cross-cutting.
- What changed:
  - Added stream-fatal troubleshooting path with stream/non-stream isolation and fallback guidance.
- Evidence:
  - `docs/troubleshooting.md` (`event stream fatal` row)

### CPB-0065 - config path is directory DX polish
- Status: `done (quick win)`
- What changed:
  - Improved non-optional config read error for directory paths with explicit remediation text.
  - Added tests covering optional vs non-optional directory-path behavior.
  - Added install-doc failure note for this exact error class.
- Evidence:
  - `pkg/llmproxy/config/config.go:680`
  - `pkg/llmproxy/config/config_test.go`
  - `docs/install.md:114`

## Focused Validation
- `go test ./pkg/llmproxy/config -run 'TestLoadConfig|TestLoadConfigOptional_DirectoryPath' -count=1`
  - Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config 7.457s`
- `go test ./pkg/llmproxy/watcher/synthesizer -run 'TestConfigSynthesizer_SynthesizeKiroKeys_UsesRefreshTokenForIDWhenProfileArnMissing' -count=1`
  - Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/watcher/synthesizer 11.350s`
- `go test ./pkg/llmproxy/auth/synthesizer -run 'TestConfigSynthesizer_SynthesizeKiroKeys_UsesRefreshTokenForIDWhenProfileArnMissing' -count=1`
  - Result: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/synthesizer 11.183s`

## Changed Files (Lane 3)
- `docs/install.md`
- `docs/provider-quickstarts.md`
- `docs/troubleshooting.md`
- `examples/process-compose.dev.yaml`
- `pkg/llmproxy/config/config.go`
- `pkg/llmproxy/config/config_test.go`
- `pkg/llmproxy/watcher/synthesizer/config.go`
- `pkg/llmproxy/watcher/synthesizer/config_test.go`
- `pkg/llmproxy/auth/synthesizer/config.go`
- `pkg/llmproxy/auth/synthesizer/config_test.go`

## Notes
- Existing untracked `docs/fragemented/` content was left untouched (other-lane workspace state).
- No commits were created.
>>>>>>> workstream-cpbv2-3
