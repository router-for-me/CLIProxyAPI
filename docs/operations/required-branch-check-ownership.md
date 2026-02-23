# Required Branch Check Ownership

Ownership map for required checks and release gate manifests.

## Required Check Sources

- Branch protection check manifest: `.github/required-checks.txt`
- Release gate check manifest: `.github/release-required-checks.txt`
- Name integrity guard workflow: `.github/workflows/required-check-names-guard.yml`

## Ownership Matrix

| Surface | Owner | Backup | Notes |
| --- | --- | --- | --- |
| `.github/required-checks.txt` | Release Engineering | Platform On-Call | Controls required check names for branch governance |
| `.github/release-required-checks.txt` | Release Engineering | Platform On-Call | Controls release gate required checks |
| `.github/workflows/pr-test-build.yml` check names | CI Maintainers | Release Engineering | Check names must stay stable or manifests must be updated |
| `.github/workflows/release.yaml` release gate | Release Engineering | CI Maintainers | Must block releases when required checks are not green |
| `.github/workflows/required-check-names-guard.yml` | CI Maintainers | Release Engineering | Prevents silent drift between manifests and workflow check names |

## Change Procedure

1. Update workflow job name(s) and required-check manifest(s) in the same PR.
2. Ensure `required-check-names-guard` passes.
3. Confirm branch protection required checks in GitHub settings match manifest names.
4. For release gate changes, verify `.github/release-required-checks.txt` remains in sync with release expectations.

## Escalation

- If a required check disappears unexpectedly: page `CI Maintainers`.
- If release gate blocks valid release due to manifest drift: page `Release Engineering`.
- If branch protection and manifest diverge: escalate to `Platform On-Call`.

## Related

- [Release Governance and Checklist](./release-governance.md)
- [Checks-to-Owner Responder Map](./checks-owner-responder-map.md)

---
Last reviewed: `2026-02-21`  
Owner: `Release Engineering`  
Pattern: `YYYY-MM-DD`
