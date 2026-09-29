/**
 * Codex 额度数据层：用量窗口 + 套餐 + 重置积分（含消费流程）。
 * React-free / SCSS-free —— 由 tests/codexQuota.test.ts 直接消费。
 */

import type { TFunction } from 'i18next';
import type {
  AuthFileItem,
  CodexRateLimitInfo,
  CodexRateLimitResetCredit,
  CodexQuotaState,
  CodexUsageWindow,
  CodexQuotaWindow,
  CodexUsagePayload,
} from '@/types';
import { apiCallApi, getApiCallErrorMessage } from '@/services/api';
import {
  CODEX_RATE_LIMIT_RESET_CREDITS_URL,
  CODEX_RATE_LIMIT_RESET_CREDITS_CONSUME_URL,
  CODEX_SUBSCRIPTION_URL,
  CODEX_USAGE_URL,
  CODEX_REQUEST_HEADERS,
  normalizeNumberValue,
  normalizePlanType,
  normalizeStringValue,
  normalizeCodexResetCreditsPayload,
  parseCodexUsagePayload,
  parseOffsetSecondsToMs,
  periodHoursFromSeconds,
  resolveResetMs,
  resolveCodexChatgptAccountId,
  resolveCodexPlanType,
  resolveCodexSubscriptionActiveUntil,
  formatCodexResetLabel,
  createStatusError,
  isCodexFile,
  isDisabledAuthFile,
} from '@/utils/quota';
import { normalizeAuthIndex } from '@/utils/authIndex';
import type { QuotaProviderData } from '../types';

const CODEX_OPTIONAL_REQUEST_TIMEOUT_MS = 8000;

type CodexResetCreditsData = {
  availableCount: number | null;
  applicableAvailableCount: number | null;
  credits: CodexRateLimitResetCredit[];
  error: string;
};

export type CodexQuotaData = {
  planType: string | null;
  subscriptionActiveUntil: string | number | null;
  rateLimitResetCreditsAvailableCount: number | null;
  rateLimitResetCreditsApplicableAvailableCount: number | null;
  rateLimitResetCredits: CodexRateLimitResetCredit[];
  rateLimitResetCreditsError: string;
  windows: CodexQuotaWindow[];
};

