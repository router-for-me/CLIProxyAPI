# Release Governance and Checklist

Use this runbook before creating a release tag.

## 1) Release Gate: Required Checks Must Be Green

Release workflow gate:

- Workflow: `.github/workflows/release.yaml`
- Required-check manifest: `.github/release-required-checks.txt`
- Rule: all listed checks for the tagged commit SHA must have at least one successful check run.

If any required check is missing or non-successful, release stops before Goreleaser.

## 2) Breaking Provider Behavior Checklist

Complete this section for any change that can alter provider behavior, auth semantics, model routing, or fallback behavior.

- [ ] `provider-catalog.md` updated with behavior impact and rollout notes.
- [ ] `routing-reference.md` updated when model selection/routing semantics changed.
- [ ] `provider-operations.md` updated with new mitigation/fallback/monitoring actions.
- [ ] Feature flags/defaults migration documented for staged rollout (including fallback model aliases).
- [ ] Per-OAuth-account proxy behavior changes documented with strict/fail-closed defaults and rollback plan.
- [ ] Backward compatibility impact documented (prefix rules, alias behavior, auth expectations).
- [ ] `/v1/models` and `/v1/metrics/providers` validation evidence captured for release notes.
- [ ] Any breaking behavior flagged in changelog under the correct scope (`auth`, `routing`, `docs`, `security`).

## 3) Changelog Scope Classifier Policy

CI classifier check:

- Workflow: `.github/workflows/pr-test-build.yml`
- Job name: `changelog-scope-classifier`
- Scopes emitted: `auth`, `routing`, `docs`, `security` (or `none` if no scope match)

Classifier is path-based and intended to keep release notes consistently scoped.

## 4) Pre-release Config Compatibility Smoke Test

CI smoke check:

- Workflow: `.github/workflows/pr-test-build.yml`
- Job name: `pre-release-config-compat-smoke`
- Verifies:
  - `config.example.yaml` loads via config parser.
  - OAuth model alias migration runs successfully.
  - migrated config reloads successfully.

## Related

- [Required Branch Check Ownership](./required-branch-check-ownership.md)
- [Checks-to-Owner Responder Map](./checks-owner-responder-map.md)

---
Last reviewed: `2026-02-21`  
Owner: `Release Engineering`  
Pattern: `YYYY-MM-DD`
