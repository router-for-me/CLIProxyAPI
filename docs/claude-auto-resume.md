# Claude auto-resume fork

This fork ports router-for-me/CLIProxyAPI PR #6245 onto v8.0.73, retaining its opt-in default. It also applies the configured status to the initial request that exhausts the last account, normalizes the retained Anthropic error body to `overloaded_error` for 529, and applies the setting through the embedded SDK lifecycle.

This option is global: every provider and API surface uses the selected client-facing cooldown status. Internal short-cooldown retries and quota fallback keep their existing 429 semantics. Claude fast-mode failures that explicitly terminate a request retain their direct upstream response; this option does not override those termination instructions.

```yaml
routing:
  cooldown:
    status-code: 529
```

Set these values in Claude Code's settings `env` (including Orca's selected managed profile):

```json
{
  "CLAUDE_CODE_MAX_RETRIES": "100000",
  "CLAUDE_CODE_RETRY_WATCHDOG": "1",
  "CLAUDE_CODE_RETRY_WATCHDOG_MAX_WAIT_MS": "21600000",
  "CLAUDE_CODE_RETRY_WATCHDOG_MAX_USAGE_LIMIT_WAIT_MS": "21600000"
}
```

Current Claude Code rejects long `Retry-After` delays without its persistent retry watchdog; increasing the retry count alone is insufficient. The six-hour watchdog budget covers a five-hour quota window. Keep `disable-cooling` false. The core continues account failover first, then returns 529 with the pool's trusted `Retry-After` when no credential is available. Claude Code keeps the request pending and retries after the delay. No upstream quota is restored or reset by this option. The default status remains 429 when the option is omitted or invalid.

Already terminated turns need one explicit continuation after installation. Do not start a second agent for an existing conversation. Existing Claude processes might need to be resumed/restarted to inherit a newly added environment setting; future processes read it from their selected profile.

## Verification

- Route/manager regression tests cover both the first exhausted-pool request and later cooling-pool requests, streaming, non-streaming and token counting, both 429 and 529, and no upstream requests during local cooldown.
- `python3 scripts/check_claude_resume.py --binary /path/to/cli-proxy-api` runs the real Claude Code against the built core and a local fake upstream. It uses only dummy credentials, injects a quota failure, and checks automatic retry and completion after reset. Run it in an Orca terminal to verify Orca's selected executable and process environment too. Add `--cooldown 7200 --expect-wait 7` to check that a two-hour hint remains pending instead of immediately failing.
- This is a core replacement. EasyCLIProxyAPI's app, account files, OAuth sessions and agents' conversations do not need reinstalling. Keep a backup of the previous core binary and configuration; installing an official core update can replace this patch.

Original contribution: https://github.com/router-for-me/CLIProxyAPI/pull/6245 by ggbdpq. The fork's additional code and verification were agent-written; review evidence is recorded in the pull request.
