"""Forecast when each quota window runs out, per account and per provider pool.

Pure functions over the bridge's saved readings and token logs, so they can be
tested and back-tested without the proxy or Fork. The method:

1. Cost: each account/window learns how many basis points of quota a proxy token
   costs, from consecutive readings in the same reset cycle and the tokens the
   proxy sent through that account in between.
2. Demand: tokens per hour by hour of day across the provider's accounts, counting
   only hours when the pool had capacity (an exhausted pool serves nothing, which
   is not the same as wanting nothing). With session data, demand can instead be
   set to N agents times the measured tokens per agent-hour.
3. Simulation: step through the coming week, split demand evenly between accounts
   that still have capacity (the proxy's round-robin), apply resets, and record when
   each account and the whole pool run out.
"""

from __future__ import annotations

import bisect
import copy
import sqlite3
from collections import defaultdict
from dataclasses import dataclass, field

HOUR = 3600
STEP = 900
# Weekly limits all reset within 7 days, after which the simulation would only
# repeat itself; this is the provider's cycle, not a tuning knob.
HORIZON = 7 * 86400
LOOKBACK = 28 * 86400
FULL = 10_000
# Readings from one usage response share a timestamp; allow for clock jitter.
BATCH_SLACK = 60
AGENT_ACTIVE = 30 * 60
# Back-tested on 29 Oct-2 Nov 2026 history: the last 16 pairs (about four active hours
# at one reading per 15 minutes) halved the 12-hour error against using all history.
CALIBRATION_PAIRS = 16
# Least movement (basis points) a cost may rest on; readings come in whole percents.
MIN_SPENT = 300

PERIOD = {"five_hour": 5 * HOUR, "seven_day": 7 * 86400, "weekly": 7 * 86400,
          "seven_day_fable": 7 * 86400, "seven_day_opus": 7 * 86400}
# Windows that stop every request when full. Model-scoped ones only stop that model.
GATING = {"five_hour", "seven_day", "weekly"}
MODEL_SCOPE = {"seven_day_fable": "fable", "seven_day_opus": "opus"}
# Weekly allowance relative to Pro, used to borrow a peer's cost when an account
# has not been calibrated yet (Anthropic: Max 5x, Ultra 10x Pro per week).
PLAN_MULTIPLIER = {"plan_pro": 1.0, "plan_max": 5.0, "plan_ultra": 10.0}


@dataclass
class Reading:
    account: str
    window: str
    used: int
    reset_at: int
    observed_at: int


@dataclass
class Usage:
    account: str
    model: str
    minute: int
    tokens: int
    session: str = ""


@dataclass
class Account:
    account: str
    provider: str
    label: str
    plan: str = ""


@dataclass
class WindowState:
    window: str
    used: float
    reset_at: int
    cost: float | None
    cost_source: str


@dataclass
class AccountSim:
    meta: Account
    windows: list[WindowState] = field(default_factory=list)
    run_out_at: int | None = None
    run_out_window: str | None = None
    back_at: int | None = None

    def available(self) -> bool:
        return all(w.used < FULL for w in self.windows if w.window in GATING)


def load(db: sqlite3.Connection, accounts: list[str], since: int) -> tuple[list[Reading], list[Usage]]:
    marks = ",".join("?" * len(accounts))
    readings = [Reading(*row) for row in db.execute(
        f"SELECT account,window,used_basis_points,reset_at,observed_at FROM observation "
        f"WHERE account IN ({marks}) AND observed_at>=? ORDER BY observed_at", (*accounts, since))]
    columns = {row[1] for row in db.execute("PRAGMA table_info(usage_event)")}
    session = ("COALESCE(NULLIF(parent_session_id,''),session_id,'')"
               if {"session_id", "parent_session_id"} <= columns else "''")
    usage = [Usage(*row) for row in db.execute(
        f"SELECT account,model,minute,input_tokens+output_tokens,{session} FROM usage_event "
        f"WHERE account IN ({marks}) AND minute>=? ORDER BY minute", (*accounts, since))]
    return readings, usage


