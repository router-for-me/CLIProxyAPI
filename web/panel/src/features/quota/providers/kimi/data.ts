/**
 * Kimi 额度数据层。React-free / SCSS-free。
 */

import type { TFunction } from 'i18next';
import type { AuthFileItem, KimiQuotaState } from '@/types';
import { apiCallApi, getApiCallErrorMessage } from '@/services/api';
import { fetchKimiPlanName, resolveKimiQuotaUrl } from '@/services/api/kimiQuota';
import { captureQuotaCacheGeneration, commitIfQuotaCacheCurrent } from '@/stores/useQuotaStore';
import {
  KIMI_REQUEST_HEADERS,
  parseKimiUsagePayload,
  buildKimiQuotaRows,
  createStatusError,
  isKimiFile,
  isDisabledAuthFile,
} from '@/utils/quota';
import { normalizeAuthIndex } from '@/utils/authIndex';
import type { QuotaProviderData } from '../types';

type KimiQuotaData = Pick<KimiQuotaState, 'rows' | 'planName'>;

const fetchKimiQuota = async (file: AuthFileItem, t: TFunction): Promise<KimiQuotaData> => {
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndex = normalizeAuthIndex(rawAuthIndex);
  if (!authIndex) {
    throw new Error(t('kimi_quota.missing_auth_index'));
  }

  const generation = captureQuotaCacheGeneration(file.name);
  const assertCurrent = () => {
    if (!commitIfQuotaCacheCurrent(generation, () => {})) {
      throw new Error(t('kimi_quota.stale_request'));
    }
  };
  let url: string;
  try {
    url = await resolveKimiQuotaUrl(file);
  } catch {
    assertCurrent();
    throw new Error(t('kimi_quota.domain_unavailable'));
  }
  assertCurrent();

  const result = await apiCallApi.request({
    authIndex,
    method: 'GET',
    url,
    header: { ...KIMI_REQUEST_HEADERS },
  });
  assertCurrent();

  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw createStatusError(getApiCallErrorMessage(result), result.statusCode);
  }

  const payload = parseKimiUsagePayload(result.body ?? result.bodyText);
  if (!payload) {
    throw new Error(t('kimi_quota.empty_data'));
  }

  const planName = await fetchKimiPlanName(authIndex, url);
  // Profile failures are optional, but session/file invalidation must still reject the result.
  assertCurrent();
  return { rows: buildKimiQuotaRows(payload), planName };
};

export const KIMI_CONFIG: QuotaProviderData<KimiQuotaState, KimiQuotaData> = {
  type: 'kimi',
  i18nPrefix: 'kimi_quota',
  filterFn: (file) => isKimiFile(file) && !isDisabledAuthFile(file),
  fetchQuota: fetchKimiQuota,
  storeSelector: (state) => state.kimiQuota,
  storeSetter: 'setKimiQuota',
  buildLoadingState: () => ({ status: 'loading', rows: [] }),
  buildSuccessState: (data) => ({ status: 'success', ...data }),
  buildErrorState: (message, status) => ({
    status: 'error',
    rows: [],
    error: message,
    errorStatus: status,
  }),
};
