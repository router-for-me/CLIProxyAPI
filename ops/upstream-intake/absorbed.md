# Absorbed upstream releases

One row per upstream release that was assessed. "Absorbed" means the listed
commits were ported onto our `main` **and** `verify-absorb.sh` exited 0.

| Upstream | Assessed | Ported | Skipped | Gate | Recorded |
| --- | --- | --- | --- | --- | --- |
| _(none yet)_ | | | | | |

## How to fill this in

After `assess-release.sh` and `verify-absorb.sh`:

```markdown
| v8.0.6 | 2026-10-01 | `abc1234` tool-schema fix, `def5678` reconnect backoff | `9999999` CI churn, `8888888` config rename | PASS | 2026-10-01 |
```

Keep the skipped column honest. A release where most commits are skipped is a
normal outcome and worth recording; a release with an empty ported column
should say why in a comment below the table.
