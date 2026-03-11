# Plan: GitLab Duo Codex Parity

**Generated**: 2026-03-10
**Estimated Complexity**: High

## Overview
Bring GitLab Duo support from the current "auth + basic executor" stage to the same practical level as `codex` inside `CLIProxyAPI`: a user logs in once, points external clients such as Claude Code at `CLIProxyAPI`, selects GitLab Duo-backed models, and gets stable streaming, multi-turn behavior, tool calling compatibility, and predictable model routing without manual provider-specific workarounds.

The core architectural shift is to stop treating GitLab Duo as only two REST wrappers (`/api/v4/chat/completions` and `/api/v4/code_suggestions/completions`) and instead use GitLab's `direct_access` contract as the primary runtime entrypoint wherever possible. Official GitLab docs confirm that `direct_access` returns AI gateway connection details, headers, token, and expiry; that contract is the closest path to codex-like provider behavior.

## Prerequisites
- Official GitLab Duo API references confirmed during implementation:
  - `POST /api/v4/code_suggestions/direct_access`
  - `POST /api/v4/code_suggestions/completions`
  - `POST /api/v4/chat/completions`
- Access to at least one real GitLab Duo account for manual verification.
- One downstream client target for acceptance testing:
  - Claude Code against Claude-compatible endpoint
  - OpenAI-compatible client against `/v1/chat/completions` and `/v1/responses`
