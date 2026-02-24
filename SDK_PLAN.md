# Unified SDK Architecture Plan

**Date:** 2026-02-23

> **See also:** 
> - `thegent/docs/plans/LITELLM_CLIPROXY_BIFROST_HARMONY.md`
> - `thegent/docs/research/BIFROST_RESEARCH_2026-02-20.md`
> - `thegent/docs/plans/2026-02-16-litellm-full-features-plan.md`
> - `thegent/docs/plans/CLIPROXY_API_AND_THGENT_UNIFIED_PLAN.md`
> - `thegent/docs/plans/CODEX_DONUT_HARNESS_PLAN.md`

---

## Key Research Findings

### From BIFROST_RESEARCH_2026-02-20.md
- Bifrost has **plugin/extension system** - governance, cache, logging, telemetry
- Go SDK for embedded use
- Can write custom extensions
- 15+ providers, semantic caching

### From CLIPROXY_API_AND_THGENT_UNIFIED_PLAN.md
- cliproxyapi-plusplus already supports: Cursor, MiniMax, Factory Droid, Kilo, Roo Code
- Provider blocks with OAuth parity

### From CODEX_DONUT_HARNESS_PLAN.md
- Unified harness: Queue, Harvest, MCP tools
- Shared across Claude Code, Codex, Cursor, Factory droid

---

## Revised Architecture (Best for Robustness)

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         thegent (Python)                              │
│              - Agent orchestration, MCP server, hooks                  │
│              - Queue, Harvest (donut layer)                         │
└─────────────────────────────────┬───────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                  heliosHarness (Controlled Harness Layer)              │
│         - Where agents connect: Claude Code, Codex, Droid           │
│         - Provides lifecycle management, hooks                        │
│         - agentapi MOUNTS ONTO this harness                        │
└─────────────────────────────────┬───────────────────────────────────┘
                                      │
                                      ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                         agentapi (intermediary layer)                 │
│         - Transformations, validation                                │
│         - Custom logic between thegent and proxy                   │
│         - Own Bifrost extension (recommended)                     │
└─────────────────────────────────┬───────────────────────────────────┘
                                      │
                     ┌────────────────┴────────────────┐
                     ▼                                 ▼
┌──────────────────────────┐          ┌──────────────────────────┐
│   agentapi Bifrost     │          │   cliproxy+bifrost    │
│   (extension)          │          │   (bundled Go)        │
│   - Custom routing    │          │   - 15+ providers     │
│   - Session-aware    │          │   - OAuth providers   │
│   - Agent rules      │          │   - minimax, kiro    │
└──────────────────────────┘          └──────────────────────────┘
```

### Layer Explanation

| Layer | Description |
|-------|-------------|
| **thegent** | Orchestration, MCP server, hooks, donut (queue/harvest) |
| **heliosHarness** | Controlled harness - where Claude Code, Codex, Droid, and other agents connect. **agentapi MOUNTS onto this harness** |
| **agentapi** | Intermediary + routing extension (sits on heliosHarness) |
| **cliproxy+bifrost** | Core proxy (bundled Go) |

1. **Isolation**: Agent routing separate from proxy routing
2. **Custom rules**: Agent-specific routing logic
3. **Session-aware**: Per-session load balancing
4. **Extensibility**: Can add custom Bifrost extensions

---

## Implementation Steps

### Step 1: Bundle Bifrost with cliproxyapi-plusplus
- [ ] Add Bifrost to cliproxyapi-plusplus build
- [ ] OpenAPI spec generation
- [ ] Single Go binary

### Step 2: Generate Python SDK
- [ ] openapi-generator for Python client
- [ ] Include in thegent

### Step 3: Create agentapi Bifrost extension
- [ ] Custom routing extension for agentapi
- [ ] Session-aware logic
- [ ] Connect to cliproxy+bifrost downstream

### Step 4: Update thegent
- [ ] Connect to agentapi (not direct to cliproxy)
- [ ] Donut adapter for queue/harvest

### Step 5: CI/CD
- [ ] SDK generation workflow
- [ ] Version management

---

## Summary

```
thegent → heliosHarness → agentapi → agentapi-bifrost → cliproxy+bifrost
```

| Layer | Role |
|-------|------|
| **thegent** | Orchestration, hooks, donut (queue/harvest) |
| **heliosHarness** | Controlled harness - Claude Code, Codex, Droid connect here. agentapi MOUNTS onto this |
| **agentapi** | Intermediary + own Bifrost extension |
| **cliproxy+bifrost** | Core proxy (bundled Go) |
