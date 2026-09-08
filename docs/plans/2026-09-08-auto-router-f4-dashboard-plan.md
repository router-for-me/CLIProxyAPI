# Auto Router F4 — Dashboard Tabs Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Turn the Auto Router Analysis page into 4 tabs sharing the existing controls (router / API key / time range): Overview (enriched), Decision distribution, Simulation, Replay.

**Architecture:** `AutoRouterAnalysisPage.jsx` gains a tab state and per-tab components in new files under `web/dashboard/src/components/autorouter/`. Charts are inline SVG (no libraries). API client additions in `client.js` for the four F3 endpoints + the profile GET/PUT (profile endpoints existed server-side but were never consumed by the dashboard).

**Tech Stack:** React 19 + Vite SPA, plain CSS classes already used by the page (`card`, `stats-grid`, `table`, `stat-card`).

**Working directory:** `/home/bilfid/projects/nixllm` (main, inline).

**Verified facts:**
- Endpoints (F3, all live): `GET /v0/management/auto-routers/stats?top=decision-stats|performance`, `POST /v0/management/auto-routers/:id/profile/simulate`, `GET /v0/management/auto-routers/:id/decisions/:request_id`; profile GET/PUT `/auto-routers/:id/profile` (shape: `{router_id, version, hash, config: ProfileConfig, is_default}`; PUT body `{config}` per handler).
- `ProfileConfig` JSON: `{thresholds:{simple_max,medium_max,complex_max}, weights:{token_count,code_presence,reasoning_markers,technical_terms,simple_indicators,multi_step_patterns,question_complexity}, keyword_tier_rules:[{id,tier,keywords[]}]}`.
- decision-stats response key: `decision_stats` `{event_count, score_histogram:[{bucket,count}], dimension_averages:{...7}, cause_counts:{literal_keyword_match,complexity_scorer}, fallback_chains:[{chain,count}], mismatch_count}`.
- performance response key: `performance: [{tier, model, request_count, p50_latency_ms, p95_latency_ms, avg_ttft_ms, cost_usd, error_count, error_rate}]`.
- decisions list response: `{router_id, decisions:[autoRouterDecisionResponse], page, page_size, total, total_pages}` — each row already carries tiers, cause, profile v/h, tokens, cost, and `auto_router_decision` (full snapshot jsonb).
- replay response: `{router_id, request_id, decision, event:{latency_ms, ttft_ms, failed, ...}}`.
- simulate response: `{router_id, candidate_hash, events, total_snapshot_events, truncated, moves:[{from,to,count,sample_request_ids}], confusion:[{from,to,count}], moved_count, unsimulable_rules, unsimulable_count, from, to}`.
- Decision jsonb `profile_snapshot` carries `thresholds` — used to draw threshold lines on the histogram.
- Router dropdown on Analysis page uses `r.model_id` as value (router_id = model_id for stats queries, per [[auto-router-analysis-fixes]] memory). NOTE: the new per-router endpoints take the **PK id** (`:id`), not model_id — the router list response rows carry `id`; the page must keep both.

---

### Task 1: API client functions

**Files:** Modify `web/dashboard/src/api/client.js`

Add after `getAutoRouterStats`:
- `getAutoRouterDecisionStats(params)` → GET `/auto-routers/stats?top=decision-stats&...`
- `getAutoRouterTierPerformance(params)` → GET `/auto-routers/stats?top=performance&...`
- `getAutoRouterProfile(id)` → GET `/auto-routers/{id}/profile`
- `putAutoRouterProfile(id, config)` → PUT `/auto-routers/{id}/profile` body `{config}`
- `simulateAutoRouterProfile(id, {config, from, to, api_key_id})` → POST `.../profile/simulate`
- `getAutoRouterDecision(id, requestId)` → GET `/auto-routers/{id}/decisions/{requestId}`

**Verify:** `cd web/dashboard && npm run build` passes.

**Commit:** `feat(dashboard): auto-router observability API client functions`

### Task 2: Tab framework + Overview enrichment

**Files:** Modify `web/dashboard/src/pages/AutoRouterAnalysisPage.jsx`