- Existing PR branch as starting point:
  - `feat/gitlab-duo-auth`
  - PR [#2028](https://github.com/router-for-me/CLIProxyAPI/pull/2028)

## Definition Of Done
- GitLab Duo models can be used via `CLIProxyAPI` from the same client surfaces that already work for `codex`.
- Upstream streaming is real passthrough or faithful chunked forwarding, not synthetic whole-response replay.
- Tool/function calling survives translation layers without dropping fields or corrupting names.
- Multi-turn and session semantics are stable across `chat/completions`, `responses`, and Claude-compatible routes.
- Model exposure stays current from GitLab metadata or gateway discovery without hardcoded stale model tables.
- `go test ./...` stays green and at least one real manual end-to-end client flow is documented.

## Sprint 1: Contract And Gap Closure
**Goal**: Replace assumptions with a hard compatibility contract between current `codex` behavior and what GitLab Duo can actually support.

**Demo/Validation**:
- Written matrix showing `codex` features vs current GitLab Duo behavior.
- One checked-in developer note or test fixture for real GitLab Duo payload examples.

### Task 1.1: Freeze Codex Parity Checklist
- **Location**: [internal/runtime/executor/codex_executor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/codex_executor.go), [internal/runtime/executor/codex_websockets_executor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/codex_websockets_executor.go), [sdk/api/handlers/openai/openai_responses_handlers.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/sdk/api/handlers/openai/openai_responses_handlers.go), [sdk/api/handlers/openai/openai_responses_websocket.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/sdk/api/handlers/openai/openai_responses_websocket.go)
- **Description**: Produce a concrete feature matrix for `codex`: HTTP execute, SSE execute, `/v1/responses`, websocket downstream path, tool calling, request IDs, session close semantics, and model registration behavior.
- **Dependencies**: None
- **Acceptance Criteria**:
  - A checklist exists in repo docs or issue notes.
  - Each capability is marked `required`, `optional`, or `not possible` for GitLab Duo.
- **Validation**:
  - Review against current `codex` code paths.

### Task 1.2: Lock GitLab Duo Runtime Contract
- **Location**: [internal/auth/gitlab/gitlab.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/auth/gitlab/gitlab.go), [internal/runtime/executor/gitlab_executor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/gitlab_executor.go)
- **Description**: Validate the exact upstream contract we can rely on:
  - `direct_access` fields and refresh cadence
  - whether AI gateway path is usable directly
  - when `chat/completions` is available vs when fallback is required
  - what streaming shape is returned by `code_suggestions/completions?stream=true`
- **Dependencies**: Task 1.1
- **Acceptance Criteria**:
  - GitLab transport decision is explicit: `gateway-first`, `REST-first`, or `hybrid`.
  - Unknown areas are isolated behind feature flags, not spread across executor logic.
- **Validation**:
  - Official docs + captured real responses from a Duo account.

### Task 1.3: Define Client-Facing Compatibility Targets
- **Location**: [README.md](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/README.md), [gitlab-duo-codex-parity-plan.md](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/gitlab-duo-codex-parity-plan.md)
- **Description**: Define exactly which external flows must work to call GitLab Duo support "like codex".
- **Dependencies**: Task 1.2
- **Acceptance Criteria**:
  - Required surfaces are listed:
    - Claude-compatible route
    - OpenAI `chat/completions`
    - OpenAI `responses`
    - optional downstream websocket path
  - Non-goals are explicit if GitLab upstream cannot support them.
- **Validation**:
  - Maintainer review of stated scope.

## Sprint 2: Primary Transport Parity
**Goal**: Move GitLab Duo execution onto a transport that supports codex-like runtime behavior.

**Demo/Validation**:
- A GitLab Duo model works over real streaming through `/v1/chat/completions`.
- No synthetic "collect full body then fake stream" path remains on the primary flow.

### Task 2.1: Refactor GitLab Executor Into Strategy Layers
- **Location**: [internal/runtime/executor/gitlab_executor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/gitlab_executor.go)
- **Description**: Split current executor into explicit strategies:
  - auth refresh/direct access refresh
  - gateway transport
  - GitLab REST fallback transport
  - downstream translation helpers
- **Dependencies**: Sprint 1
- **Acceptance Criteria**:
  - Executor no longer mixes discovery, refresh, fallback selection, and response synthesis in one path.
  - Transport choice is testable in isolation.
- **Validation**:
  - Unit tests for strategy selection and fallback boundaries.

### Task 2.2: Implement Real Streaming Path
- **Location**: [internal/runtime/executor/gitlab_executor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/gitlab_executor.go), [internal/runtime/executor/gitlab_executor_test.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/gitlab_executor_test.go)
- **Description**: Replace synthetic streaming with true upstream incremental forwarding:
  - use gateway stream if available
  - otherwise consume GitLab Code Suggestions streaming response and map chunks incrementally
- **Dependencies**: Task 2.1
- **Acceptance Criteria**:
  - `ExecuteStream` emits chunks before upstream completion.
  - error handling preserves status and early failure semantics.
- **Validation**:
  - tests with chunked upstream server
  - manual curl check against `/v1/chat/completions` with `stream=true`

### Task 2.3: Preserve Upstream Auth And Headers Correctly
- **Location**: [internal/runtime/executor/gitlab_executor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/gitlab_executor.go), [internal/auth/gitlab/gitlab.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/auth/gitlab/gitlab.go)
- **Description**: Use `direct_access` connection details as first-class transport state:
  - gateway token
  - expiry
  - mandatory forwarded headers
  - model metadata
- **Dependencies**: Task 2.1
- **Acceptance Criteria**:
  - executor stops ignoring gateway headers/token when transport requires them
  - refresh logic never over-fetches `direct_access`
- **Validation**:
  - tests verifying propagated headers and refresh interval behavior

## Sprint 3: Request/Response Semantics Parity
**Goal**: Make GitLab Duo behave correctly under the same request shapes that current `codex` consumers send.

**Demo/Validation**:
- OpenAI and Claude-compatible clients can do non-streaming and streaming conversations without losing structure.

### Task 3.1: Normalize Multi-Turn Message Mapping
- **Location**: [internal/runtime/executor/gitlab_executor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/gitlab_executor.go), [sdk/translator](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/sdk/translator)
- **Description**: Replace the current "flatten prompt into one instruction" behavior with stable multi-turn mapping:
  - preserve system context
  - preserve user/assistant ordering
  - maintain bounded context truncation
- **Dependencies**: Sprint 2
- **Acceptance Criteria**:
  - multi-turn requests are not collapsed into a lossy single string unless fallback mode explicitly requires it
  - truncation policy is deterministic and tested
- **Validation**:
  - golden tests for request mapping

### Task 3.2: Tool Calling Compatibility Layer
- **Location**: [internal/runtime/executor/gitlab_executor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/gitlab_executor.go), [sdk/api/handlers/openai/openai_responses_handlers.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/sdk/api/handlers/openai/openai_responses_handlers.go)
- **Description**: Decide and implement one of two paths:
  - native pass-through if GitLab gateway supports tool/function structures
  - strict downgrade path with explicit unsupported errors instead of silent field loss
- **Dependencies**: Task 3.1
- **Acceptance Criteria**:
  - tool-related fields are either preserved correctly or rejected explicitly
  - no silent corruption of tool names, tool calls, or tool results
- **Validation**:
  - table-driven tests for tool payloads
  - one manual client scenario using tools

### Task 3.3: Token Counting And Usage Reporting Fidelity
- **Location**: [internal/runtime/executor/gitlab_executor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/gitlab_executor.go), [internal/runtime/executor/usage_helpers.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/usage_helpers.go)
- **Description**: Improve token/usage reporting so GitLab models behave like first-class providers in logs and scheduling.
- **Dependencies**: Sprint 2
- **Acceptance Criteria**:
  - `CountTokens` uses the closest supported estimation path
  - usage logging distinguishes prompt vs completion when possible
- **Validation**:
  - unit tests for token estimation outputs

## Sprint 4: Responses And Session Parity
**Goal**: Reach codex-level support for OpenAI Responses clients and long-lived sessions where GitLab upstream permits it.

**Demo/Validation**:
- `/v1/responses` works with GitLab Duo in a realistic client flow.
- If websocket parity is not possible, the code explicitly declines it and keeps HTTP paths stable.

### Task 4.1: Make GitLab Compatible With `/v1/responses`
- **Location**: [sdk/api/handlers/openai/openai_responses_handlers.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/sdk/api/handlers/openai/openai_responses_handlers.go), [internal/runtime/executor/gitlab_executor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/gitlab_executor.go)
- **Description**: Ensure GitLab transport can safely back the Responses API path, including compact responses if applicable.
- **Dependencies**: Sprint 3
- **Acceptance Criteria**:
  - GitLab Duo can be selected behind `/v1/responses`
  - response IDs and follow-up semantics are defined
- **Validation**:
  - handler tests analogous to codex/openai responses tests

### Task 4.2: Evaluate Downstream Websocket Parity
- **Location**: [sdk/api/handlers/openai/openai_responses_websocket.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/sdk/api/handlers/openai/openai_responses_websocket.go), [internal/runtime/executor/gitlab_executor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/gitlab_executor.go)
- **Description**: Decide whether GitLab Duo can support downstream websocket sessions like codex:
  - if yes, add session-aware execution path
  - if no, mark GitLab auth as websocket-ineligible and keep HTTP routes first-class
- **Dependencies**: Task 4.1
- **Acceptance Criteria**:
  - websocket behavior is explicit, not accidental
  - no route claims websocket support when the upstream cannot honor it
- **Validation**:
  - websocket handler tests or explicit capability tests

### Task 4.3: Add Session Cleanup And Failure Recovery Semantics
- **Location**: [internal/runtime/executor/gitlab_executor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/gitlab_executor.go), [sdk/cliproxy/auth/conductor.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/sdk/cliproxy/auth/conductor.go)
- **Description**: Add codex-like session cleanup, retry boundaries, and model suspension/resume behavior for GitLab failures and quota events.
- **Dependencies**: Sprint 2
- **Acceptance Criteria**:
  - auth/model cooldown behavior is predictable on GitLab 4xx/5xx/quota responses
  - executor cleans up per-session resources if any are introduced
- **Validation**:
  - tests for quota and retry behavior

## Sprint 5: Client UX, Model UX, And Manual E2E
**Goal**: Make GitLab Duo feel like a normal built-in provider to operators and downstream clients.

**Demo/Validation**:
- A documented setup exists for "login once, point Claude Code at CLIProxyAPI, use GitLab Duo-backed model".

### Task 5.1: Model Alias And Provider UX Cleanup
- **Location**: [sdk/cliproxy/service.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/sdk/cliproxy/service.go), [README.md](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/README.md)
- **Description**: Normalize what users see:
  - stable alias such as `gitlab-duo`
  - discovered upstream model names
  - optional prefix behavior
  - account labels that clearly distinguish OAuth vs PAT
- **Dependencies**: Sprint 3
- **Acceptance Criteria**:
  - users can select a stable GitLab alias even when upstream model changes
  - dynamic model discovery does not cause confusing model churn
- **Validation**:
  - registry tests and manual `/v1/models` inspection

### Task 5.2: Add Real End-To-End Acceptance Tests
- **Location**: [internal/runtime/executor/gitlab_executor_test.go](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/internal/runtime/executor/gitlab_executor_test.go), [sdk/api/handlers/openai](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/sdk/api/handlers/openai)
- **Description**: Add higher-level tests covering the actual proxy surfaces:
  - OpenAI `chat/completions`
  - OpenAI `responses`
  - Claude-compatible request path if GitLab is routed there
- **Dependencies**: Sprint 4
- **Acceptance Criteria**:
  - tests fail if streaming regresses into synthetic buffering again
  - tests cover at least one tool-related request and one multi-turn request
- **Validation**:
  - `go test ./...`

### Task 5.3: Publish Operator Documentation
- **Location**: [README.md](/home/luxvtz/projects/cliproxyapi/CLIProxyAPI/README.md)
- **Description**: Document:
  - OAuth setup requirements
  - PAT requirements
  - current capability matrix
  - known limitations if websocket/tool parity is partial
- **Dependencies**: Sprint 5.1
- **Acceptance Criteria**:
  - setup instructions are enough for a new user to reproduce the GitLab Duo flow
  - limitations are explicit
- **Validation**:
  - dry-run docs review from a clean environment

## Testing Strategy
- Keep `go test ./...` green after every committable task.
- Add table-driven tests first for request mapping, refresh behavior, and dynamic model registration.
- Add transport tests with `httptest.Server` for:
  - real chunked streaming
  - header propagation from `direct_access`
  - upstream fallback rules
- Add at least one manual acceptance checklist:
  - login via OAuth
  - login via PAT
  - list models
  - run one streaming prompt via OpenAI route
  - run one prompt from the target downstream client

## Potential Risks & Gotchas
- GitLab public docs expose `direct_access`, but do not fully document every possible AI gateway path. We should isolate any empirically discovered gateway assumptions behind one transport layer and feature flags.
- `chat/completions` availability differs by GitLab offering and version. The executor must not assume it always exists.
- Code Suggestions is completion-oriented; lossy mapping from rich chat/tool payloads will make GitLab Duo feel worse than codex unless explicitly handled.
- Synthetic streaming is not good enough for codex parity and will cause regressions in interactive clients.
- Dynamic model discovery can create unstable UX if the stable alias and discovered model IDs are not separated cleanly.
- PAT auth may validate successfully while still lacking effective Duo permissions. Error reporting must surface this explicitly.

## Rollback Plan
- Keep the current basic GitLab executor behind a fallback mode until the new transport path is stable.
- If parity work destabilizes existing providers, revert only GitLab-specific executor changes and leave auth support intact.
- Preserve the stable `gitlab-duo` alias so rollback does not break client configuration.
