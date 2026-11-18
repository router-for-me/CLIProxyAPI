/**
 * Read-only normalization of the raw Claude OAuth usage response.
 */
import type { ClaudeDollarWindow, ClaudeUsageSnapshot } from '@/types/quota';
import { isRecord } from '@/utils/helpers';
import { normalizeNumberValue, normalizeStringValue } from '@/utils/quota/parsers';

const nullableBoolean = (value: unknown): boolean | null =>
  typeof value === 'boolean' ? value : null;

const normalizePlanWindow = (value: unknown): ClaudeUsageSnapshot['fiveHour'] =>
  isRecord(value) && 'utilization' in value && 'resets_at' in value
    ? {
        utilization: normalizeNumberValue(value.utilization),
        resetsAt: normalizeStringValue(value.resets_at),
      }
    : null;

export const normalizeClaudeUsageSnapshot = (value: unknown): ClaudeUsageSnapshot | null => {
  if (!isRecord(value)) return null;
  const fields = ['limit_dollars', 'used_dollars', 'remaining_dollars', 'utilization', 'resets_at'];
  const dollarWindows: ClaudeDollarWindow[] = Object.entries(value)
    .filter(
      ([key, window]) =>
        !['five_hour', 'seven_day', 'extra_usage'].includes(key) &&
        isRecord(window) &&
        fields.every((field) => Object.prototype.hasOwnProperty.call(window, field))
    )
    .map(([key, window]) => {
      const balance = window as Record<string, unknown>;
      return {
        key,
        limitDollars: normalizeNumberValue(balance.limit_dollars),
        usedDollars: normalizeNumberValue(balance.used_dollars),
        remainingDollars: normalizeNumberValue(balance.remaining_dollars),
        utilization: normalizeNumberValue(balance.utilization),
        resetsAt: normalizeStringValue(balance.resets_at),
      };
    });
  if (
    !['five_hour', 'seven_day', 'extra_usage', 'limits'].some((key) => key in value) &&
    !Object.values(value).some((window) => normalizePlanWindow(window) !== null) &&
    dollarWindows.length === 0
  ) {
    return null;
  }
  const extra = isRecord(value.extra_usage) ? value.extra_usage : null;
  return {
    fiveHour: normalizePlanWindow(value.five_hour),
    sevenDay: normalizePlanWindow(value.seven_day),
    extraUsage: extra
      ? {
          isEnabled: nullableBoolean(extra.is_enabled),
          monthlyLimit: normalizeNumberValue(extra.monthly_limit),
          usedCredits: normalizeNumberValue(extra.used_credits),
          utilization: normalizeNumberValue(extra.utilization),
          currency: normalizeStringValue(extra.currency),
          disabledReason: normalizeStringValue(extra.disabled_reason),
          userDisabled: nullableBoolean(extra.user_disabled),
          spendLimitReached: nullableBoolean(extra.spend_limit_reached),
        }
      : null,
    dollarWindows,
  };
};
