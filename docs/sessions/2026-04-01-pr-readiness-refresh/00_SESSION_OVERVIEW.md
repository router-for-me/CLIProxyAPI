# PR Readiness Refresh

## Goal

Stabilize PR `#942` enough to move it out of branch-local merge debt and obvious CI wiring failures.

## Scope

- Resolve the lingering `docs/plans/KILO_GASTOWN_SPEC.md` merge residue in the checked-out branch.
- Replace deprecated or broken SAST workflow wiring with current pinned actions and direct tool invocation.
- Re-target custom Semgrep content away from Rust-only patterns so the ruleset matches this Go repository.

## Outcome

- The branch no longer carries an unmerged spec file.
- `SAST Quick Check` no longer references a missing action repo or a Rust-only lint job.
- Remaining blockers are pre-existing repo debt or external issues, not broken workflow scaffolding in this PR.

## 2026-04-02 Import Surface Follow-up

- Replaced stale `internal/config` imports with the live `pkg/llmproxy/config` package across the repo-internal tests and generator template.
- Replaced stale watcher test imports to the live `pkg/llmproxy/watcher/diff` and `pkg/llmproxy/watcher/synthesizer` packages.
- Replaced stale auth test imports from the old v6 tree with the current local `internal/auth/codebuddy` and `pkg/llmproxy/auth/kiro` packages.
- `go mod vendor` now succeeds after the import-surface sweep.
- `GOFLAGS=-mod=vendor go test ./...` still fails, but now on broader vendoring/toolchain debt because the generated `vendor/` tree remains incomplete for many third-party packages on this branch.