def same_cycle(before: Reading, after: Reading) -> bool:
    return abs(before.reset_at - after.reset_at) <= BATCH_SLACK and after.used >= before.used


class TokenIndex:
    """Prefix sums of tokens per account and window scope, for fast interval totals."""

    def __init__(self, usage: list[Usage]):
        self.series: dict[tuple[str, str], tuple[list[int], list[int]]] = {}
        grouped: dict[tuple[str, str], list[tuple[int, int]]] = defaultdict(list)
        for u in usage:
            grouped[(u.account, "")].append((u.minute, u.tokens))
            for window, scope in MODEL_SCOPE.items():
                if scope in u.model.lower():
                    grouped[(u.account, window)].append((u.minute, u.tokens))
        for key, rows in grouped.items():
            rows.sort()
            totals = [0]
            for _, tokens in rows:
                totals.append(totals[-1] + tokens)
            self.series[key] = ([m for m, _ in rows], totals)

    def between(self, account: str, window: str, start: int, end: int) -> int:
        minutes, totals = self.series.get((account, window if window in MODEL_SCOPE else ""), ([], [0]))
        return totals[bisect.bisect_left(minutes, end)] - totals[bisect.bisect_left(minutes, start)]


def calibrate(readings: list[Reading], index: TokenIndex, account: str, window: str) -> tuple[float | None, int]:
    """Basis points per token from the most recent reading pairs with proxy traffic, and basis
    points used with no proxy tokens at all."""
    rows = [r for r in readings if r.account == account and r.window == window]
    pairs: list[tuple[int, int]] = []
    unexplained = 0
    for before, after in zip(rows, rows[1:]):
        # A reading at the cap hides how far past it demand went.
        if not same_cycle(before, after) or after.used >= FULL:
            continue
        sent = index.between(account, window, before.observed_at, after.observed_at)
        change = after.used - before.used
        if sent > 0:
            pairs.append((change, sent))
        else:
            unexplained += change
    # Cost per token drifts between windows (model and cache mix), so recent pairs win.
    recent = pairs[-CALIBRATION_PAIRS:]
    spent, tokens = sum(p[0] for p in recent), sum(p[1] for p in recent)
    if tokens and spent < MIN_SPENT:
        # Readings move in whole percents; a slow window can sit still for a while,
        # so look further back before calling the cost unknown.
        spent, tokens = sum(p[0] for p in pairs), sum(p[1] for p in pairs)
    # A point or two of movement against a huge token count is rounding noise, not a
    # cost; trusting it would make the window look as if it never fills.
    return (spent / tokens if tokens and spent >= MIN_SPENT else None), unexplained


def current_windows(readings: list[Reading], account: str, now: int) -> list[Reading]:
    """Latest reading per window from the account's newest usage response, rolled past resets.

    A rolled window's observed_at becomes the start of its current cycle, so traffic sent
    before the reset is not charged to the fresh window.
    """
    mine = [r for r in readings if r.account == account]
    if not mine:
        return []
    newest = max(r.observed_at for r in mine)
    latest: dict[str, Reading] = {}
    for r in mine:
        if r.observed_at >= newest - BATCH_SLACK:
            latest[r.window] = r
    rolled = []
    for r in latest.values():
        if r.window not in PERIOD:
            continue
        reset_at, used, since = r.reset_at, r.used, r.observed_at
        if reset_at and reset_at <= now:
            used = 0
            while reset_at <= now:
                reset_at += PERIOD[r.window]
            since = reset_at - PERIOD[r.window]
        rolled.append(Reading(account, r.window, used, reset_at, since))
    return rolled


