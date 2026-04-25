# Testing Strategy

## Validation Performed

- YAML parse validation for both workflow files and all Semgrep rule files.
- `semgrep scan --config .semgrep-rules/ --config .semgrep.yaml --validate`
- `semgrep scan --config p/security-audit --config p/owasp-top-ten --config p/cwe-top-25 --validate`
- `gofmt` inventory check over tracked Go files.
- `go vet ./...` attempted to confirm the repo-level blocker set.

## Validation Caveats

- Local `go vet` is not green because of pre-existing repo issues unrelated to this patch.
- The quick and full Semgrep workflows were validated structurally and via CLI config checks, not by waiting for remote Actions to finish yet.
