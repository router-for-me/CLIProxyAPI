# vX.Y.Z - YYYY-MM-DD

<!--
Copy this file to docs/releases/<tag>.md and replace every placeholder.
The release workflow rejects placeholders, missing sections, and an
ambiguous upstream alignment statement.
-->

## Summary

- Describe the user-visible outcome in one to three bullets.
- State compatibility or migration impact when it applies.

## Upstream alignment

- Upstream release: `vX.Y.Z`
- Previous baseline: `vX.Y.Z`
- Intake record: [absorbed.md](../../ops/upstream-intake/absorbed.md)
- Ported: Summarize the upstream fixes or features included in this release.
- Skipped: Summarize intentionally excluded upstream work and its reason.

Use `none (fork-only release)` for Upstream release and `none` for Previous
baseline when this release contains no upstream intake. Do not claim alignment
to a fetched tag that has not been assessed and recorded in the intake ledger.

## Breaking changes

- None.

## Added

- Describe user-visible additions.

## Changed

- Describe behavior, configuration, or compatibility changes.

## Fixed

- Describe corrected behavior from the user's perspective.

## Security

- None, or describe the security impact and migration guidance.

## Validation

- Record the regression gate, protocol replay, and cross-platform checks that
  actually ran. State coverage limits instead of implying full verification.

## Known issues

- None, or describe unresolved user-visible limitations.

The workflow appends the release-asset guide and the full commit comparison
link. Do not add either section manually.
