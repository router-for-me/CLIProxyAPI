# Issue Wave CPB-0001..0035 Lane 1 Report

## Scope
- Lane: `you`
- Window: `CPB-0001` to `CPB-0005`
- Worktree: `/Users/kooshapari/temp-PRODVERCEL/485/kush/cliproxyapi-plusplus`

## Per-Issue Status

### CPB-0001 – Extract standalone Go mgmt CLI
- Status: `blocked`
- Rationale: requires cross-process CLI extraction and ownership boundary changes across `cmd/cliproxyapi` and management handlers, which is outside a safe docs-first patch and would overlap platform-architecture work not completed in this slice.

### CPB-0002 – Non-subprocess integration surface
- Status: `blocked`
- Rationale: needs API shape design for runtime contract negotiation and telemetry, which is a larger architectural change than this lane’s safe implementation target.

### CPB-0003 – Add `cliproxy dev` process-compose profile
- Status: `blocked`
- Rationale: requires workflow/runtime orchestration definitions and orchestration tooling wiring that is currently not in this wave’s scope with low-risk edits.

### CPB-0004 – Provider-specific quickstarts
- Status: `done`
- Changes:
  - Added `docs/provider-quickstarts.md` with 5-minute success paths for Claude, Codex, Gemini, GitHub Copilot, Kiro, MiniMax, and OpenAI-compatible providers.
  - Linked quickstarts from `docs/provider-usage.md`, `docs/index.md`, and `docs/README.md`.

### CPB-0005 – Create troubleshooting matrix
- Status: `done`
- Changes:
  - Added structured troubleshooting matrix to `docs/troubleshooting.md` with symptom → cause → immediate check → remediation rows.

## Validation
- `rg -n "Provider Quickstarts|Troubleshooting Matrix" docs/provider-usage.md docs/provider-quickstarts.md docs/troubleshooting.md`

## Blockers / Follow-ups
- CPB-0001, CPB-0002, CPB-0003 should move to a follow-up architecture/control-plane lane that owns code-level API surface changes and process orchestration.
