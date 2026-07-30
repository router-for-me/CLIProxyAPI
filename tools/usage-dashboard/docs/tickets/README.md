# Usage Dashboard Rewrite — Ticket Index

**Total: 30 tickets across 5 phases. Every ticket enforces Red → Green →
Refactor TDD and the 9-step completion gate (Lint → Type Check → Build →
Unit → Integration → Functional → Contract → E2E for UI → Code Review).**

## Quick stats

- 30 tickets
- 5 phases
- Each ticket: mandatory TDD + 9-step completion gate
- UI tickets additionally require Playwright E2E

## Phase overview

| Phase | Tickets | Focus | Key deliverable |
|-------|---------|-------|-----------------|
| **0** Foundation | 4 | Project skeleton | `uv sync` + `pnpm dev` working; legacy untouched |
| **1** Back end | 9 | FastAPI cutover | All `/api/v1/*` served by FastAPI; `server.py` deleted |
| **2** Contract | 3 | OpenAPI → TypeScript | Generated `types.ts`; CI freshness guard |
| **3** Overview | 8 | React `/` view | Pixel-comparable to legacy; Playwright green |
| **4** Usage | 7 | React `/usage` view | All 3 tabs, infinite scroll, charts; Playwright green |
| **5** Polish | 5 | Cutover + docs | Legacy deleted; full E2E; docs updated |

## Dependency graph

```
Phase 0 ─────────────────────────► Phase 1 ─────► Phase 2 ─────► Phase 3 ─────► Phase 4 ─► Phase 5
[foundation]                       [backend]      [contract]     [overview]     [usage]    [cutover]
0.1 ─┬─ 0.2 ─┬─ 0.3 ── 0.4         1.1 ─┬─ 1.2 ──┬─ 1.3         3.1 ── 3.2 ─┬─ 3.3
     │        │                         │        │                            ├─ 3.4 ─┬─ 3.5
     │        │                         ├─ 1.4 ──┼─ 1.5                        │       ├─ 3.6
     │        │                         │        ├─ 1.6                        │       └─ 3.7 ── 3.8
     │        │                         │        ├─ 1.7                        └─ 3.7
     │        │                         │        └─ 1.8 ── 1.9
     │        │                         └─ 1.3
     │        │
     └─ 0.2 ──┴─ 0.3
```

Within a phase, tickets marked **parallelizable** can be done in any order
or concurrently. Cross-phase dependencies are strict.

## Every ticket contains

1. **Goal** — what success looks like in one paragraph.
2. **Files touched / NOT touched** — scope boundary.
3. **🔴 Mandatory TDD discipline** — Red → Green → Refactor, one commit per step.
4. **🪜 Steps** — each is one TDD cycle with a concrete verification command.
5. **✅ Completion gate** — 9 checks in fixed order:
   - Lint
   - Type Check
   - Build
   - Unit Tests
   - Integration Tests
   - Functional Tests
   - Contract Tests
   - **E2E** (required for any ticket touching UI behavior; N/A otherwise)
   - Code Review

## Reading order for an implementer

1. `docs/adr/0001` through `0006` — architectural decisions.
2. `docs/rewrite-plan.md` — phase-level overview.
3. `docs/tickets/phase0-foundation/README.md` → start at Ticket 0.1.
4. Each ticket's **Completion gate** is the definition of done.

## Where to start right now

**Ticket 0.1** (`011-pyproject-uv-skeleton.md`). It has no blockers and
unblocks everything downstream.
