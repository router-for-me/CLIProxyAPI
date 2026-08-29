# Playground UI/UX v1

- **Date:** 2026-08-30
- **Status:** Approved design (brainstorming outcome)
- **Scope:** Dashboard → `/playground` page (`web/dashboard/src/pages/PlaygroundPage.jsx`) only
- **Predecessor:** `docs/plans/2026-08-29-playground-feature-design.md` (sets the architecture this design polishes)

## 1. Goals and non-goals

Polish the playground so it reads as a real debugger for the proxy, without expanding its feature surface. v1 ships exactly the features the 2026-08-29 design doc already promised but never wired up — **RawInspectorDrawer, UsageFooter, CooldownBanner, retry buttons, streaming visual feedback, better abort UX** — and migrates the existing controls to the dashboard's hand-rolled component style. No new features.

**Out of scope (deferred to v2 or later brainstorming):**

- Compare mode (N-up side-by-side).
- Saved prompt library / templates.
- Conversation history sidebar (the spec already said YAGNI; we keep it that way).
- Request preset library.
- Token/cost budget meter.
- Vision / tool-call rendering (still shows the "raw-only" yellow banner).

**Visual identity.** Same CSS-variable design language as the rest of the dashboard (no Tailwind, no shadcn, no Radix, no `cmdk`, no `react-syntax-highlighter`). One accent: message bubbles and the streaming indicator get a slightly different treatment — assistant bubbles use `border` + no fill (more "code review" feel than chat-app feel), and the streaming cursor is a soft pulsing dot rather than a static `▍` glyph. The accent signals "this is the debug surface" without breaking the dashboard's visual rhythm.

**YAGNI cuts carried forward.** Single-model only. No persistence (export still the only save path). No new dependencies.

## 2. Layout (the three regions)

The page renders in a single full-height flex row, no outer padding (the shell handles it). Three regions, left-to-right:

### 2.1 Sidebar (left, `var(--sidebar-w)` ≈ 320px, fixed, scrollable)

Top-to-bottom:

1. **Model** — a custom searchable dropdown (text input + filterable grouped list). Each row shows model name, provider chip, and a status badge (`LIVE` / cooldown / catalog-only). Provider groups derived from the existing `usePlaygroundKey`/picker feeds.
2. **Protocol** — styled `<select>` (OpenAI-compat / Gemini / Claude / Codex) with a small "auto" pill next to it when the user hasn't overridden the detector.
3. **Parameters** — a styled panel (system prompt textarea, temperature slider, max tokens, top-p, stream toggle). System-prompt block can collapse via `<details>`/`<summary>`; the rest stays always-open (the form is already short).
4. **Export** — two `<button>`s (JSON / Markdown), same as today, using the existing button styles.

On screens narrower than 1024px, the sidebar collapses to icons or moves behind a toggle (same pattern the rest of the dashboard uses for narrow viewports — defer the exact pattern to implementation; the existing Sidebar component is the reference).

### 2.2 Chat column (center, flex-1)

Three stacked regions:

1. **CooldownBanner** — thin sticky strip at the top, only renders when the selected model has a cooldown chip from the management feed.
2. **Messages** — scrollable, `flex-1`, auto-pins to bottom while the user is near the bottom (see §4).
3. **Composer** — textarea + Send/Stop + a small keyboard hint ("⌘+Enter to send"). The UsageFooter pins just above the composer inside the chat column.

### 2.3 Inspector drawer (right, ~420px, hidden by default)

Toggled by an icon button in the chat column's top-right. When open, slides in from the right with a backdrop. Tabs:

- **Outgoing** — last request body, pretty-printed JSON.
- **Incoming** — last response (or final streamed aggregate), pretty-printed JSON.
- **Headers** — auth + content-type + any custom headers.

Each tab has a **Copy** button (`navigator.clipboard`) and a **Pretty/Raw** toggle (the formatter collapses whitespace in Pretty mode).

## 3. Components (hand-rolled, no new deps)

Every primitive below is a hand-rolled component against `web/dashboard/src/styles/global.css`. No Tailwind utilities beyond the existing token-mapped classes (`bg-muted`, `bg-card`, `text-muted-foreground`, etc.) that are already used throughout the dashboard.

### 3.1 Existing sidebar pieces

- **`ModelPicker`** (refactored) — text input + filterable list, grouped by provider. Rows are `<button>`s. Each row: model name, provider chip, status badge.
- **`ProtocolSwitcher`** — styled `<select>` + "auto" pill (renders when `userPickedProtocol.current === false`).
- **`ParamPanel`** (restyled) — same fields, restyled with `var(--bg-card)` panel and `var(--border)` separators. System prompt wrapped in `<details>`.
- **`ExportButton`** — two `<button>`s using existing button styles.

### 3.2 Chat column

