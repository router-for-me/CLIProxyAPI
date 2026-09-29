/**
 * Codex 额度渲染体：套餐 chip 行（elite=Pro 20x 液态铂金 / premium=金卡）、
 * 重置积分明细、用量窗口水位条。
 */

import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import type { CodexQuotaState } from '@/types';
import {
  normalizePlanType,
  resolvePlanTier,
  PREMIUM_CODEX_PLAN_TYPES,
  buildResetDisplay,
  formatInstantShort,
  parseIsoToMs,
  resolveResetMs,
} from '@/utils/quota';
import { resolveTimeZoneLabel } from '@/utils/time/timezone';
import { formatDateTimeValue } from '@/utils/format';
import { useNow } from '@/hooks/useNow';
import { QuotaMeter } from '../../components/QuotaMeter';
import { QuotaResetLabel } from '../../components/QuotaResetLabel';
import { collectQuotaRowInstants, pickUrgentRowId, resetCreditRowId } from '../../resetSchedule';
import type { QuotaBodyProps, QuotaClassMap } from '../../types';

const getPlanValueClass = (planType: string | null, classes: QuotaClassMap): string => {
  // elite/premium 顺序契约由 resolvePlanTier 承载（tests/quotaPlanTier.test.ts 守护）。
  const tier = resolvePlanTier(planType);
  if (tier === 'elite') return classes.elitePlanValue;
  if (tier === 'premium') return classes.premiumPlanValue;
  return classes.codexPlanValue;
};

export function CodexQuotaBody({ quota, classes }: QuotaBodyProps<CodexQuotaState>) {
  const { t, i18n } = useTranslation();
  const now = useNow();
  const locale = i18n.resolvedLanguage;
  // Windows and reset credits compete for the same emphasis, but only during
  // the final hour before the reset or expiry.
  const soonestRowId = useMemo(
    () => pickUrgentRowId(collectQuotaRowInstants('codex', quota), now),
    [quota, now]
  );
  const windows = quota.windows ?? [];
  const planType = quota.planType ?? null;
  const subscriptionActiveUntil = quota.subscriptionActiveUntil ?? null;
  const rateLimitResetCreditsAvailableCount = quota.rateLimitResetCreditsAvailableCount ?? null;
  const rateLimitResetCredits = quota.rateLimitResetCredits ?? [];
  const rateLimitResetCreditsError = quota.rateLimitResetCreditsError ?? '';

  const getPlanLabel = (pt?: string | null): string | null => {
    const normalized = normalizePlanType(pt);
    if (!normalized) return null;
    if (normalized === 'self_serve_business_prolite') {
      return t('codex_quota.plan_business_premium');
    }
    if (normalized === 'pro') return t('codex_quota.plan_pro');
    if (PREMIUM_CODEX_PLAN_TYPES.has(normalized) && normalized !== 'pro') {
      return t('codex_quota.plan_prolite');
    }
    if (normalized === 'plus') return t('codex_quota.plan_plus');
    if (normalized === 'team') return t('codex_quota.plan_team');
    if (normalized === 'free') return t('codex_quota.plan_free');
    return pt || normalized;
  };

  const planLabel = getPlanLabel(planType);
  const planValueClass = getPlanValueClass(planType, classes);

  // Renewal was the one date on this card in a different shape (a full
  // toLocaleString). Reformatted from the instant so it reads like the rest,
  // falling back to the old rendering when the payload isn't parseable.
  const subscriptionMs = resolveResetMs([subscriptionActiveUntil]);
  const expiryDisplay = subscriptionActiveUntil
    ? buildResetDisplay(
        subscriptionMs === null ? formatDateTimeValue(subscriptionActiveUntil) : null,
        subscriptionMs,
        now,
        locale
      )
    : null;

  return (
    <>
      {(planLabel || expiryDisplay || rateLimitResetCreditsAvailableCount !== null) && (
        <div className={classes.codexPlan}>
          {planLabel && (
            <span className={classes.codexPlanItem}>
              <span className={classes.codexPlanLabel}>{t('codex_quota.plan_label')}</span>
              <span className={planValueClass}>{planLabel}</span>
            </span>
          )}
          {expiryDisplay && (
            <span className={classes.codexPlanItem}>
              <span className={classes.codexPlanLabel}>{t('codex_quota.expires_label')}</span>
              <span className={classes.codexPlanValue}>{expiryDisplay.absolute}</span>
              {expiryDisplay.relative && (
                <span className={classes.quotaResetRelative}>{expiryDisplay.relative}</span>
              )}
            </span>
          )}
          {rateLimitResetCreditsAvailableCount !== null && (
            <span className={classes.codexPlanItem}>
              <span className={classes.codexPlanLabel}>{t('codex_quota.reset_credits_label')}</span>
              <span className={classes.codexPlanValue}>
                {rateLimitResetCreditsAvailableCount.toString()}
              </span>
            </span>
          )}
        </div>
      )}
      {rateLimitResetCredits.length > 0 ? (
        <div className={classes.codexResetCredits}>
          <div className={classes.codexResetCreditsTitle}>
            {t('codex_quota.reset_credits_expiry_label', { timezone: resolveTimeZoneLabel() })}
          </div>
          {rateLimitResetCredits.map((credit, index) => {
            const expiresAtMs = parseIsoToMs(credit.expiresAt);
            const expiresDisplay = buildResetDisplay(
              expiresAtMs === null ? credit.expiresAt : formatInstantShort(expiresAtMs),
              expiresAtMs,
              now,
              locale
            );
            // One expression for both the key and the highlight — two copies
            // that drift would emphasize the wrong row.
            const rowId = resetCreditRowId(credit, index);
            const soon = rowId === soonestRowId;
            return (
              <div
                key={rowId}
                className={
                  soon
                    ? `${classes.codexResetCreditRow} ${classes.codexResetCreditRowSoon}`
                    : classes.codexResetCreditRow
                }
                title={soon ? t('quota_management.soonest_row_hint') : undefined}
              >
                <span className={classes.codexResetCreditLabel}>
                  {t('codex_quota.reset_credit_number', { index: index + 1 })}
                </span>
                <span className={classes.codexResetCreditTime}>
                  {expiresDisplay && (
                    <QuotaResetLabel display={expiresDisplay} classes={classes} soon={soon} />
                  )}
                </span>
              </div>
            );
          })}
        </div>
      ) : rateLimitResetCreditsError ? (
        <div className={classes.codexResetCreditsError}>
          {t('codex_quota.reset_credits_expiry_failed', {
            message: rateLimitResetCreditsError,
          })}
        </div>
      ) : null}
      {windows.length === 0 ? (
        <div className={classes.quotaMessage}>{t('codex_quota.empty_windows')}</div>
      ) : (
        windows.map((window, index) => {
          const used = window.usedPercent;
          const clampedUsed = used === null ? null : Math.max(0, Math.min(100, used));
          const remaining =
            clampedUsed === null ? null : Math.max(0, Math.min(100, 100 - clampedUsed));
          const percentLabel = remaining === null ? '--' : `${Math.round(remaining)}%`;
          const windowLabel = window.labelKey
            ? t(window.labelKey, window.labelParams as Record<string, string | number>)
            : window.label;
          const resetDisplay = buildResetDisplay(window.resetLabel, window.resetAtMs, now, locale);

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
