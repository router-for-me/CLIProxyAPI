/**
 * xAI 额度数据层：免费档账单 + 付费档健康探测回退。
 * React-free / SCSS-free —— 由 tests/xaiPaidQuotaFallback.test.ts 直接消费。
 */

import type { TFunction } from 'i18next';
import type { AuthFileItem, XaiBillingSummary, XaiQuotaState } from '@/types';
import { apiCallApi, getApiCallErrorMessage } from '@/services/api';
import {
  XAI_API_CHAT_URL,
  XAI_API_ME_URL,
  XAI_API_REQUEST_HEADERS,
  XAI_BILLING_MONTHLY_URL,
  XAI_BILLING_WEEKLY_URL,
  XAI_PAID_HEALTH_MODEL,
  XAI_REQUEST_HEADERS,
  normalizeStringValue,
  parseXaiBillingPayload,
  buildXaiBillingSummary,
  buildXaiPaidHealthSummary,
  mergeXaiBillingSummaries,
  createStatusError,
  isDisabledAuthFile,
  isPaidXaiAuthFile,
  isXaiFile,
} from '@/utils/quota';
import { normalizeAuthIndex } from '@/utils/authIndex';
import type { QuotaProviderData } from '../types';

const XAI_PAID_HEALTH_REQUEST_TIMEOUT_MS = 15000;

const toXaiRecord = (value: unknown): Record<string, unknown> | null => {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
};

const resolveXaiUserId = (file: AuthFileItem): string | null => {
  const metadata = toXaiRecord(file.metadata);
  const attributes = toXaiRecord(file.attributes);
  const oauth = toXaiRecord(file.oauth ?? metadata?.oauth ?? attributes?.oauth);
  const user = toXaiRecord(file.user ?? metadata?.user ?? attributes?.user);

  const candidates = [
    file.sub,
    file.subject,
    file.user_id,
    file.userId,
    metadata?.sub,
    metadata?.subject,
    metadata?.user_id,
    metadata?.userId,
    attributes?.sub,
    attributes?.subject,
    attributes?.user_id,
    attributes?.userId,
    oauth?.sub,
    oauth?.subject,
    user?.sub,
    user?.id,
  ];

  for (const candidate of candidates) {
    const userId = normalizeStringValue(candidate);
    if (userId) return userId;
  }

  return null;
};

const buildXaiRequestHeaders = (file: AuthFileItem): Record<string, string> => {
  const headers: Record<string, string> = { ...XAI_REQUEST_HEADERS };
  const userId = resolveXaiUserId(file);
  if (userId) {
    headers['x-userid'] = userId;
  }
  return headers;
};

const requestXaiBilling = async (
  authIndex: string,
  url: string,
  header: Record<string, string>
): Promise<XaiBillingSummary | null> => {
  const result = await apiCallApi.request({
    authIndex,
    method: 'GET',
    url,
    header,
  });

  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw createStatusError(getApiCallErrorMessage(result), result.statusCode);
  }

  const payload = parseXaiBillingPayload(result.body ?? result.bodyText);
  return buildXaiBillingSummary(payload?.config);
};

const requestXaiPaidHealth = async (authIndex: string): Promise<XaiBillingSummary> => {
  const [profileRequest, chatRequest] = await Promise.allSettled([
    apiCallApi.request(
      {
        authIndex,
        method: 'GET',
        url: XAI_API_ME_URL,
        header: XAI_API_REQUEST_HEADERS,
      },
      { timeout: XAI_PAID_HEALTH_REQUEST_TIMEOUT_MS }
    ),
    apiCallApi.request(
      {
        authIndex,
        method: 'POST',
        url: XAI_API_CHAT_URL,
        header: {
          ...XAI_API_REQUEST_HEADERS,
          'Content-Type': 'application/json',
        },
        data: JSON.stringify({
          model: XAI_PAID_HEALTH_MODEL,
          messages: [{ role: 'user', content: 'ping' }],
          max_tokens: 1,
          stream: false,
        }),
      },
      { timeout: XAI_PAID_HEALTH_REQUEST_TIMEOUT_MS }
    ),
  ]);

  if (chatRequest.status === 'rejected') throw chatRequest.reason;
  if (chatRequest.value.statusCode < 200 || chatRequest.value.statusCode >= 300) {
    throw createStatusError(
      getApiCallErrorMessage(chatRequest.value),
      chatRequest.value.statusCode
    );
  }

  const profile =
    profileRequest.status === 'fulfilled' &&
    profileRequest.value.statusCode >= 200 &&
    profileRequest.value.statusCode < 300
      ? profileRequest.value.body
      : null;
  return buildXaiPaidHealthSummary(profile);
};

const fetchXaiQuota = async (file: AuthFileItem, t: TFunction): Promise<XaiBillingSummary> => {
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndex = normalizeAuthIndex(rawAuthIndex);
  if (!authIndex) {
    throw new Error(t('xai_quota.missing_auth_index'));
  }

  if (isPaidXaiAuthFile(file)) {
    return requestXaiPaidHealth(authIndex);
  }

  const requestHeader = buildXaiRequestHeaders(file);
  const [weeklyResult, monthlyResult] = await Promise.allSettled([
    requestXaiBilling(authIndex, XAI_BILLING_WEEKLY_URL, requestHeader),
    requestXaiBilling(authIndex, XAI_BILLING_MONTHLY_URL, requestHeader),
  ]);
  const weeklySummary = weeklyResult.status === 'fulfilled' ? weeklyResult.value : null;
  const monthlySummary = monthlyResult.status === 'fulfilled' ? monthlyResult.value : null;
  const summary = mergeXaiBillingSummaries(weeklySummary, monthlySummary);
  if (summary) return summary;

  const billingError =
    weeklyResult.status === 'rejected' && monthlyResult.status === 'rejected'
      ? weeklyResult.reason
      : new Error(t('xai_quota.empty_data'));

  try {
    return await requestXaiPaidHealth(authIndex);
  } catch {
    // Preserve the original free billing error when neither account mode can be queried.
    throw billingError;
  }
};

export const XAI_CONFIG: QuotaProviderData<XaiQuotaState, XaiBillingSummary> = {
  type: 'xai',
  i18nPrefix: 'xai_quota',
  filterFn: (file) => isXaiFile(file) && !isDisabledAuthFile(file),
  fetchQuota: fetchXaiQuota,
  storeSelector: (state) => state.xaiQuota,
  storeSetter: 'setXaiQuota',
  buildLoadingState: () => ({ status: 'loading', billing: null }),
  buildSuccessState: (billing) => ({ status: 'success', billing }),
  buildErrorState: (message, status) => ({
    status: 'error',
    billing: null,
    error: message,
    errorStatus: status,
  }),
};
