import { normalizePlanType } from './parsers';

/**
 * Codex 套餐档位的纯映射（卡面档位见 features/quota/cardTier.ts）。
 *
 * - elite   → Pro 500（plan=promax）
 * - premium → Pro 200（plan=pro）、Pro 100（plan=prolite）
 * - plain   → Plus / Team / Business（含 Business Premium）/ Free / 未知
 */
export type CodexPlanTier = 'elite' | 'premium' | 'plain';

/** Pro 500 的各种写法。 */
export const PRO_MAX_CODEX_PLAN_TYPES = new Set(['promax', 'pro-max', 'pro_max']);

/** Pro 100 的各种写法。 */
export const PRO_LITE_CODEX_PLAN_TYPES = new Set(['prolite', 'pro-lite', 'pro_lite']);

/** Pro 200。 */
export const PRO_CODEX_PLAN_TYPE = 'pro';

/** Business Premium：名字带 Premium，但档位与 Plus 相同。 */
export const BUSINESS_PREMIUM_CODEX_PLAN_TYPE = 'self_serve_business_prolite';

export function resolvePlanTier(planType: string | null | undefined): CodexPlanTier {
  const normalized = normalizePlanType(planType);
  if (!normalized) return 'plain';
  if (PRO_MAX_CODEX_PLAN_TYPES.has(normalized)) return 'elite';
  if (normalized === PRO_CODEX_PLAN_TYPE || PRO_LITE_CODEX_PLAN_TYPES.has(normalized)) {
    return 'premium';
  }
  return 'plain';
}
