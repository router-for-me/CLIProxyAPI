# Board Creation and Source-to-Solution Mapping Workflow

Use this workflow to keep a complete mapping from upstream requests to implemented solutions.

## Goals

- Keep every work item linked to a source request.
- Support sources from GitHub and non-GitHub channels.
- Track progress continuously (not only at final completion).
- Keep artifacts importable into GitHub Projects and visible in docs.

## Accepted Source Types

- GitHub issue
- GitHub feature request
- GitHub pull request
- GitHub discussion
- External source (chat, customer report, incident ticket, internal doc, email)

## Required Mapping Fields Per Item

- `Board ID` (example: `CP2K-0418`)
- `Title`
- `Status` (`proposed`, `in_progress`, `blocked`, `done`)
- `Priority` (`P1`/`P2`/`P3`)
- `Wave` (`wave-1`/`wave-2`/`wave-3`)
- `Effort` (`S`/`M`/`L`)
- `Theme`
- `Source Kind`
- `Source Repo` (or `external`)
- `Source Ref` (issue/pr/discussion id or external reference id)
- `Source URL` (or external permalink/reference)
- `Implementation Note`

## Board Artifacts

- Primary execution board:
  - `docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.csv`
  - `docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.json`
  - `docs/planning/CLIPROXYAPI_2000_ITEM_EXECUTION_BOARD_2026-02-22.md`
- GitHub Projects import:
  - `docs/planning/GITHUB_PROJECT_IMPORT_CLIPROXYAPI_2000_2026-02-22.csv`

## Create or Refresh a Board

Preferred command:

```text
go run ./cmd/boardsync
```

Task shortcut:

```text
task board:sync
```

The sync tool is implemented in Go (`cmd/boardsync/main.go`).

1. Pull latest sources from GitHub Issues/PRs/Discussions.
2. Normalize each source into required mapping fields.
3. Add strategic items not yet present in GitHub threads (architecture, DX, docs, runtime ops).
4. Generate CSV + JSON + Markdown together.
5. Generate Project-import CSV from the same canonical JSON.
6. Update links in README and docs pages if filenames changed.

## Work-in-Progress Update Rules

When work starts:

- Set item `Status` to `in_progress`.
- Add implementation branch/PR reference in task notes or board body.

When work is blocked:

- Set item `Status` to `blocked`.
- Add blocker reason and dependency reference.

When work completes:

- Set item `Status` to `done`.
- Add solution reference:
  - PR URL
  - merged commit SHA
  - released version (if available)
  - docs page updated (if applicable)

## Source-to-Solution Traceability Contract

Every completed board item must be traceable:

- `Source` -> `Board ID` -> `Implementation PR/Commit` -> `Docs update`

If a source has no URL (external input), include a durable internal reference:

- `source_kind=external`
- `source_ref=external:<id>`
- `source_url=<internal ticket or doc link>`

## GitHub Project Import Instructions

1. Open Project (v2) in GitHub.
2. Import `docs/planning/GITHUB_PROJECT_IMPORT_CLIPROXYAPI_2000_2026-02-22.csv`.
3. Map fields:
   - `Title` -> Title
   - `Status` -> Status
   - `Priority` -> custom field Priority
   - `Wave` -> custom field Wave
   - `Effort` -> custom field Effort
   - `Theme` -> custom field Theme
   - `Board ID` -> custom field Board ID
4. Keep `Source URL`, `Source Ref`, and `Body` visible for traceability.

## Maintenance Cadence

- Weekly: sync new sources and re-run board generation.
- Daily (active implementation periods): update statuses and completion evidence.
- Before release: ensure all `done` items have PR/commit/docs references.