export const buildCodexQuotaWindows = (
  payload: CodexUsagePayload,
  t: TFunction
): CodexQuotaWindow[] => {
  const FIVE_HOUR_SECONDS = 18000;
  const WEEK_SECONDS = 604800;
  const MIN_MONTH_SECONDS = 28 * 24 * 60 * 60;
  const MAX_MONTH_SECONDS = 31 * 24 * 60 * 60;
  const WINDOW_META = {
    codeFiveHour: { id: 'five-hour', labelKey: 'codex_quota.primary_window' },
    codeWeekly: { id: 'weekly', labelKey: 'codex_quota.secondary_window' },
    codeMonthly: { id: 'monthly', labelKey: 'codex_quota.team_secondary_window' },
    codeReviewFiveHour: {
      id: 'code-review-five-hour',
      labelKey: 'codex_quota.code_review_primary_window',
    },
    codeReviewWeekly: {
      id: 'code-review-weekly',
      labelKey: 'codex_quota.code_review_secondary_window',
    },
    codeReviewMonthly: {
      id: 'code-review-monthly',
      labelKey: 'codex_quota.code_review_team_secondary_window',
    },
  } as const;

  const rateLimit = payload.rate_limit ?? payload.rateLimit ?? undefined;
  const codeReviewLimit =
    payload.code_review_rate_limit ?? payload.codeReviewRateLimit ?? undefined;
  const additionalRateLimits = payload.additional_rate_limits ?? payload.additionalRateLimits ?? [];
  const windows: CodexQuotaWindow[] = [];

  const addWindow = (
    id: string,
    label: string,
    labelKey: string | undefined,
    labelParams: Record<string, string | number> | undefined,
    window?: CodexUsageWindow | null,
    limitReached?: boolean,
    allowed?: boolean
  ) => {
    if (!window) return;
    const resetLabel = formatCodexResetLabel(window);
    const usedPercentRaw = normalizeNumberValue(window.used_percent ?? window.usedPercent);
    const isLimitReached = Boolean(limitReached) || allowed === false;
    const usedPercent = usedPercentRaw ?? (isLimitReached && resetLabel !== '-' ? 100 : null);
    // Keep the raw instant beside the label — see utils/quota/resetInstants.
    const resetAtMs =
      resolveResetMs([window.reset_at, window.resetAt]) ??
      parseOffsetSecondsToMs(window.reset_after_seconds ?? window.resetAfterSeconds, Date.now());
    const periodHours = periodHoursFromSeconds(
      window.limit_window_seconds ?? window.limitWindowSeconds
    );
    windows.push({
      id,
      label,
      labelKey,
      labelParams,
      usedPercent,
      resetAtMs,
      periodHours,
      resetLabel,
    });
  };

  const getWindowSeconds = (window?: CodexUsageWindow | null): number | null => {
    if (!window) return null;
    return normalizeNumberValue(window.limit_window_seconds ?? window.limitWindowSeconds);
  };

  const isMonthlyWindow = (window?: CodexUsageWindow | null): boolean => {
    const seconds = getWindowSeconds(window);
    return seconds !== null && seconds >= MIN_MONTH_SECONDS && seconds <= MAX_MONTH_SECONDS;
  };

  const selectSecondaryWindowMeta = <
    TWeekly extends { id: string; labelKey: string },
    TMonthly extends { id: string; labelKey: string },
  >(
    window: CodexUsageWindow | null | undefined,
    weeklyMeta: TWeekly,
    monthlyMeta: TMonthly
  ): TWeekly | TMonthly => (isMonthlyWindow(window) ? monthlyMeta : weeklyMeta);

  const rawLimitReached = rateLimit?.limit_reached ?? rateLimit?.limitReached;
  const rawAllowed = rateLimit?.allowed;

  const pickClassifiedWindows = (
    limitInfo?: CodexRateLimitInfo | null,
    options?: { allowOrderFallback?: boolean }
  ): { fiveHourWindow: CodexUsageWindow | null; weeklyWindow: CodexUsageWindow | null } => {
    const allowOrderFallback = options?.allowOrderFallback ?? true;
    const primaryWindow = limitInfo?.primary_window ?? limitInfo?.primaryWindow ?? null;
    const secondaryWindow = limitInfo?.secondary_window ?? limitInfo?.secondaryWindow ?? null;
    const rawWindows = [primaryWindow, secondaryWindow];

    let fiveHourWindow: CodexUsageWindow | null = null;
    let weeklyWindow: CodexUsageWindow | null = null;

    for (const window of rawWindows) {
      if (!window) continue;
      const seconds = getWindowSeconds(window);
      if (seconds === FIVE_HOUR_SECONDS && !fiveHourWindow) {
        fiveHourWindow = window;
      } else if ((seconds === WEEK_SECONDS || isMonthlyWindow(window)) && !weeklyWindow) {
        weeklyWindow = window;
      }
    }

    // For legacy payloads without window duration, fallback to primary/secondary ordering.
    if (allowOrderFallback) {
      if (!fiveHourWindow) {
        fiveHourWindow = primaryWindow && primaryWindow !== weeklyWindow ? primaryWindow : null;
      }
      if (!weeklyWindow) {
        weeklyWindow =
          secondaryWindow && secondaryWindow !== fiveHourWindow ? secondaryWindow : null;
      }
    }

    return { fiveHourWindow, weeklyWindow };
  };

  const rateWindows = pickClassifiedWindows(rateLimit);
  addWindow(
    WINDOW_META.codeFiveHour.id,
    t(WINDOW_META.codeFiveHour.labelKey),
    WINDOW_META.codeFiveHour.labelKey,
    undefined,
    rateWindows.fiveHourWindow,
    rawLimitReached,
    rawAllowed
  );
  const codeSecondaryWindowMeta = selectSecondaryWindowMeta(
    rateWindows.weeklyWindow,
    WINDOW_META.codeWeekly,
    WINDOW_META.codeMonthly
  );
  addWindow(
    codeSecondaryWindowMeta.id,
    t(codeSecondaryWindowMeta.labelKey),
    codeSecondaryWindowMeta.labelKey,
    undefined,
    rateWindows.weeklyWindow,
    rawLimitReached,
    rawAllowed
  );

  const codeReviewWindows = pickClassifiedWindows(codeReviewLimit);
  const codeReviewLimitReached = codeReviewLimit?.limit_reached ?? codeReviewLimit?.limitReached;
  const codeReviewAllowed = codeReviewLimit?.allowed;
  addWindow(
    WINDOW_META.codeReviewFiveHour.id,
    t(WINDOW_META.codeReviewFiveHour.labelKey),
    WINDOW_META.codeReviewFiveHour.labelKey,
    undefined,
    codeReviewWindows.fiveHourWindow,
    codeReviewLimitReached,
    codeReviewAllowed
  );
  const codeReviewSecondaryWindowMeta = selectSecondaryWindowMeta(
    codeReviewWindows.weeklyWindow,
    WINDOW_META.codeReviewWeekly,
    WINDOW_META.codeReviewMonthly
  );
  addWindow(
    codeReviewSecondaryWindowMeta.id,
    t(codeReviewSecondaryWindowMeta.labelKey),
    codeReviewSecondaryWindowMeta.labelKey,
    undefined,
    codeReviewWindows.weeklyWindow,
    codeReviewLimitReached,
    codeReviewAllowed
  );

  const normalizeWindowId = (raw: string) =>
    raw
      .trim()
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '');

  if (Array.isArray(additionalRateLimits)) {
    additionalRateLimits.forEach((limitItem, index) => {
      const rateInfo = limitItem?.rate_limit ?? limitItem?.rateLimit ?? null;
      if (!rateInfo) return;

      const limitName =
        normalizeStringValue(limitItem?.limit_name ?? limitItem?.limitName) ??
        normalizeStringValue(limitItem?.metered_feature ?? limitItem?.meteredFeature) ??
        `additional-${index + 1}`;

      const idPrefix = normalizeWindowId(limitName) || `additional-${index + 1}`;
      const additionalWindows = pickClassifiedWindows(rateInfo);
      const additionalLimitReached = rateInfo.limit_reached ?? rateInfo.limitReached;
      const additionalAllowed = rateInfo.allowed;

      addWindow(
        `${idPrefix}-five-hour-${index}`,
        t('codex_quota.additional_primary_window', { name: limitName }),
        'codex_quota.additional_primary_window',
        { name: limitName },
        additionalWindows.fiveHourWindow,
        additionalLimitReached,
        additionalAllowed
      );
      const additionalSecondaryMeta = selectSecondaryWindowMeta(
        additionalWindows.weeklyWindow,
        { id: 'weekly', labelKey: 'codex_quota.additional_secondary_window' },
        { id: 'monthly', labelKey: 'codex_quota.additional_team_secondary_window' }
      );
      addWindow(
        `${idPrefix}-${additionalSecondaryMeta.id}-${index}`,
        t(additionalSecondaryMeta.labelKey, { name: limitName }),
        additionalSecondaryMeta.labelKey,
        { name: limitName },
        additionalWindows.weeklyWindow,
        additionalLimitReached,
        additionalAllowed
      );
    });
  }

  return windows;
};

