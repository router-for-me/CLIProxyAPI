# Sequential routing

`routing.strategy: "sequential"` sends all traffic for a provider to one account
at a time and only moves when that account is nearly used up:

1. Stay on the current account while every usage window is below the reserve
   mark (95% used with the default 5% reserve).
2. When any window reaches the mark, move to the next account in order that is
   below it, wrapping round after the last account.
3. If every account is at the mark, spend the reserves in order: account 1 until
   it is rejected, then account 2, and so on.

A reset on an account the pool has already left does not bring traffic back.
The pool only moves when the account it is on reaches the mark, so a reset
elsewhere waits its turn. While reserves are being spent, an account that
resets and has room again takes over at once.

```yaml
routing:
  strategy: "sequential"
  sequential-reserve-percent: 5 # default 5; 0 switches only at the limit
```

## Order and usage readings

Accounts are ordered by credential `priority` (highest first), then by
credential ID, which for file credentials is the file name. Set `priority` in a
credential's JSON to choose the order.

Usage comes from the headers on each upstream response:

- Codex: `x-codex-primary-used-percent` (5-hour) and
  `x-codex-secondary-used-percent` (weekly), with their reset times.
- Claude subscription accounts: `anthropic-ratelimit-unified-5h-utilization`
  and `anthropic-ratelimit-unified-7d-utilization`, with their reset times.

A reading counts only until its window's reset time. An account with no
reading yet, such as after a restart or an API-key credential without these
headers, counts as having room. Model-specific limits (such as a separate
weekly limit for one model) are not tracked.

## Behaviour details

- Session affinity is not used with this strategy; the pool's account already
  keeps every session together.
- If the current account cannot take a request (a short cooldown, or it was
  already tried for this request), that request goes to the next account with
  room and the pool stays where it is.
- A Codex websocket session pinned to an account that reached the mark drops the
  pin on its next turn when another account has room. A follow-up turn that
  needs the old upstream socket is replayed by the client with full context.
  A session whose compacted history came from the old account keeps using that
  account, because the proxy replays compaction on the account that produced it.
- Each pool's current account is saved to `sequential-routing.state` in the auth
  directory, so restarts and reboots continue where they left off. The log
  records every move as `sequential routing: <provider> moved from ... to ...`.
- Requests already in flight finish on their account, and readings arrive
  after the fact, so an account can end slightly past the mark. The reserve
  absorbs this.