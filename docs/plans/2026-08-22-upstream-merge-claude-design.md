# Upstream Claude Sync — v7.2.128 → v7.2.138

**Date**: 2026-08-22
**Baseline (nixllm core)**: `v7.2.128` (nixllm tag `v7.2.128-0.1.16`)
**Target (upstream)**: `v7.2.138`
**Author**: abilfida + Claude

## Goal

Port 28 upstream commits in the `claude` area (since `v7.2.128`) into NixLLM
selectively, without disturbing NixLLM-only features (autorouter, dashboard SPA,
internal-users, alerts, policy, pgstore, embeds).

## Scope

- 28 commits matching `(fix|feat|refactor|pref)\(claude` between
  `bd34ceca04209ef0460f4b05e3a1a047fb7fad2a` and upstream `v7.2.138`.
- Excluded: anything touching `internal/translator/` unless it is a side-effect
  of a Claude-area commit (AGENTS.md: do not edit `internal/translator/`
  standalone).
- Excluded: all other areas (openai, gemini, codex, antigravity, auth,
  management, watcher, sdk/cliproxy) — addressed in later sync passes.

## Divergence state

Only one Claude-touched file diverges between NixLLM and upstream `v7.2.128`:

- `internal/runtime/executor/claude_executor_execute.go` — NixLLM has its own
  changes. Conflicts expected for commits touching this file.

All other Claude-touched files match upstream `v7.2.128` byte-for-byte, so
cherry-picks are clean except where `claude_executor_execute.go` is involved.

NixLLM-only directories (`autorouter`, `dashboardasset`, `policy`,
`pricingsource`, `upstreamsync`, `backup`, `errormessages`, `web/dashboard`)
are not touched by any Claude-area upstream commit.

## Strategy

Cherry-pick per-batch with `-x` (records upstream SHA for traceability).
Pause after each batch, report applied / skipped / conflict-resolved count,
wait for OK before proceeding.

### Batch plan (6 batches)

| Batch | Theme | Commits | Difficulty |
|-------|-------|---------|------------|
| B1 | Single-file minimal-fix | 11 | low |
| B2 | OAuth & alias recovery | 4 | mid |
| B3 | Rate-limit & cooldown | 3 | mid |
| B4 | Header & subagent pass-through | 3 | low-mid |
| B5 | Translator & request conversion | 5 | mid-high |
| B6 | Refactor & feature besar (fingerprint-profile, CCH signing, Anthropic rate-limit) | 3 | high |

### Per-batch procedure

1. `git cherry-pick -x <sha>` (one commit at a time).
2. If conflict: inspect nixllm vs `v7.2.128` for context, three-way merge
   manually, `git add` + `cherry-pick --continue`.
3. `gofmt -w .` after the batch.
4. `go build -o test-output ./cmd/server && rm test-output` (AGENTS.md required).
5. `go test ./internal/runtime/executor/... ./internal/translator/...`.
6. Report and pause.

### Stop conditions

- Build fails after 2 attempts → stop the batch, report.
- Previously-passing tests fail → stop, report.
- Cherry-pick requires redesign (not just conflict) → stop.

## Versioning

Per `nixllm-version-tag-pairing.md`, NixLLM release tag is `v<core>-0.<patch>`.
After porting core v7.2.138, new core prefix; reset NixLLM patch to `0.1.0`.

Recommended tag: **`v7.2.138-0.1.0`** (reset signals core bump clearly).

Alternative: `v7.2.138-0.2.0` (continuous from `0.1.x` lineage). Default to
`v7.2.138-0.1.0` unless told otherwise.

## Out of scope (this design)

- ❌ Pushing to `origin` without explicit OK.
- ❌ Merging to `main` without explicit OK.
- ❌ Editing `main` directly.
- ❌ Porting other areas (openai, gemini, codex, auth, management, sdk) —
  handled in later sync passes.

## Result (filled in after porting)

**Session 1 (2026-08-22): partial sync, paused at decision point.**

### Applied commits (final state)

| Batch | SHA | Subject | Status |
|-------|-----|---------|--------|
| B1 | `0ac4e1ea` | fix(claude): drop auto context_management without eligible thinking | ✅ applied |
| B1 | `ec71b9c8` | fix(claude): recover OAuth tool names with duplicated server alias prefixes | ✅ applied |
| B1 | `bbdab8a8` | fix(claude): recover OAuth tool aliases for repeated prefixes and malformed IDs | ✅ applied |
| B1 | `31e9835b` | fix(claude): keep tool_result blocks first when injecting currentDate | ✅ applied |
| B5-prereq | `d4d31539` | fix(claude,gemini,antigravity): centralize tool-call ID generation | ✅ applied (auto-merge) |
| B5-prereq | `6bbda17f` | fix(claude): deduplicate duplicate tool outputs | ✅ applied (auto-merge) |
| B1 | `c01a0b45` | pref(claude): keep raw Claude tool IDs for deduplication | ✅ applied |
| B1 | `5e6b32b8` | feat(claude): map OpenAI `service_tier` to Claude `speed` | ✅ applied |
| B6-prereq | `85cd5a2b` | fix(claude): restore cloaked prompt-cache ownership and native shape | ✅ applied (required for B1 test build) |
| B6 | `f1b0431c` | feat(claude): add fingerprint-profile=claude-code-cli | ❌ reverted (NixLLM-divergence too large) |

### Deferred (require prerequisite commit chains not yet ported)

