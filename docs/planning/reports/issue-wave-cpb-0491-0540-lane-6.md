# Issue Wave CPB-0491-0540 Lane 6 Report

## Scope
- Lane: lane-6
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus`
- Window: `CPB-0516` to `CPB-0520`

## Status Snapshot
- `evidence-backed`: 5
- `implemented`: 0
- `planned`: 0
- `in_progress`: 0
- `blocked`: 0

## Per-Item Status

### CPB-0516 - Expand docs and examples for "支持包含模型配置" with copy-paste quickstart and troubleshooting section.
- Status: `evidence-backed`
- Theme: `provider-model-registry`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/892`
- Evidence:
  - `docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv:517` maps CPB-0516 to `implemented-wave80-lane-ad`.
  - `docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv:1316` maps CP2K-0688 / `issue#892` to `implemented-wave80-lane-ad`.

### CPB-0517 - Add QA scenarios for "Cursor subscription support" including stream/non-stream parity and edge-case payloads.
- Status: `evidence-backed`
- Theme: `oauth-and-authentication`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/891`
- Evidence:
  - `docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv:518` maps CPB-0517 to `implemented-wave80-lane-ad`.
  - `docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv:226` maps CP2K-0689 / `issue#891` to `implemented-wave80-lane-ad`.

### CPB-0518 - Refactor implementation behind "增加qodercli" to reduce complexity and isolate transformation boundaries.
- Status: `evidence-backed`
- Theme: `cli-ux-dx`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/889`
- Evidence:
  - `docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv:519` maps CPB-0518 to `implemented-wave80-lane-ad`.
  - `docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv:639` maps CP2K-0690 / `issue#889` to `implemented-wave80-lane-ad`.

### CPB-0519 - Ensure rollout safety for "[Bug] Codex auth file overwritten when account has both Plus and Team plans" via feature flags, staged defaults, and migration notes.
- Status: `evidence-backed`
- Theme: `thinking-and-reasoning`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/887`
- Evidence:
  - `docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv:520` maps CPB-0519 to `implemented-wave80-lane-ad`.
  - `docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv:227` maps CP2K-0691 / `issue#887` to `implemented-wave80-lane-ad`.
  - Bounded test evidence: `go test ./pkg/llmproxy/auth/codex -run 'TestCredentialFileName_TeamWithoutHashAvoidsDoubleDash|TestCredentialFileName_PlusAndTeamAreDisambiguated|TestCredentialFileName|TestNormalizePlanTypeForFilename' -count=1` (pass)

### CPB-0520 - Standardize metadata and naming conventions touched by "新版本有超时Bug,切换回老版本没问题" across both repos.
- Status: `evidence-backed`
- Theme: `responses-and-chat-compat`
- Source: `https://github.com/router-for-me/CLIProxyAPI/issues/886`
- Evidence:
  - `docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv:521` maps CPB-0520 to `implemented-wave80-lane-ad`.
  - `docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv:1317` maps CP2K-0692 / `issue#886` to `implemented-wave80-lane-ad`.

## Evidence & Commands Run
- `go test ./pkg/llmproxy/executor -run 'TestClassifyIFlowRefreshError|TestNewProxyAwareHTTPClient|TestCodexExecutor_ExecuteStripsPromptCacheRetention|TestCodexExecutor_ExecuteStreamStripsPromptCacheRetention' -count=1`
  - Output: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/executor 0.712s`
- `go test ./pkg/llmproxy/auth/codex -run 'TestCredentialFileName_TeamWithoutHashAvoidsDoubleDash|TestCredentialFileName_PlusAndTeamAreDisambiguated|TestCredentialFileName|TestNormalizePlanTypeForFilename' -count=1`
  - Output: `ok   github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/auth/codex 0.323s`
- `rg -n "CPB-0516|CPB-0517|CPB-0518|CPB-0519|CPB-0520" docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
  - Output: `517`, `518`, `519`, `520`, `521` all `implemented-wave80-lane-ad`.
- `rg -n "CP2K-0688|CP2K-0689|CP2K-0690|CP2K-0691|CP2K-0692|issue#892|issue#891|issue#889|issue#887|issue#886" docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - Output: `226`, `227`, `639`, `1316`, `1317` all `implemented-wave80-lane-ad`.

## Next Actions
- Lane window `CPB-0516..0520` is evidence-backed and board-aligned for Wave-80 Lane AD.