def pool_capacity_hours(readings: list[Reading], accounts: list[str], start: int, end: int) -> set[int]:
    """Hour starts in [start, end) when at least one account had capacity, judged at each hour's middle."""
    ordered = sorted((r for r in readings if r.account in accounts), key=lambda r: r.observed_at)
    last: dict[str, dict[str, Reading]] = defaultdict(dict)
    hours, i = set(), 0
    for hour in range(start, end, HOUR):
        middle = hour + HOUR // 2
        while i < len(ordered) and ordered[i].observed_at <= middle:
            last[ordered[i].account][ordered[i].window] = ordered[i]
            i += 1
        if any(not any(r.window in GATING and r.used >= FULL and r.reset_at > middle for r in windows.values())
               for windows in last.values() if windows):
            hours.add(hour)
    return hours


def demand_profile(readings: list[Reading], usage: list[Usage], accounts: list[str],
                   now: int) -> tuple[list[float] | None, float]:
    """Tokens per hour for each hour of day (UTC), and the share of tokens that went to Fable."""
    if not usage:
        return None, 0.0
    first = usage[0].minute // HOUR * HOUR
    totals: dict[int, int] = defaultdict(int)
    fable = 0
    for u in usage:
        totals[u.minute // HOUR * HOUR] += u.tokens
        if "fable" in u.model.lower():
            fable += u.tokens
    by_hour: dict[int, list[int]] = defaultdict(list)
    for start in sorted(pool_capacity_hours(readings, accounts, first, now // HOUR * HOUR)):
        by_hour[(start // HOUR) % 24].append(totals.get(start, 0))
    samples = [v for values in by_hour.values() for v in values]
    if not samples:
        return None, 0.0
    flat = sum(samples) / len(samples)
    # An hour of day needs two days of samples before its own average beats the overall one.
    profile = [sum(by_hour[h]) / len(by_hour[h]) if len(by_hour[h]) >= 2 else flat for h in range(24)]
    total = sum(u.tokens for u in usage)
    return profile, (fable / total if total else 0.0)


def agent_rate(usage: list[Usage], now: int) -> tuple[int, float | None]:
    """Agents active now (top-level sessions) and tokens per agent-hour over the last day's
    finished hours (a partial hour would understate the rate)."""
    active = {u.session for u in usage if u.session and u.minute >= now - AGENT_ACTIVE}
    sessions: dict[int, set[str]] = defaultdict(set)
    tokens = 0
    for u in usage:
        if u.session and now - 86400 <= u.minute < now // HOUR * HOUR:
            sessions[u.minute // HOUR].add(u.session)
            tokens += u.tokens
    agent_hours = sum(len(s) for s in sessions.values())
    return len(active), (tokens / agent_hours if agent_hours else None)


def simulate(sims: list[AccountSim], demand, fable_share: float, now: int) -> tuple[int | None, int | None, int]:
    """Step through the horizon, mutating `sims`. Returns when the pool first runs dry, when it
    first comes back, and how many steps it spent dry in total."""
    dry_from = dry_until = None
    dry_steps = 0
    for at in range(now, now + HORIZON, STEP):
        for sim in sims:
            was_out = not sim.available()
            for w in sim.windows:
                if w.reset_at and w.reset_at <= at:
                    w.used = 0.0
                    w.reset_at += PERIOD[w.window]
            if was_out and sim.available() and sim.back_at is None:
                sim.back_at = at
        live = [s for s in sims if s.available()]
        if not live:
            dry_steps += 1
            if dry_from is None:
                dry_from = at
            continue
        if dry_from is not None and dry_until is None:
            dry_until = at
        share = demand(at) * STEP / HOUR / len(live)
        for sim in live:
            for w in sim.windows:
                if w.cost is None:
                    continue
                part = share * (fable_share if w.window == "seven_day_fable" else 1.0)
                if part > 0 and not w.reset_at:
                    # An unstarted window opens with its first request.
                    w.reset_at = at + PERIOD[w.window]
                w.used = min(FULL, w.used + w.cost * part)
            if not sim.available() and sim.run_out_at is None:
                sim.run_out_at = at + STEP
                sim.run_out_window = next(w.window for w in sim.windows
                                          if w.window in GATING and w.used >= FULL)
    return dry_from, dry_until, dry_steps


def safe_agent_count(sims: list[AccountSim], per_agent: float, fable_share: float, now: int) -> int | None:
    """Most agents, at the measured tokens per agent-hour, that add no dry time over the week.

    Time the pool is dry even with no agents (every account already out) is unavoidable and
    does not count against the answer.
    """
    def dry(count: int) -> int:
        return simulate(copy.deepcopy(sims), lambda at: count * per_agent, fable_share, now)[2]

    # Without a limit that stops all requests, more agents never run an account dry.
    if per_agent <= 0 or any(not any(w.window in GATING for w in s.windows) for s in sims):
        return None
    baseline = dry(0)
    low, high = 0, 1
    # Grow until the pool runs dry; with a stopping limit on every account it always does.
    while dry(high) == baseline:
        low, high = high, high * 2
    while high - low > 1:
        middle = (low + high) // 2
        if dry(middle) == baseline:
            low = middle
        else:
            high = middle
    return low


def forecast_pool(provider: str, accounts: list[Account], readings: list[Reading], usage: list[Usage],
                  now: int, agents: int | None = None) -> dict:
    keys = [a.account for a in accounts]
    index = TokenIndex(usage)
    sims: list[AccountSim] = []
    costs: dict[tuple[str, str], tuple[float, float]] = {}
    unexplained = 0
    for meta in accounts:
        sim = AccountSim(meta)
        for r in current_windows(readings, meta.account, now):
            cost, outside = calibrate(readings, index, meta.account, r.window)
            unexplained += outside
            used = float(r.used)
            if cost is not None:
                # Bring the last reading up to now with the tokens sent since.
                used = min(FULL, used + cost * index.between(meta.account, r.window, r.observed_at, now))
                costs[(meta.account, r.window)] = (cost, PLAN_MULTIPLIER.get(meta.plan, 0.0))
            sim.windows.append(WindowState(r.window, used, r.reset_at, cost, "measured" if cost else ""))
        sims.append(sim)
    for sim in sims:
        own = PLAN_MULTIPLIER.get(sim.meta.plan)
        for w in sim.windows:
            if w.cost is not None or not own:
                continue
            peers = [c * m / own for (acc, win), (c, m) in costs.items()
                     if win == w.window and m and acc != sim.meta.account]
            if peers:
                w.cost, w.cost_source = sum(peers) / len(peers), "plan"

    profile, fable_share = demand_profile(readings, usage, keys, now)
    active, per_agent = agent_rate(usage, now)
    if agents is not None and per_agent is not None:
        demand_source = "agents"
        demand = lambda at: agents * per_agent  # noqa: E731
    elif profile is not None:
        demand_source = "history"
        demand = lambda at: profile[(at // HOUR) % 24]  # noqa: E731
    else:
        demand_source, demand = "none", None

    # An account with no readings has unknown capacity: leave it out rather than treat it
    # as limitless. An unmeasured limit makes the forecast unknown even while the account
    # is out, because it starts filling again after the reset.
    no_readings = [s.meta.label for s in sims if not s.windows]
    sims = [s for s in sims if s.windows]
    learning = [s.meta.label for s in sims if any(w.cost is None for w in s.windows if w.window in GATING)]
    available_now = {s.meta.account: s.available() for s in sims}
    for sim in sims:
        if not sim.available():
            # Back only when every full limit has reset.
            sim.back_at = max((w.reset_at for w in sim.windows if w.window in GATING and w.used >= FULL),
                              default=None) or None
    # The simulation mutates window state; report the state as of now.
    windows_now = {s.meta.account: [{"window": w.window, "used_percent": round(w.used / 100, 1),
                                     "reset_at": w.reset_at, "cost_source": w.cost_source or None}
                                    for w in s.windows] for s in sims}
    dry_from = dry_until = None
    safe_agents = None
    if demand is not None and not learning and sims:
        if per_agent:
            safe_agents = safe_agent_count(sims, per_agent, fable_share, now)
        dry_from, dry_until, _ = simulate(sims, demand, fable_share, now)

    current = demand(now) if demand is not None else None
    return {
        "provider": provider,
        "status": "learning" if learning else ("ready" if demand is not None else "no_usage"),
        "learning": learning,
        "no_readings": no_readings,
        "dry_from": dry_from,
        "dry_until": dry_until,
        "demand_tokens_per_hour": round(current) if current is not None else None,
        "demand_source": demand_source,
        "agents": {"active": active, "tokens_per_agent_hour": round(per_agent) if per_agent else None,
                   "what_if": agents, "safe": safe_agents},
        "unexplained_points": unexplained / 100,
        "accounts": [{
            "account": s.meta.account,
            "label": s.meta.label,
            "available_now": available_now[s.meta.account],
            "run_out_at": s.run_out_at,
            "run_out_window": s.run_out_window,
            "back_at": s.back_at,
            "windows": windows_now[s.meta.account],
        } for s in sims],
    }


def forecast(db: sqlite3.Connection, accounts: list[Account], now: int,
             agents: dict[str, int] | None = None) -> dict:
    """Forecast every provider pool; `agents` optionally sets a what-if agent count per provider."""
    pools = []
    for provider in sorted({a.provider for a in accounts}):
        members = [a for a in accounts if a.provider == provider]
        readings, usage = load(db, [a.account for a in members], now - LOOKBACK)
        pools.append(forecast_pool(provider, members, readings, usage, now, (agents or {}).get(provider)))
    return {"generated_at": now, "pools": pools}


def backtest(db: sqlite3.Connection, accounts: list[Account], cutoffs: list[int], ahead: int) -> dict:
    """Forecast from past cutoffs and compare with what happened `ahead` seconds later.

    cost_errors: points between the actual reading and (state at cutoff + learned cost
    times tokens actually sent), which isolates the cost model. demand_ratios: forecast
    tokens over actual tokens for the same hours, which isolates the demand model.
    """
    cost_errors: list[float] = []
    demand_ratios: list[float] = []
    for provider in sorted({a.provider for a in accounts}):
        members = [a for a in accounts if a.provider == provider]
        keys = [a.account for a in members]
        readings, usage = load(db, keys, min(cutoffs) - LOOKBACK)
        everything = TokenIndex(usage)
        for cutoff in cutoffs:
            past = [r for r in readings if r.observed_at <= cutoff]
            seen = [u for u in usage if u.minute < cutoff]
            target = cutoff + ahead
            profile, _ = demand_profile(past, seen, keys, cutoff)
            if profile is not None:
                start, end = cutoff // HOUR * HOUR, target // HOUR * HOUR
                capacity = pool_capacity_hours(readings, keys, start, end)
                if len(capacity) == (end - start) // HOUR:
                    predicted = sum(profile[(h // HOUR) % 24] for h in capacity)
                    actual = sum(everything.between(k, "", start, end) for k in keys)
                    if actual:
                        demand_ratios.append(predicted / actual)
            pool = forecast_pool(provider, members, past, seen, cutoff)
            past_index = TokenIndex(seen)
            for acc in pool["accounts"]:
                for w in acc["windows"]:
                    later = [r for r in readings if r.account == acc["account"] and r.window == w["window"]
                             and target <= r.observed_at <= target + 1800]
                    if not later or w["reset_at"] <= later[0].observed_at or later[0].used >= FULL:
                        continue
                    cost, _ = calibrate(past, past_index, acc["account"], w["window"])
                    if cost is None:
                        continue
                    sent = everything.between(acc["account"], w["window"], cutoff, later[0].observed_at)
                    predicted_used = min(FULL, w["used_percent"] * 100 + cost * sent)
                    cost_errors.append((later[0].used - predicted_used) / 100)
    return {"cost_errors": cost_errors, "demand_ratios": demand_ratios}
