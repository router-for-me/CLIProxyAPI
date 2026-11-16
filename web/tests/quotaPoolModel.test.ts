import { describe, expect, test } from 'bun:test';
import {
  formatMonthDay,
  formatRate,
  formatWhen,
  splitDuration,
} from '@/features/quota/pool/format';
import { buildPoolCardModel, worstHistoryStatus } from '@/features/quota/pool/model';
import { accountOrdinals } from '@/features/quota/pool/members';
import type { HistoryStatus, PoolCardModel } from '@/features/quota/pool/types';
import type { QuotaHistoryPoint } from '@/services/api/quotaHistory';
import { DAY_MS, HOUR_MS } from '@/utils/time/durations';
import {
  CLAUDE_RESETS,
  CODEX_EMPTY,
  CODEX_RESET,
  NOW,
  TZ,
  claudeName,
  codexName,
  historyCredential,
  okHistory,
  poolModelInput,
  weeklyFile,
} from './fixtures/quotaPoolFixture';

const claudeModel = (overrides = {}) =>
  buildPoolCardModel(poolModelInput('claude', overrides)) as PoolCardModel;

const codexModel = (overrides = {}) =>
  buildPoolCardModel(poolModelInput('codex', overrides)) as PoolCardModel;

const countdown = (at: number, now: number) => splitDuration(at - now);

describe('buildPoolCardModel on the fixture', () => {
  test('Claude matches the fixture numbers', () => {
    const model = claudeModel();
    const { firstEmpty, nextReset, poolEmpty } = model.stats;

    expect(model.poolNow).toBeCloseTo(555, 9);
    expect(model.capacity).toBe(600);
    expect(model.memberCount).toBe(6);
    expect(model.noDataCount).toBe(0);
    expect(model.historyStatus).toBe('ok');
    expect(formatRate(model.burn.value ?? 0)).toBe('3.7');
    expect(model.burn).toMatchObject({ approximate: false, hours: 3 });
    expect(firstEmpty.kind === 'at' && firstEmpty.key).toBe(claudeName('a'));
    expect(firstEmpty.kind === 'at' && countdown(firstEmpty.at, NOW)).toEqual({
      d: 0,
      h: 15,
      m: 49,
    });
    expect(nextReset).toEqual({ at: CLAUDE_RESETS[0], gain: 100, kind: 'at', lowerBound: false });
    expect(formatWhen(CLAUDE_RESETS[0], NOW, 'en', TZ)).toBe('Tue 10:00 AM');
    expect(poolEmpty).toEqual({ horizonEnd: NOW + 14 * DAY_MS, kind: 'never' });
    expect(formatMonthDay(NOW + 14 * DAY_MS, 'en', TZ)).toBe('Nov 9');
    expect(
      model.remaining.steps
        .filter((step) => step.kind === 'projected')
        .slice(0, 4)
        .map((step) => [step.t, Math.round(step.gain)])
    ).toEqual([
      [CLAUDE_RESETS[0], 100],
      [CLAUDE_RESETS[1], 97],
      [CLAUDE_RESETS[2], 60],
      [CLAUDE_RESETS[3], 48],
    ]);
    expect(model.remaining.yMax).toBe(600);
    expect(model.remaining.emptyAt).toBeNull();
    expect(model.remaining.recorded).toHaveLength(1);
    expect(model.range).toMatchObject({
      end: Date.UTC(2026, 10, 6, 8),
      start: Date.UTC(2026, 9, 23, 7),
    });
    expect(formatMonthDay(model.range.start, 'en', TZ)).toBe('Oct 23');
    expect(formatMonthDay(model.range.end - 1, 'en', TZ)).toBe('Nov 5');
  });

  test('Codex matches the fixture numbers', () => {
    const model = codexModel();
    const { firstEmpty, nextReset, poolEmpty } = model.stats;

    expect(model.poolNow).toBeCloseTo(270, 9);
    expect(model.capacity).toBe(300);
    expect(formatRate(model.burn.value ?? 0)).toBe('1.7');
    expect(firstEmpty.kind === 'at' && firstEmpty.key).toBe(codexName('b'));
    expect(firstEmpty.kind === 'at' && countdown(firstEmpty.at, NOW)).toMatchObject({
      d: 2,
      h: 10,
    });
    expect(nextReset).toEqual({ at: CODEX_RESET, gain: 300, kind: 'at', lowerBound: false });
    expect(formatWhen(CODEX_RESET, NOW, 'en', TZ)).toBe('Sun 10:08 PM');
    expect(poolEmpty).toEqual({ at: CODEX_EMPTY, kind: 'at' });
    expect(formatWhen(CODEX_EMPTY, NOW, 'en', TZ)).toBe('Sun 4:39 PM');
    expect(model.remaining.emptyAt).toBe(CODEX_EMPTY);
    expect(model.remaining.steps.map((step) => [step.kind, step.t, Math.round(step.gain)])).toEqual(
      [['projected', CODEX_RESET, 300]]
    );
  });

  test('computes the drain view only when it is shown', () => {
    const remaining = claudeModel();
    const drain = claudeModel({ metric: 'drain' });

    expect(remaining.drain.recorded).toEqual([]);
    expect(drain.drain.recorded.length).toBeGreaterThan(0);
    expect(drain.drain.yMax).toBe(5);
    expect(remaining.drain.projected).toEqual(drain.drain.projected);
  });
});

