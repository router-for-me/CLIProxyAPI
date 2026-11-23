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
- Why the response ended, as a stop reason. See [Turns and sessions](#turns-and-sessions).
- The upstream account. OAuth accounts are stored by their email or project;
  API-key credentials are stored masked (`sk-a...0001`).
- The client API key, masked.
- Client IP, user agent, session id, and parent session id.
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
- Every cost is also split by what it paid for: uncached input, cache reads,
  cache writes, and output. Reasoning tokens are billed as output. The four
  parts add up to the total, so the Usage page can show where each model's
  cost comes from.

Subscription value compares that cost with the monthly plan price you enter for
each account, prorated to the days recorded in the selected range. Each account
also lists the models it served, with every model's API-equivalent cost and its
share of the account's total.

## Turns and sessions

A turn is one finished reply. A session is one conversation, which the Usage
page calls a thread. Both are counted at query time from two stored columns,
`stop_reason` and `session_id`.

### Stop reason

Each row stores why its response ended in `stop_reason`, in one vocabulary for
every provider:

| `stop_reason` | Meaning | A turn |
| --- | --- | --- |
| `end_turn` | The model finished its reply. | Yes |
| `max_tokens` | The reply hit the output or context limit. | Yes |
| `refusal` | The model or a content filter declined to answer. | Yes |
| `tool_use` | The model stopped to ask the client to run a tool. | No |
| `pause` | The upstream paused a long server-side turn for the client to continue. | No |
| empty | Unknown. | No |

The proxy translates each protocol's value once, when it parses the response:

| Protocol | Upstream value | `stop_reason` |
| --- | --- | --- |
| Anthropic Messages | `end_turn`, `stop_sequence` | `end_turn` |
| | `tool_use` | `tool_use` |
| | `max_tokens`, `model_context_window_exceeded` | `max_tokens` |
| | `refusal` | `refusal` |
| | `pause_turn` | `pause` |
| OpenAI Responses | `completed`, with an output item that asks the client to run a tool | `tool_use` |
| | `completed`, with no such item | `end_turn` |
| | `incomplete`, with reason `max_output_tokens` | `max_tokens` |
| | `incomplete`, with reason `content_filter` | `refusal` |

- Anthropic Messages reports `stop_reason` at the top of a response, and in
  `delta` of the `message_delta` event of a stream.
- OpenAI Responses has no stop reason, so the proxy reads `response.status` and
  `response.incomplete_details.reason` of the terminal event. The items that ask
  the client to run a tool are `function_call`, `custom_tool_call`,
  `local_shell_call`, `shell_call`, `computer_call`, and `apply_patch_call`.
  Server-side tools, such as web search, run inside the response and do not end
  it.
- Every other value is stored empty.

The Claude executor, for OAuth and API keys, and the Codex executor, over HTTP
and websocket, record a stop reason for streamed and non-streamed requests.
Other executors that parse Anthropic Messages or OpenAI Responses with the same
code, such as xAI, Kimi, Meta, and plugin executors, record one too, but only
Claude and Codex are tested end to end. OpenAI Chat Completions
(`finish_reason`), Gemini (`finishReason`), and every other protocol store an
empty stop reason, so their requests count no turns.

Rows recorded before this version have an empty stop reason. Turn counts start
when the proxy starts recording stop reasons.

### Turns

`turns` counts the rows that did not fail and whose stop reason is `end_turn`,
`max_tokens`, or `refusal`. A coding agent that runs three tools and then
answers makes four requests and one turn.

Each upstream attempt is its own row, so a request that the proxy retries
leaves a failed row before the successful one. A failed row never counts as a
turn, even when its response carried a stop reason, as a stream that broke off
after `message_delta` does. A reply therefore counts once. The same rule leaves
out a Claude refusal that carries `stop_details`, because the proxy returns it
to the client as an error.

### Sessions

`sessions` counts distinct non-empty `session_id` values. The session id is the
one the client declares, such as Claude Code's `X-Claude-Code-Session-Id` or
Codex's `session_id`. A client that declares none gets an id derived from the
conversation's leading instructions and first user message, so two
conversations that start with the same text count as one session.

A subagent has its own session id, and its main session is stored in
`parent_session_id`. `sessions` counts subagents as sessions of their own.
To count only main sessions, filter on `parent_session_id` with an empty value.
Rows recorded before this version have no parent session id either, so that
filter counts their subagent sessions as main sessions.

Unlike every other measure, `sessions` is not a sum. One session can span
models, days, and other groups, so the sessions of the groups add up to more
than the sessions of the range. Never add `sessions` across groups. For the
total of a range, request it without `group_by`, and for sessions per month,
group by `month`.

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
| Codex | `X-Codex-Primary-*` and `X-Codex-Secondary-*`: `Used-Percent`, `Window-Minutes`, `Reset-At` or `Reset-After-Seconds` | The window length: `5h` for 300 minutes, `7d` for 10080, otherwise `<n>d`, `<n>h`, or `<n>m`. A slot with no length is not recorded: an account with one limit reports its other slot empty. |

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
| `GET /stats` | Request, turn, session, token, and cost totals grouped by `group_by` |
| `GET /speed` | Latency and first-token percentiles and output tokens per second |
| `GET /settings` | Price overrides and plan prices, plus the built-in price table |
| `PUT /settings` | Replace price overrides and plan prices |
| `GET /quota-samples` | Quota window history per account, for burn-down charts |
| `GET /limit-value` | API-equivalent cost of one percent of each quota window |

Query parameters for `/stats` and `/speed`:

- `from`, `to`: Unix milliseconds. Default is the last 30 days.
- `utc_offset_minutes`: minutes east of UTC, used to bucket days and hours in
  the viewer's calendar.
- `group_by`: comma-separated dimensions. `/stats` accepts `month`, `day`,
  `hour`, `model`, `alias`, `provider`, `account`, `auth_index`, `auth_type`,
  `client_key`, `client_ip`, `user_agent`, `endpoint`, `project`, `session_id`,
  `parent_session_id`, `stop_reason`, `reasoning_effort`, `service_tier`,
  `status_code`, `stream`, and `failed`. `/speed` accepts one of those, except
  `month`, `day`, and `hour`.
- `filter.<dimension>=<value>`: keep only rows with that exact value.
- `limit`: maximum number of groups (default 2000). The largest groups are kept.

Example:

```bash
curl -H "Authorization: Bearer $MANAGEMENT_KEY" \
  "http://127.0.0.1:8317/v8/management/observability/usage/stats?group_by=day,project"
```

### `GET /stats`

Returns one entry in `groups` per combination of the `group_by` values, with
those values in `dimensions`. Without `group_by`, it returns one group for the
whole range. This group from the end-to-end test is the fake Claude account:

```json
{
  "dimensions": {"provider": "claude"},
  "requests": 7,
  "failed": 1,
  "turns": 3,
  "sessions": 3,
  "input_tokens": 7000,
  "cache_read_tokens": 0,
  "cache_write_tokens": 3750,
  "cache_write_5m_tokens": 1350,
  "cache_write_1h_tokens": 2400,
  "output_tokens": 1400,
  "reasoning_tokens": 0,
  "unclassified_tokens": 0,
  "total_tokens": 12150,
  "web_search_requests": 0,
  "cost_usd": 0.08195,
  "input_cost_usd": 0.028,
  "cache_read_cost_usd": 0,
  "cache_write_cost_usd": 0.02595,
  "output_cost_usd": 0.028,
  "unpriced_tokens": 0
}
```

- `input_cost_usd`, `cache_read_cost_usd`, `cache_write_cost_usd`, and
  `output_cost_usd` split `cost_usd` by token bucket and add up to it.
  `output_cost_usd` covers output and reasoning tokens.

- `turns` and `sessions` are described in [Turns and sessions](#turns-and-sessions).
  `sessions` is the one field that does not add up across groups.
- `cache_write_5m_tokens` and `cache_write_1h_tokens` split
  `cache_write_tokens` by how long the upstream keeps the cache. Only Anthropic
  reports these two lifetimes. OpenAI Responses reports cache writes with no
  lifetime or with 30 minute and 12 hour lifetimes, which are not stored, so
  Codex cache writes count in `cache_write_tokens` alone.

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

### `GET /limit-value`

Answers "what did 1% of this limit buy" for each quota window. It takes
`from`, `to`, and `filter.<dimension>` like `/quota-samples`, with the same
filter dimensions, and returns one entry per series:

```json
{
  "from": 1794707338923,
  "to": 1794707464790,
  "unpriced_models": [],
  "series": [
    {
      "provider": "claude",
      "auth_index": "0682b3e397dccf1e",
      "account": "sk-a...-key",
      "window": "7d",
      "model_scope": "",
      "intervals": 47,
      "percent_used": 48,
      "requests": 277,
      "failed": 0,
      "turns": 277,
      "sessions": 3,
      "input_tokens": 2242000,
      "cache_read_tokens": 0,
      "cache_write_tokens": 0,
      "cache_write_5m_tokens": 0,
      "cache_write_1h_tokens": 0,
      "output_tokens": 45891600,
      "reasoning_tokens": 0,
      "unclassified_tokens": 0,
      "total_tokens": 48133600,
      "web_search_requests": 0,
      "cost_usd": 1138,
      "unpriced_tokens": 0,
      "usd_per_percent": 23.708333333333332,
      "output_tokens_per_percent": 956075,
      "models": [
        {
          "model": "claude-opus-5-5",
          "intervals": 20,
          "percent_used": 20,
          "requests": 170,
          "failed": 0,
          "turns": 170,
          "sessions": 1,
          "input_tokens": 1700000,
          "cache_read_tokens": 0,
          "cache_write_tokens": 0,
          "cache_write_5m_tokens": 0,
          "cache_write_1h_tokens": 0,
          "output_tokens": 33660000,
          "reasoning_tokens": 0,
          "unclassified_tokens": 0,
          "total_tokens": 35360000,
          "web_search_requests": 0,
          "cost_usd": 680,
          "unpriced_tokens": 0,
          "usd_per_percent": 34,
          "output_tokens_per_percent": 1683000
        }
      ]
    }
  ]
}
```

The example is the end-to-end test's fake Claude account, cut to one model.

How the numbers are built:

- An interval is the span between two consecutive samples of one series. The
  range holds the intervals whose later sample is in `[from, to)`, so the
  first interval can start at the `carry_in` sample before `from`.
- Only intervals in which `used_percent` rose count. A drop is a window reset
  or a redeemed reset grant, and the requests in it are left out. So is an
  interval whose two samples report resets more than a minute apart, even
  when the percent rose: its samples belong to two windows.
- A request belongs to the interval that contains its completion time,
  `ts_ms + latency_ms`, counting the interval's later sample as inside it.
  Requests join a series by `auth_index`, and a request can have started
  before `from`.
- The series fields sum the counted intervals: `intervals`, `percent_used`,
  and the request, turn, token, and cost fields of `/stats`, priced with the
  same price table. `sessions` counts the distinct sessions of the requests in
  the counted intervals.
  `usd_per_percent` is `cost_usd / percent_used`, and
  `output_tokens_per_percent` is `(output_tokens + reasoning_tokens) /
  percent_used`. Both are `null` when `percent_used` is 0.
- `models` holds a model only from the intervals in which it carried at least
  90% of the interval's cost. Such an interval adds all of its requests, so up
  to 10% of a model's cost can come from other models. Use `intervals` and
  `percent_used` to judge how much evidence a figure rests on. Mixed intervals
  count only in the series totals.
- A window that only moves with one model family is charged only with that
  family's requests, by `model_scope`:

| Window | `model_scope` | Why |
| --- | --- | --- |
| Claude `7d_oi` | `fable` | Anthropic reports the Fable weekly limit under the `seven_day_overage_included` claim. While it is exhausted, Opus requests on the same account still succeed (upstream issue #7197). |
| Claude `7d_<x>`, any other suffix | `x` | The suffix names the family, as `7d_sonnet` and `7d_opus` do. A suffix that is not a family matches no model, so the series shows intervals with no cost. |
| Every other window | `""` | The window is charged with every model of the credential. A Codex model with its own limit, such as GPT-5.3-Codex-Spark, may not move the main Codex windows; that limit is not recorded. |

A model is in a family when the family is one of the parts of its name
between dashes, dots, or slashes: `claude-fable-5-1` is in `fable`.

### Accuracy of `usd_per_percent`

The figures recover a known price per percent within 1% in a simulated
account (`TestLimitValueRecoversKnownDollarsPerPercent`) and within 0.8% in an
end-to-end run of the server against a fake upstream
(`TestUsageQuotaEndToEnd`, run with `CLIPROXY_E2E=1`). These limit them on a
real account:

- **Quantization.** Claude reports utilization to 1% and Codex reports whole
  percents. Within a run of rising intervals the rounding cancels out, and
  what is left is the rounding at each end of the run, up to one reporting
  step each: a first sample taken just under a step makes a small request
  look like a whole percent. The error is about one step divided by
  `percent_used`, so a figure over 20% of evidence is good to a few percent
  and a figure over 2% is not.
- **Usage the proxy does not see.** Requests made outside the proxy, such as
  claude.ai or a client signed in directly, raise the percent with no cost,
  which makes the figures too low. So do gaps in recording.
- **List prices.** Cost is the API-equivalent cost at list prices, with the
  limits described under [Cost](#cost). Tokens of unpriced models add no
  cost; the models are listed in `unpriced_models`.
- **When the state is taken.** A sample is stamped with the start of the
  request that reported it, on the inference that the upstream takes the
  state when it admits a request. Requests that complete between a request's
  start and its admission upstream land one interval late.
- **Millisecond timestamps.** Request and sample times are stored in
  milliseconds. A request that completes within about 2 ms of another
  request's start on the same account can land one interval early. Real
  upstream latencies make this rare; with sub-millisecond responses it
  dominates.
- **Window scope.** The `7d_oi` to Fable mapping comes from observed headers,
  not from an Anthropic contract. If a window moves with models outside its
  scope, its figure is too high.
- **Model weights.** A model's figure comes only from intervals it dominated,
  so it rests on fewer intervals than the series total, and a model that is
  always mixed with others gets no figure.