- Tab state (`overview | distribution | simulation | replay`); simple button row styled like existing toolbar. Router dropdown value stays `model_id` for stats; store the whole selected router row (`{id, model_id}`) so per-router tabs can address the PK.
- Overview tab = existing two views, plus:
  - tier cards get a cause badge line (from decision-stats `cause_counts`, share of keyword-routed);
  - model table gains p50/p95 latency and error-rate columns (from `getAutoRouterTierPerformance`, joined on model id).
- Keep existing empty/loading states intact.

**Commit:** `feat(dashboard): analysis page tab framework + overview enrichment`

### Task 3: Decision distribution tab

**Files:** Create `web/dashboard/src/components/autorouter/DecisionDistributionTab.jsx`; wire into the page.

- Histogram SVG: 20 bars from `score_histogram`; vertical dashed threshold lines at the profile's `simple_max/medium_max/complex_max` (from the newest decision's `profile_snapshot`, fetched via the decisions list first row; fall back to built-ins 0.15/0.35/0.60 when absent).
- Dimension averages: 7 horizontal bars with value labels.
- Cause: two stat cards (keyword vs scorer) + share %.
- Fallback chains: table (chain → count), chain rendered as `reasoning → complex`.
- Mismatch count with a link that switches to the Replay tab filtered by mismatch (`effective_tier != mapping_tier` — Replay tab filter computed client-side from the decisions list since the list endpoint supports both tier filters).

**Commit:** `feat(dashboard): decision distribution tab (histogram, dimensions, causes, chains)`

### Task 4: Simulation tab

**Files:** Create `web/dashboard/src/components/autorouter/SimulationTab.jsx`; wire into the page.

- Load current profile via `getAutoRouterProfile(routerId)` (fallback to built-in defaults when `is_default`).
- Form: 3 threshold number inputs (0..1 step 0.01), 7 weight inputs, keyword rules editor (rows: id, tier select, keywords comma-separated; add/remove row). Local state only — nothing written until operator acts.
- **Simulate over range** button → `simulateAutoRouterProfile` → results panel: confusion matrix table (from × to with counts), moved % of events, moves list with sample request ids (each id clickable → switches to Replay tab and opens that request), truncated/unsampled hints, unsimulable banner: "N rule(s) could not be simulated (evaluated on stored signals only)" listing the IDs.
- **Apply** button (explicit, second step): `putAutoRouterProfile(routerId, candidateConfig)` after a confirm; disabled until a simulation has been run (guard against apply-without-preview).

**Commit:** `feat(dashboard): profile simulation tab (dry-run + explicit apply)`

### Task 5: Replay tab

**Files:** Create `web/dashboard/src/components/autorouter/ReplayTab.jsx`; wire into the page.

- Reuse the decisions list endpoint (`GET /auto-routers/:id/decisions` — needs a small client fn `listAutoRouterDecisions(id, params)`): table with time, scored→effective tier, mapping tier, cause, target model, tokens, cost, status dot (failed red).
- Row click → `getAutoRouterDecision(routerId, request_id)` → detail panel: full snapshot (score_total, all score fields, reasoning markers, matched rules table, fallback chain, mapping tier, target model, profile v/hash) + event metrics (latency, ttft, tokens, cost, failure status).
- Filters: tier selects + cause + mismatch-only toggle (client-side or via existing query params — the endpoint already supports scored_tier/effective_tier/mapping_tier/decision_cause/target_model/profile_hash).
- Accepts an external `focusRequestId` prop (from Simulation samples) that auto-opens the detail panel.

**Commit:** `feat(dashboard): decision replay tab with detail panel`

### Task 6: Build + embed + verification

- `cd web/dashboard && npm run build` — no build errors.
- `make dash-embed` (builds SPA + copies into `internal/dashboardasset/dist` + rebuilds Go binary) — compile check OK.
- Manual smoke: `go run ./cmd/server` + open `/dashboard` → Analysis page tabs render; histogram draws; simulate round-trips against a router with data (requires PG; if PG unavailable in this environment, verify endpoints via unit-level store tests already green and state that the manual pass is pending).
- Design doc Progress line; commit; `git add -f` for docs.

## Explicit non-goals

- No chart library; no new CSS framework; no changes to Go API beyond what F3 shipped.
- No auto-apply of simulated profiles; no profile version history UI (YAGNI).
