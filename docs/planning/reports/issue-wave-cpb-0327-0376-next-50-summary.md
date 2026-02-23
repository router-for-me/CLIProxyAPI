# Issue Wave CPB-0327..0376 Next-50 Summary

## Scope

- Window: `CPB-0327` to `CPB-0376` (50 items)
- Mode: 6-lane child-agent triage + rolling execution
- Date: `2026-02-23`

## Queue Snapshot

- `proposed` in board snapshot: 50/50
- `implemented with verified evidence in this repo`: partial (tracked in lane reports)
- `triaged with concrete file/test targets this pass`: 50/50

## Child-Agent Lanes

- Lane A (`CPB-0327..0334`): identified low-risk closure paths across install/docs, translator hardening, and OAuth/model-alias surfaces.
- Lane B (`CPB-0335..0342`): mapped CLI UX, thinking regression docs/tests, and go-cli extraction touchpoints.
- Lane C (`CPB-0343..0350`): mapped restart-loop observability, refresh workflow, and naming/rollout safety surfaces.
- Lane D (`CPB-0351..0358`): confirmed lane reports still planning-heavy; no landed evidence to claim implementation without new repro payloads.
- Lane E (`CPB-0359..0366`): mapped malformed function-call guards, metadata standardization, whitelist-model config path, and Gemini logging/docs hooks.
- Lane F (`CPB-0367..0376`): mapped docs-first quick wins (quickstarts/troubleshooting/release-governance) and deferred code-heavy items pending reproductions.

## Verified Execution This Pass

- Built the exact next-50 queue from board CSV (`CPB-0327..0376`).
- Ran 6 child-agent triage lanes and captured concrete file/test targets.
- Continued rolling closure workflow in existing lane reports (`CPB-0321..0326` completed in prior tranche).

## Highest-Confidence Next Batch (10)

- `CPB-0327`, `CPB-0336`, `CPB-0340`, `CPB-0347`, `CPB-0348`
- `CPB-0359`, `CPB-0362`, `CPB-0364`, `CPB-0366`, `CPB-0376`

These are the strongest candidates for immediate low-risk closures because they have direct doc/translator/test touchpoints already identified by the lane triage.

## Validation Commands for Next Rolling Tranche

- `rg -n 'CPB-0327|CPB-0376' docs/planning/CLIPROXYAPI_1000_ITEM_BOARD_2026-02-22.csv`
- `GOCACHE=$PWD/.cache/go-build go test ./sdk/api/handlers ./sdk/auth`
- `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/translator/gemini/openai/chat-completions ./pkg/llmproxy/translator/antigravity/openai/chat-completions`
- `GOCACHE=$PWD/.cache/go-build go test ./pkg/llmproxy/util`

## Next Actions

- Execute the highest-confidence 10-item subset above with code+docs+tests in one pass.
- Update `issue-wave-cpb-0316-0350-lane-3.md` and `issue-wave-cpb-0351-0385-lane-*.md` as items close.
