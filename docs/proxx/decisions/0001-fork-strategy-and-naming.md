# 0001: Fork strategy and naming

Date: 2026-11-16. Status: accepted.

## Context

proxx must keep pulling upstream fixes from `router-for-me/CLIProxyAPI` quickly. Upstream is very
active and touches most files. The owner also wants the product named "proxx" instead of CLIProxyAPI.

## Decision

- **Keep the Go module path** `github.com/router-for-me/CLIProxyAPI/v8`. Renaming it rewrites the
  import block of nearly every `.go` file, so every upstream merge would conflict everywhere.
- Rename only what users see: the README/docs branding, the built binary name (`go build -o proxx`),
  the panel title, and the default panel repository in our docs and example config.
- Fork features go in new files and new packages. Upstream files get one-line hooks marked
  `proxx:` and listed in `docs/proxx/README.md`.
- All fork config sits under one `proxx:` root; all fork management routes under `/v8/management/proxx`.
- Fork documentation lives in `docs/proxx/`; upstream `AGENTS.md` only gets a single pointer line.

## Consequences

- `git merge upstream/main` normally conflicts only on the few hook lines.
- Import paths still say CLIProxyAPI. That's cosmetic and invisible to users of the binary.
