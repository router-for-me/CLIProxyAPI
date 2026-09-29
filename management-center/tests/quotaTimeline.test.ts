import { describe, expect, test } from 'bun:test';
import {
  DAY_MS,
  HOUR_MS,
  buildTimelineLane,
  laneHasWindow,
  pickLaneWindow,
  projectLane,
  projectResetCredits,
  startOfDay,
  startOfWeek,
  timelineSpan,
  windowsIn,
} from '../src/features/quota/quotaTimelineModel';
import type { TimelineLane } from '../src/features/quota/quotaTimelineModel';

const at = (y: number, m: number, d: number, h = 0, min = 0) => new Date(y, m, d, h, min).getTime();

describe('windowsIn', () => {
  test('projects backwards and forwards from the anchor', () => {
    // Anchor is a known reset; the span opens before the current window did.
    const anchor = at(2026, 6, 29, 12);
    const from = anchor - 2.5 * DAY_MS;
    const to = anchor + 1.5 * DAY_MS;

    const windows = windowsIn(anchor, DAY_MS, from, to);

    // A 4-day span of daily windows needs 5 bars: the span edges fall mid-window,
    // so there's a partial window at each end.
    expect(windows.length).toBe(5);
    // Every boundary sits on a whole period from the anchor.
    for (const window of windows) {
      // `+ 0` normalizes JS's -0 from a negative remainder (windows before the anchor).
      expect(((window.endMs - anchor) % DAY_MS) + 0).toBe(0);
      expect(window.endMs - window.startMs).toBe(DAY_MS);
    }
    expect(windows.some((w) => w.startMs <= anchor && w.endMs >= anchor)).toBe(true);
    // Fully covers the requested range.
    expect(windows[0].startMs).toBeLessThanOrEqual(from);
    expect(windows[windows.length - 1].endMs).toBeGreaterThanOrEqual(to);
  });

  test('covers the whole span with no gaps or overlaps', () => {
    const windows = windowsIn(at(2026, 6, 29), 5 * HOUR_MS, at(2026, 6, 28), at(2026, 6, 30));
    for (let i = 1; i < windows.length; i += 1) {
      expect(windows[i].startMs).toBe(windows[i - 1].endMs);
    }
  });

  test('rejects a degenerate period, span or anchor', () => {
    expect(windowsIn(1000, 0, 0, 5000)).toEqual([]);
    expect(windowsIn(1000, -5, 0, 5000)).toEqual([]);
    expect(windowsIn(NaN, 1000, 0, 5000)).toEqual([]);
    expect(windowsIn(1000, 1000, 5000, 5000)).toEqual([]);
  });

  test('bails out rather than looping forever on an absurd period', () => {
    // A bad payload could give a 1ms period over a fortnight.
    expect(windowsIn(0, 1, 0, 14 * DAY_MS)).toEqual([]);
  });
});

describe('span boundaries', () => {
  test('startOfDay and startOfWeek land on local midnight', () => {
    const mid = at(2026, 6, 29, 14, 37);
    expect(new Date(startOfDay(mid)).getHours()).toBe(0);
    expect(new Date(startOfWeek(mid)).getDay()).toBe(0);
    expect(new Date(startOfWeek(mid)).getHours()).toBe(0);
  });

  test('weekly span is a fortnight from the containing Sunday', () => {
    const now = at(2026, 6, 29, 14, 0); // a Wednesday
    const span = timelineSpan('weekly', 0, now);

    expect(new Date(span.startMs).getDay()).toBe(0);
    expect(span.days).toBe(14);
    expect(span.startMs).toBeLessThanOrEqual(now);
    expect(span.endMs).toBeGreaterThan(now);
  });

  test('offsets step a week in weekly mode and a day in session mode', () => {
    const now = at(2026, 6, 29, 14, 0);
    const weekly = timelineSpan('weekly', 0, now);
    const weeklyNext = timelineSpan('weekly', 1, now);
    expect(Math.round((weeklyNext.startMs - weekly.startMs) / DAY_MS)).toBe(7);

    const session = timelineSpan('session', 0, now);
    const sessionNext = timelineSpan('session', 1, now);
    expect(Math.round((sessionNext.startMs - session.startMs) / DAY_MS)).toBe(1);
    expect(session.days).toBe(3);
  });

  test('spans a whole number of days even across a DST transition', () => {
    // US DST springs forward 2026-03-08; a fixed +14*DAY_MS would land at 23:00.
    const span = timelineSpan('weekly', 0, at(2026, 2, 10, 12));
    expect(new Date(span.startMs).getHours()).toBe(0);
    expect(new Date(span.endMs).getHours()).toBe(0);
  });
});

