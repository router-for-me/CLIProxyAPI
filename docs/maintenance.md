# Fork maintenance

`hrygo/CLIProxyAPI` owns `main`, release tags and release assets. The Go module
path remains upstream to keep selective ports small. Procedures below describe
operations; a request to review them does not authorize publishing or deployment.

## Choose the workflow

| Task | Authoritative procedure | Completion evidence |
| --- | --- | --- |
| Develop | [AGENTS.md](../AGENTS.md) | Relevant tests and server build for Go changes |
| Assess or absorb upstream | [Intake](../ops/upstream-intake/README.md) | Decision ledger, baseline and candidate gate results |
| Release | This document | Successful release workflow, complete public assets |
| Install, upgrade or roll back | [Homebrew](../ops/homebrew/README.md) | Formula, installed binary, running service verified separately |
| Agent-assisted intake | [Project skill](../.agents/skills/cliproxyapi-fork-maintenance/SKILL.md) | Same intake evidence; skill discovery grants no deployment authority |

## Release a version

1. Merge the reviewed task branch into our `main`. Resolve compatibility changes
   and choose our semantic version: patch for compatible fixes, minor for
   compatible additions, major for breaking changes.
2. Build the release tag as `vX.Y.Z-upstreamA.B.C`, where `A.B.C` is the
   upstream release this version is aligned to. A version that ports no new
   upstream work keeps the previous release's suffix, because it is still built
   on that baseline; the `Ported` field records that nothing new was taken. The
   suffix is what distinguishes our tags from upstream's identical-looking
   `vX.Y.Z` names, and the workflow rejects a tag that still points at an
   upstream commit.
3. Copy `docs/releases/RELEASE_TEMPLATE.md` to `docs/releases/<tag>.md` and
   write the release from the user's perspective. Explain the compatibility or
   migration impact, validation actually performed, known limitations, and the
   exact upstream release aligned with this version, which must match the tag
   suffix. Validate it locally:

   ```bash
   python3 ops/release-notes/validate.py validate \
     v1.2.3-upstream8.0.6 docs/releases/v1.2.3-upstream8.0.6.md
   ```

   Commit the curated note before tagging. Generated commit summaries do not
   explain compatibility and are not a substitute for this file.
4. Run `ops/upstream-intake/verify-absorb.sh` on the candidate. For protocol
   changes, include the relevant client replay evidence and its coverage limits.
5. Confirm a clean checkout, exact release commit, `origin/main` ancestry, and
   absence of the proposed tag both locally and remotely. Read-only checks:

   ```bash
   git status --short
   git fetch --no-tags origin main
   git merge-base --is-ancestor HEAD origin/main
   git rev-parse HEAD
   git ls-remote --tags origin refs/tags/v1.2.3-upstream8.0.6
   ops/upstream-intake/guard-release-tag.sh v1.2.3-upstream8.0.6
   ```

6. When release execution is authorized, create an annotated tag and push only
   its ref. Replace `v1.2.3-upstream8.0.6` with the reviewed tag:

   ```bash
   git tag -a v1.2.3-upstream8.0.6 -m "Release v1.2.3-upstream8.0.6"
   git push origin refs/tags/v1.2.3-upstream8.0.6:refs/tags/v1.2.3-upstream8.0.6
   ```

   Never push `--tags`, `--mirror` or upstream refs. Do not move a published tag.

7. `release.yaml` validates stable tag syntax, ancestry on `main`, the curated
   release note and its upstream-alignment statement, then runs the full
   regression gate. It creates a draft and builds ten platform archives from the
   tagged source. One final job verifies the complete archive set, downloads the
   uploaded assets and checks their hashes, uploads `checksums.txt`, then
   publishes the draft. A failed build leaves the release unpublished.
7. Verify workflow success and the public assets before updating the tap.
   Manual workflow dispatch accepts an existing tag and resumes an incomplete
   draft; published releases are refused. Fix a published regression with a new
   version rather than replacing its binaries.

The catalog bundled in a release comes from the tag. Update and review model
catalog data as a normal source change before tagging. PR and release builds do
not fetch a floating model catalog. This avoids passing tests against source
that differs from the release commit.

## Verify installation and operation

The tap is a separate repository. Updating `ops/homebrew/cli-proxy-api.rb` here
does not update users' Homebrew metadata. Generate and review the formula, test
it in the tap, then publish that exact formula as a separately authorized step.

Before an upgrade, preserve a tested rollback formula and the existing config.
Afterward verify four separate facts: public Release, tap version and checksum,
installed binary banner, and restarted process behavior. A successful
`brew upgrade` does not establish that a process has restarted.

## Research and local choices

Official documents checked on **2026-10-01**:

- [Codex AGENTS.md](https://developers.openai.com/codex/guides/agents-md/) and
  [skills](https://developers.openai.com/codex/skills/): scoped instructions and
  progressive disclosure. Keep project invariants in `AGENTS.md`, specific
  procedures in `ops/`, and decision guidance in the skill.
- [GitHub immutable releases](https://docs.github.com/en/code-security/supply-chain-security/understanding-your-software-supply-chain/immutable-releases):
  create a draft, attach assets, then publish. The workflow follows this order
  and refuses to rewrite published releases even without account-level
  immutability enabled.
- [GitHub Actions security](https://docs.github.com/en/actions/security-for-github-actions/security-guides/security-hardening-for-github-actions):
  minimum token permissions and full commit SHA pins. Workflows use read
  permissions by default, write only where assets are published, and pin
  official actions to resolved full commit SHAs (with major-version comments).
- [Homebrew Formula Cookbook](https://docs.brew.sh/Formula-Cookbook):
  versioned URLs, SHA256, and a formula test.
- [Homebrew Versions](https://docs.brew.sh/Versions) and
  [manual](https://docs.brew.sh/Manpage): use a maintained versioned formula or
  `brew extract` for an older version. `brew rollback` is not a supported command.

The 72-hour settling rule and cherry-picking individual upstream commits are
this fork's policy, not requirements imposed by GitHub or Homebrew.

Repository-level immutable releases, tag protection and artifact attestations
need separate configuration or a further reviewed workflow change. No remote
settings were changed by the documentation/tooling optimization. SHA256 verifies
integrity; it is not a substitute for provenance.
