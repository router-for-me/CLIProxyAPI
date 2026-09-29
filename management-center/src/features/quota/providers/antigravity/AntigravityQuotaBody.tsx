/**
 * Antigravity 额度渲染体：套餐 chip 行（ultra/ultra-lite=金卡）+ 分组水位条。
 */

import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import type { AntigravityQuotaState, AntigravityQuotaSubscription } from '@/types';
import { QuotaMeter } from '../../components/QuotaMeter';
import { collectQuotaRowInstants, pickUrgentRowId } from '../../resetSchedule';
import type { QuotaBodyProps } from '../../types';
import { getNextAntigravityCountdownUpdateDelay } from './countdown';

const formatAntigravityDuration = (t: TFunction, deltaMs: number): string => {
  const totalMinutes = Math.max(1, Math.ceil(deltaMs / 60000));
  const days = Math.floor(totalMinutes / 1440);
  const hours = Math.floor((totalMinutes % 1440) / 60);
  const minutes = totalMinutes % 60;

  if (days > 0) {
    return t('antigravity_quota.duration_day_hour', {
      days,
      hours,
    });
  }
  if (hours > 0) {
    return t('antigravity_quota.duration_hour_minute', {
      hours,
      minutes,
    });
  }
  if (minutes > 0) {
    return t('antigravity_quota.duration_minute', {
      minutes,
    });
  }
  return t('antigravity_quota.duration_less_than_minute');
};

const formatAntigravityResetLabel = (
  resetTime: string | undefined,
  t: TFunction,
  nowMs: number
): string => {
  if (!resetTime) return '-';
  const resetMs = new Date(resetTime).getTime();
  if (Number.isNaN(resetMs)) return '-';
  const deltaMs = resetMs - nowMs;
  if (deltaMs <= 0) return t('antigravity_quota.refresh_available');
  return t('antigravity_quota.refreshes_in', {
    duration: formatAntigravityDuration(t, deltaMs),
  });
};

const ANTIGRAVITY_GROUP_LABEL_KEYS = new Map<string, string>([
  ['gemini models', 'group_gemini_models'],
  ['claude and gpt models', 'group_claude_gpt_models'],
]);

const ANTIGRAVITY_BUCKET_LABEL_KEYS = new Map<string, string>([
  ['weekly limit', 'weekly_limit'],
  ['daily limit', 'daily_limit'],
  ['5 hour limit', 'five_hour_limit'],
  ['5-hour limit', 'five_hour_limit'],
  ['five hour limit', 'five_hour_limit'],
  ['monthly limit', 'monthly_limit'],
]);

const normalizeAntigravityQuotaText = (value: string): string =>
  value.trim().toLowerCase().replace(/\s+/g, ' ');

const translateAntigravityQuotaLabel = (
  value: string,
  keys: Map<string, string>,
  t: TFunction
): string => {
  const key = keys.get(normalizeAntigravityQuotaText(value));
  return key ? t(`antigravity_quota.${key}`) : value;
};

const translateAntigravityQuotaDescription = (
  value: string | undefined,
  t: TFunction
): string | undefined => {
  if (!value) return undefined;
  const modelsMatch = value.match(/^models within this group:\s*(.+)$/i);
  if (modelsMatch) {
    return t('antigravity_quota.group_models_description', {
      models: modelsMatch[1].trim(),
    });
  }
  return value;
};

const getAntigravityPlanLabel = (
  subscription: AntigravityQuotaSubscription | null | undefined,
  t: TFunction
): string | null => {
  if (!subscription) return null;
  if (subscription.plan === 'free') return t('antigravity_subscription.plan_free');
  if (subscription.plan === 'pro') return t('antigravity_subscription.plan_pro');
  if (subscription.plan === 'ultra') return t('antigravity_subscription.plan_ultra');
  if (subscription.plan === 'ultra-lite') return t('antigravity_subscription.plan_ultra_lite');
  return (
    subscription.tierName ||
    subscription.tierId ||
    (subscription.plan === 'unknown' ? t('antigravity_subscription.plan_unknown') : null)
  );
};

