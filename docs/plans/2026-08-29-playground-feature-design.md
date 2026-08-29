# Playground / Chat Feature

- **Date:** 2026-08-29
- **Status:** Approved design
- **Scope:** Dashboard → new top-level page `/playground`

## 1. Goals and non-goals

Add a chat / playground feature to the NixLLM dashboard that lets operators test any LIVE model from the upstream providers or from the model catalog, end-to-end through the proxy. The feature targets power users and serves three overlapping purposes in one page:

- **Debug & troubleshoot:** verify a model is actually working — latency, raw request/response, upstream error semantics.
- **Demo & exploration:** send prompts to models and inspect their behavior, including side-by-side comparison.
- **Ad-hoc integration testing:** confirm request/response shape compliance for each protocol the proxy translates.

Non-goals for v1:

- Persisting conversations across sessions (export instead).
- Rendering vision/image content, tool calls, or function-calling loops in the chat UI (raw payload still inspectable).
- Replacing any existing page (ModelsCatalog, ModelHealth, AutoRouter) — the playground is additive.
- Any new Go backend endpoint. Everything routes through existing standard proxy routes.

## 2. Architecture & routing

Two-path design so the UI uses management auth while chat traffic uses proxy auth:

```
Browser (Playground UI)
    │
    ├─ /v0/management/*  (auth via MANAGEMENT_PASSWORD — existing dashboard client)
    │     ├─ GET /upstream-providers
    │     ├─ GET /model-health
    │     ├─ GET /models-catalog
    │     └─ POST /api-keys            (idempotent create-if-missing for "playground" key)
    │
    └─ Standard proxy routes (auth via dedicated "playground" key)
          ├─ /v1/chat/completions      (OpenAI-compat, Codex)
          ├─ /v1/messages              (Claude)
          └─ /v1beta/models/{m}:generateContent   (Gemini)
```

The split is intentional: management calls reuse the existing dashboard auth (`MANAGEMENT_PASSWORD` bearer); chat calls go through the proxy's standard routes so every auth/quota/policy/routing path is exercised exactly the way a real client would exercise it. This is the whole point of a proxy playground.

## 3. Dedicated playground key

On first load of `/playground`, `usePlaygroundKey` ensures a key named `playground` exists:

- Calls `POST /v0/management/api-keys` with `name: "playground"` and a permissive policy (`rpm: 10000`, `tpm: 10000000` — effectively unlimited for human-driven testing).
- On `409 Conflict` (already exists), falls back to `GET /api-keys?name=playground` and uses the returned key.
- Caches the key value in `localStorage[nixllm.playground.key]` and a module-level ref for the session.
- If the management password is rotated, the proxy key itself still works, but the next bootstrap call fails until the user re-authenticates; the UI surfaces this as a recoverable error.

Playground traffic is therefore attributable in usage stats under the `playground` key without polluting a user's real keys.

## 4. Frontend structure

New page: `web/dashboard/src/pages/PlaygroundPage.jsx`. Top-level route `/playground`. Sidebar entry placed next to *Model Health* / *Cooldown Providers*.

Component tree:

```
PlaygroundPage
├── <ModelPicker>          # Two tabs: Catalog | Upstream
│     ├── <CatalogTab>     # From GET /models-catalog
│     └── <UpstreamTab>    # From GET /upstream-providers ∩ GET /model-health
│                          #   filtered to LIVE only, with cooldown chip on each
├── <ProtocolSwitcher>     # OpenAI-compat | Gemini | Claude | Codex
├── <ParamPanel>           # system prompt, temperature, max_tokens, top_p, stream toggle
├── <Conversation>         # multi-turn message bubbles
├── <ResponsePanel>        # streaming assistant output
├── <CompareGrid>          # optional N-up mode (same prompt → N models)
├── <RawInspectorDrawer>   # right-side drawer: Outgoing JSON | Incoming JSON | Headers
├── <UsageFooter>          # token counts, est. cost per message and cumulative
└── <ExportButton>         # download conversation as JSON or Markdown
```

State uses a single `useReducer` per-conversation (no new global state library). Compare mode fans out N parallel requests from one dispatch, each with its own raw inspector + usage footer.

The shared hook `usePlaygroundKey()` owns the bootstrap described in §3.

## 5. Protocol adapters

Each protocol is encapsulated as a small adapter object so the chat pipeline is protocol-agnostic:

