# Issue Wave CPB-0316..CPB-0350 Lane 1 Report

## Scope

- Lane: lane-1
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus-wave-cpb7-1`
- Window: `CPB-0316` to `CPB-0320`

## Status Snapshot

- `implemented`: 5
- `planned`: 0
- `in_progress`: 0
- `blocked`: 0

## Per-Item Status

### CPB-0316 – Expand docs and examples for "可以出个检查更新吗，不然每次都要拉下载然后重启" with copy-paste quickstart and troubleshooting section.
- Status: `implemented`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1195`
- Rationale:
  - Added copy-paste update workflow to installation docs (fetch, pull, rebuild, restart) for binary users.
  - Added concrete quick verification commands aligned with existing local dev workflow.
- Proposed verification commands:
  - `rg -n "check update flow|git fetch --tags|go build ./cmd/cliproxyapi" docs/install.md`
  - `rg -n "CPB-0316" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.

### CPB-0317 – Add QA scenarios for "antigravity可以增加配额保护吗 剩余额度多少的时候不在使用" including stream/non-stream parity and edge-case payloads.
- Status: `implemented`
- Theme: `general-polish`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1194`
- Rationale:
  - Added no-capacity retry QA scenarios for nested capacity markers and unrelated 503 responses.
  - Locked down retry behavior with focused unit tests on `antigravityShouldRetryNoCapacity`.
- Proposed verification commands:
  - `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/executor -run 'TestAntigravity(ShouldRetryNoCapacity|ErrorMessage)' -count=1`
  - `rg -n "CPB-0317" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.

### CPB-0318 – Refactor implementation behind "codex总是有失败" to reduce complexity and isolate transformation boundaries.
- Status: `implemented`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1193`
- Rationale:
  - Isolated Codex request transformation into `prepareCodexRequestBundle` to separate translation concerns from streaming response dispatch.
  - Preserved original payload for downstream response conversion while keeping responses-format passthrough behavior.
- Proposed verification commands:
  - `GOCACHE=$PWD/.cache/go-build go test ./sdk/api/handlers/openai -run 'Test.*Codex|TestShouldTreatAsResponsesFormat' -count=1`
  - `rg -n "prepareCodexRequestBundle|codexRequestBundle" sdk/api/handlers/openai/openai_handlers.go`
- Next action: none for this item.

### CPB-0319 – Add process-compose/HMR refresh workflow tied to "建议在使用Antigravity 额度时，设计额度阈值自定义功能" so local config and runtime can be reloaded deterministically.
- Status: `implemented`
- Theme: `dev-runtime-refresh`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1192`
- Rationale:
  - Documented Antigravity quota/routing hot-reload knobs under process-compose workflow.
  - Added deterministic touch/health verification sequence for live reload checks.
- Proposed verification commands:
  - `rg -n "quota-exceeded.switch-project|routing.strategy|touch config.yaml" docs/install.md`
  - `rg -n "CPB-0319" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- Next action: none for this item.

### CPB-0320 – Standardize metadata and naming conventions touched by "Antigravity: rev19-uic3-1p (Alias: gemini-2.5-computer-use-preview-10-2025) nolonger useable" across both repos.
- Status: `implemented`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/1190`
- Rationale:
  - Stopped seeding deprecated Antigravity alias `gemini-2.5-computer-use-preview-10-2025` into default oauth-model-alias output.
  - Preserved migration conversion to canonical `rev19-uic3-1p` and added assertions preventing alias reinjection.
- Proposed verification commands:
  - `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/config -run 'TestMigrateOAuthModelAlias_(ConvertsAntigravityModels|AddsDefaultIfNeitherExists)' -count=1`
  - `rg -n "gemini-2.5-computer-use-preview-10-2025|defaultAntigravityAliases" pkg/llmproxy/config/oauth_model_alias_migration.go config.example.yaml`
- Next action: none for this item.

## Evidence & Commands Run

- `rg -n 'CPB-0316|CPB-0320' docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
- `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/config -run 'TestMigrateOAuthModelAlias_(ConvertsAntigravityModels|AddsDefaultIfNeitherExists)' -count=1`
- `rg -n "check update flow|quota-exceeded.switch-project|routing.strategy|OAuth Callback Server Start Failure" docs/install.md docs/troubleshooting.md`
- `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/executor -run 'TestAntigravity(ShouldRetryNoCapacity|ErrorMessage)' -count=1`
- `GOCACHE=$PWD/.cache/go-build go test ./sdk/api/handlers/openai -run 'Test.*Codex|TestShouldTreatAsResponsesFormat' -count=1`
- `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/config -run 'TestMigrateOAuthModelAlias_' -count=1`


## Next Actions
- Lane complete for `CPB-0316`..`CPB-0320`.
