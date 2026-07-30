# ADR 0003: Split state — TanStack Query for server, Zustand for UI

**Status**: Accepted
**Date**: 2026-07-27

## Context

The primary pain point that triggered the rewrite is **multi-view state
interference**: in the current single-file front end, the `/` overview and
the `/usage` detail view share one global namespace. Filters, Chart.js
instances, the 30-second refresh timer, and the cursor-pagination state of
one view leak into the other. Changing one table's render order can break
the other view.

React alone does not solve this. React's `useState`/`useReducer` is local
to a component subtree, but the two views are not nested — they are sibling
routes. Sharing filter state across routes via Context re-renders every
consumer on every change. The question is what state library to use, and
how to partition state.

## Decision

Split state by **kind**, not by view:

- **Server state** (data that originates from the API: summary, timeseries,
  request rows, pricing, aliases, collector health) is owned exclusively
  by **TanStack Query** (React Query). Each query has a cache key scoped
  by `[view, range, filters...]`. TanStack Query owns cache invalidation,
  polling, retries, background refresh, and `staleTime`.
- **UI state** (data that lives only in the browser: column-visibility
  settings, chart-mode toggles, drawer open/close, selected request row
  for the detail drawer) is owned by **Zustand** stores. Stores are sliced
  by domain, not by view: `filtersStore`, `settingsStore`, `uiStore`.
  Zustand selectors give per-component subscriptions that do not
  re-render the whole tree.

The two layers communicate in one direction: UI state changes call
TanStack Query's `queryClient.invalidateQueries({ queryKey })`. Server
state never writes to Zustand.

Redux Toolkit was considered and rejected as over-engineered for a two-view
dashboard; its boilerplate-to-value ratio is wrong here.

## Alternatives considered

- **React Context + `useReducer`.** Rejected: every context value change
  re-renders every consumer. With two sibling routes and shared filter
  state, this reproduces the current interference.
- **Redux Toolkit.** Rejected: wrong size for the problem. No
  time-travel debugging need, no middleware need, two views.
- **Jotai / Recoil (atomic state).** Rejected: atomic model fits
  fine-grained UI state but does not solve the server-state problem
  (caching, polling, retries). Picking TanStack Query for server state
  already covers the hard half; Zustand covers the rest with a smaller
  learning curve than atomic libraries.
- **Single Zustand store for everything.** Rejected: server state in
  Zustand loses TanStack Query's caching, dedup, and background refresh.
  Server state and UI state have fundamentally different lifecycles.

## Consequences

**Positive**

- The two sibling-route views cannot interfere: each view's server state
  is isolated by its TanStack Query cache key, and UI state slices are
  subscribed to granularly.
- Polling, retry, and stale-while-revalidate are no longer hand-written
  `setInterval` + `fetch` + `setState` — they are TanStack Query config.
- The 30-second refresh and the cursor-pagination state become query keys
  and `nextCursor` page params, not module-level globals.

**Negative**

- Two state libraries instead of one. Mitigated by the clear ownership
  rule: "from the API → TanStack; from the user → Zustand".
- TanStack Query's cache is in-memory; a hard refresh re-fetches. This is
  already the current behavior and is correct for live analytics.

**Neutral**

- `queryClient` lives at the React Router root. Per-view invalidation
  happens via the shared client.
