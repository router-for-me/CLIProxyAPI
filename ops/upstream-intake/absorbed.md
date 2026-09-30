# Absorbed upstream releases

One row per upstream release that was assessed. "Absorbed" means the listed
commits were ported onto our `main` **and** `verify-absorb.sh` exited 0.

| Upstream | Assessed | Ported | Skipped | Gate | Recorded |
| --- | --- | --- | --- | --- | --- |
| _(none yet)_ | | | | | |

## Comparison baseline

No baseline has been recorded yet. First intake must establish the inherited
upstream release from repository history and record the evidence here. Never
infer absorption from the newest locally fetched tag.

For each assessment, record the explicit baseline, candidate and our main SHA,
baseline/candidate gate logs, upstream-to-local commit mapping and replay coverage
(or "not performed"). An assessed release is not automatically a new baseline;
record that decision explicitly, including any remaining skipped work.

## How to fill this in

After `assess-release.sh` and `verify-absorb.sh`:

```markdown
| v8.0.6 | 2026-10-01 | `abc1234` tool-schema fix, `def5678` reconnect backoff | `9999999` CI churn, `8888888` config rename | PASS | 2026-10-01 |
```

Keep the skipped column honest. A release where most commits are skipped is a
normal outcome and worth recording; a release with an empty ported column
should say why in a comment below the table.
