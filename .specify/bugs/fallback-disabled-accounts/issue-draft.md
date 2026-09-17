# Issue Draft: excluded-models is never enforced during credential selection

- **Slug**: fallback-disabled-accounts
- **Recorded**: 2026-09-17
- **Status**: NOT FILED — `gh issue create` was rejected with
  `the 'arrrrny/CLIProxyAPIPlus' repository has disabled issues`.
  GitHub Issues are turned off on the fork the reporter asked to file against.
  Recording the draft locally instead, as the workflow requires.

## Filing options (needs the user's decision)

1. **Enable Issues on `arrrrny/CLIProxyAPIPlus`**, then re-file with the body below.
2. **Cross-post to upstream `router-for-me/CLIProxyAPI`** — that is where the fix would
   ultimately land and where the original report's "Option 3: report to maintainers"
   points. This was not done unasked because it publishes a bug report to a third-party
   repository under the user's account.
3. **Leave it as this draft** and reference the assessment instead.

## Title

```text
excluded-models is never enforced during credential selection: config-declared keys disabled via /auth-files/status stay eligible
```

## Body

## Symptom

With `routing.strategy: fill-first` and several credentials serving the same model, requests
keep being dispatched to credentials that were disabled through
`PATCH /v0/management/auth-files/status`. The request fails on the disabled credential instead of
falling through to an enabled one. Reported against a model served by 8 config-declared
credentials with 7 of them disabled.

## Root cause

`PATCH /auth-files/status` does **not** set `auth.Disabled` for config-declared API keys (anything
where `coreauth.IsConfigAPIKeyAuth(auth)` is true, e.g. provider key entries and
`openai-compatibility` entries). Instead it rewrites `config.yaml` with `excluded-models: ["*"]`,
saves and reloads:

- `internal/api/handlers/management/auth_files.go:1319-1347` (the `IsConfigAPIKeyAuth` branch,
  responding `"via": "config:excluded-models"`)
- `internal/api/handlers/management/config_apikey_disable.go:12-33`
  (`configAPIKeyDisablePattern = "*"`)

The synthesizer then stores that list on the auth as the `excluded_models` attribute —
`internal/watcher/synthesizer/helpers.go:99`, carrying the comment *"Store the combined excluded
models list so that routing can read it at runtime"* — but **nothing in the codebase reads it**.
A repo-wide search for `excluded_models` finds only the writer, the file-metadata parser
(`internal/watcher/synthesizer/file.go:283`), and tests. `internal/registry`, `internal/runtime`
and the SDK contain no exclusion enforcement; the `ExcludedModels` config fields
(`internal/config/config.go:492`, `:562`, `:623`, `internal/config/vertex_compat.go:38`) are only
normalized and diffed.

Credential eligibility is decided by `isAuthBlockedForModel` (`sdk/cliproxy/auth/selector.go`),
which checks only `auth.Disabled` / `auth.Status == StatusDisabled`. A config-key "disable" sets
neither, so the credential stays a candidate, and `fill-first` deterministically picks the
lowest-ID one first.

File-backed OAuth auths are unaffected: they set both `Disabled` and `Status`
(`internal/watcher/synthesizer/file.go:113-119`, `:160-176`), and both selectors already
filter them.

## Expected behaviour

A credential excluded for the requested model — including the `"*"` disable sentinel — must not be
a routing candidate: for `fill-first` and `round-robin` alike, in the legacy selector path and the
fast-path scheduler.

## Proposed fix

Enforce the attribute at the single choke point both paths already share:
`isAuthBlockedForModel` in `sdk/cliproxy/auth/selector.go`, returning `blockReasonDisabled` when
`excluded_models` contains `"*"` or the requested model (`canonicalModelKey`, base-model match for
thinking suffixes, `"*"` only when the model is empty). Introduce a named constant for the attribute
key and use it from both the writer and the new reader.

## Impact

Any config-declared API key disabled through the management API is silently still eligible for
traffic, and the documented per-key `excluded-models` option has never been enforced either.
