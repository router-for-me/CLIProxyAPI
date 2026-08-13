# PostgreSQL Sizing Baseline

This page is a sizing reference for the NixLLM proxy's optional PostgreSQL
backend (`PGSTORE_DSN`). It covers the CPU/RAM/disk/Database-style knobs you
need for the workloads the proxy actually generates, plus the env knobs added by
the usage-stat scaling effort and the accepted rollup-freshness tradeoff.

## Workloads and how each maps to DB load

The proxy does almost nothing to PostgreSQL in its default configuration —
tokens, keys, and routing state live in memory and on disk. The DB only earns
its keep when you turn on three things: **budget caps** on managed keys,
**polling of user usage from the dashboard** on a cadence, and the **usage
stats** pages.

Two pieces of work collapse the polling burden so it does not scale with the
number of users you poll:

- A **daily rollup** (`usage_stat_day`) folds per-day, per-user aggregates out of
  the grow-only `usage_events` table, so whole-day-aligned group-by-user reads
  no longer scan raw event rows.
- A **short-TTL (15s), single-flight aggregate cache** shields the DB from
  repeated identical / duplicated polls.

The legacy/unindexed path (no rollup, no cache, on-the-fly `GROUP BY` over raw
`usage_events`) is what you would need to brute-force 100-user polling and is
**not recommended**. The sizing note below shows that path in the last row as
the warning it is.

## Sizing baseline

| Scenario | CPUs | RAM | Disk | `shared_buffers` | `max_connections` |
|---|---|---|---|---|---|
| Proxy only, no budget caps | 2 vCPU | 2 GB | SSD 50 GB+ | 512 MB | 200 |
| Proxy + budget caps (managed keys) | 2–4 vCPU | 4 GB | NVMe 100 GB+ | 1 GB | 200 |
| Proxy + 100-user usage polling (rollup + cache + indexes) | 4 vCPU | 8 GB | NVMe 100 GB+ | 2 GB | 300 |
| Legacy usage polling without rollup/cache (unindexed on-the-fly) | 8+ vCPU | 16 GB+ | NVMe 200 GB+ | 4 GB | 300 |

### Why each row

**Proxy only, no budget caps.** With no budget caps and no usage polling, the
proxy holds all authoritative state in memory. PostgreSQL is used barely at all
(occasional metadata reads/writes), so a small box is plenty. 2 vCPU / 2 GB keeps
the database process and its cache warm without waste.

**Proxy + budget caps (managed keys).** Enabling budget caps on managed API keys
adds roughly **5 synchronous writes per proxied request** (usage events plus the
window/counter bookkeeping that enforcement reads). The DB is now on the request
hot path, so it needs headroom for write concurrency and a faster disk. 2–4 vCPU,
4 GB, and NVMe comfortably absorb the write pattern; `shared_buffers` at 1 GB
keeps the hot counter/usage pages resident.

**Proxy + 100-user usage polling (rollup + cache + indexes, as built here).**
This is the intended configuration. The daily rollup collapses `usage_events`
into one row per (day, user, key, model, provider, source) bucket, and the 15s
cache absorbs repeated identical polls, so a 100-user polling cadence degrades to
cheap, bounded reads. Because the polling path no longer scans raw rows, the box
stays modest: 4 vCPU / 8 GB / NVMe. `max_connections` at 300 gives the proxy's
bounded pool headroom without letting the database run out of slots.

**Legacy usage polling WITHOUT rollup/cache (unindexed on-the-fly).** Without the
rollup and cache, each whole-day group-by-user read is an on-the-fly `GROUP BY`
over every matching row in `usage_events`. At hundreds of thousands to millions of
rows, 100 users polling on their own cadence turns this into sustained table
scans. You end up needing 8+ vCPU / 16 GB+ just to brute-force it, and it only
gets worse as the table grows. **Do not run this configuration** — the rollup and
cache above already exist and should be left on.

## Env knobs added by this effort

