/**
 * 套餐档位：跨 provider 的统一阶梯，决定套餐文字的样式
 * （flagship = 和田玉金镶玉 elitePlanValue，premium = 金卡 premiumPlanValue）。
 *
 * 规则：各家最高个人档 = flagship；其下的付费个人档 = premium；入门付费档、
 * 团队/商业套餐与免费档 = standard。
 *
 * - flagship：Codex Pro 500、Kimi Allegro、Claude Max 20x、Antigravity Ultra、SuperGrok Heavy
 * - premium： Codex Pro 200 / Pro 100、Claude Max 5x / Max（未区分）、
 *            Kimi Allegretto、Antigravity Ultra Lite、SuperGrok、xAI 付费 API
 * - standard：Plus、Team、Business（含 Business Premium）、Pro（Claude /
 *            Antigravity）、Kimi Moderato / Andante、Free，以及档位未知
 *
 * React-free，便于单测。
 */

import type { XaiBillingSummary } from '@/types';
import { resolvePlanTier } from '@/utils/quota';
import type { QuotaClassMap } from './types';

export type QuotaCardTier = 'standard' | 'premium' | 'flagship';

/** 订阅接口无标签时，按月度额度（分）识别 SuperGrok / Heavy。 */
export const XAI_SUPERGROK_LIMIT_CENTS = 15_000;
export const XAI_SUPERGROK_HEAVY_LIMIT_CENTS = 150_000;

export function resolveCodexCardTier(planType: string | null | undefined): QuotaCardTier {
  const tier = resolvePlanTier(planType);
  if (tier === 'elite') return 'flagship';
  if (tier === 'premium') return 'premium';
  return 'standard';
}

/** Kimi: Allegro = Pro 500, Allegretto = Pro 200, Moderato = Plus. */
export function resolveKimiCardTier(planName: string | null | undefined): QuotaCardTier {
  const normalized = planName?.trim().toLowerCase();
  if (normalized === 'allegro') return 'flagship';
  if (normalized === 'allegretto') return 'premium';
  return 'standard';
}

export function resolveAntigravityCardTier(plan: string | null | undefined): QuotaCardTier {
  const normalized = plan?.trim().toLowerCase() ?? '';
  if (normalized === 'ultra') return 'flagship';
  if (normalized === 'ultra-lite') return 'premium';
  return 'standard';
}

export function resolveClaudeCardTier(planType: string | null | undefined): QuotaCardTier {
  if (planType === 'plan_max20') return 'flagship';
  // 未区分 5x/20x 的 Max 按低一档保守处理。
  if (planType === 'plan_max5' || planType === 'plan_max') return 'premium';
  return 'standard';
}

export function resolveXaiCardTier(billing: XaiBillingSummary): QuotaCardTier {
  if (billing.planLabel) {
    if (billing.planTier === 'elite') return 'flagship';
    if (billing.planTier === 'premium') return 'premium';
    return 'standard';
  }
  // 付费 API 只做健康检查，拿不到套餐名，但它本身就是付费档。
  if (billing.mode === 'paid-health') return 'premium';
  // 按月度额度识别时与订阅接口的判定保持一致（resolveXaiSubscriptionPlan）。
  if (billing.monthlyLimitCents === XAI_SUPERGROK_HEAVY_LIMIT_CENTS) return 'flagship';
  if (billing.monthlyLimitCents === XAI_SUPERGROK_LIMIT_CENTS) return 'premium';
  return 'standard';
}

/** 档位对应的套餐文字样式。 */
export function planValueClassForTier(tier: QuotaCardTier, classes: QuotaClassMap): string {
  if (tier === 'flagship') return classes.elitePlanValue;
  if (tier === 'premium') return classes.premiumPlanValue;
  return classes.codexPlanValue;
}
