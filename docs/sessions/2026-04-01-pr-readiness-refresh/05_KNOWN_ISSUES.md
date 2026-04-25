# Known Issues

- `go vet ./...` is still blocked locally by a mix of transient Go module proxy failures and an existing import cycle in `pkg/llmproxy/interfaces`.
- `security/snyk (kooshapari)` remains an external billing/quota blocker.
- The custom Semgrep ruleset is syntactically valid but still too noisy to gate this repo without a dedicated false-positive reduction pass.