const buildCodexRequestHeader = (file: AuthFileItem): Record<string, string> => {
  const accountId = resolveCodexChatgptAccountId(file);
  const requestHeader: Record<string, string> = {
    ...CODEX_REQUEST_HEADERS,
  };
  if (accountId) {
    requestHeader['Chatgpt-Account-Id'] = accountId;
  }
  return requestHeader;
};

const parseCodexSubscriptionActiveUntil = (payload: unknown): string | number | null => {
  if (typeof payload === 'string') {
    const trimmed = payload.trim();
    if (!trimmed) return null;
    try {
      return parseCodexSubscriptionActiveUntil(JSON.parse(trimmed));
    } catch {
      return null;
    }
  }

  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return null;
  const subscription = payload as Record<string, unknown>;
  const value = subscription.active_until ?? subscription.activeUntil;
  const numberValue = normalizeNumberValue(value);
  if (numberValue !== null && numberValue !== 0) return numberValue;
  const stringValue = normalizeStringValue(value);
  return stringValue && stringValue !== '0' ? stringValue : null;
};

const fetchCodexSubscriptionActiveUntil = async (
  authIndex: string,
  accountId: string | null,
  requestHeader: Record<string, string>
): Promise<string | number | null> => {
  if (!accountId) return null;

  try {
    const result = await apiCallApi.request(
      {
        authIndex,
        method: 'GET',
        url: `${CODEX_SUBSCRIPTION_URL}?account_id=${encodeURIComponent(accountId)}`,
        header: requestHeader,
      },
      { timeout: CODEX_OPTIONAL_REQUEST_TIMEOUT_MS }
    );
    if (result.statusCode < 200 || result.statusCode >= 300) return null;
    return parseCodexSubscriptionActiveUntil(result.body ?? result.bodyText);
  } catch {
    return null;
  }
};

