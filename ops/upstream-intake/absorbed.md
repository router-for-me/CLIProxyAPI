# Absorbed upstream releases

One row per upstream release that was assessed. "Absorbed" means the listed
commits were ported onto our `main` **and** `verify-absorb.sh` exited 0.

| Upstream | Assessed | Ported | Skipped | Gate | Recorded |
| --- | --- | --- | --- | --- | --- |
| _(none yet)_ | | | | | |

## Comparison baseline

**Baseline: `v8.0.5`** (`e5b5a1cfca354ec80d5c31242ccfb6c0f6b5fbcd`).

Established on 2026-10-01 from repository history rather than from tag
presence. Evidence:

- `v8.0.0` through `v8.0.5` are ancestors of our `main`; `v8.0.6` and
  `v8.0.7` are not. The fork shares upstream history, so ancestry — not tag
  existence — is what establishes that the code is already on our trunk.
- The tags were reached through the shared fork history, not by a reviewed
  port, so this baseline records inherited content only. It carries no
  per-commit assessment and no replay evidence.
- Verified against `main` at `f667aaf566ecabd5e419d11bd50a79a9b6d1637d`.

Do not advance this baseline on tag presence alone. An assessed release is not
automatically a new baseline; record that decision explicitly, along with any
work left skipped.

For each assessment, record the explicit baseline, candidate and our main SHA,
baseline/candidate gate logs, upstream-to-local commit mapping and replay coverage
(or "not performed"). An assessed release is not automatically a new baseline;
record that decision explicitly, including any remaining skipped work.

## Assessments

### v8.0.6 and v8.0.7 — rejected by the age gate, 2026-10-01

Baseline `v8.0.5`, candidate `v8.0.7` (`97f244b8ddb9cbf564b6e6faab0159102cca8617`),
our `main` `f667aaf566ecabd5e419d11bd50a79a9b6d1637d`.

Both releases were fetched with `--no-tags` into `refs/upstream/tags/*` and
rejected before any diff assessment:

```text
$ ops/upstream-intake/assess-release.sh v8.0.7 v8.0.5
error: upstream v8.0.7 was published 5h ago; we require at least 72h
exit=3

$ ops/upstream-intake/assess-release.sh v8.0.6 v8.0.5
error: upstream v8.0.6 was published 6h ago; we require at least 72h
exit=3
```

Assessed at `2026-10-01T00:43:57Z`; `v8.0.6` was 6.6h old and `v8.0.7` was
5.5h old. Nothing was ported and no gate was run, so this is not an absorption.

The five commits in `v8.0.5..v8.0.7` are all `fix(codex)` / `fix(executor)`
compatibility work — the category this fork prefers — so they are expected to
be worth assessing once they settle:

| Upstream | Subject | Earliest eligible |
| --- | --- | --- |
| `a8ffd5a8` | fix(codex): skip tool parameter integer normalization for codex executor targets | 2026-10-03T18:05:31Z |
| `82f8e92b` | fix(executor): support negotiated ALPN protocols in uTLS client | 2026-10-03T18:05:31Z |
| `9e71c20d` | fix(codex): handle upstream stream disconnect before first payload as bad gateway | 2026-10-03T19:14:25Z |
| `67cb32b6` | fix(codex): determine tool integer normalization by target executor identity | 2026-10-03T19:14:25Z |
| `97f244b8` | fix(codex): pass target executor to compatibility translation and token counting | 2026-10-03T19:14:25Z |

`a8ffd5a8` and `82f8e92b` ship in `v8.0.6`; the rest ship in `v8.0.7`. Note
that `67cb32b6` and `97f244b8` revise the approach taken by `a8ffd5a8`, so the
three should be assessed together rather than ported piecemeal.

## How to fill this in

After `assess-release.sh` and `verify-absorb.sh`:

```markdown
| v8.0.6 | 2026-10-01 | `abc1234` tool-schema fix, `def5678` reconnect backoff | `9999999` CI churn, `8888888` config rename | PASS | 2026-10-01 |
```

Keep the skipped column honest. A release where most commits are skipped is a
normal outcome and worth recording; a release with an empty ported column
should say why in a comment below the table.
