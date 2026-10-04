# Retrying after an early provider quota reset

A manual usage reset or subscription upgrade can restore quota before the deadline
reported by an earlier upstream 429. The proxy receives no notification of that
change and ordinarily retains the reported deadline. The authenticated management
cooldown reset remains available for immediate operator-directed recovery.

To let subsequent requests discover early recovery, optionally bound quota cooldowns:

```yaml
routing:
  cooldown:
    quota-retry-interval-seconds: 300
```

The default is `0`, which preserves existing behavior. Negative values also disable
the option. Positive values below ten seconds use ten seconds. Choose an interval
appropriate for the upstream limits; shorter intervals cause more attempts against
accounts that may still be exhausted.

This setting caps local quota retry deadlines, including existing in-memory state
on config reload and saved cooldowns on restoration. The next ordinary request
after the deadline can reach upstream. No background inference calls or usage
polling are added. The existing request concurrency and retry settings still apply.
A new quota rejection establishes another bounded cooldown; successful requests
use the existing recovery path. Shorter provider deadlines remain unchanged.

Disabled credentials, unauthorized credentials, forced cooldowns and independent
failure deadlines are preserved. When a retry deadline is longer than the quota
deadline, or its last error is unrelated to quota, the restriction is retained
conservatively. This option does not guarantee recovery through such mixed failures.
Already shortened deadlines are persisted and are not lengthened when the option
is subsequently disabled. The option applies to locally managed credentials.