describe('projectLane', () => {
  const lane = (over: Partial<TimelineLane> = {}): TimelineLane => ({
    name: 'a.json',
    displayName: 'Alice',
    provider: 'claude',
    anchorMs: at(2026, 6, 29, 20),
    periodHours: 24 * 7,
    remaining: 40,
    limits: [],
    resetCredits: [],
    ...over,
  });

  const span = timelineSpan('weekly', 0, at(2026, 6, 29, 12));

  test('classifies past, live and next windows against now', () => {
    const now = at(2026, 6, 29, 12);
    const windows = projectLane(lane(), span.startMs, span.endMs, now, 'weekly');

    expect(windows.length).toBeGreaterThan(0);
    const live = windows.filter((w) => w.state === 'live');
    expect(live.length).toBe(1);
    expect(live[0].startMs).toBeLessThanOrEqual(now);
    expect(live[0].endMs).toBeGreaterThan(now);
    expect(live[0].remaining).toBe(40);
    expect(windows.filter((w) => w.state === 'past').every((w) => w.endMs <= now)).toBe(true);
    expect(windows.filter((w) => w.state === 'next').every((w) => w.startMs > now)).toBe(true);
  });

  test('does not carry stale remaining usage past the reported reset', () => {
    const nowAfterReset = at(2026, 6, 30, 12);
    const windows = projectLane(lane(), span.startMs, span.endMs, nowAfterReset, 'weekly');
    const live = windows.find((window) => window.state === 'live');

    expect(live).toBeDefined();
    expect(live?.startMs).toBe(at(2026, 6, 29, 20));
    expect(live?.remaining).toBeNull();
  });

  test('clips bars to the visible span', () => {
    const windows = projectLane(lane(), span.startMs, span.endMs, at(2026, 6, 29, 12), 'weekly');
    for (const window of windows) {
      expect(window.leftPercent).toBeGreaterThanOrEqual(0);
      expect(window.widthPercent).toBeGreaterThan(0);
      expect(window.leftPercent + window.widthPercent).toBeLessThanOrEqual(100.0001);
    }
  });

  test('returns nothing when the lane has no anchor or period', () => {
    const now = at(2026, 6, 29, 12);
    expect(projectLane(lane({ anchorMs: null }), span.startMs, span.endMs, now, 'weekly')).toEqual(
      []
    );
    expect(
      projectLane(lane({ periodHours: null }), span.startMs, span.endMs, now, 'weekly')
    ).toEqual([]);
  });

  test('session mode projects only real 5-hour windows', () => {
    const now = at(2026, 6, 29, 12);
    const sessionSpan = timelineSpan('session', 0, now);

    expect(projectLane(lane(), sessionSpan.startMs, sessionSpan.endMs, now, 'session')).toEqual([]);

    const windows = projectLane(
      lane({ periodHours: 5 }),
      sessionSpan.startMs,
      sessionSpan.endMs,
      now,
      'session'
    );
    expect(windows.some((w) => w.endMs - w.startMs === 5 * HOUR_MS)).toBe(true);
  });

  test('projects only unexpired reset credits inside the visible span', () => {
    const now = at(2026, 6, 29, 12);
    const visibleExpiry = at(2026, 7, 2, 12);
    const marks = projectResetCredits(
      lane({
        resetCredits: [
          { id: 'expired', grantedAtMs: null, expiresAtMs: now - HOUR_MS },
          { id: 'visible', grantedAtMs: now - DAY_MS, expiresAtMs: visibleExpiry },
          { id: 'outside', grantedAtMs: now, expiresAtMs: span.endMs + HOUR_MS },
        ],
      }),
      span.startMs,
      span.endMs,
      now
    );

    expect(marks).toHaveLength(1);
    expect(marks[0].id).toBe('visible');
    expect(marks[0].leftPercent).toBe(
      ((visibleExpiry - span.startMs) / (span.endMs - span.startMs)) * 100
    );
  });
});

