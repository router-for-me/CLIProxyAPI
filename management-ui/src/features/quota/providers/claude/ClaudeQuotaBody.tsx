/**
 * Claude plan windows, extra usage, and separate read-only dollar balances.
 */

import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import type { ClaudeQuotaState } from '@/types';
import { buildResetDisplay, formatQuotaResetTime, resolveResetMs } from '@/utils/quota';
import { useNow } from '@/hooks/useNow';
import { QuotaMeter } from '../../components/QuotaMeter';
import { QuotaResetLabel } from '../../components/QuotaResetLabel';
import { collectQuotaRowInstants, pickUrgentRowId } from '../../resetSchedule';
import type { QuotaBodyProps } from '../../types';

export function ClaudeQuotaBody({ quota, classes }: QuotaBodyProps<ClaudeQuotaState>) {
  const { t, i18n } = useTranslation();
  const now = useNow();
  const soonestRowId = useMemo(
    () => pickUrgentRowId(collectQuotaRowInstants('claude', quota), now),
    [quota, now]
  );
  const windows = quota.windows ?? [];
  const extraUsage = quota.extraUsage ?? null;
  const planType = quota.planType ?? null;
  const language = i18n.resolvedLanguage;
  const formatMoney = (value: number | null, currency = 'USD', divisor = 1): string => {
    if (value === null) return '--';
    try {
      return new Intl.NumberFormat(language, {
        style: 'currency',
        currency,
      }).format(value / divisor);
    } catch {
      return `${(value / divisor).toFixed(2)} ${currency}`;
    }
  };

  return (
    <>
      {quota.stale && (
        <div role="status" className={classes.codexResetCreditsError}>
          {t('claude_quota.stale_snapshot', {
            time: quota.observedAt ? new Date(quota.observedAt).toLocaleString(language) : '--',
          })}
        </div>
      )}
      {planType && (
        <div className={classes.codexPlan}>
          <span className={classes.codexPlanLabel}>{t('claude_quota.plan_label')}</span>
          <span className={classes.codexPlanValue}>{t(`claude_quota.${planType}`)}</span>
        </div>
      )}
      {extraUsage && (
        <div className={classes.quotaRow}>
          <div className={classes.codexPlan}>
            <span className={classes.codexPlanLabel}>{t('claude_quota.extra_usage_label')}</span>
            <span className={classes.codexPlanValue}>
              {t(
                extraUsage.isEnabled === null
                  ? 'claude_quota.plan_unknown'
                  : extraUsage.isEnabled
                    ? 'claude_quota.extra_usage_enabled'
                    : 'claude_quota.extra_usage_disabled'
              )}
            </span>
          </div>
          <div className={classes.quotaAmount}>
            {t('claude_quota.usage_spent_limit', {
              // Extra-usage amounts are minor units; dollar windows are already dollars.
              spent: formatMoney(extraUsage.usedCredits, extraUsage.currency ?? 'USD', 100),
              limit: formatMoney(extraUsage.monthlyLimit, extraUsage.currency ?? 'USD', 100),
            })}
          </div>
          {extraUsage.utilization !== null && (
            <span className={classes.quotaReset}>
              {t('claude_quota.usage_utilization', { percent: extraUsage.utilization })}
            </span>
          )}
          {extraUsage.userDisabled && (
            <span className={classes.quotaReset}>
              {t('claude_quota.extra_usage_user_disabled')}
            </span>
          )}
          {extraUsage.spendLimitReached && (
            <span className={classes.quotaReset}>
              {t('claude_quota.extra_usage_spend_limit_reached')}
            </span>
          )}
          {extraUsage.disabledReason && (
            <span className={classes.quotaReset}>
              {t('claude_quota.extra_usage_disabled_reason', { reason: extraUsage.disabledReason })}
            </span>
          )}
        </div>
      )}
      {(quota.dollarWindows ?? []).map((balance) => {
        const resetDisplay = buildResetDisplay(
          formatQuotaResetTime(balance.resetsAt ?? undefined),
          resolveResetMs([balance.resetsAt]),
          now,
          language
        );
        return (
          <div key={balance.key} className={classes.quotaRow}>
            <div className={classes.codexPlan}>
              <span className={classes.codexResetCreditLabel} title={balance.key}>
                {balance.key}
              </span>
              <span className={classes.quotaAmount}>
                {t('claude_quota.dollar_balance_remaining', {
                  amount: formatMoney(balance.remainingDollars),
                })}
              </span>
            </div>
            <div className={classes.quotaAmount}>
              {t('claude_quota.usage_spent_limit', {
                spent: formatMoney(balance.usedDollars),
                limit: formatMoney(balance.limitDollars),
              })}
            </div>
            <div className={classes.codexPlan}>
              <span className={classes.codexPlanLabel}>{t('claude_quota.balance_reset')}</span>
              {resetDisplay ? (
                <QuotaResetLabel display={resetDisplay} classes={classes} />
              ) : (
                <span className={classes.quotaReset}>
                  {t('claude_quota.balance_reset_unknown')}
                </span>
              )}
            </div>
          </div>
        );
      })}
      {windows.length === 0 && !extraUsage && !quota.dollarWindows?.length ? (
        <div className={classes.quotaMessage}>{t('claude_quota.empty_windows')}</div>
      ) : (
        windows.map((window, index) => {
          const used = window.usedPercent;
          const clampedUsed = used === null ? null : Math.max(0, Math.min(100, used));
          const remaining =
            clampedUsed === null ? null : Math.max(0, Math.min(100, 100 - clampedUsed));
          const percentLabel = remaining === null ? '--' : `${Math.round(remaining)}%`;
          const windowLabel = window.labelKey ? t(window.labelKey) : window.label;
          const resetDisplay = buildResetDisplay(
            window.resetLabel,
            window.resetAtMs,
            now,
            i18n.resolvedLanguage
          );

          const soon = window.id === soonestRowId;

          return (
            <div
              key={window.id}
              className={classes.quotaRow}
              title={soon ? t('quota_management.soonest_row_hint') : undefined}
            >
              <div className={classes.quotaRowHeader}>
                <span className={classes.quotaModel}>{windowLabel}</span>
                <div className={classes.quotaMeta}>
                  <span className={classes.quotaPercent}>{percentLabel}</span>
                  {resetDisplay && (
                    <QuotaResetLabel display={resetDisplay} classes={classes} soon={soon} />
                  )}
                </div>
              </div>
              <QuotaMeter percent={remaining} classes={classes} index={index} />
            </div>
          );
        })
      )}
    </>
  );
}
