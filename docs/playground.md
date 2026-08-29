# Playground

The playground is the `/dashboard/playground` page: an in-browser chat surface
that lets operators try any model exposed by the proxy against the
playground key, without leaving the management UI.

## v1 polish (2026-08-30)

The playground page now includes the features the design doc originally
promised but never wired up:

- **Raw inspector drawer** — click *Inspect* on any assistant message
  to see the outgoing JSON, incoming JSON, and headers. Pretty/Raw
  toggle. Copy button.
- **Usage footer** — shows prompt / completion / total tokens and an
  estimated cost (when pricing is available).
- **Cooldown banner** — yellow strip at the top of the chat column
  when the selected model's provider is in cooldown.
- **Retry button** — re-sends the same prompt and parameters with a
  new request id; history appends honestly.
- **Streaming indicator** — replaces the old `▍` glyph with a pulsing
  dot that speeds up mid-flight.
- **Keyboard shortcuts** — ⌘+Enter sends, Esc aborts.
- **Searchable model picker** — the Catalog / Upstream tab strip is
  gone; a single text input filters a unified, provider-grouped list
  with per-row status badges (LIVE / COOLDOWN).

No new dependencies. Compare mode and saved prompts remain deferred
to v2.

### Cooldown banner (graceful degradation)

The banner reads `window.__nixllmCooldownSnapshot`, which is published
by the Go-side alerts runner when its `CooldownStateSnapshot()` finds
a provider in cooldown. The frontend hook (`useCooldownWatch`) falls
back to `null` when no snapshot has been published, so the banner
renders nothing rather than crash. The publisher will land alongside
the matching `/v0/management/alerts/cooldown-snapshot` endpoint.
