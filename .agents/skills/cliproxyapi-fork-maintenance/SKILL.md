---
name: cliproxyapi-fork-maintenance
description: >
  维护 hrygo/CLIProxyAPI 自有 fork 的上游 release 评估与选择性吸收、自有版本发布、
  Homebrew tap 安装、升级与回退，并校验或修复这些维护流程。用户请求上述工作时使用；
  普通 API 功能开发、一般运行故障排查和其他仓库同步不触发。
---

# CLIProxyAPI fork maintenance

Maintain the `hrygo/CLIProxyAPI` fork with `main` as trunk and independent
`vX.Y.Z-upstreamA.B.C` releases. Keep `module github.com/router-for-me/CLIProxyAPI/v8`
unchanged.
The requested operation determines which procedure to load:

| Request | Read | Expected result |
| --- | --- | --- |
| Assess or absorb upstream releases | [Intake](../../../ops/upstream-intake/README.md) and [ledger](../../../ops/upstream-intake/absorbed.md) | Explicit baseline, per-commit decision, verification evidence |
| Prepare or execute our release | [Maintenance](../../../docs/maintenance.md), release section | Reviewed version/commit, successful complete release |
| Install, upgrade, roll back or update the tap | [Homebrew](../../../ops/homebrew/README.md) | Verified formula, binary and service state |
| Audit or repair these processes | The affected procedure and script/workflow | Evidence-backed correction and isolated verification |

Read only the branch needed for the task. The documents and this skill describe
operations; they do not grant permission to push tags, publish a tap, change
installed binaries, or restart a service. Existing explicit authorization
continues to apply to its target; do not ask again for routine steps it covers.

## Upstream decisions

- Accept published stable releases after at least 72 hours, measured from
  `publishedAt`. The window exists to let regressions and hurried follow-up
  fixes surface upstream before we port from a release. Upstream publishes
  frequently, so a fixed wait costs little; do not shorten it, tier it by
  commit prefix, or lower it through an environment override.
- Use an explicit reviewed baseline from the ledger; a fetched tag does not mean
  it was absorbed. For first intake, derive it with `establish-baseline.sh` and
  record the evidence. Ancestry against the trunk is the evidence, not tag
  presence: this fork shares upstream history.
- Fetch only into `refs/upstream/tags/*` with `--no-tags`. Never push those refs.
- Run `check-tag-namespace.sh` when unsure whether upstream tags leaked into
  `refs/tags/*`. It never proposes deleting a tag origin also publishes, because
  this fork's version numbers overlap upstream's historical ones.
- Evaluate individual diffs, including overlap with our changes. Prefer protocol,
  compatibility and security fixes; assess configuration changes and dependency
  upgrades on impact rather than commit-message prefixes alone.
- When an upstream commit and our fork solve the same problem, prefer the upstream
  implementation. Keep fork-only deltas only where they cover behavior upstream does
  not address.
- Cherry-pick selected commits individually, preferably with `-x`. Do not merge
  upstream `dev`/`main` or a release range into our trunk.
- Verify the baseline before porting and the candidate afterward. A baseline
  failure blocks absorption; identify its cause without silently broadening a
  sync request into unrelated fixes or recording a blanket waiver.
- Record upstream/local SHAs, skipped work and reasons, exact gate results and
  client replay coverage. A green gate only covers the checks actually run.

## Release and installation decisions

Release only reviewed commits reachable from our `main`. Tag releases
`vX.Y.Z-upstreamA.B.C`, where the suffix names the upstream release the tag is
aligned to and must match the note's upstream alignment. A release that ports no
new upstream work keeps the previous release's suffix, because it is still
built on that baseline; record that in `Ported` rather than declaring the
alignment absent. Push a single own tag, assemble complete assets as a draft,
and publish after final checksum validation. Do not move published tags or
overwrite published assets; fix forward with a new version. Every release
requires a curated `docs/releases/<tag>.md` that explains
user-visible changes, validation limits and the exact aligned upstream release.
Update the separate tap only after the public release is complete.

Upgrade through Homebrew. Prepare a versioned rollback formula first when a
rollback path is needed. Verify public assets, tap metadata, installed binary and
running process separately. Preserve existing config and credentials; do not
replace a Homebrew-managed executable with a hand-built file.

If completing the task requires an uncovered configuration migration, public
interface change or deployment decision, explain the concrete impact and ask for
that decision while continuing independent work. Report failed and unverified
checks, rather than converting them into completion claims.
