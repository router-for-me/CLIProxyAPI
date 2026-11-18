/**
 * Claude plan windows, extra usage, and separate read-only dollar balances.
 */

import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import type { ClaudeQuotaState } from '@/types';
import { buildResetDisplay, formatQuotaResetTime, resolveResetMs } from '@/utils/quota';
import { useNow } from '@/hooks/useNow';
import { planValueClassForTier, resolveClaudeCardTier } from '../../cardTier';
import { QuotaMeter } from '../../components/QuotaMeter';
import { QuotaResetLabel } from '../../components/QuotaResetLabel';
import { collectQuotaRowInstants, pickUrgentRowId } from '../../resetSchedule';
import type { QuotaBodyProps } from '../../types';

export function ClaudeQuotaBody({
  quota,
  classes,
  planItems,
  afterPlan,
}: QuotaBodyProps<ClaudeQuotaState>) {
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

  const hasUserDisabledReason =
    extraUsage?.userDisabled === true || extraUsage?.disabledReason === 'user_disabled';
  // Consolidate upstream off signals into the single state beside the label.
  const extraUsageStatusKey =
    extraUsage?.isEnabled === false || hasUserDisabledReason
      ? 'claude_quota.extra_usage_disabled'
      : extraUsage?.isEnabled === true
        ? 'claude_quota.extra_usage_enabled'
        : 'claude_quota.plan_unknown';
  const extraUsageNotes = extraUsage
    ? [
        extraUsage.spendLimitReached ? t('claude_quota.extra_usage_spend_limit_reached') : '',
        extraUsage.disabledReason && extraUsage.disabledReason !== 'user_disabled'
          ? t('claude_quota.extra_usage_disabled_reason', { reason: extraUsage.disabledReason })
          : '',
      ].filter(Boolean)
    : [];

  return (
    <>
      {quota.stale && (
        <div role="status" className={classes.codexResetCreditsError}>
          {t('claude_quota.stale_snapshot', {
            time: quota.observedAt ? new Date(quota.observedAt).toLocaleString(language) : '--',
          })}
        </div>
      )}
      {(planType || planItems) && (
        <div className={classes.codexPlan}>
          {planType && (
            <span className={classes.codexPlanItem} data-quota-plan="">
              <span className={classes.codexPlanLabel}>{t('claude_quota.plan_label')}</span>
              <span className={planValueClassForTier(resolveClaudeCardTier(planType), classes)}>
                {t(`claude_quota.${planType}`)}
              </span>
            </span>
          )}
          {planItems}
        </div>
      )}
      {afterPlan}
      {/* Keep the extra-usage state with its label; only numeric usage goes on the right. */}
      {extraUsage && (
        <div className={classes.quotaRow}>
          <div className={classes.quotaRowHeader}>
            <div className={classes.claudeExtraUsageTitle}>
              <span className={classes.quotaModel}>{t('claude_quota.extra_usage_label')}</span>
              <span className={classes.claudeExtraUsageStatus}>{t(extraUsageStatusKey)}</span>
            </div>
            {extraUsage.utilization !== null && (
              <div className={classes.quotaMeta}>
                <span className={classes.quotaPercent}>
                  {t('claude_quota.usage_utilization', { percent: extraUsage.utilization })}
                </span>
              </div>
            )}
          </div>
          <div className={classes.quotaAmount}>
            {t('claude_quota.usage_spent_limit', {
              // Extra-usage amounts are minor units; dollar windows are already dollars.
              spent: formatMoney(extraUsage.usedCredits, extraUsage.currency ?? 'USD', 100),
              limit: formatMoney(extraUsage.monthlyLimit, extraUsage.currency ?? 'USD', 100),
            })}
          </div>
          {extraUsageNotes.length > 0 && (
            <div className={classes.quotaMeta}>
              {extraUsageNotes.map((note, index) => (
                <span
                  key={note}
                  className={index === 0 ? classes.quotaReset : classes.quotaResetRelative}
                >
                  {note}
                </span>
              ))}
            </div>
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
            <div className={classes.quotaRowHeader}>
              <span className={classes.quotaModel} title={balance.key}>
                {balance.key}
              </span>
              <div className={classes.quotaMeta}>
                <span className={classes.quotaPercent}>
                  {t('claude_quota.dollar_balance_remaining', {
                    amount: formatMoney(balance.remainingDollars),
                  })}
                </span>
              </div>
            </div>
            <div className={classes.quotaAmount}>
              {t('claude_quota.usage_spent_limit', {
                spent: formatMoney(balance.usedDollars),
                limit: formatMoney(balance.limitDollars),
              })}
            </div>
            <div className={classes.quotaMeta}>
              <span className={classes.quotaReset}>{t('claude_quota.balance_reset')}</span>
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
