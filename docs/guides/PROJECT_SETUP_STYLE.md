# Project Setup Style (Vercel/ai Inspired)

This repository follows a setup style focused on fast local feedback and strict release hygiene.

## Core Commands
- `task build`
- `task test`
- `task lint`
- `task quality`
- `task check` (alias for full quality gate)
- `task release:prep` (pre-release checks + changelog guard)

## Process Rules
- Keep `CHANGELOG.md` updated under `## [Unreleased]`.
- Keep docs and examples in sync with behavior changes.
- Prefer package-scoped checks for iteration and `task quality` before push.

## Release Readiness
Run:
1. `task changelog:check`
2. `task check`
3. `task quality:release-lint`
