import type { AuthFileItem, QoderQuotaRow, QoderQuotaState } from '@/types';
import { apiCallApi, getApiCallErrorMessage } from '@/services/api';
import { isRecord } from '@/utils/helpers';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { createStatusError, isDisabledAuthFile, resolveAuthProvider } from '@/utils/quota';
import { parseTimestampMs } from '@/utils/timestamp';
import type { QuotaProviderData } from '../types';

type QoderQuotaData = Pick<QoderQuotaState, 'rows' | 'plan'>;

// The backend pins this host to the credential's own region (qoder.sh or qoder.com.cn).
const QODER_OPENAPI_BASE = 'https://openapi.qoder.sh';
const QODER_HEADERS = { Authorization: 'Bearer $TOKEN$', Accept: 'application/json' };

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

const field = (record: Record<string, unknown>, ...keys: string[]): unknown =>
  keys.map((key) => record[key]).find((value) => value !== undefined && value !== null);

/** Mirrors the official CLI's /api/v2/quota/usage normalizer (camelCase or snake_case). */
export function parseQoderQuota(payload: unknown, nowMs = Date.now()): QoderQuotaData {
  if (typeof payload === 'string') {
    try {
      payload = JSON.parse(payload) as unknown;
    } catch {
      throw new Error('Invalid Qoder quota response');
    }
  }
  if (isRecord(payload) && isRecord(payload.data)) payload = payload.data;
  if (!isRecord(payload)) throw new Error('Invalid Qoder quota response');

  const resetAtMs = instant(field(payload, 'expiresAt', 'expires_at'));
  const cycleReset = resetAtMs !== null && resetAtMs > nowMs ? resetAtMs : null;
  const rows: QoderQuotaRow[] = [];
  const addRow = (
    raw: unknown,
    id: string,
    labelKey: string,
    totalKeys: string[],
    rowReset: number | null
  ) => {
    if (!isRecord(raw)) return;
    if (field(raw, 'available') === false) return;
    const limit = finiteNumber(field(raw, ...totalKeys));
    const used = finiteNumber(field(raw, 'used'));
    if (limit === null || used === null || limit <= 0) return;
    rows.push({ id, labelKey, used, limit, resetAtMs: rowReset, periodHours: null });
  };
  addRow(field(payload, 'userQuota', 'user_quota'), 'plan', 'qoder_quota.plan_credits', ['total'], cycleReset);
  addRow(field(payload, 'addOnQuota', 'add_on_quota'), 'add-on', 'qoder_quota.add_on_credits', ['total'], null);
  addRow(
    field(payload, 'orgResourcePackage', 'org_resource_package', 'sharedQuota', 'shared_quota'),
    'org',
    'qoder_quota.org_credits',
    ['cap', 'total'],
    null
  );
  const hasBucket = ['userQuota', 'user_quota', 'addOnQuota', 'add_on_quota'].some((key) =>
    isRecord(payload[key])
  );
  if (!hasBucket && rows.length === 0) throw new Error('Invalid Qoder quota response');
  return { rows };
}

export function parseQoderPlan(payload: unknown): string | undefined {
  if (typeof payload === 'string') {
    try {
      payload = JSON.parse(payload) as unknown;
    } catch {
      return undefined;
    }
  }
  if (isRecord(payload) && isRecord(payload.data)) payload = payload.data;
  if (!isRecord(payload)) return undefined;
  const plan = field(payload, 'plan_tier_name', 'planTierName', 'plan_name', 'planName');
  return typeof plan === 'string' && plan.trim() ? plan.trim() : undefined;
}

const requestQoder = async (file: AuthFileItem, path: string, missingIndex: string) => {
  const authIndex = normalizeAuthIndex(file.auth_index ?? file.authIndex);
  if (!authIndex) throw new Error(missingIndex);
  const result = await apiCallApi.request({
    authIndex,
    method: 'GET',
    url: `${QODER_OPENAPI_BASE}${path}`,
    header: { ...QODER_HEADERS },
  });
  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw createStatusError(getApiCallErrorMessage(result), result.statusCode);
  }
  return result.body ?? result.bodyText;
};

export const QODER_CONFIG: QuotaProviderData<QoderQuotaState, QoderQuotaData> = {
  type: 'qoder',
  i18nPrefix: 'qoder_quota',
  filterFn: (file: AuthFileItem) =>
    resolveAuthProvider(file) === 'qoder' && !isDisabledAuthFile(file),
  fetchQuota: async (file, t) => {
    const body = await requestQoder(file, '/api/v2/quota/usage', t('qoder_quota.missing_auth_index'));
    try {
      return parseQoderQuota(body);
    } catch {
      throw new Error(t('qoder_quota.empty_data'));
    }
  },
  // The plan name is optional; quota rows render first and the plan fills in afterwards.
  enrichQuota: async (file, data, t) => {
    const plan = parseQoderPlan(
      await requestQoder(file, '/api/v2/user/plan', t('qoder_quota.missing_auth_index'))
    );
    return plan ? { ...data, plan } : data;
  },
  storeSelector: (state) => state.qoderQuota,
  storeSetter: 'setQoderQuota',
  buildLoadingState: () => ({ status: 'loading', rows: [] }),
  buildSuccessState: (data) => ({ status: 'success', ...data }),
  buildErrorState: (message, status) => ({
    status: 'error',
    rows: [],
    error: message,
    errorStatus: status,
  }),
};