| Env var | Default | What it does | When to tune |
|---|---|---|---|
| `PGSTORE_MAX_OPEN_CONNS` | unset (Go pool default, effectively unbounded) | Caps the max simultaneous connections the proxy's bounded pool opens to PostgreSQL. | Leave unset unless you run many proxy replicas against one DB. If several proxies share a database, keep `sum(replicas × this)` well under Postgres' `max_connections`. Typical values are 10–50. |
| `PGSTORE_MAX_IDLE_CONNS` | unset (follows `MAX_OPEN_CONNS`) | Caps idle pooled connections held open between bursts. | Usually leave defaulted. Lower it to shrink idle connection overhead on small boxes. |
| `PGSTORE_ROLLUP_BACKFILL_DAYS` | `7` | On startup, the rollup folds this many prior days from `usage_events` into `usage_stat_day`. `0` disables the startup backfill. | Only matters at first boot (or after the rollup table is dropped). 7 gives the dashboard a week of pre-aggregated history. Raise it if you need deeper pre-aggregated history cheaply on day one; the fold is idempotent, so re-running is safe. |

### Bounded pool vs. Postgres `max_connections`

The proxy's pool is bounded by `PGSTORE_MAX_OPEN_CONNS`. This is independent of
Postgres' own `max_connections`. Keep the pool limit (times the number of proxy
instances sharing the DB) comfortably below the Postgres `max_connections`,
leaving headroom for pg_dump, maintenance, and ad-hoc queries. If you hit
`FATAL: sorry, too many clients already`, the fix is usually to lower the proxy's
pool cap rather than raise Postgres' limit.

### Backfill on first startup

On the first boot of a DB that already has history, the rollup backfills
`PGSTORE_ROLLUP_BACKFILL_DAYS` (default 7) days. If you are adopting an existing,
large `usage_events` table, prefer a modest backfill and let it run; the fold is
bounded to prior days and is idempotent. Because it reads the raw table once at
startup, a very large backfill window can take a while on first boot — size
`PGSTORE_ROLLUP_BACKFILL_DAYS` accordingly.

## Growth

`usage_events` grows **linearly** with proxied requests and is never dropped by
normal operation. Plan for growth:

- When the table approaches **~50M rows**, consider **partitioning by
  `requested_at`** so old data can be cheaply archived and new data stays hot.
  Retention uses the existing `DeleteEventsBefore`; partition-level drop is the
  natural complement for very old partitions.
- The `usage_stat_day` rollup **bounds the cost of aggregate reads** even as
  `usage_events` grows: whole-day-aligned aggregates hit the small rollup table,
  never the raw table. Indexes on `usage_events` (added by the migration) cover
  the non-day-aligned and error-count queries.

## Rollup freshness (accepted tradeoff)

A whole-day-aligned usage query is answered from `usage_stat_day`, which
reflects the **last fold**. The fold runs on a daily cadence (re-folding each
"today" every 24 hours) plus a startup backfill, so **"today" data is at most
~24h stale** relative to wall-clock. The **15s aggregate cache hides DB load but
does not change rollup freshness**.

This is an **accepted tradeoff**, deliberately not changed by this work: the
rollup exists to bound read cost for high-frequency polling, and its freshness
window is a known cost of that. Operators who need sub-hour accuracy for "today"
should read the non-rollup (on-the-fly) path for the partial-day window, and
should expect the full-day number to "catch up" after the next fold. See the code
notes at `tryRollupAggregateUser` / `rollupGroupByUserSQL` in
`internal/store/pg_usage.go`.

## Detecting usage-flush backpressure (observability)

There is no Prometheus/metrics endpoint today, so backpressure on the async PG
usage flusher is surfaced through logs:

- The flusher logs one warning **per dropped record** when its flush queue is full:
  `postgres usage flusher: queue full; dropping usage record` with a cumulative
  `drops` field.
- The alerts sweep now also logs a **periodic warning when the cumulative drop
  count increases since the last sweep** (the flusher's `Drops()` delta):
  `postgres usage flusher: usage records dropped since last sweep (queue
  backpressure)`. This turns the cumulative counter into a visible anomaly flag
  instead of relying on ad-hoc per-drop logs.

**How to watch for it**: tail logs for `queue full; dropping usage record`, or
for the sweep-level backpressure warning. A rising cumulative `drops` count under
sustained load means the flush queue cannot keep up — increase flush throughput
(e.g. raise the flusher's queue/batch capacity) or reduce event volume. In
steady state the counter is expected to stay flat; a large jump between sweeps is
the anomaly to investigate.