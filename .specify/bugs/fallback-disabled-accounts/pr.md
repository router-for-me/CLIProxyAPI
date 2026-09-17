# Bug Fix PR: skip credentials excluded for the requested model

- **Slug**: fallback-disabled-accounts
- **Opened**: 2026-09-17
- **PR**: 18
- **URL**: https://github.com/arrrrny/CLIProxyAPIPlus/pull/18
- **Branch**: fix/fallback-disabled-accounts
- **Issue**: n/a — GitHub Issues are disabled on `arrrrny/CLIProxyAPIPlus`, so no tracked issue could be filed. The report is preserved in `./issue-draft.md`.

Enforces the `excluded_models` auth attribute during credential selection so config-declared API
keys disabled through `PATCH /v0/management/auth-files/status` are no longer routing candidates,
fixing `fill-first` dispatching requests to disabled credentials instead of falling through to an
enabled one.

Opened against base `snapshot` (the branch this work forked from, commit `109a066d`) rather than
the fork's default `sync`, which carries two unrelated commits that would otherwise appear in the
PR.
