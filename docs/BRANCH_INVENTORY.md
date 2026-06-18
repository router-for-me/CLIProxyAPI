# Branch inventory (Wave 16 G16 boundary)

**ADR:** ADR-ECO-007 Option B — cliproxyapi-plusplus is the canonical proxy plane.  
**Lane:** `feat/wave16-g16-boundary`  
**Snapshot date:** 2026-06-17  
**Main HEAD:** `866ca6dd49f7ba72c0e1349a235df4137b4e890c`

## Policy

Remote branches are capped at **≤5**. After Wave 16 G16 cleanup the retained set is:

| Branch | Role |
|--------|------|
| `main` | Canonical integration branch |
| `feat/wave16-g16-boundary` | Active Wave 16 boundary lane (this PR) |

All other remote branches listed in `branch-inventory.csv` were stale (6–129 commits behind `main`, no open PRs) and are **deleted** as part of this wave.

## vibeproxy absorption

`VIBEPROXY_ABSORPTION.md` is already on `main` (merged via #1024). No cherry-pick required.

## Related repos

| Repo | Branch | Action |
|------|--------|--------|
| [vibeproxy](https://github.com/KooshaPari/vibeproxy) | `feat/wave16-g16-redirect` | README deprecation banner → cliproxyapi-plusplus |
| [phenotype-go-sdk](https://github.com/KooshaPari/phenotype-go-sdk) | `feat/wave16-g16-pin` | Pin cliproxy SHA in `third_party/cliproxyapi-plusplus/` |

## Machine-readable inventory

See [`branch-inventory.csv`](branch-inventory.csv) for the pre-prune branch snapshot.
