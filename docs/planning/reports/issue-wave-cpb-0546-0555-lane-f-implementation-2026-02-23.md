# Issue Wave CPB-0546-0555 Lane F Implementation (2026-02-23)

## Scope
- Lane: `wave-80-lane-f`
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus`
- Slice: `CPB-0546` to `CPB-0555` (10 items)

## Delivery Status
- Implemented: `10`
- Blocked: `0`

## Items

### CPB-0546
- Status: `implemented`
- Delivery: Added Homebrew/macOS config file path quickstart and verification commands.
- Evidence:
  - `docs/provider-quickstarts.md` (`macOS Homebrew install: where is the config file?`)

### CPB-0547
- Status: `implemented`
- Delivery: Added deterministic QA scenarios around codex 404 isolate flow and model exposure checks.
- Evidence:
  - `docs/provider-quickstarts.md` (`Codex 404 triage (provider-agnostic)`)

### CPB-0548
- Status: `implemented`
- Delivery: Added long-run incident handling guidance for noisy account/provider error surfaces (retry/cooldown/log scan).
- Evidence:
  - `docs/provider-operations.md` (`iFlow account errors shown in terminal`)

### CPB-0549
- Status: `implemented`
- Delivery: Added rollout safety checklist for Windows duplicate auth-file display across restart cycles.
- Evidence:
  - `docs/provider-operations.md` (`Windows duplicate auth-file display safeguards`)

### CPB-0550
- Status: `implemented`
- Delivery: Standardized provider quota/refresh metadata field naming for ops consistency.
- Evidence:
  - `docs/provider-operations.md` (`Metadata naming conventions for provider quota/refresh commands`)

### CPB-0551
- Status: `implemented`
- Delivery: Added `/v1/embeddings` quickstart probe and pass criteria for OpenAI-compatible embedding flows.
- Evidence:
  - `docs/provider-quickstarts.md` (`/v1/embeddings quickstart (OpenAI-compatible path)`)

### CPB-0552
- Status: `implemented`
- Delivery: Added `force-model-prefix` parity validation for Gemini model-list exposure.
- Evidence:
  - `docs/provider-quickstarts.md` (`force-model-prefix with Gemini model-list parity`)

### CPB-0553
- Status: `implemented`
- Delivery: Added operational observability checks and mitigation thresholds for iFlow account terminal errors.
- Evidence:
  - `docs/provider-operations.md` (`iFlow account errors shown in terminal`)

### CPB-0554
- Status: `implemented`
- Delivery: Added provider-agnostic codex `404` runbook flow tied to model exposure and explicit recovery path.
- Evidence:
  - `docs/provider-quickstarts.md` (`Codex 404 triage (provider-agnostic)`)

### CPB-0555
- Status: `implemented`
- Delivery: Added TrueNAS Apprise notification setup checks and non-blocking alerting guidance.
- Evidence:
  - `docs/provider-operations.md` (`TrueNAS Apprise notification DX checks`)

## Validation Commands
1. `bash .github/scripts/tests/check-wave80-lane-f-cpb-0546-0555.sh`
2. `go test ./pkg/llmproxy/thinking -count=1`
3. `go test ./pkg/llmproxy/store -count=1`

## Notes
- This lane intentionally avoided contested runtime files already under concurrent modification in the shared worktree.
- Deliverables are scoped to lane-F documentation/operations implementation with deterministic validation commands.
