import { describe, expect, test } from 'bun:test';
import {
  buildLedgerRows,
  buildLedgerSummary,
  maskCredentialName,
  selectLedgerMeters,
  weeklyRemaining,
} from '@/features/quota/ledger/ledgerModel';
import type { QuotaFileEntry } from '@/features/quota/logic';

const HOUR = 3_600_000;
const NOW = 1_800_000_000_000;

const claude = (name: string): QuotaFileEntry => ({ type: 'claude', file: { name } });

const claudeQuota = (fableUsed: number, resetInHours: number) => ({
  status: 'success',
  windows: [
    { id: 'five-hour', usedPercent: 0, resetAtMs: null, resetLabel: '' },
    { id: 'seven-day', usedPercent: 20, resetAtMs: NOW + resetInHours * HOUR, resetLabel: 'x' },
    {
      id: 'seven-day-fable',
      usedPercent: fableUsed,
      resetAtMs: NOW + resetInHours * HOUR,
      resetLabel: 'x',
    },
    { id: 'seven-day-opus', usedPercent: 90, resetAtMs: null, resetLabel: '' },
  ],
});

describe('ledger meters', () => {
  test('claude spends the model-scoped weekly window and greys the shared one', () => {
    const meters = selectLedgerMeters('claude', claudeQuota(42, 24).windows);
    expect(meters.map((m) => [m.id, m.muted])).toEqual([
      ['seven-day-fable', false],
      ['five-hour', false],
      ['seven-day', true],
    ]);
    expect(meters[0].remaining).toBe(58);
  });

  test('codex shows the weekly window first', () => {
    const meters = selectLedgerMeters('codex', [
      { id: 'five-hour', usedPercent: 10 },
      { id: 'weekly', usedPercent: 83 },
      { id: 'code-review-weekly', usedPercent: 0 },
    ]);
    expect(meters.map((m) => m.id)).toEqual(['weekly', 'five-hour']);
  });
});

describe('ledger rows', () => {
  const entries = [claude('a.json'), claude('b.json'), claude('c.json'), claude('d.json')];
  const quotas: Record<string, unknown> = {
    'a.json': claudeQuota(0, 96),
    'b.json': claudeQuota(49, 20),
    'c.json': claudeQuota(100, 2),
    'd.json': undefined,
  };
  const rows = buildLedgerRows('claude', entries, (e) => quotas[e.file.name], NOW);

  test('orders by soonest primary reset, unloaded last', () => {
    expect(rows.map((r) => r.entry.file.name)).toEqual(['c.json', 'b.json', 'a.json', 'd.json']);
  });

  test('marks the soonest-resetting account with quota left as next up', () => {
    expect(rows.filter((r) => r.nextUp).map((r) => r.entry.file.name)).toEqual(['b.json']);
  });

  test('summary sums remaining primary quota', () => {
    const summary = buildLedgerSummary('claude', rows);
    expect(summary.remainingTotal).toBe(0 + 51 + 100);
    expect(summary.capacity).toBe(300);
  });

  test('orders claude by the shared weekly reset, as routing does', () => {
    const quota = (fableHours: number, sharedHours: number) => ({
      status: 'success',
      windows: [
        { id: 'seven-day', usedPercent: 0, resetAtMs: NOW + sharedHours * HOUR },
        { id: 'seven-day-fable', usedPercent: 0, resetAtMs: NOW + fableHours * HOUR },
      ],
    });
    const byName: Record<string, unknown> = { 'x.json': quota(1, 50), 'y.json': quota(40, 10) };
    const ordered = buildLedgerRows(
      'claude',
      [claude('x.json'), claude('y.json')],
      (e) => byName[e.file.name],
      NOW
    );
    expect(ordered.map((r) => r.entry.file.name)).toEqual(['y.json', 'x.json']);
    expect(ordered[0].weeklyResetAtMs).toBe(NOW + 10 * HOUR);
  });

  test('an exhausted shared weekly limit is never next up', () => {
    const quota = (sharedUsed: number, hours: number) => ({
      status: 'success',
      windows: [
        { id: 'seven-day', usedPercent: sharedUsed, resetAtMs: NOW + hours * HOUR },
        { id: 'seven-day-fable', usedPercent: 0, resetAtMs: NOW + hours * HOUR },
      ],
    });
    const byName: Record<string, unknown> = { 'x.json': quota(100, 5), 'y.json': quota(30, 50) };
    const ordered = buildLedgerRows(
      'claude',
      [claude('x.json'), claude('y.json')],
      (e) => byName[e.file.name],
      NOW
    );
    expect(ordered.map((r) => [r.entry.file.name, r.nextUp])).toEqual([
      ['x.json', false],
      ['y.json', true],
    ]);
    expect(weeklyRemaining(ordered[0])).toBe(0);
    expect(weeklyRemaining(ordered[1])).toBe(70);
  });

  test('a five-hour window alone gives no weekly reset', () => {
    const [only] = buildLedgerRows(
      'codex',
      [{ type: 'codex', file: { name: 'z.json' } }],
      () => ({
        status: 'success',
        windows: [{ id: 'five-hour', usedPercent: 0, resetAtMs: NOW + HOUR }],
      }),
      NOW
    );
    expect(only.primary?.id).toBe('five-hour');
    expect(only.weeklyResetAtMs).toBeNull();
    expect(only.nextUp).toBe(false);
  });
});

describe('maskCredentialName', () => {
  test('keeps the provider prefix and the first letter of each part', () => {
    expect(maskCredentialName('claude-theo@ping.gg.json')).toBe('claude-t•••@p•••.gg.json');
    expect(maskCredentialName('codex-ae5d455f-sam@example.com-pro.json')).toBe(
      'codex-ae5d455f-s•••@e•••.com-pro.json'
    );
  });

  test('masks every hyphenated part of the local name', () => {
    expect(maskCredentialName('claude-jane-doe@example.com.json')).toBe(
      'claude-j•••@e•••.com.json'
    );
    expect(maskCredentialName('claude-0123abcd-jane-doe@example.com.json')).toBe(
      'claude-0123abcd-j•••@e•••.com.json'
    );
  });

  test('keeps no prefix that is not a provider', () => {
    expect(maskCredentialName('jane-doe@example.com.json')).toBe('j•••@e•••.com.json');
  });

  test('leaves names without an email alone', () => {
    expect(maskCredentialName('kimi-1784240993036.json')).toBe('kimi-1784240993036.json');
  });
});
