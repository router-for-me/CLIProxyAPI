import type { AuthFileItem, KiroQuotaRow, KiroQuotaState } from '@/types';
import { apiCallApi, getApiCallErrorMessage } from '@/services/api';
import { isRecord } from '@/utils/helpers';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { createStatusError, isDisabledAuthFile, resolveAuthProvider } from '@/utils/quota';
import { parseTimestampMs } from '@/utils/timestamp';
import type { QuotaProviderData } from '../types';

const finiteNumber = (value: unknown): number | null => {
  if (typeof value !== 'number' && typeof value !== 'string') return null;
  if (typeof value === 'string' && !value.trim()) return null;
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : null;
};

const instant = (value: unknown): number | null => {
  const number = finiteNumber(value);
  if (number !== null && number > 0) return number < 1e12 ? number * 1000 : number;
  const parsed = parseTimestampMs(value);
  return Number.isFinite(parsed) ? parsed : null;
};

export function parseKiroQuota(
  payload: unknown,
  nowMs = Date.now()
): Pick<KiroQuotaState, 'rows' | 'plan'> {
  if (typeof payload === 'string') {
    try {
      payload = JSON.parse(payload) as unknown;
    } catch {
      throw new Error('Invalid Kiro quota response');
    }
  }
  if (!isRecord(payload) || !Array.isArray(payload.usageBreakdownList)) {
    throw new Error('Invalid Kiro quota response');
  }
  const rows: KiroQuotaRow[] = [];
  const addRow = (
    data: Record<string, unknown>,
    id: string,
    labelKey: string,
    resetAtMs: number | null,
    periodHours: number | null
  ) => {
    const used = finiteNumber(data.currentUsageWithPrecision) ?? finiteNumber(data.currentUsage);
    const limit = finiteNumber(data.usageLimitWithPrecision) ?? finiteNumber(data.usageLimit);
    if (used === null || limit === null || (resetAtMs !== null && resetAtMs <= nowMs)) return;
    rows.push({ id, labelKey, used, limit, resetAtMs, periodHours });
  };
  const monthlyReset = instant(payload.nextDateReset);
  payload.usageBreakdownList.forEach((item: unknown, index: number) => {
    if (!isRecord(item)) return;
    const id = `${String(item.resourceType ?? 'credits')}-${index}`;
    addRow(item, id, 'kiro_quota.monthly_credits', monthlyReset, 24 * 30);
    if (isRecord(item.freeTrialInfo) && item.freeTrialInfo.freeTrialStatus === 'ACTIVE') {
      addRow(
        item.freeTrialInfo,
        `${id}-trial`,
        'kiro_quota.trial_credits',
        instant(item.freeTrialInfo.freeTrialExpiry),
        null
      );
    }
    if (Array.isArray(item.bonuses)) {
      item.bonuses.forEach((bonus: unknown, bonusIndex: number) => {
        if (!isRecord(bonus) || bonus.status !== 'ACTIVE') return;
        addRow(
          bonus,
          `${id}-bonus-${bonusIndex}`,
          'kiro_quota.bonus_credits',
          instant(bonus.expiresAt),
          null
        );
      });
    }
  });
  const plan =
    isRecord(payload.subscriptionInfo) &&
    typeof payload.subscriptionInfo.subscriptionTitle === 'string'
      ? payload.subscriptionInfo.subscriptionTitle
      : undefined;
  return { rows, plan };
}

export const KIRO_CONFIG: QuotaProviderData<
  KiroQuotaState,
  Pick<KiroQuotaState, 'rows' | 'plan'>
> = {
  type: 'kiro',
  i18nPrefix: 'kiro_quota',
  filterFn: (file: AuthFileItem) =>
    resolveAuthProvider(file) === 'kiro' && !isDisabledAuthFile(file),
  fetchQuota: async (file, t) => {
    const authIndex = normalizeAuthIndex(file.auth_index ?? file.authIndex);
    if (!authIndex) throw new Error(t('kiro_quota.missing_auth_index'));
    const result = await apiCallApi.request({
      authIndex,
      method: 'GET',
      url: 'https://management.us-east-1.kiro.dev/getUsageLimits?origin=AI_EDITOR&resourceType=AGENTIC_REQUEST&isEmailRequired=true',
      header: { Authorization: 'Bearer $TOKEN$', Accept: 'application/json' },
    });
    if (result.statusCode < 200 || result.statusCode >= 300) {
      throw createStatusError(getApiCallErrorMessage(result), result.statusCode);
    }
    try {
      return parseKiroQuota(result.body ?? result.bodyText);
    } catch {
      throw new Error(t('kiro_quota.empty_data'));
    }
  },
  storeSelector: (state) => state.kiroQuota,
  storeSetter: 'setKiroQuota',
  buildLoadingState: () => ({ status: 'loading', rows: [] }),
  buildSuccessState: (data) => ({ status: 'success', ...data }),
  buildErrorState: (message, status) => ({
    status: 'error',
    rows: [],
    error: message,
    errorStatus: status,
  }),
};