| Batch | SHA | Subject | Blocker |
|-------|-----|---------|---------|
| B1 | `a8f9814a` | fix(claude): keep Fable-only rate limits model scoped | needs cooling refactor (5bffd151) which diverges in 11 NixLLM files |
| B1 | `3230e370` | fix(claude): accept both `max_tokens` and `max_completion_tokens` | post-prereq (was blocked by 7eefab98 — now resolved) |
| B1 | `4ac37ed3` | fix(claude): always send `stop` as an array | needs `f1b0431c` (deferred) |
| B1 | `5fef17e2` | fix(claude): pass incoming headers in caller-owned mode | transitive (4ac37ed3) |
| B1 | `aa5dccc2` | fix(claude): use `message.reasoning_content` | needs `788e9b79` (B4) |
| B2-B6 | various | not attempted this session | |

### Validation

- `gofmt -w .` clean
- `go build -o test-output ./cmd/server` OK
- Tests pass except 3 pre-existing failures unrelated to cherry-picks:
  - TestApplyClaudeHeaders_DisableDeviceProfileStabilization
  - TestApplyClaudeHeaders_LegacyModePreservesConfiguredUserAgentOverrideForClaudeClients
  - TestClaudeExecutor_NonClaudeRequestUsesClaudeCode220CLIFingerprint
  These fail identically on `main` NixLLM (environment-dependent, expects `Linux`,
  receives `MacOS` from `helps.MapStainlessOS()`).

### Lessons learned

1. **Topological ordering is essential**: Upstream patches assume their parent
   chain. `git cherry-pick` in chronological order breaks because prerequisite
   commits (e.g., `616d1b11`, `1ecb7df2`) introduce helpers that downstream
   commits depend on.
2. **`git revert --no-edit` is imperfect**: When reverting a commit that
   cherry-picked cleanly via auto-merge, leftover helper calls in modified files
   and new test files can remain. Manual cleanup of `>>>>>>>`, residual helper
   calls, and untracked test files is required.
3. **NixLLM has substantial divergence in helper signatures**: `claude_executor_execute.go`,
   `config_lists.go`, and many `sdk/cliproxy/auth/*` files diverge from upstream.
   `f1b0431c` (fingerprint-profile) requires too many new helpers to port cleanly.
4. **Test-helper signature changes cascade**: `f0034ca6` (B6) upgraded
   `assertEphemeralUserTextBlock` from 3 to 4 args. Without that commit, the
   `297139cc` test does not compile.

### Next steps (deferred)

- B2 (OAuth/alias recovery): `bdde638c` and `f6f03e4d` blocked — NixLLM uses
  base32 digest for MCP aliases, upstream replaced it with BIP-39 wordlist.
  `8aa6868d` blocked — modifies `claude_cli_identity_seed.go` which NixLLM never
  ported (created by `f1b0431c`).
- B4 (header & subagent): `788e9b79` and `85d2fadd` reverted. `788e9b79` adds
  `restoreClaudeOAuthToolNamesFromResponse`/`StreamLine` reverse-remap logic
  that conflicts with NixLLM's existing implementation. `85d2fadd` chains on
  the same reverse-remap. Both deferred.
- B6 with care: design a smaller per-commit port for cooling refactor + Anthropic
  rate-limit helpers, accepting larger conflict surface
- OpenAI, Gemini, Codex areas (deferred to separate session)

### Session 2 attempts (2026-08-22)

- Tried `788e9b79` (tool_search_tool_result, B4): applied with auto-merge but
  test `TestReverseRemapPassesThroughCallerMCPToolsOnVirtualServerCollision` failed
  because NixLLM's `restoreClaudeOAuthToolNamesFromResponse` does not pass caller
  MCP tools through unchanged (NixLLM-divergence in reverse-remap). Reverted
  with manual cleanup of leftover test functions.
- Tried `8aa6868d` (setup-tokens, B2): modify/delete conflict on
  `claude_cli_identity_seed.go` (NixLLM never ported this helper). Aborted.
- Tried `bdde638c` (alias server collision, B2): conflict in
  `claude_mcp_alias.go` — NixLLM uses base32 digest, upstream uses BIP-39
  wordlist with `AllocateClaudeMCPToolAlias`. Aborted.

Conclusion: B2/B4 commits require NixLLM-side redesign of MCP alias and
reverse-remap logic before they can be ported. Out of scope for current session.

### Files NOT to commit in this state

- ~~Branch is **not** pushed to origin~~ → pushed as `upstream/v7.2.138-claude-sync`
- Branch is **not** merged to main
- ~~Tag `v7.2.138-0.1.0` is **not** created~~ → created and pushed

### Release actions taken (2026-08-22)

- Branch `upstream/v7.2.138-claude-sync` pushed to origin
  (`https://github.com/abilfida/nixllm/tree/upstream/v7.2.138-claude-sync`).
- Annotated tag `v7.2.138-0.1.0` created at `bf8e87bb` (final cleanup commit)
  and pushed. Tag message: "v7.2.138-0.1.0: upstream core sync to v7.2.138
  (10 Claude-area commits, 2 reverted due to NixLLM-divergence)".
- Tag follows the NixLLM release-tag convention `v<core>-0.<patch>` per
  `nixllm-version-tag-pairing.md`. Core bumped from v7.2.128 → v7.2.138;
  patch reset to 0.1.0 to signal the core rebase.