const fetchCodexResetCredits = async (
  authIndex: string,
  requestHeader: Record<string, string>,
  t: TFunction
): Promise<CodexResetCreditsData> => {
  try {
    const result = await apiCallApi.request(
      {
        authIndex,
        method: 'GET',
        url: CODEX_RATE_LIMIT_RESET_CREDITS_URL,
        header: {
          ...requestHeader,
          Accept: 'application/json',
          'OpenAI-Beta': 'codex-1',
          Originator: 'Codex Desktop',
        },
      },
      { timeout: CODEX_OPTIONAL_REQUEST_TIMEOUT_MS }
    );

    if (result.statusCode < 200 || result.statusCode >= 300) {
      return {
        availableCount: null,
        applicableAvailableCount: null,
        credits: [],
        error: getApiCallErrorMessage(result),
      };
    }

    const summary = normalizeCodexResetCreditsPayload(result.body ?? result.bodyText);
    if (summary.invalidPayload) {
      return {
        availableCount: null,
        applicableAvailableCount: null,
        credits: [],
        error: t('codex_quota.reset_credits_invalid_payload'),
      };
    }

    return {
      availableCount: summary.availableCount,
      applicableAvailableCount: summary.applicableAvailableCount,
      credits: summary.credits,
      error: '',
    };
  } catch (err: unknown) {
    return {
      availableCount: null,
      applicableAvailableCount: null,
      credits: [],
      error: err instanceof Error ? err.message : t('common.unknown_error'),
    };
  }
};

const fetchCodexQuota = async (file: AuthFileItem, t: TFunction): Promise<CodexQuotaData> => {
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndex = normalizeAuthIndex(rawAuthIndex);
  if (!authIndex) {
    throw new Error(t('codex_quota.missing_auth_index'));
  }

  const planTypeFromFile = resolveCodexPlanType(file);
  const subscriptionActiveUntilFromFile = resolveCodexSubscriptionActiveUntil(file);
  const accountId = resolveCodexChatgptAccountId(file);
  const requestHeader = buildCodexRequestHeader(file);

  const [result, liveSubscriptionActiveUntil] = await Promise.all([
    apiCallApi.request({
      authIndex,
      method: 'GET',
      url: CODEX_USAGE_URL,
      header: requestHeader,
    }),
    fetchCodexSubscriptionActiveUntil(authIndex, accountId, requestHeader),
  ]);

  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw createStatusError(getApiCallErrorMessage(result), result.statusCode);
  }

  const payload = parseCodexUsagePayload(result.body ?? result.bodyText);
  if (!payload) {
    throw new Error(t('codex_quota.empty_windows'));
  }

  const planTypeFromUsage = normalizePlanType(payload.plan_type ?? payload.planType);
  const resetCredits = payload.rate_limit_reset_credits ?? payload.rateLimitResetCredits ?? null;
  const usageResetCreditsData = normalizeCodexResetCreditsPayload(resetCredits);
  const resetCreditsData = await fetchCodexResetCredits(authIndex, requestHeader, t);
  const resetCreditsCountFromDetails =
    resetCreditsData.credits.length > 0 ? resetCreditsData.credits.length : null;
  const rateLimitResetCreditsAvailableCount =
    resetCreditsData.availableCount ??
    resetCreditsCountFromDetails ??
    usageResetCreditsData.availableCount;
  const rateLimitResetCreditsApplicableAvailableCount =
    usageResetCreditsData.applicableAvailableCount ??
    resetCreditsData.applicableAvailableCount ??
    rateLimitResetCreditsAvailableCount;
  const planType = planTypeFromUsage ?? planTypeFromFile;
  const subscriptionActiveUntil = liveSubscriptionActiveUntil ?? subscriptionActiveUntilFromFile;
  const windows = buildCodexQuotaWindows(payload, t);
  return {
    planType,
    subscriptionActiveUntil,
    rateLimitResetCreditsAvailableCount,
    rateLimitResetCreditsApplicableAvailableCount,
    rateLimitResetCredits: resetCreditsData.credits,
    rateLimitResetCreditsError: resetCreditsData.error,
    windows,
  };
};

