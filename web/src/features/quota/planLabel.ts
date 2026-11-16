/**
 * Plan labels shared by the provider card bodies, so every card names a
 * credential's plan the same way.
 */

import type { TFunction } from 'i18next';
import type { AntigravityQuotaSubscription } from '@/types';
import { normalizePlanType, PREMIUM_CODEX_PLAN_TYPES } from '@/utils/quota';
import type { QuotaProviderType } from './providers/types';

export const getCodexPlanLabel = (planType: string | null | undefined, t: TFunction) => {
  const normalized = normalizePlanType(planType);
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
  return planType || normalized;
};

export const getAntigravityPlanLabel = (
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

/** Plan label for any provider's loaded quota; null when the provider reported none. */
export function getQuotaPlanLabel(
  provider: QuotaProviderType,
  quota: { status?: string } | undefined,
  t: TFunction
): string | null {
  if (!quota || quota.status !== 'success') return null;
  switch (provider) {
    case 'claude': {
      const planType = (quota as { planType?: string | null }).planType;
      return planType ? t(`claude_quota.${planType}`) : null;
    }
    case 'codex':
      return getCodexPlanLabel((quota as { planType?: string | null }).planType, t);
    case 'antigravity':
      return getAntigravityPlanLabel(
        (quota as { subscription?: AntigravityQuotaSubscription | null }).subscription,
        t
      );
    case 'xai':
      return (quota as { billing?: { planLabel?: string } | null }).billing?.planLabel ?? null;
    case 'devin':
      return (quota as { plan?: string | null }).plan ?? null;
    case 'meta':
      return (quota as { data?: { planName?: string } }).data?.planName ?? null;
    default:
      return null;
  }
}
