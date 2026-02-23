# cliproxyapi++ Optimization Plan — 2026-02-23

## Current State (after Phase 1 fixes)
- Go: ~183K LOC (after removing 21K dead runtime/executor copy)
- Duplicate executor deleted: pkg/llmproxy/runtime/executor/ (47 files, 21K LOC)
- Security wave 3 in progress (bad-redirect-check, weak-hashing)

## What Was Done Today
- Deleted stale `pkg/llmproxy/runtime/executor/` (commit be548bbd)
- This was 47 files / 21,713 LOC of orphaned code never imported by anything
- Live executor at `pkg/llmproxy/executor/` is the sole implementation

## Remaining Optimization Tracks

### Track 1: Security Wave 3 Completion
- Complete remaining bad-redirect-check alerts
- Verify all weak-sensitive-data-hashing fixes are in
- Run full golangci-lint pass: `task quality`
- Target: 0 security lint warnings

### Track 2: Large File Modularization
- `kiro_executor.go` (4,675 LOC) — split into kiro_executor_auth.go + kiro_executor_streaming.go
- `auth_files.go` (3,020 LOC) — split by provider
- `conductor.go` (2,300 LOC) — extract provider conductor per LLM
- Target: no single .go file > 1,500 LOC

### Track 3: SDK Test Coverage
- Recent commits fixed SDK test failures (a6eec475)
- Run full test suite: `task test`
- Ensure all 272 test files pass consistently
- Add coverage metrics

### Track 4: Documentation Consolidation
- 450+ markdown files — add index/navigation
- Ensure docs/ARCHITECTURE.md reflects removal of runtime/executor/
- Update provider list docs to reflect current implementation

## Architecture Outcome
- Single executor package ✅ (done)
- Clean SDK imports ✅ (only pkg/llmproxy/executor/)
- Security hardening: in progress
- Large file splits: TODO
- Full test suite green: TODO
