# Release Batching Guide

This repository follows release tags in the format:

- `v<major>.<minor>.<patch>-<batch>`
- Examples: `v6.8.24-0`, `v6.8.18-1`

## Batch Strategy

1. Land a coherent batch of commits on `main`.
2. Run release tool in default mode:
   - bumps patch
   - resets batch suffix to `0`
3. For same-patch follow-up release, run hotfix mode:
   - keeps patch
   - increments batch suffix (`-1`, `-2`, ...)

## Commands

Dry run:

```bash
go run ./cmd/releasebatch --mode create --target main --dry-run
```

Patch batch release:

```bash
go run ./cmd/releasebatch --mode create --target main
```

Hotfix release on same patch:

```bash
go run ./cmd/releasebatch --mode create --target main --hotfix
```

Automatic notes generation on tag push:

```bash
go run ./cmd/releasebatch --mode notes --tag v6.8.24-0 --out /tmp/release-notes.md --edit-release
```

## What the Tool Does

- Validates clean working tree (create mode, fail-fast if dirty).
- Fetches tags/target branch state.
- Detects latest release tag matching `v<semver>-<batch>`.
- Computes next tag per mode (batch vs hotfix).
- Builds release notes in the current upstream style:
  - `## Changelog`
  - one bullet per commit: `<full_sha> <subject>`
- Creates/pushes annotated tag (create mode).
- Publishes release (`gh release create`) or updates release notes (`gh release edit`).

## Best Practices

- Keep each release batch focused (single wave/theme).
- Merge lane branches first; release only from `main`.
- Ensure targeted tests pass before release.
- Prefer one patch release per merged wave; use hotfix only for urgent follow-up.
