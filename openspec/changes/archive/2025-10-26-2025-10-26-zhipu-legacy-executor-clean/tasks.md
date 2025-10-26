## Tasks: zhipu-legacy-executor-clean

- [x] Make Python Bridge mandatory default (no legacy)
- [x] Remove legacy fallback requirements (OpenAICompatExecutor)
- [x] Provider resolution: glm-* strictly routes to zhipu
- [x] Add Bridge URL validation (http/https; localhost-only by default; env gate for remote)
- [x] Update tests for routing, bridge validation, and logging masking
- [x] Update docs: proposal/spec/tasks, config.example.yaml, CHANGELOG, MIGRATION.md
- [x] Validate OpenSpec and run pre-commit, tests

Validation commands:
- openspec validate 2025-10-26-zhipu-legacy-executor-clean --strict
- openspec show 2025-10-26-zhipu-legacy-executor-clean --json --deltas-only > openspec/changes/2025-10-26-zhipu-legacy-executor-clean/validation.json