- **`CooldownBanner`** *(new)* — renders only when `cooldownState[providerKey]` exists. Yellow left border, `var(--warning-dim)` background. Includes "Refresh status" link.
- **`MessageBubble`** *(rewritten)* — user bubbles: right-aligned, max-width ~85%, `bg-muted`, rounded. Assistant bubbles: left-aligned, full-width, `border` with no fill. Each assistant bubble gets a small action row: **Copy**, **Inspect** (opens the drawer to that message's payload), **Retry** (re-sends the same prompt with a new `reqId`).
- **Streaming indicator** *(replaced)* — `<span class="playground-streaming-dot">●</span>` styled with a CSS keyframe in `global.css`. Fades in/out while in-flight, disappears on `DONE`.
- **`UsageFooter`** *(new)* — sticky above the composer. Per-message and cumulative: prompt / completion / total tokens, est. cost (from `pricing_sources` if available, "—" otherwise), latency (ms).
- **Composer** — `<textarea>` with `resize-none`, Send `<button>` (primary style) / Stop `<button>` (outline destructive).

### 3.3 Inspector drawer *(new)*

- Right-side slide-in panel.
- Three `<button>`s for tabs + a content swap.
- **Pretty** mode = 2-space JSON formatter (hand-rolled, no highlighter dep).
- **Copy** uses `navigator.clipboard.writeText`.
- **Pretty/Raw** toggle: a small `<button>` that swaps formatter mode.

### 3.4 New CSS additions

All added to `web/dashboard/src/styles/global.css`:

- `.playground-streaming-dot` — pulse keyframe (`@keyframes`).
- `.playground-message--user` / `.playground-message--assistant` — bubble styles.
- `.playground-usage` — sticky footer.
- `.playground-drawer` — right-side slide-in (`transform: translateX` + transition).
- `.playground-cooldown-banner` — yellow left border, `var(--warning-dim)` background.
- `.playground-inspector-tabs` — tab strip styling.
- `.playground-pre` — `<pre>` wrapper for JSON content (scroll + monospace + padding).

## 4. Data flow and state

### 4.1 What stays

- `usePlaygroundKey` — unchanged.
- `usePlaygroundChat` (reducer) — unchanged in its reducer logic; messages grow two new optional fields (see §4.3).
- Protocol-adapter layer (`web/dashboard/src/playground/adapters/*`) — unchanged.
- Export helpers (`web/dashboard/src/playground/exportConversation.js`) — unchanged.

### 4.2 New hooks

**`usePlaygroundInspector`** *(new)* — owns the drawer's payload cache.

```
state: { outgoing, incoming, headers, messageId }
actions:
  recordOutgoing(reqId, body)
  recordIncoming(reqId, body, headers)
  select(messageId)         // jump the drawer to a historical message
  clear()
```

Storage is keyed by `reqId`. A re-send overwrites the previous entry for that `reqId`.

**`useCooldownWatch(providerKey)`** *(new)* — subscribes to `authManager.CooldownStateSnapshot()` (already polled by the alerts runner) and exposes `{ state, refresh }`. Polling cadence matches the existing alerts runner (no new timers).

### 4.3 Message-level fields

Each message in the reducer state grows two optional fields:

- `outgoing` — the body sent (set on `SEND`).
- `incoming` — final response (set on `DONE`; for streamed responses, the adapter's accumulated final payload).

This lets the **Inspect** action on a historical bubble find the right snapshot without re-running the request. Conversation is session-local so storage cost is bounded.

### 4.4 Send flow (updated)

```
1. User clicks Send (or ⌘+Enter in composer).
2. dispatch { type: 'SEND', id: reqId, model, protocol, userText, params }
3. inspector.recordOutgoing(reqId, body)
4. POST → openStream({ onChunk, onDone, onError })
5. onChunk → adapter.parseStreamChunk → dispatch { type: 'TOKEN' }
6. onDone  → adapter.parseStreamFinal → dispatch { type: 'DONE', usage }
              inspector.recordIncoming(reqId, finalBody, responseHeaders)
7. onError → dispatch { type: 'ERROR', error }
              inspector.recordIncoming(reqId, errorPayload, responseHeaders)
```

The reducer no longer touches the inspector directly — it just tracks message state.

### 4.5 Retry flow

The `Retry` button on an assistant message dispatches a new `SEND` with a fresh `reqId` but identical params + user text. The inspector overwrites by `reqId`. The message list appends a new bubble (we don't mutate the existing one — keeps history honest about what was actually sent).

### 4.6 Streaming scroll

A small `useEffect` watches `state.messages[last].content.length` and scrolls the messages container to the bottom if `scrollHeight - scrollTop - clientHeight < 80px` (i.e., the user is "near the bottom"). If they've scrolled up to read, no auto-scroll (the pulsing-dot indicator is enough).

### 4.7 Composer keyboard

- **⌘+Enter / Ctrl+Enter** — send.
- **Esc** — abort in-flight (calls `abortRef.current.abort()`).

## 5. Error handling, testing, and shipping

### 5.1 Error layers (per the 2026-08-29 doc, refined)

| Failure | Severity | Treatment |
|---|---|---|
| Cooldown (transient) | warning | Yellow `Alert` banner at top of chat column. Doesn't block send; surfaces result honestly. |
| Network / upstream 5xx | recoverable | Amber `Alert` under affected message + **Retry** button (preserves outgoing payload). |
| Upstream 4xx | not retryable | Red `Alert` under affected message with normalized message + expandable raw body. **No Retry** button. |
| Bootstrap failure | blocking | Full-page red `Alert` with **Retry** button (calls `refreshKey`). Unchanged. |
| Stream interrupted mid-flight | recoverable | Same as 5xx. |
| Vision / tool content | info | Yellow inline note "Content not rendered in playground; raw payload still inspectable." Inspector still shows full body. |
| User-initiated abort | info | Red info-only "Stopped" message (not an error severity). |

Error philosophy from the original doc still holds: every error gets three layers — short user-facing message, structured payload in the error banner (expandable), and full raw response in the inspector drawer.

### 5.2 Abort UX

While a request is in flight:

- Composer swaps Send → Stop.
- Stop button uses outline destructive hover style.
- The streaming dot pulses faster (shorter animation cycle) to signal "actively working."
- Clicking Stop fires `abortRef.current.abort()` → dispatch `ERROR { kind: 'aborted' }` → info "Stopped" message in the conversation.

### 5.3 Testing

Four layers, matching the rest of the repo:

1. **Unit tests for new hooks** — `usePlaygroundInspector.test.js` (record/select/overwrite by `reqId`), `useCooldownWatch.test.js` (mocked snapshot, banner visibility).
2. **Component tests** — `MessageBubble.test.jsx` (user vs assistant variant, action row, retry click), `UsageFooter.test.jsx` (cost fallback to "—"), `CooldownBanner.test.jsx`.
3. **Visual smoke** — `npm run dev`, manually exercise: pick model → send → watch streaming → click **Inspect** → see payload → click **Retry** → see new bubble → toggle theme → collapse sidebar on a narrow viewport.
4. **No backend tests** — no Go code changes (still true).

### 5.4 Shipping

- `make dash-embed` produces the new binary. No target changes; commit the new page + the embedded `dist/` refresh.
- Add a paragraph + a screenshot to `docs/playground.md`.
- No new dependencies — all work lives in `web/dashboard/src/pages/PlaygroundPage.jsx`, `web/dashboard/src/playground/*`, and `web/dashboard/src/styles/global.css`.

### 5.5 Definition of done

On top of the 2026-08-29 checklist, the v1 polish is done when:

- [ ] Raw inspector drawer: open, switch tabs (Outgoing / Incoming / Headers), Copy, Pretty/Raw toggle.
- [ ] `CooldownBanner` renders correctly against a stubbed snapshot and disappears when the snapshot clears.
- [ ] `UsageFooter` shows prompt/completion/total + cost (or "—" if pricing is missing) + latency.
- [ ] Retry on an assistant message re-sends identical body with a new `reqId`; history appends honestly.
- [ ] Streaming dot pulses during in-flight, gone after `DONE`.
- [ ] Composer keyboard: ⌘+Enter sends, Esc aborts in-flight.
- [ ] No raw inline color values in the new components; everything uses CSS variables (`var(--bg-card)`, `var(--accent)`, etc.).
- [ ] Light + dark theme parity for every new piece.
- [ ] `npm run lint` clean, `npm test` passes, `npm run build` clean.
- [ ] `make dash-embed` produces a binary that serves `/playground` from `/dashboard`.
- [ ] No new dependencies in `web/dashboard/package.json`.
- [ ] `gofmt -w .` and `go build -o nixllm ./cmd/server` still pass (no Go changes).

## 6. Risks and follow-ups

- **Compare mode** is the most-asked-for v2 feature; explicit deferred-to-v2 to keep v1 focused.
- **Inspector drawer width** is fixed at ~420px; if a future user wants resizable, that's a small follow-up (one `resize` handler + a CSS variable).
- **Pretty-print perf** for very large responses: the hand-rolled formatter is fine for the typical payload sizes; if a use case shows up with multi-MB responses, add a size cap (e.g., truncate Pretty mode above 500KB and fall back to Raw).
- **Cooldown subscription coupling**: `useCooldownWatch` reuses the alerts runner's snapshot. If the runner is disabled in a deployment, the banner simply won't appear (graceful — no separate polling path).
