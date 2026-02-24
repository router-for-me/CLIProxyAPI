# CODEBASE HEALTH ATLAS
## Generated: 2026-02-23

---

## EXECUTIVE SUMMARY

| Project | Language | Size | Health | Priority |
|---------|----------|------|--------|----------|
| **thegent** | Python/Rust | 517K LOC | ⚠️ BLOATED | HIGH - Needs refactor |
| **cliproxyapi++** | Go | ~200K LOC | ✅ Focused | MEDIUM - Maintain sync |
| **agentapi++** | Go | ~5K LOC | ✅ Lean | LOW - Integration ready |
| **civ** | Rust | ~1K LOC | ✅ Early | PLANNING |
| **parpour** | Python/Spec | ~500 LOC | ✅ Early | PLANNING |

---

## DETAILED ANALYSIS

### 1. THEGENT (Primary Concern)

#### Size Metrics
- **Python Source**: 246,386 LOC (1,330 files)
- **Python Tests**: 271,413 LOC (1,215 files)
- **Rust Crates**: 34 crates (partial usage)
- **Total**: ~517K+ Python LOC

#### Module Breakdown (Top 10 by LOC)
| Module | LOC | % of Total | Action |
|--------|-----|------------|--------|
| cli | 34,435 | 14.0% | Refactor into sub-modules |
| utils | 17,801 | 7.2% | Audit for dead code |
| agents | 15,443 | 6.3% | Move cliproxy code out |
| mcp | 14,314 | 5.8% | Keep, core functionality |
| governance | 13,322 | 5.4% | Keep, core functionality |
| orchestration | 12,299 | 5.0% | Keep, core functionality |
| infra | 11,221 | 4.6% | Audit for consolidation |
| integrations | 10,783 | 4.4% | Move non-core to cliproxyapi++ |
| tui | 5,418 | 2.2% | Keep |
| ui | 4,509 | 1.8% | Audit for consolidation |

#### Large Files (>1000 LOC) - Refactor Candidates
| File | LOC | Issue | Recommendation |
|------|-----|-------|----------------|
| install.py | 1,773 | Monolithic | Split into install_*.py modules |
| commands/sync.py | 1,745 | Duplicates cli/apps/sync.py | Consolidate |
| clode_main.py | 1,717 | Legacy entry point | Deprecate/migrate |
| run_execution_core_helpers.py | 1,571 | Helper bloat | Extract to services |
| config.py | 1,540 | Config sprawl | Use pydantic-settings |
| provider_model_manager.py | 1,526 | Provider coupling | Move to cliproxyapi++ |
| shadow_audit_git.py | 1,518 | Audit complexity | Split by concern |
| jsonrpc_agent_server.py | 1,375 | Protocol code | Move to agentapi++ |
| cliproxy_adapter.py | 1,250 | **Wrong project** | Move to cliproxyapi++ |
| cliproxy_manager.py | 1,119 | **Wrong project** | Move to cliproxyapi++ |

#### Code Ownership Issues
- `cliproxy_adapter.py` → Belongs in cliproxyapi++
- `cliproxy_manager.py` → Belongs in cliproxyapi++
- `jsonrpc_agent_server.py` → Consider agentapi++
- Provider management code → Should use cliproxyapi++

#### Dead Code Candidates
- Multiple `*_old.py` files
- Shadow directories (`.shadow-DEL-*`)
- Duplicate sync implementations
- Unused integrations (lmcache, nats_event_bus, graphiti)

---

### 2. CLIPROXYAPI++ 

#### Purpose
- API proxy for AI coding assistants
- OAuth handling (GitHub Copilot, Kiro)
- Rate limiting, metrics, token refresh

#### Integration with thegent
| thegent Code | Should Move? | cliproxyapi++ Target |
|--------------|--------------|---------------------|
| cliproxy_adapter.py | ✅ YES | `/adapters/thegent.go` |
| cliproxy_manager.py | ✅ YES | `/managers/` |
| provider_model_manager.py | Partial | `/providers/` |
| JSON-RPC protocols | Maybe | `/protocols/` |

#### Current State
- 1,018 Go files
- Well-structured
- Community maintained

---

### 3. AGENTAPI++

#### Purpose
- HTTP API to control coding agents (Claude, Aider, Goose, etc.)
- MCP server backend capability
- Unified chat interface

#### Integration Opportunities
| Feature | thegent | agentapi++ |
|---------|---------|------------|
| Agent control | Custom impl | Use agentapi++ |
| HTTP endpoints | Custom | Standardized |
| MCP integration | Custom | Native support |
| Multi-agent | Custom | Could leverage |

#### Recommendation
- Consider using agentapi++ as thegent's agent control layer
- Reduce thegent agent code by delegating to agentapi++

---

### 4. CIV (Planning Stage)

#### Purpose
- Deterministic simulation
- Policy-driven architecture

#### Current State
- 9 Rust files
- 5 crates
- Spec-first development

#### Recommendations
- Keep focused on simulation/policy
- Use thegent for agent orchestration
- Use cliproxyapi++ for API proxying

---

### 5. PARPOUR (Planning Stage)

#### Purpose
- Planning and architecture specs
- Control-plane systems

#### Current State
- 4 Python files
- Mostly specs/docs

#### Recommendations
- Define integration points with thegent
- Plan for Rust implementation
- Use civ for simulation

---

## QUALITY METRICS

### Test Coverage
| Project | Test Files | Est. Coverage |
|---------|------------|---------------|
| thegent | 1,215 | ~70% (needs verification) |
| cliproxyapi++ | Unknown | Go testing |
| agentapi++ | e2e/ | Good |
| civ | Cargo tests | Speculative |
| parpour | None | N/A (specs) |

### Code Quality
| Metric | thegent | Target |
|--------|---------|--------|
| Lint errors | 0 (fixed) | 0 ✅ |
| Large files | 18 | <5 |
| Duplicate code | High | Low |
| Dead code | Unknown | Audit needed |

---

## RECOMMENDED ACTIONS

### Immediate (Week 1-2)
1. **Move cliproxy code** from thegent to cliproxyapi++
2. **Audit and remove** dead code in thegent
3. **Consolidate** duplicate sync implementations
4. **Split** large files (>1000 LOC)

### Short-term (Month 1)
1. **Integrate** agentapi++ for agent control
2. **Refactor** CLI into focused sub-modules
3. **Reduce** Python LOC by 20% (target: <200K)
4. **Increase** Rust share for performance-critical paths

### Long-term (Quarter 1)
1. **Migrate** utils to Rust/Zig where appropriate
2. **Consolidate** integrations
3. **Improve** test coverage to 85%+
4. **Document** architecture decisions

---

## RUST/ZIG/MOJO MIGRATION OPPORTUNITIES

| Module | Current | Target | Rationale |
|--------|---------|--------|-----------|
| utils/routing_impl | Python | Rust | Performance |
| infra/cache | Python | Rust | Performance |
| orchestration/* | Python | Rust | Parallelism |
|protocols/* | Python | Zig | Low-latency |
| compute/* | Python | Mojo | ML/AI ops |

---

## GOVERNANCE & TRACEABILITY GAPS

1. **Missing ADRs** for major decisions
2. **No ownership tracking** for modules
3. **Incomplete** worklog linking
4. **Missing** coverage reports in CI

---

## NEXT STEPS

1. Run full test suite with coverage
2. Generate dead code report
3. Create migration plan for cliproxy code
4. Set up quality gates for PR size
5. Document module ownership
