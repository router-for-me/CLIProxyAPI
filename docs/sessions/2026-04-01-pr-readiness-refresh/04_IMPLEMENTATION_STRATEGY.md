# Implementation Strategy

- Use direct `semgrep scan` invocation with a pinned Semgrep CLI version instead of the deprecated GitHub Action wrapper.
- Pin mutable third-party actions to verified release tags.
- Replace the Rust-only quick lint step with Go-native formatting and `go vet`.
- Downgrade license checking to a deterministic dependency inventory until the repo has a working allowlist-based compliance lane.
- Keep custom Semgrep rules versioned in-repo, but gate CI on the upstream Semgrep packs first to avoid instantly blocking the PR on inherited repo debt.