export function AntigravityQuotaBody({ quota, classes }: QuotaBodyProps<AntigravityQuotaState>) {
  const { t } = useTranslation();
  const groups = quota.groups ?? [];
  const planLabel = getAntigravityPlanLabel(quota.subscription, t);
  const normalizedPlan = quota.subscription?.plan?.toLowerCase() ?? '';
  const isPremiumPlan = normalizedPlan === 'ultra' || normalizedPlan === 'ultra-lite';
  const serverTimeOffsetMs = quota.serverTimeOffsetMs ?? 0;
  const resetTimestamps = useMemo(
    () =>
      (quota.groups ?? []).flatMap((group) =>
        group.buckets
          .map((bucket) => (bucket.resetTime ? new Date(bucket.resetTime).getTime() : Number.NaN))
          .filter(Number.isFinite)
      ),
    [quota.groups]
  );
  // 首屏直接显示准确文案；后续 effect 会在最近的分钟边界更新并重新排程。
  const [nowMs, setNowMs] = useState(() => Date.now() + serverTimeOffsetMs);

  useEffect(() => {
    let timeoutId: ReturnType<typeof setTimeout> | undefined;

    const updateCountdown = () => {
      const currentNowMs = Date.now() + serverTimeOffsetMs;
      setNowMs(currentNowMs);
      const delay = getNextAntigravityCountdownUpdateDelay(resetTimestamps, currentNowMs);
      if (delay !== null) {
        timeoutId = setTimeout(updateCountdown, delay);
      }
    };

    updateCountdown();
    return () => {
      if (timeoutId !== undefined) clearTimeout(timeoutId);
    };
  }, [resetTimestamps, serverTimeOffsetMs]);

  // Ranked against this provider's own server-corrected clock rather than the
  // shared one, so the final-hour warning and the countdown always agree.
  const soonestRowId = useMemo(
    () => pickUrgentRowId(collectQuotaRowInstants('antigravity', quota), nowMs),
    [quota, nowMs]
  );

  return (
    <>
      {planLabel && (
        <div className={classes.codexPlan}>
          <span className={classes.codexPlanItem}>
            <span className={classes.codexPlanLabel}>{t('antigravity_quota.plan_label')}</span>
            <span className={isPremiumPlan ? classes.premiumPlanValue : classes.codexPlanValue}>
              {planLabel}
            </span>
          </span>
        </div>
      )}
      {groups.length === 0 ? (
        <div className={classes.quotaMessage}>{t('antigravity_quota.empty_models')}</div>
      ) : (
        groups.map((group) => {
          const groupLabel = translateAntigravityQuotaLabel(
            group.label,
            ANTIGRAVITY_GROUP_LABEL_KEYS,
            t
          );
          const groupDescription = translateAntigravityQuotaDescription(group.description, t);

          return (
            <div key={group.id} className={classes.antigravityQuotaGroup}>
              <div className={classes.antigravityQuotaGroupHeader}>
                <span className={classes.antigravityQuotaGroupTitle}>{groupLabel}</span>
                {groupDescription && (
                  <span className={classes.antigravityQuotaGroupDescription}>
                    {groupDescription}
                  </span>
                )}
              </div>
              {group.buckets.map((bucket, index) => {
                const clamped = Math.max(0, Math.min(1, bucket.remainingFraction));
                const percent = clamped * 100;
                const percentLabel =
                  bucket.remainingFraction === 1
                    ? t('antigravity_quota.quota_available')
                    : t('antigravity_quota.remaining_percent', {
                        percent: Math.round(percent),
                      });
                const resetLabel = formatAntigravityResetLabel(bucket.resetTime, t, nowMs);
                const bucketLabel = translateAntigravityQuotaLabel(
                  bucket.label,
                  ANTIGRAVITY_BUCKET_LABEL_KEYS,
                  t
                );
                const bucketDescription = translateAntigravityQuotaDescription(
                  bucket.description,
                  t
                );

                const soon = bucket.id === soonestRowId;

                return (
                  <div key={bucket.id} className={classes.quotaRow}>
                    <div className={classes.quotaRowHeader}>
                      <span className={classes.quotaModel} title={bucketDescription}>
                        {bucketLabel}
                      </span>
                      <div className={classes.quotaMeta}>
                        <span className={classes.quotaPercent}>{percentLabel}</span>
                        <span
                          className={
                            soon
                              ? `${classes.quotaReset} ${classes.quotaResetRelativeSoon}`
                              : classes.quotaReset
                          }
                          title={soon ? t('quota_management.soonest_row_hint') : undefined}
                        >
                          {resetLabel}
                        </span>
                      </div>
                    </div>
                    <QuotaMeter percent={percent} classes={classes} index={index} />
                  </div>
                );
              })}
            </div>
          );
        })
      )}
    </>
  );
}
