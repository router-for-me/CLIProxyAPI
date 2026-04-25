# Research

- `semgrep/semgrep-action` is archived and points users to `returntocorp/semgrep`.
- `aquasecurity/trivy-action` latest release verified during this session: `v0.35.0`.
- `trufflesecurity/trufflehog` latest release verified during this session: `v3.94.2`.
- `github/codeql-action` latest release verified during this session resolves to the current v4 bundle line.

## Repo Findings

- `cliproxyapi-plusplus` is a Go repository with no Rust source files.
- The prior quick SAST workflow failed for mechanical reasons:
  - `cargo clippy` was invoked in a non-Rust repo.
  - SARIF upload referenced `semgrep.sarif` even when the deprecated action never created it.
  - `licensefinder/license_finder_action` no longer resolves.
