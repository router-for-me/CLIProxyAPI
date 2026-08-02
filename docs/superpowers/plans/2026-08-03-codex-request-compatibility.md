# Codex Request Compatibility Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task by task in a single primary-agent session.

**Goal:** Match official Codex request behavior while preventing model-specific high reasoning levels from failing when a nearby supported level is available.

**Architecture:** Keep `internal/thinking` as the canonical capability and validation pipeline. Add `ultra` to the canonical level vocabulary, clamp only high-intent OpenAI/Codex levels to the nearest lower supported level, and keep exact per-model capabilities in the registry. At the executor boundary, remove unsupported metadata fields according to the actual upstream target and cap compact requests at `xhigh`.

**Tech Stack:** Go 1.26, gjson/sjson, embedded JSON model registries, Go table-driven tests.

---

### Task 1: Define the high-reasoning compatibility contract

**Files:**
- Modify: `internal/thinking/apply_configured_api_key_test.go`
- Modify: `internal/thinking/types.go`
- Modify: `internal/thinking/suffix.go`
- Modify: `internal/thinking/convert.go`
- Modify: `internal/thinking/validate.go`

**Step 1: Write the failing tests**

Add table-driven tests proving that OpenAI/Codex high-intent levels fall back downward by capability:

- `ultra -> max -> xhigh -> high`
- `max -> xhigh -> high`
- `xhigh -> high`

Also prove that unsupported low/medium requests remain validation errors and that `-ultra` suffix parsing produces canonical `LevelUltra`.

**Step 2: Run the focused tests and confirm red**

Run: `go test ./internal/thinking -run 'TestApplyThinking.*HighIntent|TestParse.*Ultra' -count=1`

Expected: failure because `ultra` is not canonical and same-family high levels are currently rejected.

**Step 3: Implement the smallest canonical change**

- Add `LevelUltra`.
- Include `ultra` in suffix parsing and standard level ordering.
- Permit downward clamping only when the target format is OpenAI/Codex and the requested level is `xhigh`, `max`, or `ultra`.
- Preserve existing cross-family mapping and ordinary strict validation.
- Extend conversions so canonical `ultra` has deterministic behavior when translated to token budgets or Claude effort.

**Step 4: Run the focused tests and confirm green**

Run: `go test ./internal/thinking -count=1`

Expected: all thinking tests pass.

### Task 2: Align built-in model capabilities with the official Codex manifest

**Files:**
- Modify: `internal/registry/model_definitions_test.go`
- Modify: `internal/registry/models/models.json`

**Step 1: Write the failing registry test**

Assert for every built-in Codex subscription tier:

- GPT-5.6 Sol and Terra advertise `low, medium, high, xhigh, max, ultra`.
- GPT-5.6 Luna advertises `low, medium, high, xhigh, max` and excludes `ultra`.

**Step 2: Run the focused test and confirm red**

Run: `go test ./internal/registry -run 'Test.*GPT56.*Thinking' -count=1`

Expected: Sol/Terra assertions fail because the built-in registry currently stops at `max`.

**Step 3: Update exact model definitions**

Add `ultra` to every Sol/Terra definition while leaving Luna capped at `max`.

**Step 4: Run registry tests and confirm green**

Run: `go test ./internal/registry -count=1`

Expected: all registry tests pass.

### Task 3: Normalize Codex metadata and compact reasoning at the upstream boundary

**Files:**
- Modify: `internal/runtime/executor/codex_executor_cache_test.go`
- Modify: `internal/runtime/executor/codex_executor_request.go`
- Modify: `internal/runtime/executor/codex_fingerprint.go`

**Step 1: Write the failing executor tests**

Cover these target-sensitive cases:

- Official ChatGPT Codex `/responses` keeps official `client_metadata` and removes top-level `metadata`.
- `/responses/compact` removes both metadata fields and maps `max`/`ultra` effort to `xhigh`.
- Public API and custom upstream URLs remove both metadata fields.
- Ordinary official non-compact requests preserve a supported `ultra` effort.

**Step 2: Run the focused tests and confirm red**

Run: `go test ./internal/runtime/executor -run 'TestCodex.*(Metadata|Compact|Reasoning)' -count=1`

Expected: custom-target `client_metadata` and compact high-effort assertions fail.

**Step 3: Implement endpoint-aware normalization**

- Factor official ChatGPT Codex URL recognition into a shared executor helper.
- Always remove unsupported top-level `metadata`.
- Keep `client_metadata` only for ordinary official ChatGPT Codex responses requests.
- Remove it for compact, public API, and custom targets.
- Cap compact `reasoning.effort` values `max` and `ultra` to `xhigh` immediately before sending upstream.

**Step 4: Run focused executor tests and confirm green**

Run: `go test ./internal/runtime/executor -run 'TestCodex.*(Metadata|Compact|Reasoning)' -count=1`

Expected: all selected executor tests pass.

### Task 4: Verify integration and deliver to main

**Files:**
- Verify all modified Go and JSON files.

**Step 1: Format code**

Run: `gofmt -w .`

**Step 2: Run affected package tests**

Run: `go test ./internal/thinking ./internal/registry ./internal/runtime/executor -count=1`

**Step 3: Run the full suite**

Run: `go test ./...`

**Step 4: Run the required compile check**

Run: `go build -o test-output ./cmd/server && rm test-output`

**Step 5: Review the final diff**

Run: `git diff --check && git status --short && git diff --stat && git diff`

Confirm that the change is limited to the canonical thinking pipeline, exact model capabilities, executor boundary normalization, tests, and design/plan documents.

**Step 6: Commit and push**

Create a focused commit, then push the current verified `HEAD` explicitly to `origin/main` because `main` is checked out in another local worktree.

Run: `git push origin HEAD:main`
