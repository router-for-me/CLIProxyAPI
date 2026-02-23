# Issue Wave CPB-0138..0147 Lane 1 Plan

## Scope
- Lane: `1`
- Target items: `CPB-0138`..`CPB-0147`
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus`
- Date: 2026-02-23
- Focus: document implementable deltas and verification commands for these ten items; other lanes can ignore unrelated edits in the repository.

## Per-Item Plan

### CPB-0138 Define non-subprocess integration path
- Status: `planned`
- Implementation deltas:
  - Extend `docs/sdk-usage.md` so the `Integration Contract` section walks through the recommended in-process `sdk/cliproxy.NewBuilder()` lifecycle, the HTTP fallback (`/v1/*`, `/v0/management/config`), and the capability/version negotiation probes (`/health`, `/v1/models`, `remote-management.secret-key`).
  - Add a troubleshooting row that highlights the version sniffing steps and points to the HTTP fallback endpoints exposed by `cmd/server` and `sdk/api/handlers`.
  - Capture the benchmark plan called for in the board by recording the pre-change `task test:baseline` results and explaining that the same command will be rerun after the implementable delta.
- Planned files:
  - `docs/sdk-usage.md`
  - `docs/troubleshooting.md`
- Notes: keep the focus on documentation and observable experience; no deep runtime refactor is scheduled yet.

### CPB-0139 Gemini CLI rollout safety guardrails
- Status: `planned`
- Implementation deltas:
  - Add table-driven API contract tests in `pkg/llmproxy/executor/gemini_cli_executor_test.go` that exercise missing credential fields, legacy vs. new parameter mixes, and the `statusErr` path that surfaces the upstream `额度获取失败` message.
  - Extend `pkg/llmproxy/auth/gemini/gemini_auth_test.go` with fixtures that simulate malformed tokens (missing `refresh_token`, expired credential struct) so the CLI can surface `请检查凭证状态` before hitting production.
  - Reference the new guardrails in `docs/troubleshooting.md` (Gemini CLI section) and the `Gemini` quickstart so operators know which fields to check during a rollout.
- Planned files:
  - `pkg/llmproxy/executor/gemini_cli_executor_test.go`
  - `pkg/llmproxy/auth/gemini/gemini_auth_test.go`
  - `docs/troubleshooting.md`
  - `docs/provider-quickstarts.md`

### CPB-0140 Normalize 403 metadata/naming
- Status: `planned`
- Implementation deltas:
  - Add a canonical `403` troubleshooting entry that maps each provider alias to the metadata fields we record (e.g., `provider`, `alias`, `model`, `reason`) so repeated 403 patterns can be channeled into the same remediation path.
  - Bake a short migration note in `docs/FEATURE_CHANGES_PLUSPLUS.md` (or the nearest changelog) that restates the compatibility guarantee when renaming aliases or metadata fields.
- Planned files:
  - `docs/troubleshooting.md`
  - `docs/FEATURE_CHANGES_PLUSPLUS.md`

### CPB-0141 iFlow compatibility gap closure
- Status: `planned`
- Implementation deltas:
  - Introduce a normalization helper inside `pkg/llmproxy/executor/iflow_executor.go` (e.g., `normalizeIFlowModelName`) so requests that carry alternate suffixes or casing are converted before we apply thinking/translators.
  - Emit a mini telemetry log (reusing `recordAPIRequest` or `reporter.publish`) that tags the normalized `model` and whether a suffix translation was applied; this will be used by future telemetry dashboards.
  - Add focused tests in `pkg/llmproxy/executor/iflow_executor_test.go` covering the normalized inputs and ensuring the telemetry hook fires when normalization occurs.
- Planned files:
  - `pkg/llmproxy/executor/iflow_executor.go`
  - `pkg/llmproxy/executor/iflow_executor_test.go`

### CPB-0142 Harden Kimi OAuth
- Status: `planned`
- Implementation deltas:
  - Tighten validation in `pkg/llmproxy/auth/kimi/kimi.go` so empty `refresh_token`, `client_id`, or `client_secret` values fail fast with a clear error and default to safer timeouts.
  - Add regression tests in `pkg/llmproxy/auth/kimi/kimi_test.go` that assert each missing field path returns the new error and that a simulated provider fallback metric increments.
  - Document the new validation expectations in `docs/troubleshooting.md` under the Kimi section.
- Planned files:
  - `pkg/llmproxy/auth/kimi/kimi.go`
  - `pkg/llmproxy/auth/kimi/kimi_test.go`
  - `docs/troubleshooting.md`

### CPB-0143 Operationalize Grok OAuth
- Status: `planned`
- Implementation deltas:
  - Update `docs/provider-operations.md` with a Grok OAuth observability subsection that lists the thresholds (latency, failure budget) operators should watch and ties each alert to a specific remediation script or CLI command.
  - Add deterministic remediation text with command examples to the `docs/troubleshooting.md` Grok row.
  - Mention the same commands in the `docs/provider-operations.md` runbook so alerts can point to this lane’s work when Grok authentication misbehaves.
- Planned files:
  - `docs/provider-operations.md`
  - `docs/troubleshooting.md`

### CPB-0144 Provider-agnostic token refresh runbook
- Status: `planned`
- Implementation deltas:
  - Document the provider-agnostic `token refresh failed` sequence in `docs/provider-quickstarts.md` and `docs/troubleshooting.md`, including the `stop/relogin/management refresh/canary` choreography and sample request/response payloads.
  - Reference the existing translation utilities (`pkg/llmproxy/thinking`) to highlight how they already canonicalize the error so every provider can look at the same diagnostics.
- Planned files:
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`

### CPB-0145 Process-compose/HMR deterministic refresh
- Status: `planned`
- Implementation deltas:
  - Extend `docs/install.md` with a step-by-step process-compose/HMR refresh workflow (touch `config.yaml`, poll `/health`, probe `/v1/models`, run `cliproxy reload`) using precise commands.
  - Introduce a small helper script under `scripts/process_compose_refresh.sh` that encapsulates the workflow and can be run from CI/local dev loops.
  - Explain the workflow in `docs/troubleshooting.md` so operators have a deterministic repro for `Gemini 3` refresh failures.
- Planned files:
  - `docs/install.md`
  - `scripts/process_compose_refresh.sh`
  - `docs/troubleshooting.md`

### CPB-0146 Cursor root-cause UX/logs
- Status: `planned`
- Implementation deltas:
  - Add a Cursor-specific quickstart entry in `docs/provider-quickstarts.md` that walks through the `cursor login` flow, the key indicators of a root-cause `cursor` error, and the commands to surface structured logs.
  - Inject structured logging fields (`cursor_status`, `config_path`, `response_code`) inside `pkg/llmproxy/cmd/cursor_login.go` so the new quickstart can point operators to log lines that capture the symptom.
  - Mention the new log fields in `docs/troubleshooting.md` so the runbook references the exact columns in logs when diagnosing the `cursor` root cause.
- Planned files:
  - `docs/provider-quickstarts.md`
  - `pkg/llmproxy/cmd/cursor_login.go`
  - `docs/troubleshooting.md`

### CPB-0147 ENABLE_TOOL_SEARCH QA
- Status: `planned`
- Implementation deltas:
  - Add QA scenarios to `pkg/llmproxy/executor/claude_executor_test.go` that exercise the `ENABLE_TOOL_SEARCH` flag for both stream and non-stream flows; mock the MCP response that returns `tools unavailable 400` and assert the fallback behavior.
  - Expose the `claude.enable_tool_search` toggle in `config.example.yaml` (under the Claude section) and document it in `docs/provider-quickstarts.md`/`docs/troubleshooting.md` so rollouts can be staged via config toggles.
  - Capture the config toggle in tests by seeding `pkg/llmproxy/config/config_test.go` or a new fixture file.
- Planned files:
  - `pkg/llmproxy/executor/claude_executor_test.go`
  - `config.example.yaml`
  - `docs/provider-quickstarts.md`
  - `docs/troubleshooting.md`

## Verification Strategy
1. `go test ./pkg/llmproxy/executor -run 'TestIFlow.*|TestGeminiCLI.*|TestClaude.*ToolSearch'`
2. `go test ./pkg/llmproxy/auth/gemini ./pkg/llmproxy/auth/kimi -run 'TestGeminiAuth|TestKimi'`
3. `task test:baseline` (captures the latency/memory snapshot required by CPB-0138 before/after the doc-driven change).
4. `rg -n "ENABLE_TOOL_SEARCH" config.example.yaml docs/provider-quickstarts.md docs/troubleshooting.md`
5. `rg -n "cursor_status" pkg/llmproxy/cmd/cursor_login.go docs/troubleshooting.md` (ensures the new structured logging message is documented).
