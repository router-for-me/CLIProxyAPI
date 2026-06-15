<!--
Thanks for opening a pull request!

Please complete the sections below. Sections marked with an asterisk
are required. Reviewers use this template to verify scope, risk, and
readiness, so please be thorough but concise.

If a section does not apply, write "N/A" rather than deleting it.
-->

## What*

<!-- One- or two-sentence summary of the change.
     Example: "Add retry-with-backoff to the platform SDK client." -->

## Why*

<!-- The motivation: bug, user request, spec link, incident, or tech-debt item.
     Link the issue, ticket, or design doc (e.g. Closes #123, Refs SPEC.md §4.2). -->

## How*

<!-- Implementation notes reviewers should know.
     - High-level approach
     - Key files / modules touched
     - Non-obvious decisions or trade-offs
     - Backward-compatibility implications -->

### Type of Change

- [ ] Bug fix (non-breaking change that fixes an issue)
- [ ] New feature (non-breaking change that adds functionality)
- [ ] Breaking change (fix or feature that would cause existing functionality to not work as expected)
- [ ] Refactor / cleanup (no behavior change)
- [ ] Performance improvement
- [ ] Documentation only
- [ ] Build / CI / tooling
- [ ] Security fix

## Testing*

<!-- Describe how this was verified. Check all that apply. -->

- [ ] Unit tests added or updated
- [ ] Integration / end-to-end tests added or updated
- [ ] Manual smoke test performed
- [ ] Lint / format / type-check passes locally
- [ ] Existing tests still pass

### Test Commands

<!-- Paste the exact commands you ran, e.g. -->

```sh
# Example:
# cargo test --workspace
# pnpm -r test
# go test ./...
```

### Test Evidence

<!-- Paste relevant output, screenshots, or links to CI runs. -->

## Checklist*

<!-- Standard pre-merge checks. -->

- [ ] My code follows the project's style guidelines
- [ ] I have performed a self-review of my own code
- [ ] I have commented my code, particularly in hard-to-understand areas
- [ ] I have updated the documentation (README, CHANGELOG, docs/) as needed
- [ ] My changes generate no new warnings
- [ ] I have added tests that prove my fix is effective or that my feature works
- [ ] New and existing unit tests pass locally with my changes
- [ ] Any dependent changes have been merged and published

## Risk & Rollout*

<!-- Force the author to think about blast radius. -->

- **Risk level**: `low` / `medium` / `high`
- **Blast radius**: <!-- who/what is affected: users, services, schemas, etc. -->
- **Feature flag required?**: `yes` / `no` — if yes, link the flag
- **Migration / data backfill needed?**: `yes` / `no` — describe
- **Rollback plan**: <!-- single revert? disable flag? drain queue? -->

### Affected Surfaces

<!-- Check every surface this PR touches. -->

- [ ] Public API / SDK
- [ ] CLI / install / packaging
- [ ] Configuration / environment variables
- [ ] Database schema or migrations
- [ ] Network / IPC / RPC contracts
- [ ] Authentication / authorization
- [ ] Telemetry / logging / tracing
- [ ] Dependencies (added, removed, or upgraded)
- [ ] Documentation site or example apps

## Related

<!-- Issues, PRs, design docs, specs. -->

- Closes #
- Refs #
- Related: #

## Reviewer Notes

<!-- Anything reviewers should pay extra attention to: tricky logic,
     concurrency, performance, security, etc. -->