describe('pickLaneWindow', () => {
  test('ignores windows with no reset instant', () => {
    const chosen = pickLaneWindow([
      { resetAtMs: null, periodHours: 168 },
      { resetAtMs: 1000, periodHours: 5 },
      { resetAtMs: undefined, periodHours: 168 },
    ]);
    expect(chosen?.resetAtMs).toBe(1000);
  });

  /**
   * The bug this rule exists for: across a fortnight, the 5-hour window always
   * resets soonest, so "pick the soonest" drew ~67 slivers per lane instead of
   * two readable weekly bars.
   */
  test('prefers the longest window that fits the span, not the soonest reset', () => {
    const fiveHour = { resetAtMs: 1_000, periodHours: 5 };
    const weekly = { resetAtMs: 9_000, periodHours: 168 };

    expect(pickLaneWindow([fiveHour, weekly], 14 * 24)).toBe(weekly);
    // A three-day span can't fit a weekly window, so the short one wins.
    expect(pickLaneWindow([fiveHour, weekly], 3 * 24)).toBe(fiveHour);
  });

  test('breaks a period tie on the soonest reset', () => {
    const later = { resetAtMs: 9_000, periodHours: 168 };
    const sooner = { resetAtMs: 5_000, periodHours: 168 };
    expect(pickLaneWindow([later, sooner], 14 * 24)).toBe(sooner);
  });

  test('falls back to the shortest available rather than drawing nothing', () => {
    // Every window is longer than the span — still better to draw one.
    const monthly = { resetAtMs: 5_000, periodHours: 720 };
    expect(pickLaneWindow([monthly], 3 * 24)).toBe(monthly);
  });

  test('returns null when nothing qualifies', () => {
    expect(pickLaneWindow([{ resetAtMs: null }])).toBeNull();
    expect(pickLaneWindow([])).toBeNull();
  });
});

