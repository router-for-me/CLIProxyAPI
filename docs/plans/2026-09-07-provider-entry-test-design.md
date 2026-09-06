# Design: Per-entry / per-model test button on the upstream provider editor

**Date:** 2026-09-07
**Status:** Approved (brainstorming session 2026-09-07)
**Goal:** A test panel on the upstream provider edit/detail page that runs one chat-completion probe against a chosen entry (or the provider level) with a chosen model, through the real execution pipeline, and reports latency / answer / errors.

## Decisions

1. **Placement**: a separate "Test entry" panel below the form (operator chose the split-panel layout), rendered as the last `form-section` on the editor page, **edit mode only** (create mode has no persisted row/entry to test).
2. **Probe type**: one real chat-completion request with a **random math question** (never "ping" — avoids upstream prompt caches). Server generates two random operands + a random operator (+, ×, or subtraction with a positive result) per call. The question, the expected answer, and the model's reply are all returned so the operator can verify.
3. **Execution path — internal pipeline (option 1)**: the handler executes via `h.authManager.Execute(...)` pinned to the target auth with `opts.Metadata[PinnedAuthMetadataKey] = auth.ID`. Translators, executor, proxy/relay, cloak all run exactly as in production. No executor/conductor changes; this mirrors `probeModel` in `model_health_runner.go`.
4. **Applies to all provider types**: entry-bearing providers (openai-compatibility, claude-api-key) pick a specific entry; single-key (gemini, codex, xai, vertex, interactions) and OAuth types test at provider level.
5. **Payload**: OpenAI chat-completions format `{"model", "messages":[{"role":"user","content":"<math q>"}], "max_tokens":200, "stream":false}` with `SourceFormat: openai`; the manager/registry translates per provider.
6. **No form-state coupling**: the panel only reads `form.models` + `form.api_key_entries`; running a test never marks the form dirty.

## Backend

**Endpoint**: `POST /v0/management/upstream-providers/:id/test`
**Body**: `{ "entry_id": <number>|null, "model": "<model-id>" }` — `model` required.

Handler flow (`upstream_providers_test.go`, new file under management):
1. Load the provider row from the upstream provider store (404 if missing).
2. Resolve the target auth from `h.authManager.List()`:
   - Entry-bearing: match attribute `entry_provider_key` with suffix `key-<entry_id>` whose parent `provider_key` is `<channel>:<rowID>`.
   - Non-entry: match `provider_key` suffix `:<rowID>` (compat_name / file_name for OAuth shapes).
   - Not found → 404 with "credential not live in registry — try again after reload" (not 500).
3. Generate the math question server-side; build the payload.
4. Execute with `probe`-style timeout (`modelHealthProbeTimeout` — a deliberate management-probe timeout, same category as model health).
5. Respond `{ ok, latency_ms, question, expected_answer, answer, model, entry_id, error }`. Scheduler/executor errors (incl. model-not-served, cooldown, upstream 4xx/5xx) surface as `ok:false, error` — the pipeline's own cooldown behavior is untouched because we use the same Execute path.

**Math generator** (pure function, own file + test): operands 11–99, operators `+`, `×`, subtraction guaranteed positive; returns question string + expected integer answer.

## Frontend

**New component** `web/dashboard/src/pages/upstream-provider-editor/TestPanel.jsx`, rendered last in the editor body (`index.jsx`), edit mode only:

- **Entry dropdown** (entry-bearing only): `(provider-level)` + one option per entry, labeled by identity with fallback `key-<id>`; disabled entries still listed with a `(disabled)` suffix; entries with `id === 0` (unsaved) are listed disabled with hint "save first".
- **Model dropdown**: options from `form.models` (name/alias), plus a free-text input fallback (models may be empty). Model required before Run.
- **Run test**: POST to the new endpoint; button shows "Testing…" while running (one run at a time); result renders inline: ✓/✗ status, latency, error message on failure, and the Q → expected vs actual answer block. A mismatching answer shows ⚠ (wrong answer) rather than ✗ — the test verifies connectivity, not math ability. The last result stays visible until the next run.
- **API client**: one function `testUpstreamProvider(id, { entryId, model })` in `api/client.js`.

## Error handling

- Auth not live → 404, message shown as-is.
- Scheduler errors (no auth for model, cooldowns) → `ok:false` + message.
- Probe timeout → `ok:false, error:"timeout"` (deliberate exception per AGENTS.md).
- Dashboard network errors → caught and shown in the panel.

## Testing

1. **Go**: math generator unit test (ranges, positive subtraction, answer correctness); handler test for body validation (missing model → 400) and auth-not-found → 404; execution itself is exercised via the model-health pattern, not mocked.
2. **Frontend pure tests** (`editor.test.js`): entry label builder (identity fallback, `(disabled)` suffix, id=0 filter), result mapping (ok/latency/error/answer-match states).
3. **Render test** (`editor-render.test.jsx`): panel renders in edit mode with entries; absent in create mode.
4. **Manual**: `make dash-embed` → test a Claude entry + an openai-compat entry → verify latency + math answer.

## Implementation order

1. Backend: math generator + unit test.
2. Backend: endpoint handler + route + auth resolution + unit tests.
3. Frontend: client function + `TestPanel.jsx` + wiring into `index.jsx`.
4. Frontend: pure + render tests.
5. Full verification: gofmt, `go test`, `go build`, `make dash-embed`.

## Out of scope

- GET /models probing (FetchModelsInline already covers it).
- Batch "test all entries" button.
- Persisting test results (unlike model health).
- Pinning headers/API keys client-side (the pipeline resolves everything).