const createCodexRedeemRequestId = (): string => {
  if (typeof globalThis.crypto?.randomUUID === 'function') {
    return globalThis.crypto.randomUUID();
  }

  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (char) => {
    const value = Math.floor(Math.random() * 16);
    const segment = char === 'x' ? value : (value & 0x3) | 0x8;
    return segment.toString(16);
  });
};

const consumeCodexRateLimitResetCredit = async (
  file: AuthFileItem,
  t: TFunction
): Promise<void> => {
  const rawAuthIndex = file['auth_index'] ?? file.authIndex;
  const authIndex = normalizeAuthIndex(rawAuthIndex);
  if (!authIndex) {
    throw new Error(t('codex_quota.missing_auth_index'));
  }

  const requestHeader = buildCodexRequestHeader(file);

  const result = await apiCallApi.request({
    authIndex,
    method: 'POST',
    url: CODEX_RATE_LIMIT_RESET_CREDITS_CONSUME_URL,
    header: requestHeader,
    data: JSON.stringify({
      redeem_request_id: createCodexRedeemRequestId(),
    }),
  });

  if (result.statusCode < 200 || result.statusCode >= 300) {
    throw createStatusError(getApiCallErrorMessage(result), result.statusCode);
  }
};

const resetCodexQuota = async (file: AuthFileItem, t: TFunction): Promise<CodexQuotaData> => {
  await consumeCodexRateLimitResetCredit(file, t);
  return fetchCodexQuota(file, t);
};

export const CODEX_CONFIG: QuotaProviderData<CodexQuotaState, CodexQuotaData> = {
  type: 'codex',
  i18nPrefix: 'codex_quota',
  filterFn: (file) => isCodexFile(file) && !isDisabledAuthFile(file),
  fetchQuota: fetchCodexQuota,
  resetQuota: resetCodexQuota,
  canResetQuota: (quota) => (quota.rateLimitResetCreditsAvailableCount ?? 0) > 0,
  storeSelector: (state) => state.codexQuota,
  storeSetter: 'setCodexQuota',
  buildLoadingState: () => ({
    status: 'loading',
    windows: [],
    rateLimitResetCredits: [],
    rateLimitResetCreditsError: '',
  }),
  buildSuccessState: (data) => ({
    status: 'success',
    windows: data.windows,
    planType: data.planType,
    subscriptionActiveUntil: data.subscriptionActiveUntil,
    rateLimitResetCreditsAvailableCount: data.rateLimitResetCreditsAvailableCount,
    rateLimitResetCreditsApplicableAvailableCount:
      data.rateLimitResetCreditsApplicableAvailableCount,
    rateLimitResetCredits: data.rateLimitResetCredits,
    rateLimitResetCreditsError: data.rateLimitResetCreditsError,
  }),
  buildErrorState: (message, status) => ({
    status: 'error',
    windows: [],
    rateLimitResetCredits: [],
    rateLimitResetCreditsError: '',
    error: message,
    errorStatus: status,
  }),
};
