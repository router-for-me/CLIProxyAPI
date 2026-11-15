# 0001: Fork strategy and naming

Date: 2026-11-16. Status: accepted.

## Context

proxx must keep pulling upstream fixes from `router-for-me/CLIProxyAPI` quickly. Upstream is very
active and touches most files. The owner also wants the product named "proxx" instead of CLIProxyAPI.

## Decision

- **Keep the Go module path** `github.com/router-for-me/CLIProxyAPI/v8`. Renaming it rewrites the
  import block of nearly every `.go` file, so every upstream merge would conflict everywhere.
- Rename only what users see, with the smallest possible footprint in upstream files:
  - one header line at the top of `README.md` (upstream's most-edited doc, so nothing more);
  - build the binary as `proxx` (`go build -o proxx ./cmd/server`); no change to upstream build scripts;
  - the management panel (`iamkunal9/proxx-panel`) is fully branded "proxx".
- `DefaultPanelGitHubRepository` stays upstream's until `proxx-panel` has a published release; until
  then point `remote-management.panel-github-repository` at `https://github.com/iamkunal9/proxx-panel`
  in config. Changing the default first would break panel downloads for anyone running defaults.
- Fork features go in new files and new packages. Upstream files get one-line hooks marked
  `proxx:` and listed in `docs/proxx/README.md`.
- All fork config sits under one `proxx:` root; all fork management routes under `/v8/management/proxx`.
- Fork documentation lives in `docs/proxx/`; upstream `AGENTS.md` only gets a single pointer line.

## Consequences

- `git merge upstream/main` normally conflicts only on the few hook lines.
- Import paths still say CLIProxyAPI. That's cosmetic and invisible to users of the binary.
