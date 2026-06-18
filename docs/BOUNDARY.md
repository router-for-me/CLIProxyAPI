# Boundary — cliproxyapi-plusplus

**Status:** Active (Wave H / G16)  
**Disposition:** DYNAMIC-KEEP → phenotype-gateway (`packages/cliproxy`)  
**Registry row:** `gw-cliproxy` in [disposition-index.json](https://github.com/KooshaPari/phenotype-registry/blob/main/registry/disposition-index.json)  
**Charter:** [boundary-shaping.md](https://github.com/KooshaPari/phenotype-registry/blob/main/docs/rationalization/boundary-shaping.md)  
**ADR:** [ADR-ECO-007 Option B](https://github.com/KooshaPari/phenotype-registry/blob/main/docs/adrs/ADR-ECO-007-gateway-merge-superset.md)

---

## Role

Canonical **CLI subscription proxy plane** — OpenAI-compatible gateway routing LLM provider traffic through CLI-based subscriptions (Claude, ChatGPT, Gemini, etc.). Absorbs deprecated macOS client [vibeproxy](https://github.com/KooshaPari/vibeproxy) per [`VIBEPROXY_ABSORPTION.md`](./VIBEPROXY_ABSORPTION.md).

---

## Stack

| Tier | Language | Justification |
|------|----------|---------------|
| Core | Go | Proxy server, provider catalog, auth handlers |
| Edge | — | No secondary runtime required for core plane |

phenotype-go-sdk pins this repo in `third_party/cliproxyapi-plusplus/` (go-sdk#17).

---

## Owns

| Path / concern | Notes |
|----------------|-------|
| `cmd/`, `internal/`, `api/` | Proxy server and routing |
| `docs/provider-*`, `docs/routing-reference.md` | Operator docs |
| `docs/VIBEPROXY_ABSORPTION.md` | Deprecated client absorption ledger |
| Optional `client/vibeproxy/` | macOS UX harvest (future) |

---

## Forbidden (out of scope)

| Concern | Canonical owner |
|---------|-----------------|
| Agent terminal PTY control | agentapi-plusplus |
| Enterprise inference compose | bifrost |
| Fleet submodule orchestration | phenotype-gateway |
| Long-term LLM combo router | OmniRoute (interim peer) |

---

## Related repos

| Repo | Action |
|------|--------|
| vibeproxy | Archive after absorption (#1024); redirect → this repo |
| phenotype-go-sdk | Pin cliproxy SHA (`feat/wave16-g16-pin`) |

---

## verify

```bash
go build ./...
go test ./...
```

Branch cap: ≤5 remotes. See [`BRANCH_INVENTORY.md`](./BRANCH_INVENTORY.md).

---

## FSM

| Field | Value |
|-------|-------|
| Wave | H |
| fsm | `done` |
| PR | cliproxyapi-plusplus#1025, #1026, #1024 |
| relocated_date | 2026-06-18 |
