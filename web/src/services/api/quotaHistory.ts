/**
 * Recorded weekly quota readings per credential (`GET /credentials/quota-history`).
 *
 * The server records raw observations only; the pool model rebuilds rollovers
 * from `resetAt`. `get` never throws: every failure becomes a result status.
 */

import { HISTORY_RETENTION_HOURS_DEFAULT } from '@/features/quota/pool/constants';
import { apiClient } from '@/services/api/client';
import { getErrorMessage, isRecord } from '@/utils/helpers';

export type QuotaHistoryKind = 'five_hour' | 'weekly' | 'weekly_scoped';

/** `t` and `resetAt` are epoch ms; `used` is a 0–1 fraction. */
export interface QuotaHistoryPoint {
  resetAt: number | null;
  t: number;
  used: number;
}

export interface QuotaHistoryWindow {
  kind: QuotaHistoryKind;
  periodMs: number | null;
  points: QuotaHistoryPoint[];
  scope: string;
}

export interface QuotaHistoryCredential {
  authIndex: string | null;
  id: string;
  name: string;
  provider: string;
  windows: QuotaHistoryWindow[];
}

export type QuotaHistoryResult =
  | {
      credentials: QuotaHistoryCredential[];
      retentionHours: number;
      serverNow: number;
      since: number;
      status: 'ok';
    }
  | { status: 'disabled' }
  /** HTTP 404: the server predates the endpoint. */
  | { status: 'unsupported' }
  | { message: string; status: 'error' };

/** 2000-01-01 in unix seconds; Go encodes an unknown reset as 0 or year 1. */
const MIN_RESET_UNIX_S = 946_684_800;
const MALFORMED_MESSAGE = 'Malformed quota history response';

const isFiniteNumber = (value: unknown): value is number =>
  typeof value === 'number' && Number.isFinite(value);

const isQuotaHistoryKind = (value: unknown): value is QuotaHistoryKind =>
  value === 'five_hour' || value === 'weekly' || value === 'weekly_scoped';

const isPresent = <T>(value: T | null): value is T => value !== null;

const normalizePoint = (raw: unknown): QuotaHistoryPoint | null => {
  if (!Array.isArray(raw)) return null;

  const [t, used, reset]: unknown[] = raw;

  if (!isFiniteNumber(t) || !isFiniteNumber(used) || !isFiniteNumber(reset)) return null;

  return {
    resetAt: reset > MIN_RESET_UNIX_S ? reset * 1000 : null,
    t: t * 1000,
    used: Math.min(1, Math.max(0, used)),
  };
};

const normalizePoints = (raw: unknown[]): QuotaHistoryPoint[] => {
  const points = raw
    .map(normalizePoint)
    .filter(isPresent)
    .sort((left, right) => left.t - right.t);

  return points.filter((point, index) => index === 0 || point.t !== points[index - 1].t);
};

const normalizeWindow = (raw: unknown): QuotaHistoryWindow | null => {
  if (!isRecord(raw) || !isQuotaHistoryKind(raw.kind) || !Array.isArray(raw.points)) return null;

  const { kind, period_seconds: period, points, scope } = raw;

  return {
    kind,
    periodMs: isFiniteNumber(period) && period > 0 ? period * 1000 : null,
    points: normalizePoints(points),
    scope: typeof scope === 'string' ? scope : '',
  };
};

const normalizeCredential = (raw: unknown): QuotaHistoryCredential | null => {
  if (!isRecord(raw) || !Array.isArray(raw.windows)) return null;

  const { auth_index: authIndex, id, name, provider, windows } = raw;

  if (typeof id !== 'string' || !id || typeof name !== 'string') return null;

  return {
    authIndex: authIndex == null || authIndex === '' ? null : String(authIndex),
    id,
    name,
    provider: typeof provider === 'string' ? provider : '',
    windows: windows.map(normalizeWindow).filter(isPresent),
  };
};

const parseTime = (value: unknown): number => (typeof value === 'string' ? Date.parse(value) : NaN);

/** Pure. `enabled:false` -> disabled. Drops malformed credentials/windows/points. */
export const normalizeQuotaHistory = (raw: unknown): QuotaHistoryResult => {
  if (!isRecord(raw)) return { message: MALFORMED_MESSAGE, status: 'error' };
  if (raw.enabled === false) return { status: 'disabled' };

  const { credentials, retention_hours: retention } = raw;
  const serverNow = parseTime(raw.now);
  const since = parseTime(raw.since);

  if (!Number.isFinite(serverNow) || !Number.isFinite(since) || !Array.isArray(credentials)) {
    return { message: MALFORMED_MESSAGE, status: 'error' };
  }

  return {
    credentials: credentials.map(normalizeCredential).filter(isPresent),
    retentionHours:
      isFiniteNumber(retention) && retention > 0 ? retention : HISTORY_RETENTION_HOURS_DEFAULT,
    serverNow,
    since,
    status: 'ok',
  };
};

const getErrorStatus = (error: unknown): unknown => (isRecord(error) ? error.status : undefined);

export const quotaHistoryApi = {
  get: async ({ sinceMs }: { sinceMs: number }): Promise<QuotaHistoryResult> => {
    try {
      const raw = await apiClient.get<unknown>('/credentials/quota-history', {
        params: { since: Math.floor(sinceMs / 1000) },
      });

      return normalizeQuotaHistory(raw);
    } catch (error) {
      if (getErrorStatus(error) === 404) return { status: 'unsupported' };

      return { message: getErrorMessage(error, 'Request failed'), status: 'error' };
    }
  },
};