describe('buildTimelineLane', () => {
  const base = { name: 'a.json', displayName: 'Alice' };

  test('claude/codex: anchors on the span-appropriate window, remaining from used', () => {
    const soon = at(2026, 6, 29, 20);
    const later = at(2026, 7, 1, 20);
    const quota = {
      status: 'success',
      windows: [
        { label: '7-day', usedPercent: 93, resetAtMs: later, periodHours: 168 },
        { label: '5-hour', usedPercent: 20, resetAtMs: soon, periodHours: 5 },
      ],
    };

    // Fortnight view: the weekly window, even though the 5-hour resets sooner.
    const weekly = buildTimelineLane({
      ...base,
      provider: 'claude',
      quota,
      maxPeriodHours: 14 * 24,
    });
    expect(weekly.anchorMs).toBe(later);
    expect(weekly.periodHours).toBe(168);
    expect(weekly.remaining).toBe(7); // stored USED

    // Three-day view: the weekly window doesn't fit, so the short one is used.
    const session = buildTimelineLane({
      ...base,
      provider: 'claude',
      quota,
      maxPeriodHours: 3 * 24,
    });
    expect(session.anchorMs).toBe(soon);
    expect(session.periodHours).toBe(5);
    expect(session.remaining).toBe(80);

    // Every limit is still summarized in the lane head regardless of the pick.
    expect(weekly.limits).toEqual([
      { label: '7-day', remaining: 7 },
      { label: '5-hour', remaining: 80 },
    ]);
  });

  test('codex: keeps the weekly lane on the account quota instead of Spark quota', () => {
    const accountReset = at(2026, 7, 1, 20);
    const sparkReset = at(2026, 6, 29, 20);
    const lane = buildTimelineLane({
      ...base,
      provider: 'codex',
      quota: {
        status: 'success',
        windows: [
          {
            id: 'weekly',
            label: 'Weekly limit',
            usedPercent: 70,
            resetAtMs: accountReset,
            periodHours: 168,
          },
          {
            id: 'gpt-5-3-codex-spark-weekly-0',
            label: 'GPT-5.3-Codex-Spark weekly limit',
            usedPercent: 2,
            resetAtMs: sparkReset,
            periodHours: 168,
          },
        ],
      },
      maxPeriodHours: 14 * 24,
    });

    expect(lane.anchorMs).toBe(accountReset);
    expect(lane.periodHours).toBe(168);
    expect(lane.remaining).toBe(30);
  });

  test('codex: includes available reset credits with parseable expiry dates', () => {
    const expiresAt = '2026-08-02T12:00:00Z';
    const lane = buildTimelineLane({
      ...base,
      provider: 'codex',
      quota: {
        status: 'success',
        windows: [{ label: '7-day', usedPercent: 90, resetAtMs: 5000, periodHours: 168 }],
        rateLimitResetCredits: [
          {
            id: 'credit-1',
            status: 'available',
            grantedAt: '2026-07-01T12:00:00Z',
            expiresAt,
          },
          {
            id: 'spent',
            status: 'consumed',
            grantedAt: '2026-07-01T12:00:00Z',
            expiresAt,
          },
          { id: 'invalid', status: 'available', grantedAt: '', expiresAt: 'not-a-date' },
        ],
      },
    });

    expect(lane.resetCredits).toEqual([
      {
        id: 'credit-1',
        grantedAtMs: new Date('2026-07-01T12:00:00Z').getTime(),
        expiresAtMs: new Date(expiresAt).getTime(),
      },
    ]);
  });

  test('kimi: derives remaining from raw used/limit counts', () => {
    const lane = buildTimelineLane({
      ...base,
      provider: 'kimi',
      quota: {
        status: 'success',
        rows: [
          { label: 'Daily', used: 540, limit: 1000, resetAtMs: 5000, periodHours: 24 },
          { label: 'Monthly', used: 8100, limit: 30000, resetAtMs: 9000, periodHours: 720 },
        ],
      },
      // A fortnight fits the daily window but not the monthly one.
      maxPeriodHours: 14 * 24,
    });

    expect(lane.anchorMs).toBe(5000);
    expect(lane.remaining).toBe(46);
    expect(lane.limits).toEqual([
      { label: 'Daily', remaining: 46 },
      { label: 'Monthly', remaining: 73 },
    ]);
  });

  test('antigravity anchors on its bucket reset, with remaining from the fraction', () => {
    const lane = buildTimelineLane({
      ...base,
      provider: 'antigravity',
      quota: {
        status: 'success',
        groups: [
          {
            buckets: [
              { label: '5h', remainingFraction: 0.4, resetAtMs: 1000, periodHours: 5 },
              { label: 'Weekly', remainingFraction: 0.82, resetAtMs: 5000, periodHours: 168 },
            ],
          },
        ],
      },
      maxPeriodHours: 336,
    });

    // Longest window that fits wins, exactly as it does for claude/codex.
    expect(lane.anchorMs).toBe(5000);
    expect(lane.periodHours).toBe(168);
    // remainingFraction is REMAINING, so it is not inverted.
    expect(lane.remaining).toBe(82);
    expect(lane.limits).toEqual([
      { label: '5h', remaining: 40 },
      { label: 'Weekly', remaining: 82 },
    ]);
  });

  test('antigravity buckets without a parseable reset do not anchor the lane', () => {
    const lane = buildTimelineLane({
      ...base,
      provider: 'antigravity',
      quota: {
        status: 'success',
        groups: [{ buckets: [{ label: '5h', remainingFraction: 0.4, resetAtMs: null }] }],
      },
    });
    expect(lane.anchorMs).toBeNull();
    expect(lane.limits).toEqual([]);
  });

  test('xai anchors on the weekly limit, with per-product limits', () => {
    const lane = buildTimelineLane({
      ...base,
      provider: 'xai',
      quota: {
        status: 'success',
        billing: {
          periodType: 'weekly',
          usagePercent: 5,
          resetAtMs: 9000,
          periodHours: 168,
          productUsage: [
            { product: 'GrokBuild', usagePercent: 5 },
            { product: 'GrokChat', usagePercent: null },
          ],
        },
      },
    });

    expect(lane.anchorMs).toBe(9000);
    expect(lane.periodHours).toBe(168);
    expect(lane.remaining).toBe(95);
    // GrokChat has no percentage, so it is not summarized.
    expect(lane.limits).toEqual([{ label: 'GrokBuild', remaining: 95 }]);
  });

  test('xai without a weekly limit produces no window at all', () => {
    // A monthly summary carries periodEnd too, but that is a billing cycle.
    for (const periodType of ['monthly', 'unknown'] as const) {
      const lane = buildTimelineLane({
        ...base,
        provider: 'xai',
        quota: {
          status: 'success',
          billing: { periodType, usagePercent: 69, resetAtMs: 9000, periodHours: 720 },
        },
      });
      expect(lane.anchorMs).toBeNull();
      expect(laneHasWindow(lane)).toBe(false);
    }
  });

  test('xai defaults to a 7-day period when the payload states no start', () => {
    const lane = buildTimelineLane({
      ...base,
      provider: 'xai',
      quota: {
        status: 'success',
        billing: { periodType: 'weekly', usagePercent: 5, resetAtMs: 9000, periodHours: null },
      },
    });
    expect(lane.periodHours).toBe(24 * 7);
  });

  test('laneHasWindow drops only lanes that can never draw a bar', () => {
    const drawable = buildTimelineLane({
      ...base,
      provider: 'claude',
      quota: {
        status: 'success',
        windows: [{ label: '7-day', usedPercent: 10, resetAtMs: 5000, periodHours: 168 }],
      },
    });
    expect(laneHasWindow(drawable)).toBe(true);
    // Unloaded quota has nothing to show yet, so it takes no row.
    expect(
      laneHasWindow(buildTimelineLane({ ...base, provider: 'claude', quota: undefined }))
    ).toBe(false);
  });

  test('providers with no usable reset produce an empty lane, not a dropped one', () => {
    // buildTimelineLane always returns a lane; dropping is the caller's job via
    // laneHasWindow, so the two concerns stay separately testable.
    for (const provider of ['antigravity', 'xai'] as const) {
      const lane = buildTimelineLane({
        ...base,
        provider,
        quota: { status: 'success', groups: [] },
      });
      expect(lane.name).toBe('a.json');
      expect(lane.anchorMs).toBeNull();
      expect(lane.limits).toEqual([]);
    }
  });

  test('unloaded or errored quota produces an empty lane', () => {
    expect(
      buildTimelineLane({ ...base, provider: 'claude', quota: undefined }).anchorMs
    ).toBeNull();
    expect(
      buildTimelineLane({ ...base, provider: 'claude', quota: { status: 'error' } }).anchorMs
    ).toBeNull();
  });

  test('windows without a reset instant do not anchor the lane', () => {
    const lane = buildTimelineLane({
      ...base,
      provider: 'claude',
      quota: {
        status: 'success',
        windows: [{ label: '7-day', usedPercent: 50, resetAtMs: null, periodHours: 168 }],
      },
    });
    expect(lane.anchorMs).toBeNull();
  });
});
