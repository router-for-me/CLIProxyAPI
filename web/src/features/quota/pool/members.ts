import { DEFAULT_PERIOD_MS } from '@/features/quota/pool/constants';
import type { PoolMember, PoolModelInput, Reading } from '@/features/quota/pool/types';
import { readServerQuotaWindows, resetAtMs } from '@/features/quota/serverQuota';
import type { QuotaHistoryResult, QuotaHistoryWindow } from '@/services/api/quotaHistory';
import type { AuthFileItem } from '@/types';
import { getQuotaCacheKey } from '@/utils/quota/identity';

const isUnscopedWeekly = (window: { kind: string; scope?: string }): boolean =>
  window.kind === 'weekly' && !window.scope;

const findHistoryWindow = (
  history: QuotaHistoryResult | null,
  file: AuthFileItem
): QuotaHistoryWindow | undefined => {
  if (history?.status !== 'ok') return undefined;

  const id = String(file.id ?? '');
  const credential =
    history.credentials.find((candidate) => candidate.id === id) ??
    history.credentials.find((candidate) => candidate.name === file.name);

  return credential?.windows.find(isUnscopedWeekly);
};

const getReadings = (
  file: AuthFileItem,
  historyWindow: QuotaHistoryWindow | undefined,
  dataAt: number
): Reading[] => {
  const readings: Reading[] = (historyWindow?.points ?? [])
    .filter((point) => point.t < dataAt)
    .map(({ resetAt, t, used }) => ({ live: false, resetAt, t, used }));
  const live = readServerQuotaWindows(file).find(isUnscopedWeekly);

  if (live) {
    readings.push({
      live: true,
      resetAt: resetAtMs(live.reset_at),
      t: dataAt,
      used: Math.min(1, Math.max(0, live.used)),
    });
  }

  return readings;
};

/** Every file with at least one weekly reading (history or live) becomes a member. */
export const buildPoolMembers = (
  input: PoolModelInput
): { members: PoolMember[]; noDataCount: number } => {
  const { dataAt, files, history } = input;
  const members: PoolMember[] = [];

  for (const file of files) {
    const historyWindow = findHistoryWindow(history, file);
    const readings = getReadings(file, historyWindow, dataAt);

    if (readings.length > 0) {
      const key = getQuotaCacheKey(file);

      members.push({
        file,
        key,
        periodMs: historyWindow?.periodMs ?? DEFAULT_PERIOD_MS,
        readings,
      });
    }
  }

  return { members, noDataCount: files.length - members.length };
};
