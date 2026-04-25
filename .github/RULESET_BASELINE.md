# cliproxyapi-plusplus Ruleset Baseline

Version: 2026-04-02
Ruleset JSON: `.github/rulesets/main.json`

## Changelog

- 2026-04-02: aligned the checked-in baseline with the repo-local governance wave, safer workflow pins, and the next required-check manifest pass.

This repository now has a checked-in baseline that matches the repaired remote `Main` ruleset.

## Enforced Branch Protection Baseline

- require pull requests before merge on the default branch
- no branch deletion
- no force push / non-fast-forward updates
- require at least 1 approval
- dismiss stale approvals on new push
- require code owner review
- require last push approval before merge
- require resolved review threads before merge
- allow merge methods: `merge`, `squash`
- enable GitHub `copilot_code_review`

## Repo-Local Governance Gates

The repo-local workflow set remains the main CI and policy contract:

- `policy-gate`
- `pr-path-guard`
- `pr-test-build`
- `required-check-names-guard`
- `quality-gate`
- `security-guard`
- `codeql`
- `sast-quick`
- `sast-full`

Current required check manifests:

- `.github/required-checks.txt`
- `.github/release-required-checks.txt`
- `.github/rulesets/main.json`

Those manifests should drive the next remote ruleset wave once the stable job names are re-verified
against live workflow output.

## Exception Policy

- only documented billing or quota failures may be excluded from blocking CI evaluation
- review threads and blocking comments must be resolved before merge
- PRs must not rely on local `--no-verify` bypasses instead of server-side checks