describe('data time', () => {
  test('a later wall clock moves only the countdowns', () => {
    const atLoad = claudeModel();
    const later = claudeModel({ now: NOW + 4 * HOUR_MS });
    const earlierHorizon = (model: PoolCardModel) =>
      model.projection?.points.filter((point) => point.t < NOW + 14 * DAY_MS);
    const { firstEmpty } = later.stats;

    expect(later.burn).toEqual(atLoad.burn);
    expect(later.poolNow).toBe(atLoad.poolNow);
    expect(later.projection?.poolEmptyAt).toBe(atLoad.projection?.poolEmptyAt ?? null);
    expect(later.projection?.steps).toEqual(atLoad.projection?.steps);
    expect(earlierHorizon(later)).toEqual(earlierHorizon(atLoad));
    expect(later.projection?.end).toBe(NOW + 4 * HOUR_MS + 14 * DAY_MS);
    expect(firstEmpty.kind === 'at' && countdown(firstEmpty.at, later.now)).toEqual({
      d: 0,
      h: 11,
      m: 49,
    });
    expect(later.burn.value).toBeGreaterThan(0);
  });
});

describe('degradation', () => {
  test('history still loading: no burn, no projection, a lower-bound next reset', () => {
    const model = claudeModel({ history: null });

    expect(model.historyStatus).toBe('loading');
    expect(model.burn.value).toBeNull();
    expect(model.projection).toBeNull();
    expect(model.lowerBound).not.toBeNull();
    expect(model.stats.poolEmpty).toEqual({ kind: 'unknown' });
    expect(model.stats.firstEmpty).toEqual({ kind: 'unknown' });
    expect(model.stats.nextReset).toEqual({
      at: CLAUDE_RESETS[0],
      gain: 42,
      kind: 'at',
      lowerBound: true,
    });
    expect(model.remaining.recorded).toEqual([]);
    expect(model.drain.yMax).toBe(1);
  });

  test('passes unsupported and disabled through', () => {
    expect(claudeModel({ history: { status: 'unsupported' } }).historyStatus).toBe('unsupported');
    expect(claudeModel({ history: { status: 'disabled' } }).historyStatus).toBe('disabled');
  });

  test('reports an ok response without points as empty', () => {
    expect(claudeModel({ history: okHistory([]) }).historyStatus).toBe('empty');
  });

  test('still projects "No usage" without history', () => {
    const model = claudeModel({ futureUsage: 'none', history: null });

    expect(model.projection?.rate).toBe(0);
    expect(model.lowerBound).toBeNull();
    expect(model.stats.nextReset).toMatchObject({ gain: 42, lowerBound: false });
  });

  test('scales the projection by the future usage factor', () => {
    expect(claudeModel({ futureUsage: 'double' }).projection?.rate).toBeCloseTo(7.4, 9);
    expect(claudeModel({ futureUsage: 'half' }).projection?.rate).toBeCloseTo(1.85, 9);
  });

  test('returns null without any weekly reading', () => {
    const files = [{ name: 'empty.json', type: 'claude' }];

    expect(
      buildPoolCardModel(poolModelInput('claude', { files, history: okHistory([]) }))
    ).toBeNull();
  });
});

describe('worstHistoryStatus', () => {
  const withStatus = (historyStatus: HistoryStatus) => ({ ...claudeModel(), historyStatus });

  test('picks error > unsupported > disabled > empty and ignores ok and loading', () => {
    expect(worstHistoryStatus([withStatus('ok'), withStatus('empty')])).toBe('empty');
    expect(worstHistoryStatus([withStatus('unsupported'), withStatus('error')])).toBe('error');
    expect(worstHistoryStatus([withStatus('disabled'), withStatus('unsupported')])).toBe(
      'unsupported'
    );
    expect(worstHistoryStatus([withStatus('ok'), withStatus('loading')])).toBeNull();
    expect(worstHistoryStatus([])).toBeNull();
  });
});

describe('performance', () => {
  const MEMBERS = 50;
  const READINGS = 740;
  const STEP_MS = (14 * DAY_MS) / READINGS;

  const largeInput = () => {
    const files = Array.from({ length: MEMBERS }, (_, index) =>
      weeklyFile(`claude-${index}@example.com.json`, 0.5, NOW + (index + 1) * HOUR_MS, {
        type: 'claude',
      })
    );
    const credentials = files.map((file, index) => {
      const points: QuotaHistoryPoint[] = Array.from({ length: READINGS }, (_, reading) => {
        const t = NOW - 14 * DAY_MS + reading * STEP_MS;
        const weekStart = NOW - 7 * DAY_MS + index * HOUR_MS;

        return {
          resetAt: t < weekStart ? weekStart : weekStart + 7 * DAY_MS,
          t: Math.round(t),
          used: ((reading % 370) / 370) * 0.9,
        };
      });

      return historyCredential(file.name, 'claude', points);
    });

    return poolModelInput('claude', {
      files,
      history: okHistory(credentials),
      ordinals: accountOrdinals(files),
    });
  };

  test('50 members x 740 readings build in under 200ms per metric', () => {
    const input = largeInput();

    for (const metric of ['remaining', 'drain'] as const) {
      const started = performance.now();
      const model = buildPoolCardModel({ ...input, metric });

      expect(performance.now() - started).toBeLessThan(200);
      expect(model?.memberCount).toBe(MEMBERS);
    }
  });
});