```
interface ProtocolAdapter {
  id: 'openai-compat' | 'gemini' | 'claude' | 'codex';
  endpoint(model: string): string;
  buildRequest(model, messages, params): unknown;
  parseStreamChunk(chunk): { token, done, usage? };
  buildErrorPayload(err): { message, type };
  supports: { streaming, tools, vision, system_prompt };
}
```

Endpoints and auth headers per protocol:

| Protocol | Endpoint | Auth |
|---|---|---|
| OpenAI-compat | `/v1/chat/completions` | `Authorization: Bearer <key>` |
| Codex / Responses | `/v1/responses` | `Authorization: Bearer <key>` |
| Claude Messages | `/v1/messages` | `x-api-key: <key>` + `anthropic-version` |
| Gemini native | `/v1beta/models/{model}:generateContent` (`:streamGenerateContent?alt=sse` when streaming) | `x-goog-api-key: <key>` |

Per-model protocol is auto-detected from the registry provider type. Advanced users can override via `<ProtocolSwitcher>` to test translator behavior (e.g. force Gemini protocol on a model that's normally exposed as OpenAI-compat).

## 6. Chat flow

```
1. User clicks Send.
2. adapter.buildRequest(model, history, params) → wire body.
3. Snapshot body → RawInspectorDrawer (Outgoing JSON).
4. POST to adapter.endpoint(model) with playground key.
5. If stream:
     read SSE chunks → adapter.parseStreamChunk → render incrementally.
     On done: snapshot final response → RawInspector (Incoming JSON) + UsageFooter.
   Else:
     await JSON → render → snapshot.
6. On error: adapter.buildErrorPayload → red banner with normalized message + raw payload.
7. Append to conversation history (multi-turn).
```

## 7. Edge cases

- **Model goes un-LIVE mid-conversation** — yellow banner at top of `<ResponsePanel>` ("This model's auth is now in cooldown; subsequent messages may fail or auto-route"). In-flight messages finish.
- **Streaming interrupted** — three failure modes in the UI:
  - Network error: amber banner, "Retry" button with same body.
  - Upstream 4xx: red banner, show payload, no auto-retry.
  - Upstream 5xx: amber banner, "Retry" button.
- **Compare-mode protocol mismatch** — each cell shows its own protocol chip so the operator knows why responses differ.
- **Token usage missing** — display "—" instead of `undefined`; cost = `(prompt_tokens * input_price + completion_tokens * output_price) / 1e6` using the existing `pricing_sources` table.
- **Vision / tools in v1** — out of scope for chat rendering. If a message contains images or `tools`, show a yellow banner ("This message contains content not yet rendered in the playground; raw payload still inspectable"). Raw inspector still shows full payload.
- **Bootstrap failure** — recoverable error with explicit "Retry" button; the page never silently swallows auth failures.

Error philosophy: every error gets three layers — short user-facing message, structured payload in the error banner (expandable), and full raw response in the inspector drawer. Console log for debugging.

## 8. Testing strategy

Four layers, matching the rest of the repo:

1. **Unit tests for protocol adapters** — pure functions, fixture payloads (sample OpenAI, Gemini, Claude, Codex responses) under `web/dashboard/src/pages/PlaygroundPage.adapters.test.js`.
2. **Hook test for `usePlaygroundKey`** — mock `fetch`, verify idempotent bootstrap, 409 fallback, cache-hit short-circuit.
3. **Component smoke test** under `web/dashboard/tests/playground.smoke.spec.js` (or `.test.jsx`): render picker, send, assert streamed tokens appear, raw inspector populates, export downloads JSON.
4. **No backend tests** — no Go code changes.

## 9. Build, embed, and shipping

- Add sidebar entry in `web/dashboard/src/components/Sidebar.jsx`.
- Add route in `App.jsx`.
- `make dash-embed` already handles `npm run build` → copy `dist/` → `go:embed` rebuild. No target changes; commit the new page and the embedded dist refresh.
- Add a short section to `docs/playground.md` describing the feature. No new endpoint to add to `developerDocs.js`.

## 10. Definition of done

- [ ] All four protocol adapters pass unit tests.
- [ ] `usePlaygroundKey` tested for create + 409-fallback + cache-hit.
- [ ] Manual smoke: pick model → send → stream → inspect → compare two models → export.
- [ ] `npm run build` clean.
- [ ] `make dash-embed` produces a binary that serves `/playground` from `/dashboard`.
- [ ] No new Go code; `gofmt -w .` and `go build -o nixllm ./cmd/server` still pass.
