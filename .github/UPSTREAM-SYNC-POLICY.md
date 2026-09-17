# Fork Upstream Sync Policy (MANDATORY)

This repo (`arrrrny/CLIProxyAPIPlus`) is a **fork** of `router-for-me/CLIProxyAPI`.
The `sync` branch carries fork-owned providers and features that upstream will
never accept. The upstream sync must preserve them.

This document is the authoritative description of that contract. The workflow
that enforces it is `.github/workflows/sync-upstream.yml`; the inventory it
checks is `.github/FORK_OWNED_FILES`.

> **Why this policy lives in two places:** `agents-md-guard.yml` auto-closes any
> pull request that modifies `AGENTS.md`, so the policy cannot ride a PR. The
> `AGENTS.md` section is applied by a direct push to `sync`. The block in
> "Agent instructions" below is kept in sync with it by hand — if you change one,
> change the other via direct push.

## What the fork owns

Upstream will never accept these, so a merge that drops any of them is a
regression:

| Area | Where |
| --- | --- |
| kiro provider | `internal/auth/kiro/`, `internal/runtime/executor/kiro_executor.go`, `internal/translator/kiro/`, `internal/cmd/kiro_login.go`, `sdk/auth/kiro.go` |
| cursor provider | `internal/auth/cursor/`, `internal/runtime/executor/cursor_executor.go` |
| codebuddy provider | `internal/auth/codebuddy/`, `internal/runtime/executor/codebuddy_executor.go` |
| github-copilot provider | `internal/auth/copilot/`, `internal/runtime/executor/github_copilot_executor.go` |
| gitlab duo provider | `internal/auth/gitlab/`, `internal/runtime/executor/gitlab_executor.go` |
| kilo provider | `internal/auth/kilo/`, `internal/runtime/executor/kilo_executor.go`, `internal/registry/kilo_models.go` |
| gemini-cli provider | `internal/runtime/executor/gemini_cli_executor.go`, `internal/runtime/geminicli/` |
| dedicated model providers | `internal/registry/dedicated_provider_models.go`, `internal/registry/provider_models.go`, `sdk/api/handlers/openai/endpoint_compat.go` |
| fork OAuth model alias defaults | `internal/config/oauth_model_alias_defaults.go` |
| AMP module registry (V2 route modules) | `internal/api/modules/modules.go` |
| selection-time model-exclusion guard | `sdk/cliproxy/auth/selector.go` (`authExcludedForModel`), `sdk/cliproxy/auth/classification.go`, `internal/watcher/synthesizer/helpers.go` |
| fork edits inside upstream files | `internal/config/config.go`, `internal/api/handlers/management/auth_files.go`, `internal/registry/model_registry.go`, `sdk/cliproxy/service.go`, `config.example.yaml` |

Each of these has a verified entry in `.github/FORK_OWNED_FILES`.

## The three rules

### 1. Never auto-resolve conflicts in favour of upstream

On any merge conflict the sync **aborts**, opens a `sync`-labeled issue listing
the conflicted files, and fails the job. Nothing is pushed.

`git checkout --theirs`, `git merge -X theirs`, and `git merge -s ours` are
forbidden against upstream — in scripts, in CI, and by hand. They do not
"resolve" a conflict, they delete fork code and report success. An earlier
version of this repo's `sync-and-release.yml` did exactly that with a hardcoded
owned-file list; it silently dropped fork work. The file has been deleted.

Resolve conflicts on a `sync/fork-sync-resolution` branch, then merge manually.

### 2. Verify fork-owned markers after every clean merge

A conflict is loud. A **clean** merge is the dangerous case: when upstream edits
neighbouring lines, git takes upstream's hunk without complaint and the fork's
change disappears quietly. Nothing fails. The provider just stops working.

So after every clean merge the sync walks `.github/FORK_OWNED_FILES`, checking
each path still exists and still contains its survival marker. Any miss fails
the job and opens an issue.

When you add or change a fork-owned file, add it to that list with a marker that
is unique to the fork. Verify both directions before committing:

```bash
grep -c -F '<marker>' <path>                              # must be > 0
git show upstream/main:<path> | grep -c -F '<marker>'     # must be 0
```

Do not remove entries unless the fork feature was intentionally retired **and**
the upstream merge removed the source code — both in the same PR.

### 3. Never push a merged tree that does not compile

After a clean merge and a passing marker check, the sync runs `go build ./...`.
If it fails, nothing is pushed and an issue is opened. A broken `sync` branch
would be published as a release by `sync-release.yml`.

## Workflow map

| Workflow | Role |
| --- | --- |
| `sync-upstream.yml` | **Source of truth.** Daily upstream merge with the three rules above. |
| `sync-release.yml` | Unrelated to upstream syncing — builds and publishes release artifacts on every push to `sync`. |

Note that `sync-upstream.yml` pushes with the `CLIPROXY_SYNC` PAT rather than
`GITHUB_TOKEN`: pushes authenticated with `GITHUB_TOKEN` do not trigger other
workflows, and the push to `sync` must trigger `sync-release.yml`.

## Agent instructions

This is the mirror of the "Fork Upstream Sync Policy (MANDATORY)" section in
`AGENTS.md`, which was applied by a direct push to `sync`. A PR cannot carry it,
and `agents-md-guard.yml` will close one that tries. If you amend the policy,
update both copies the same way:

```markdown
## Fork Upstream Sync Policy (MANDATORY)

This repo (`arrrrny/CLIProxyAPIPlus`) is a **fork** of `router-for-me/CLIProxyAPI`. The `sync` branch carries fork-owned providers upstream will never accept — kiro, cursor, codebuddy, github-copilot, gitlab, kilo, gemini-cli, the dedicated model providers, the AMP module registry, the fork's OAuth model alias defaults, and the selection-time model-exclusion guard. The upstream sync MUST preserve them. See `.github/UPSTREAM-SYNC-POLICY.md`.

- **Never auto-resolve conflicts in favor of upstream.** `git checkout --theirs`, `git merge -X theirs`, and `git merge -s ours` are forbidden against upstream. On any conflict, abort, open a `sync`-labeled issue, and resolve by hand on a `sync/fork-sync-resolution` branch.
- **After a clean merge, verify `.github/FORK_OWNED_FILES`.** A clean merge can overwrite fork code without conflicting. When you add or change a fork-owned file, append it with a fork-unique survival marker.
- **Never push a merged tree that fails `go build ./...`.**
- The `-X theirs` sync is banned outright. `.github/workflows/sync-and-release.yml` implemented it and has been deleted — do not reintroduce it. `sync-upstream.yml` is the source of truth.

**Note:** `AGENTS.md` cannot be changed by a pull request — `agents-md-guard.yml` auto-closes any PR that touches it. Edit it via a direct push to `sync`, as done here.
```

## Reference implementation

`arrrrny/kimi-code-sync` — `.github/workflows/sync-upstream.yml` and
`.github/FORK_OWNED_FILES`, plus the "Fork Upstream Sync Policy (MANDATORY)"
section of its `AGENTS.md`. This fork adapts that design, adding the Go build
gate and passing conflict output through the environment rather than
interpolating it into the `github-script` source.
