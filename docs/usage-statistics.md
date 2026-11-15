# Usage statistics

The proxy can record one row per upstream request and serve aggregates for the
management panel's **Usage** page: tokens and API-equivalent cost over time,
what each subscription delivered against its price, and breakdowns by project,
model, client key, and more.

## Turning it on

Set the flag in `config.yaml`, or use the "Turn on recording" button on the
Usage page:

```yaml
observability:
  usage:
    usage-statistics-enabled: true
```

The change applies on config reload, with no restart. Recording is off in Home
(cluster) mode, where usage is reported to Home instead.

## What is stored

Rows go to a SQLite database at `<auth-dir>/usage.db`. Each row holds:

- Time, provider, model, endpoint, status code, latency, and time to first token.
- Token counts in mutually exclusive buckets: uncached input, cache read, cache
  write (with the 5 minute and 1 hour split), output, and reasoning.
- The upstream account. OAuth accounts are stored by their email or project;
  API-key credentials are stored masked (`sk-a...0001`).
- The client API key, masked.
- Client IP, user agent, and session id.
- The working directory the coding agent declared, and the project derived
  from it.

No prompt or response content is stored, and no full API key.

The same database also holds quota samples: how full each subscription
window was each time an upstream reported it. See [Quota samples](#quota-samples).

Rows are kept until you delete them, also after recording is turned off. To
start over, stop the server and delete `usage.db` together with its `-wal` and
`-shm` files. The database file is readable by its owner only.

## Projects

The proxy never sees a repository name. Coding agents state their working
directory in each request (Claude Code in a system message or its system
prompt, Codex in its environment context), and the project is the last
directory of that path. Only those places are read, never user or assistant
content, and only near the start of the conversation.
Agent worktrees are folded into their repository:

| Working directory | Project |
| --- | --- |
| `~/repos/app` | `app` |
| `~/.t3/worktrees/app/t3-1234` | `app` |
| `~/repos/app/.claude/worktrees/feature` | `app` |
| `~/repos/app/.worktrees/feature` | `app` |
| `~/.codex/worktrees/1a2b/app` | `app` |

A request that declares no working directory inherits the one last seen in its
session for the same client key, and is otherwise listed as unknown.

## Cost

Cost is computed at query time from the stored token counts and a price table,
so it is an estimate of what the same usage would cost at API list prices. It is
not a bill. Because nothing is priced at write time, changing a price re-prices
all history.

- Built-in prices live in `internal/usagestats/default_prices.json`, which
  records its source and retrieval date. Refresh that file when providers
  change their prices.
- A model name matches its exact price first, then the longest priced name it
  extends at a separator, so `claude-opus-4-5-20251101` uses the
  `claude-opus-4-5` price.
- Models without a price are listed on the Usage page, and their tokens are
  left out of the cost. Add or override prices with the page's **Prices** button.
- Service tiers, batch discounts, and long-context surcharges are not modelled.

Subscription value compares that cost with the monthly plan price you enter for
each account, prorated to the days recorded in the selected range. Each account
also lists the models it served, with every model's API-equivalent cost and its
share of the account's total.

## Quota samples

Claude and Codex subscriptions report in every response how much of each
usage window is used. The proxy keeps a history of those reports in the
`quota_samples` table, one row each time a window's state changes:

| Column | Meaning |
| --- | --- |
| `ts_ms` | When the upstream reported the state, in Unix milliseconds |
| `provider` | `claude` or `codex` |
| `auth_index` | The credential, the same value as `requests.auth_index` |
| `account` | The account, masked by the same rule as `requests.account` |
| `window` | The window id, described below |
| `used_percent` | Used share of the window, 0 to 100 |
| `resets_at_ms` | When the window resets, in Unix milliseconds, or 0 when unknown |

Window ids are the same across providers where the windows match:

| Provider | Headers | Window id |
| --- | --- | --- |
| Claude | `Anthropic-Ratelimit-Unified-<w>-Utilization` (a fraction) and `-<w>-Reset` (Unix seconds) | `<w>` in lowercase: `5h`, `7d`, `7d_oi`, `7d_<name>` |
| Codex | `X-Codex-Primary-*` and `X-Codex-Secondary-*`: `Used-Percent`, `Window-Minutes`, `Reset-At` or `Reset-After-Seconds` | The window length: `5h` for 300 minutes, `7d` for 10080, otherwise `<n>d`, `<n>h`, or `<n>m`. `primary` or `secondary` when the length is missing. |

Codex limits for a single model, such as `X-Codex-Bengalfox-*`, are not
recorded.

Rules for writing a sample:

- `ts_ms` is the start of the request whose response reported the state. The
  upstream takes the state when it admits the request, so the state leaves
  out that request's own usage. The time to first token would be later: it can
  include a model's thinking.
- A sample is written only when `used_percent` changed, or `resets_at_ms`
  moved by a minute or more, since the newest stored sample of the same
  `(provider, auth_index, account, window)`. A Codex reset given as seconds
  from now drifts by a second or two between responses, which is why small
  moves are ignored.
- A report older than the newest stored sample is dropped. It comes from a
  long stream that finished after shorter requests had already reported a
  newer state.
- The newest sample of each series is kept in memory and read from the
  database on first use, so a restart does not write duplicates.
- Failed attempts, such as a 429, still carry the headers and are sampled.
  Requests without a credential index are not.

## Management API

All endpoints are under `/v8/management/observability/usage` and need the
management key. They answer `409` with `usage_statistics_disabled` while
recording is off.

| Endpoint | Purpose |
| --- | --- |
| `GET /stats` | Token, request, and cost totals grouped by `group_by` |
| `GET /speed` | Latency and first-token percentiles and output tokens per second |
| `GET /settings` | Price overrides and plan prices, plus the built-in price table |
| `PUT /settings` | Replace price overrides and plan prices |
| `GET /quota-samples` | Quota window history per account, for burn-down charts |

Query parameters for `/stats` and `/speed`:

- `from`, `to`: Unix milliseconds. Default is the last 30 days.
- `utc_offset_minutes`: minutes east of UTC, used to bucket days and hours in
  the viewer's calendar.
- `group_by`: comma-separated dimensions. `/stats` accepts `day`, `hour`,
  `model`, `alias`, `provider`, `account`, `auth_index`, `auth_type`,
  `client_key`, `client_ip`, `user_agent`, `endpoint`, `project`, `session_id`,
  `reasoning_effort`, `service_tier`, `status_code`, `stream`, and `failed`.
  `/speed` accepts one of those, except `day` and `hour`.
- `filter.<dimension>=<value>`: keep only rows with that exact value.
- `limit`: maximum number of groups (default 2000). The largest groups are kept.

Example:

```bash
curl -H "Authorization: Bearer $MANAGEMENT_KEY" \
  "http://127.0.0.1:8317/v8/management/observability/usage/stats?group_by=day,project"
```

### `GET /quota-samples`

Takes `from`, `to`, and `filter.<dimension>` like `/stats`. The filter
dimensions are `provider`, `auth_index`, `account`, and `window`. It returns
one series per `(provider, auth_index, account, window)` that has a sample in
the range or before it:

```json
{
  "from": 1794700000000,
  "to": 1794786400000,
  "bucket_ms": 0,
  "series": [
    {
      "provider": "claude",
      "auth_index": "9d7cca53e8b86e21",
      "account": "me@example.com",
      "window": "7d",
      "carry_in": {"ts_ms": 1794690000000, "used_percent": 41, "resets_at_ms": 1795066345000},
      "points": [
        {"ts_ms": 1794706348637, "used_percent": 42, "resets_at_ms": 1795066345000}
      ],
      "thinned": false
    }
  ]
}
```

- `points` are the samples in `[from, to)`, oldest first. Two points can share
  a `ts_ms`.
- `carry_in` is the newest sample before `from`, or `null`. The window held
  that state when the range began, so a chart can start at the left edge. A
  series can have a `carry_in` and no points.
- A series with more than 1000 points in the range is thinned: the range is
  split into 500 buckets of `bucket_ms` each, and each bucket keeps its
  highest and its last point. The highest point keeps the peak a window
  reached before it reset. `thinned` is `true` on such a series, and
  `bucket_ms` is 0 when no series was thinned.
- Series are sorted by provider, auth index, account, and window.
