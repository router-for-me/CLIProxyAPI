import sqlite3
import unittest

from tools.quota_forecast import (
    FULL, HOUR, Account, Reading, Usage, TokenIndex, agent_rate, calibrate, current_windows,
    demand_profile, forecast, forecast_pool,
)

NOW = 1_800_000_000 // HOUR * HOUR  # a whole hour, so hour-of-day buckets are exact
WEEK = 7 * 86400


def steady(account, window, start, hours, per_hour_bp, reset_at, step=900, start_bp=0):
    """Readings every `step` seconds rising by `per_hour_bp` an hour."""
    return [Reading(account, window, min(FULL, start_bp + round(per_hour_bp * i * step / HOUR)), reset_at,
                    start + i * step) for i in range(int(hours * HOUR / step) + 1)]


def traffic(account, start, hours, per_hour, model="claude-opus-5-5", session=""):
    """Token use spread evenly, one record a minute."""
    return [Usage(account, model, start + m * 60, per_hour // 60, session) for m in range(int(hours * 60))]


class CalibrationTest(unittest.TestCase):
    def test_cost_is_points_per_token_from_recent_pairs(self):
        readings = steady("a", "seven_day", NOW - 4 * HOUR, 4, 100, NOW + WEEK)
        index = TokenIndex(traffic("a", NOW - 4 * HOUR, 4, 1_000_000))
        cost, unexplained = calibrate(readings, index, "a", "seven_day")
        self.assertAlmostEqual(cost, 100 / 1_000_000, delta=1e-6)
        self.assertEqual(unexplained, 0)

    def test_quota_rise_without_proxy_tokens_is_reported_not_learned(self):
        readings = steady("a", "seven_day", NOW - 2 * HOUR, 2, 300, NOW + WEEK)
        cost, unexplained = calibrate(readings, TokenIndex([]), "a", "seven_day")
        self.assertIsNone(cost)
        self.assertEqual(unexplained, 600)

    def test_readings_at_the_cap_are_not_learned_from(self):
        readings = [Reading("a", "five_hour", 9_000, NOW + HOUR, NOW - HOUR),
                    Reading("a", "five_hour", FULL, NOW + HOUR, NOW)]
        cost, _ = calibrate(readings, TokenIndex(traffic("a", NOW - HOUR, 1, 600_000)), "a", "five_hour")
        self.assertIsNone(cost)

    def test_fable_window_learns_only_from_fable_tokens(self):
        readings = steady("a", "seven_day_fable", NOW - 4 * HOUR, 4, 100, NOW + WEEK)
        usage = traffic("a", NOW - 4 * HOUR, 4, 6_000_000) + traffic("a", NOW - 4 * HOUR, 4, 600_000, "claude-fable-5-1")
        cost, _ = calibrate(readings, TokenIndex(usage), "a", "seven_day_fable")
        self.assertAlmostEqual(cost, 100 / 600_000, delta=1e-6)

    def test_a_point_or_two_against_huge_traffic_is_not_a_cost(self):
        readings = [Reading("a", "five_hour", 0, NOW + HOUR, NOW - 2 * HOUR),
                    Reading("a", "five_hour", 100, NOW + HOUR, NOW - HOUR)]
        cost, _ = calibrate(readings, TokenIndex(traffic("a", NOW - 2 * HOUR, 1, 300_000_000)), "a", "five_hour")
        self.assertIsNone(cost)


class CurrentWindowTest(unittest.TestCase):
    def test_window_missing_from_newest_response_is_dropped(self):
        readings = [Reading("a", "five_hour", 500, NOW + HOUR, NOW - 3600),
                    Reading("a", "seven_day", 400, NOW + WEEK, NOW - 3600),
                    Reading("a", "seven_day", 900, NOW + WEEK, NOW - 60)]
        self.assertEqual([r.window for r in current_windows(readings, "a", NOW)], ["seven_day"])

    def test_past_reset_rolls_to_empty_and_next_reset(self):
        readings = [Reading("a", "five_hour", 9_000, NOW - HOUR, NOW - 2 * HOUR)]
        (window,) = current_windows(readings, "a", NOW)
        self.assertEqual((window.used, window.reset_at), (0, NOW + 4 * HOUR))


class DemandTest(unittest.TestCase):
    def test_hours_when_the_pool_was_out_do_not_count_as_no_demand(self):
        start = NOW - 48 * HOUR
        readings = [Reading("a", "seven_day", 100, NOW + WEEK, start),
                    Reading("a", "seven_day", FULL, NOW + WEEK, start + 24 * HOUR)]
        usage = traffic("a", start, 24, 600_000)  # nothing served while exhausted
        profile, _ = demand_profile(readings, usage, ["a"], start + 48 * HOUR)
        self.assertAlmostEqual(min(profile), 600_000, delta=1)

    def test_agents_are_top_level_sessions(self):
        usage = (traffic("a", NOW - 2 * HOUR, 2, 600_000, session="lead-1")
                 + traffic("a", NOW - 2 * HOUR, 2, 600_000, session="lead-2"))
        active, per_agent = agent_rate(usage, NOW)
        self.assertEqual(active, 2)
        self.assertAlmostEqual(per_agent, 600_000, delta=1)


class PoolTest(unittest.TestCase):
    def setUp(self):
        start = NOW - 6 * HOUR
        # Account a: 1M tokens/h costs 100 bp/h; it sits at 80%. Account b: same cost, at 10%.
        self.readings = (steady("a", "seven_day", start, 6, 100, NOW + 3 * 86400, start_bp=7_400)
                         + steady("b", "seven_day", start, 6, 100, NOW + 3 * 86400, start_bp=400))
        self.usage = traffic("a", start, 6, 1_000_000, session="s1") + traffic("b", start, 6, 1_000_000, session="s2")
        self.accounts = [Account("a", "claude", "A", "plan_ultra"), Account("b", "claude", "B", "plan_ultra")]

    def test_round_robin_drains_the_fuller_account_first_then_the_pool(self):
        pool = forecast_pool("claude", self.accounts, self.readings, self.usage, NOW)
        by = {a["label"]: a for a in pool["accounts"]}
        self.assertEqual(pool["status"], "ready")
        # Pool demand is 2M tokens/h, 1M to each account = 100 bp/h. a has 20% left -> 20 h.
        self.assertAlmostEqual((by["A"]["run_out_at"] - NOW) / HOUR, 20, delta=0.5)
        # b is then at 30% and takes all 2M/h = 200 bp/h: 70% left -> 35 h more.
        self.assertAlmostEqual((by["B"]["run_out_at"] - NOW) / HOUR, 55, delta=0.5)
        self.assertEqual(pool["dry_from"], by["B"]["run_out_at"])
        self.assertEqual(pool["dry_until"], NOW + 3 * 86400)  # weekly reset brings both back
        self.assertEqual(by["A"]["windows"][0]["used_percent"], 80.0)

    def test_what_if_agent_count_scales_demand(self):
        busy = forecast_pool("claude", self.accounts, self.readings, self.usage, NOW, agents=4)
        self.assertEqual(busy["demand_source"], "agents")
        self.assertAlmostEqual(busy["demand_tokens_per_hour"], 4_000_000, delta=1_000)
        normal = forecast_pool("claude", self.accounts, self.readings, self.usage, NOW)
        self.assertLess(busy["dry_from"], normal["dry_from"])

    def test_uncalibrated_account_borrows_cost_by_plan(self):
        readings = self.readings + [Reading("c", "seven_day", 0, NOW + 3 * 86400, NOW - 60)]
        accounts = self.accounts + [Account("c", "claude", "C", "plan_max")]
        pool = forecast_pool("claude", accounts, readings, self.usage, NOW)
        (c,) = [a for a in pool["accounts"] if a["label"] == "C"]
        self.assertEqual(c["windows"][0]["cost_source"], "plan")
        self.assertEqual(pool["status"], "ready")

    def test_unstarted_five_hour_window_opens_on_first_use(self):
        readings = self.readings + [Reading("a", "five_hour", 0, 0, r.observed_at) for r in self.readings
                                    if r.account == "a"]
        readings += [Reading("a", "five_hour", 5, 0, NOW - HOUR - 900)]
        pool = forecast_pool("claude", self.accounts, readings, self.usage, NOW)
        self.assertEqual(pool["status"], "learning")  # its 5-hour cost is unknown, so it says so


class ReviewRegressionTest(unittest.TestCase):
    """Cases from the 2026-11-02 review that made the forecast too hopeful or wrong."""

    def setUp(self):
        start = NOW - 6 * HOUR
        self.readings = steady("a", "seven_day", start, 6, 100, NOW + 3 * 86400, start_bp=7_400)
        self.usage = traffic("a", start, 6, 1_000_000)

    def test_account_without_readings_is_not_limitless_capacity(self):
        alone = forecast_pool("claude", [Account("a", "claude", "A")], self.readings, self.usage, NOW)
        both = forecast_pool("claude", [Account("a", "claude", "A"), Account("b", "claude", "B")],
                             self.readings, self.usage, NOW)
        self.assertEqual(both["dry_from"], alone["dry_from"])
        self.assertEqual(both["no_readings"], ["B"])

    def test_out_account_with_unmeasured_limit_blocks_a_hopeful_forecast(self):
        readings = self.readings + [Reading("c", "five_hour", FULL, NOW + 2 * HOUR, NOW - 60)]
        pool = forecast_pool("claude", [Account("a", "claude", "A"), Account("c", "claude", "C")],
                             readings, self.usage, NOW)
        self.assertEqual(pool["status"], "learning")
        self.assertEqual(pool["learning"], ["C"])

    def test_out_account_comes_back_only_when_every_full_limit_resets(self):
        readings = self.readings + [Reading("c", "five_hour", FULL, NOW + 2 * HOUR, NOW - 60),
                                    Reading("c", "seven_day", FULL, NOW + 72 * HOUR, NOW - 60)]
        pool = forecast_pool("claude", [Account("a", "claude", "A"), Account("c", "claude", "C")],
                             readings, self.usage, NOW)
        (c,) = [x for x in pool["accounts"] if x["label"] == "C"]
        self.assertEqual(c["back_at"], NOW + 72 * HOUR)

    def test_traffic_before_a_reset_is_not_charged_to_the_new_window(self):
        # Last readings an hour ago (the bridge polls hourly when limited); the 5-hour window
        # reset half an hour ago; traffic stopped at the reset.
        five = steady("a", "five_hour", NOW - 3 * HOUR, 2, 2_000, NOW - HOUR // 2)
        week = steady("a", "seven_day", NOW - 3 * HOUR, 2, 100, NOW + WEEK)
        usage = traffic("a", NOW - 3 * HOUR, 2.5, 1_000_000)
        pool = forecast_pool("claude", [Account("a", "claude", "A")], five + week, usage, NOW)
        (five,) = [w for w in pool["accounts"][0]["windows"] if w["window"] == "five_hour"]
        self.assertEqual(five["used_percent"], 0.0)

    def test_a_window_that_sat_still_recently_still_has_a_cost(self):
        rising = steady("a", "seven_day", NOW - 8 * HOUR, 4, 100, NOW + WEEK)
        flat = [Reading("a", "seven_day", rising[-1].used, NOW + WEEK, rising[-1].observed_at + i * 900)
                for i in range(1, 17)]
        usage = traffic("a", NOW - 8 * HOUR, 8, 1_000_000)
        cost, _ = calibrate(rising + flat, TokenIndex(usage), "a", "seven_day")
        self.assertIsNotNone(cost)


class ForecastQueryTest(unittest.TestCase):
    def test_reads_from_sqlite_outbox(self):
        db = sqlite3.connect(":memory:")
        self.addCleanup(db.close)
        db.executescript("""
            CREATE TABLE observation (id TEXT, account TEXT, window TEXT, used_basis_points INTEGER,
              reset_at INTEGER, observed_at INTEGER, sent INTEGER);
            CREATE TABLE usage_event (id TEXT, account TEXT, model TEXT, minute INTEGER,
              input_tokens INTEGER, output_tokens INTEGER, session_id TEXT, parent_session_id TEXT);
        """)
        for i, r in enumerate(steady("a", "seven_day", NOW - 4 * HOUR, 4, 100, NOW + WEEK)):
            db.execute("INSERT INTO observation VALUES (?,?,?,?,?,?,0)",
                       (str(i), r.account, r.window, r.used, r.reset_at, r.observed_at))
        for i, u in enumerate(traffic("a", NOW - 4 * HOUR, 4, 1_000_000)):
            db.execute("INSERT INTO usage_event VALUES (?,?,?,?,?,0,'child','lead')",
                       (str(i), u.account, u.model, u.minute, u.tokens))
        result = forecast(db, [Account("a", "claude", "A", "plan_ultra")], NOW)
        (pool,) = result["pools"]
        self.assertEqual(pool["status"], "ready")
        self.assertEqual(forecast(db, [Account("a", "claude", "A")], NOW, {"codex": 9})["pools"][0]["agents"]["what_if"], None)
        self.assertEqual(pool["agents"]["active"], 1)  # a subagent counts under its parent


if __name__ == "__main__":
    unittest.main()
