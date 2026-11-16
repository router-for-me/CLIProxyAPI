import type { PoolProvider } from '@/features/quota/pool/constants';
import { accountOrdinals } from '@/features/quota/pool/members';
import type { PoolModelInput } from '@/features/quota/pool/types';
import type {
  QuotaHistoryCredential,
  QuotaHistoryPoint,
  QuotaHistoryResult,
} from '@/services/api/quotaHistory';
import type { AuthFileItem } from '@/types';
import { HOUR_MS, MINUTE_MS } from '@/utils/time/durations';

export const TZ = 'America/Los_Angeles';
/** Mon Oct 26 03:52 PDT. */
export const NOW = Date.UTC(2026, 9, 26, 10, 52);
/** Oct 22 00:00 PDT, before the range start. */
export const H0 = Date.UTC(2026, 9, 22, 7, 0);
/** Tue Oct 27 10:00 PDT ... Sun Nov 1 12:58 PST. */
export const CLAUDE_RESETS = [
  Date.UTC(2026, 9, 27, 17, 0),
  Date.UTC(2026, 9, 28, 5, 0),
  Date.UTC(2026, 9, 28, 21, 8),
  Date.UTC(2026, 9, 29, 10, 4),
  Date.UTC(2026, 10, 1, 6, 4),
  Date.UTC(2026, 10, 1, 20, 58),
];
/** Sums to 555. */
export const CLAUDE_REMAINING = [58.5, 100, 100, 100, 96.5, 100];
/** Sun Nov 1 22:08 PST. */
export const CODEX_RESET = Date.UTC(2026, 10, 2, 6, 8);
/** Sun Nov 1 16:39 PST. */
export const CODEX_EMPTY = Date.UTC(2026, 10, 2, 0, 39);
/** Sums to 270. */
export const CODEX_REMAINING = [70, 100, 100];
/** About 1.7112 %/h. */
export const CODEX_RATE = 270 / ((CODEX_EMPTY - NOW) / HOUR_MS);
export const DATA_AT = NOW;
export const WEEK_MS = 168 * HOUR_MS;

const LETTERS = ['a', 'b', 'c', 'd', 'e', 'f'];
const CLAUDE_A_USED = [0.304, 0.3595];
const CODEX_A_START_USED = 1 - (CODEX_REMAINING[0] + 3 * CODEX_RATE) / 100;

export const claudeName = (letter: string): string => `claude-${letter}@example.com.json`;
export const codexName = (letter: string): string => `codex-${letter}@example.com.json`;

export const weeklyFile = (
  name: string,
  used: number,
  resetAt: number,
  extra: Partial<AuthFileItem> = {}
): AuthFileItem => ({
  id: name,
  name,
  quota_windows: [
    {
      kind: 'weekly',
      observed_at: new Date(NOW - 2 * MINUTE_MS).toISOString(),
      reset_at: new Date(resetAt).toISOString(),
      source: 'usage',
      used,
    },
  ],
  ...extra,
});

export const claudeFiles = (): AuthFileItem[] =>
  LETTERS.map((letter, index) =>
    weeklyFile(claudeName(letter), 1 - CLAUDE_REMAINING[index] / 100, CLAUDE_RESETS[index], {
      label: `user-${letter}@example.com`,
      type: 'claude',
    })
  );

export const codexFiles = (): AuthFileItem[] =>
  LETTERS.slice(0, 3).map((letter, index) =>
    weeklyFile(codexName(letter), 1 - CODEX_REMAINING[index] / 100, CODEX_RESET, {
      label: `coder-${letter}@example.com`,
      type: 'codex',
    })
  );

/** Hourly keep-alive points from H0 while t < until, like the real recorder. */
const keepAlives = (used: number, resetAt: number, until: number): QuotaHistoryPoint[] =>
  Array.from({ length: Math.ceil((until - H0) / HOUR_MS) }, (_, hour) => ({
    resetAt,
    t: H0 + hour * HOUR_MS,
    used,
  }));

export const historyCredential = (
  name: string,
  provider: PoolProvider,
  points: QuotaHistoryPoint[]
): QuotaHistoryCredential => ({
  authIndex: `index-${name}`,
  id: name,
  name,
  provider,
  windows: [{ kind: 'weekly', periodMs: WEEK_MS, points, scope: '' }],
});

export const okHistory = (credentials: QuotaHistoryCredential[]): QuotaHistoryResult => ({
  credentials,
  retentionHours: 720,
  serverNow: NOW,
  since: H0,
  status: 'ok',
});

const claudeHistory = (letter: string, index: number): QuotaHistoryCredential => {
  const resetAt = CLAUDE_RESETS[index];

  if (index > 0) {
    const used = 1 - CLAUDE_REMAINING[index] / 100;

    return historyCredential(claudeName(letter), 'claude', keepAlives(used, resetAt, NOW));
  }

  return historyCredential(claudeName(letter), 'claude', [
    ...keepAlives(CLAUDE_A_USED[0], resetAt, NOW - 3 * HOUR_MS),
    { resetAt, t: NOW - 3 * HOUR_MS, used: CLAUDE_A_USED[0] },
    { resetAt, t: NOW - 90 * MINUTE_MS, used: CLAUDE_A_USED[1] },
  ]);
};

const codexHistory = (letter: string, index: number): QuotaHistoryCredential => {
  if (index > 0) {
    return historyCredential(codexName(letter), 'codex', keepAlives(0, CODEX_RESET, NOW));
  }

  return historyCredential(codexName(letter), 'codex', [
    ...keepAlives(CODEX_A_START_USED, CODEX_RESET, NOW - 3 * HOUR_MS),
    { resetAt: CODEX_RESET, t: NOW - 3 * HOUR_MS, used: CODEX_A_START_USED },
  ]);
};

export const historyOk = (): QuotaHistoryResult =>
  okHistory([...LETTERS.map(claudeHistory), ...LETTERS.slice(0, 3).map(codexHistory)]);

/** claude-a only: a 48h observation gap with a 60-point drop. Pair with claudeOutageFiles(). */
export const historyWithOutage = (): QuotaHistoryResult =>
  okHistory([
    historyCredential(claudeName('a'), 'claude', [
      { resetAt: CLAUDE_RESETS[0], t: NOW - 48 * HOUR_MS - 29 * MINUTE_MS, used: 0.304 },
      { resetAt: CLAUDE_RESETS[0], t: NOW - 29 * MINUTE_MS, used: 0.904 },
    ]),
  ]);

export const claudeOutageFiles = (): AuthFileItem[] => [
  weeklyFile(claudeName('a'), 0.904, CLAUDE_RESETS[0], { type: 'claude' }),
];

/** The model input the page builds for the fixture; overrides replace any field. */
export const poolModelInput = (
  provider: PoolProvider,
  overrides: Partial<PoolModelInput> = {}
): PoolModelInput => {
  const files = overrides.files ?? (provider === 'claude' ? claudeFiles() : codexFiles());

  return {
    burnWindow: 3,
    dataAt: DATA_AT,
    files,
    futureUsage: 'same',
    history: historyOk(),
    metric: 'remaining',
    now: NOW,
    ordinals: accountOrdinals(files),
    provider,
    rangeOffset: 0,
    timeZone: TZ,
    ...overrides,
  };
};
