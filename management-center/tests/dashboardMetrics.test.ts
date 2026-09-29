import { describe, expect, test } from 'bun:test';
import { formatCompactNumber, formatPercent } from '../src/utils/format';
import { getProviderKeyCounts } from '../src/features/dashboard/hooks/useDashboardOverview';
import {
  axisMax,
  niceCeil,
  providerLabel,
  splitWindowMinutes,
  toneForSuccessRate,
} from '../src/features/dashboard/utils';

describe('formatCompactNumber', () => {
  test('leaves values below one thousand alone', () => {
    expect(formatCompactNumber(0)).toBe('0');
    expect(formatCompactNumber(999)).toBe('999');
  });

  test('compacts with a single decimal and no redundant .0', () => {
    expect(formatCompactNumber(1000)).toBe('1K');
    expect(formatCompactNumber(1284)).toBe('1.3K');
    expect(formatCompactNumber(12_900)).toBe('12.9K');
    expect(formatCompactNumber(1_500_000)).toBe('1.5M');
  });

  test('carries into the next tier instead of rendering 1000K', () => {
    expect(formatCompactNumber(999_999)).toBe('1M');
    expect(formatCompactNumber(999_999_999)).toBe('1B');
  });

  test('keeps the sign and survives non-finite input', () => {
    expect(formatCompactNumber(-1500)).toBe('-1.5K');
    expect(formatCompactNumber(Number.NaN)).toBe('0');
  });
});

describe('formatPercent', () => {
  test('trims trailing zeros but keeps meaningful decimals', () => {
    expect(formatPercent(100)).toBe('100%');
    expect(formatPercent(99.5)).toBe('99.5%');
    expect(formatPercent(0)).toBe('0%');
  });

  test('renders an em dash for non-finite rates', () => {
    expect(formatPercent(Number.NaN)).toBe('—');
  });
});

describe('niceCeil', () => {
  test('rounds up onto the step ladder', () => {
    expect(niceCeil(1)).toBe(1);
    expect(niceCeil(7)).toBe(8);
    expect(niceCeil(12)).toBe(15);
    expect(niceCeil(48)).toBe(50);
    expect(niceCeil(320)).toBe(400);
  });

  test('never returns zero, so bar heights cannot divide by zero', () => {
    expect(niceCeil(0)).toBe(1);
    expect(niceCeil(-5)).toBe(1);
  });
});

describe('axisMax', () => {
  test('lands every gridline on a whole number', () => {
    // 峰值 112 → 上限 120（刻度 0/30/60/90/120），而不是浪费半张图的 200
    expect(axisMax(112, 4)).toBe(120);
    expect(axisMax(7, 4)).toBe(8);
    expect(axisMax(1533, 4)).toBe(1600);
  });

  test('keeps the axis just above the peak', () => {
    for (const peak of [1, 3, 9, 17, 64, 112, 250, 999, 4321]) {
      const max = axisMax(peak, 4);
      expect(max).toBeGreaterThanOrEqual(peak);
      // 上限不应超过峰值的两倍，否则柱子被压得太矮。
      // 峰值极小时受「每格至少 1」约束，下限就是间隔数本身。
      expect(max).toBeLessThanOrEqual(Math.max(4, peak * 2));
      // 每格都必须是整数
      expect(Number.isInteger(max / 4)).toBe(true);
    }
  });

  test('degrades safely with no traffic', () => {
    expect(axisMax(0, 4)).toBe(4);
    expect(axisMax(-3, 4)).toBe(4);
  });
});

describe('toneForSuccessRate', () => {
  test('maps a rate onto a severity band', () => {
    expect(toneForSuccessRate(null)).toBe('idle');
    expect(toneForSuccessRate(100)).toBe('good');
    expect(toneForSuccessRate(95)).toBe('good');
    expect(toneForSuccessRate(94.9)).toBe('warning');
    expect(toneForSuccessRate(80)).toBe('warning');
    expect(toneForSuccessRate(79.9)).toBe('critical');
  });
});

describe('splitWindowMinutes', () => {
  test('splits the rolling window into hours and minutes', () => {
    expect(splitWindowMinutes(200)).toEqual({ hours: 3, minutes: 20 });
    expect(splitWindowMinutes(60)).toEqual({ hours: 1, minutes: 0 });
    expect(splitWindowMinutes(40)).toEqual({ hours: 0, minutes: 40 });
  });
});

describe('provider key counts', () => {
  test('includes Muse API keys and defaults missing configuration to zero', () => {
    expect(getProviderKeyCounts({ metaApiKeys: [{ apiKey: 'fixture-meta-key' }] }).meta).toBe(1);
    expect(getProviderKeyCounts({}).meta).toBe(0);
  });
  test('includes native Interactions API keys in the dashboard total inputs', () => {
    const counts = getProviderKeyCounts({
      geminiApiKeys: [{ apiKey: 'gemini-key' }],
      interactionsApiKeys: [{ apiKey: 'interactions-1' }, { apiKey: 'interactions-2' }],
      codexApiKeys: [{ apiKey: 'codex-key' }],
    });

    expect(counts.interactions).toBe(2);
    expect(Object.values(counts).reduce((sum, count) => sum + count, 0)).toBe(4);
  });
});

describe('providerLabel', () => {
  test('uses the brand spelling for known providers', () => {
    expect(providerLabel('xai', 'Unattributed')).toBe('xAI');
    expect(providerLabel('aistudio', 'Unattributed')).toBe('AI Studio');
    expect(providerLabel('gemini-interactions', 'Unattributed')).toBe('Interactions API');
  });

  test('falls back to a capitalised id, and localises unknown', () => {
    expect(providerLabel('somenewbrand', 'Unattributed')).toBe('Somenewbrand');
    expect(providerLabel('unknown', 'Unattributed')).toBe('Unattributed');
    expect(providerLabel('', 'Unattributed')).toBe('Unattributed');
  });
});
