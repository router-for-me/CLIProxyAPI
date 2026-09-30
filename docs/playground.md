# Playground

The playground is the `/dashboard/playground` page: an in-browser chat surface
that lets operators try any model exposed by the proxy against the
playground key, without leaving the management UI.

## UI/UX overhaul (2026-10)

The page was previously styled with Tailwind utility classes, but the
dashboard ships no Tailwind — so the layout never applied. It now uses the
dashboard design system (`global.css` tokens/classes + `components/Primitives`).

- **Full-height layout** — `App.jsx` adds `main--flush` on this route; a left
  settings rail (model / protocol / parameters / export) sits beside a chat
  column with a header showing the active model. Below 900px the rail becomes
  an off-canvas drawer toggled from the chat header.
- **Auto-scroll** — streaming replies stay pinned to the newest token; if the
  user scrolls up, autoscroll pauses and a "Jump to latest" pill appears.
- **New chat** — clears the conversation and inspector (confirms when
  non-empty).
- **Per-message Retry** — re-runs the user prompt that produced that specific
  assistant message, replacing it in place (messages after it are dropped).
- **Composer** — Enter sends, Shift+Enter inserts a newline (⌘/Ctrl+Enter also
  sends); Esc aborts while streaming; disabled Send explains why via `title`.
- **Markdown** — assistant messages render via `react-markdown` +
  `remark-gfm` (code blocks, tables, lists, links); user messages stay plain.
- **Accessibility** — keyboard-navigable model list, inspector focus trap +
  Esc-to-close, `prefers-reduced-motion` disables the streaming pulse.

## Model list (per upstream provider)

The picker lists models grouped by upstream provider (`GET /upstream-providers`
returns each provider's `models[]`). Each row shows the model's **alias** — the
client-facing id a request must send (`buildConfiguredModelInfo` sets
`ModelInfo.ID = alias || name`) — and, when the alias differs from the upstream
name, a muted `→ name` mapping so the operator knows what will actually be
proxied. Selecting a row sets the model to the alias (or the name when no alias
is set).

- Provider group headers show the provider name + `provider_type`, plus a
  **LIVE** dot (from `upstream-providers/live-status`) or **COOLDOWN** badge
  (from `/cooldown-providers`), degrading gracefully when unavailable.
- Catalog-only models stay discoverable in fallback groups.
- Search matches provider name/type, model name, and alias.

## Live telemetry

The previous version read `window.__nixllm*` globals that nothing published,
so the usage footer, cooldown banner, and headers tab were always empty. They
are now wired to real endpoints:

- **Cooldown banner + model badges** — `GET /v0/management/cooldown-providers`
  (`useCooldownWatch`, auto-refreshed ~10s), matched by provider/model.
- **Usage footer** — usage is parsed from the SSE stream per adapter
  (`parseStreamChunk`); non-streaming responses are parsed via
  `parseResponse`. Cost uses `GET /models-catalog/:id/pricing`.
- **Inspector** — real outgoing body, assembled incoming response + usage,
  and response headers. Pretty/Raw toggle + Copy.

## Protocol adapters

Each protocol owns its wire format (`playground/adapters/*`): endpoint, request
body, stream-chunk parsing (including usage), non-stream response parsing, and
error normalization. The chat pipeline calls `buildRequest`,
`parseStreamChunk`, and `parseResponse` and never branches on protocol id.

Compare mode and saved prompts remain deferred to a later version.
