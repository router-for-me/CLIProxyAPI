# Issue Wave CPB-0781-0830 Implementation Batch 1

- Date: `2026-02-23`
- Scope: first high-confidence execution set (`12` items)
- Mode: docs + config safety hardening

## IDs Covered

- `CPB-0782`, `CPB-0786`, `CPB-0796`, `CPB-0799`
- `CPB-0801`, `CPB-0802`, `CPB-0806`, `CPB-0811`
- `CPB-0814`, `CPB-0815`, `CPB-0826`, `CPB-0829`

## Implemented in This Pass

- `CPB-0782`, `CPB-0786`, `CPB-0796`, `CPB-0799`
  - Added/expanded provider quickstart probes for Opus 4.5, Nano Banana, dynamic model provider routing, and auth-path mismatch scenarios.
  - Evidence: `docs/provider-quickstarts.md`

- `CPB-0801`, `CPB-0802`, `CPB-0806`, `CPB-0811`
  - Added Gemini 3 Pro / `gemini-3-pro-preview` quick probes and thinking-budget normalization checks.
  - Evidence: `docs/provider-quickstarts.md`, `docs/troubleshooting.md`

- `CPB-0814`, `CPB-0815`
  - Clarified `auth-dir` default usage/permissions in template config.
  - Tightened config-dir creation mode in `cliproxyctl` bootstrap (`0700` instead of `0755`).
  - Evidence: `config.example.yaml`, `cmd/cliproxyctl/main.go`

- `CPB-0826`, `CPB-0829`
  - Added scoped `auto` routing and `candidate_count` rollout-guard guidance.
  - Evidence: `docs/provider-quickstarts.md`, `docs/troubleshooting.md`

## Verification

```bash
GOCACHE=$PWD/.cache/go-build go test ./cmd/cliproxyctl -run 'TestEnsureConfigFile|TestRunDoctorJSONWithFixCreatesConfigFromTemplate' -count=1
rg -n "CPB-0782|CPB-0786|CPB-0796|CPB-0799|CPB-0802|CPB-0806|CPB-0811|CPB-0826|CPB-0829|auth-dir|candidate_count" docs/provider-quickstarts.md docs/troubleshooting.md config.example.yaml
```
