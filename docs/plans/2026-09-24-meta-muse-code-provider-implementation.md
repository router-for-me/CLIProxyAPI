# Meta (Muse Code) Implementation Plan — Phase 4 of NixLLM Upstream Re-Implementation

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.
> **Worktree discipline:** Work langsung di `main`. No `git stash`. Each task ships as one commit.

**Goal:** Complete native implementation of Meta/Muse Code provider di NixLLM, sesuai design doc `docs/plans/2026-09-22-nixllm-upstream-releases-design.md` §98-109.

## Scope

Provider baru `meta` (alias `muse code`) mengikuti template antigravity/xai:
- Device-flow OAuth di `internal/auth/meta/` (konstanta + device flow + token storage).
- Authentication layer di `sdk/auth/` + registration di `auth_manager.go`.
- Model registry di `internal/registry/` + sink model list.
- HTTP endpoint di `internal/api/handlers/*`.
- Konfig `config.Config` field MetaKey/MetaModel.
- Runtime executor di `internal/runtime/executor/`.
- Sink di config/schemas/alias/registry.
- Testing (unit + integration + e2e minimal).
- Dashboard SPA `web/dashboard/src/api/schemas.js` + aliases + registry.

**Prinsip:** mengikuti pola provider Xai/Antigravity, tidak invent new pola, minimal diff, KISS.

---

## Task 1: Setup auth module `internal/auth/meta/`

**Files:** `internal/auth/meta/meta.go` (baru), `internal/auth/meta/constants.go` (baru).

**Step 1: Failing test** (placeholder, fill on implementation).
**Step 2: Implementasi** - struct + func mirror antigravity/xai.
**Step 3: Verify** `gofmt -l internal/auth/meta/*.go`.
**Step 4: Commit** `feat(meta): add auth module internal/auth/meta`.

## Task 2: Sink auth manager

**Files:** `sdk/auth/auth_manager.go` (mod), `sdk/auth/meta_authenticator.go` (new file).
**Step 1-4:** register sink authenticator + registration di registry.

## Task 3: Model registration registry

**Files:** `internal/registry/models/meta_registry.json` (new), `internal/registry/models/models.json` (mod), `internal/registry/model_definitions.go` (mod), `internal/registry/model_updater.go` (mod), `internal/registry/models.go` (mod).

**Step 1:** Add JSON array `[...]` for Meta model di `models.json`.
**Step 2:** Wire `WithMetaBuiltins()` di `model_definitions.go`.
**Step 3:** Update updater section mapping `{name: "meta", models: data.Meta}`.
**Step 4:** Commit.

## Task 4: Route API

**Files:** `internal/api/handlers/management/auth_files_provider_oauth.go` (mod), `internal/api/handlers/management/oauth_sessions.go` (mod), `internal/api/server_routes.go` (mod), `internal/api/server_management.go` (mod), `internal/api/server.go` (mod), `internal/api/handlers/config.go` (mod).

**Step 1:** Add `h.RequestMetaToken` endpoint mirror xai.
**Step 2:** Add case `case "meta", "muse code": return "meta", nil` di NormalizeOAuthProvider.
**Step 3:** Add endpoint `/meta-auth-url`.
**Step 4:** Commit.

## Task 5: Config wiring

**Files:** `internal/config/config_types.go` (mod), `internal/config/config.go` (mod), `internal/config/sanitize.go` (mod).

**Step 1:** Add `MetaKey []MetaKey` alias type, mirror XAIKey.
**Step 2:** Add `MetaModel []MetaModel` alias type.
**Step 3:** Add `SanitizeMetaKeys()` mirror xai.
**Step 4:** Commit.

## Task 6: Runtime Executor

**Files:** `internal/runtime/executor/meta_executor.go` (new), `internal/runtime/executor/meta_executor_auth.go` (new), `internal/runtime/executor/meta_executor_request.go` (new), `internal/runtime/executor/meta_executor_response.go` (new).

**Step 1:** Implement `MetaExecutor` mirror xai.
**Step 2:** Implement refresh (Re-use device flow if needed).
**Step 3:** Commit.

## Task 7: Registry Sink

**Files:** `internal/registry/registry.go` (mod) + `internal/registry/model_definitions.go` (mod).

**Step 1:** Add helper `GetMetaModels() []*ModelInfo`.
**Step 2:** Add case "meta" in `GetStaticModelDefinitionsByChannel`.
**Step 3:** Commit.

## Task 8: Config/Schema Sink

**Files:** `internal/config/types/schemas.go` (mod), `internal/config/validators.go` (mod), `internal/config/aliases.go` (mod).

**Step 1:** Update validators for meta alias support.
**Step 2:** Add `func (x MetaModel) IsValid() error`.
**Step 3:** Commit.

## Task 9: Unit tests

**Files:** `internal/auth/meta/meta_test.go` (new), `internal/runtime/executor/meta_executor_test.go` (new).

**Step 1:** Unit test for auth meta flow (mock device flow).
**Step 2:** Unit test for meta executor (token refresh logic).
**Step 3:** Commit.

## Task 10: Integration & e2e tests

**Files:** `internal/store/postgresstore.go` (mod), `internal/store/postgres_test.go` (mod), `internal/api/server_test.go` (mod), `web/dashboard/src/api/schemas.js` (mod).

**Step 1:** Add `case "meta":` di `authIsOAuthProvider`.
**Step 2:** Mock PostgresSink test untuk row provider `meta`.
**Step 3:** Update server test (route existence check).
**Step 4:** Commit.

## Task 11: Final Verification

**Step 1:** Run `gofmt -w .` full.
**Step 2:** Run `go build -o nixllm ./cmd/server` — pastikan sukses.
**Step 3:** Run `go test ./...` — pastikan pass.
**Step 4:** `git log -10` + `git diff HEAD~12 --stat` to confirm.
**Step 5:** Update memory file `phase4-meta-muse-code-status.md`.
**Step 6:** Final commit + git tag `v7.<version bump>-0.<patch>`.

---

## Acceptance Criteria

- `go build ./...` green.
- No new lint errors (zero new lint errors).
- All unit + integration test pass (excluding pre-existing failure di internal/util).
- `internal/auth/meta/` folder exists.
- `internal/registry/models/meta_registry.json` exists.
- `models.json` has key `meta`.
- `config.Config` includes `MetaKey` & `MetaModel`.
- Alias `meta` di `internal/config/aliases.go`.
- Runtime executor `MetaExecutor` di `internal/runtime/executor/`.
- Sink ke config/validators/aliases.
- Unit test di `internal/auth/meta/` dan `internal/runtime/executor/`.
- Integration test di `internal/store/` dan `internal/api/`.
- Sink di web/dashboard `schemas.js`.

## Verification Command

```bash
cd /home/bilfid/projects/nixllm && go build -o test-output ./cmd/server && rm test-output && go test./...
```
