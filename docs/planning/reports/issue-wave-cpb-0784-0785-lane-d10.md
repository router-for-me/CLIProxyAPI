# Issue Wave CPB-0784-0785 Lane D10 Report

- Lane: `D10`
- Scope: `CPB-0784`, `CPB-0785` (next unclaimed implementation slice after `CPB-0783`)
- Domain: `cliproxy`
- Status: completed (code + tests + docs)
- Completion time: 2026-02-23

## Completed Items

### CPB-0784
- Focus: RooCode compatibility via provider-agnostic alias normalization.
- Code changes:
  - Added Roo alias normalization in `cmd/cliproxyctl/main.go`:
    - `roocode` -> `roo`
    - `roo-code` -> `roo`
- Test changes:
  - Added alias coverage in `cmd/cliproxyctl/main_test.go` under `TestResolveLoginProviderAliasAndValidation`.

### CPB-0785
- Focus: DX polish for `T.match`-class front-end failures through deterministic CLI checks.
- Docs changes:
  - Added `RooCode alias + T.match quick probe` section in `docs/provider-quickstarts.md`.
  - Added troubleshooting matrix row for RooCode `T.match` failure in `docs/troubleshooting.md`.

## Validation

- `go test ./cmd/cliproxyctl -run "TestResolveLoginProviderAliasAndValidation" -count=1`
- `rg -n "roocode|roo-code|CPB-0784|CPB-0785|T.match" cmd/cliproxyctl/main.go cmd/cliproxyctl/main_test.go docs/provider-quickstarts.md docs/troubleshooting.md`
