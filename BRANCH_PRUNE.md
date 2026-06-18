# Branch prune ledger — cliproxyapi-plusplus

Post **Wave H3** (#1024): vibeproxy absorption documented; proxy plane canonical.

## Taxonomy (11 remote branches)

| Bucket | Examples | Action |
|--------|----------|--------|
| hygiene | `chore/pin-actions`, `chore/workflow-*` | DELETE if 0 ahead of main |
| ci | `ci/add-golangci-lint` | DELETE if merged |
| dependabot | `dependabot/go_modules/*` | DELETE post-merge |
| convoy/worktree | `convoy/agileplus-kilo-specs-*` | DELETE (stale worktree lane) |
| cursor | `cursor/workflow-*` | DELETE if 0 ahead |
| docs | `docs/sladge-badge` | DELETE if 0 ahead |

## Policy

- Never delete `main`
- Compare before delete: `ahead_by == 0` only
- OpenAI-compatible surface: https://kooshapari.github.io/cliproxyapi-plusplus/

## Gate

```bash
go build ./...
go test ./...
```
