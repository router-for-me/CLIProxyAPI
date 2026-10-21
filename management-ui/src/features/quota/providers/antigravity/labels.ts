/**
 * Antigravity 额度标签 → i18n key 解析（纯函数，便于测试）。
 *
 * 上游 displayName 会随版本变化（如 "Five Hour Limit Remaining"），因此先按
 * 归一化后的文案匹配，匹配不到再回落到结构化的 `window` 字段。
 */

const GROUP_LABEL_KEYS = new Map<string, string>([
  ['gemini models', 'group_gemini_models'],
  ['claude and gpt models', 'group_claude_gpt_models'],
]);

const BUCKET_LABEL_KEYS = new Map<string, string>([
  ['weekly limit', 'weekly_limit'],
  ['daily limit', 'daily_limit'],
  ['5 hour limit', 'five_hour_limit'],
  ['5-hour limit', 'five_hour_limit'],
  ['five hour limit', 'five_hour_limit'],
  ['five-hour limit', 'five_hour_limit'],
  ['monthly limit', 'monthly_limit'],
]);

const BUCKET_WINDOW_KEYS = new Map<string, string>([
  ['5h', 'five_hour_limit'],
  ['five-hour', 'five_hour_limit'],
  ['five_hour', 'five_hour_limit'],
  ['daily', 'daily_limit'],
  ['day', 'daily_limit'],
  ['weekly', 'weekly_limit'],
  ['week', 'weekly_limit'],
  ['monthly', 'monthly_limit'],
  ['month', 'monthly_limit'],
]);

/**
 * 上游分组描述未列出、但实际共享该分组额度的模型，追加到「此分组包含」列表末尾。
 */
const GROUP_EXTRA_MODELS = new Map<string, readonly string[]>([
  ['group_gemini_models', ['Gemini Argon']],
]);

const GROUP_MODELS_PATTERN = /^models within this group:\s*(.+)$/i;

const normalizeText = (value: string): string => value.trim().toLowerCase().replace(/\s+/g, ' ');

/** 去掉上游常见的 "Remaining" 后缀，如 "Weekly Limit Remaining" → "weekly limit"。 */
const stripRemainingSuffix = (value: string): string =>
  value.replace(/\s*(?:-|:)?\s*remaining$/, '').trim();

export function resolveAntigravityGroupLabelKey(label: string): string | null {
  return GROUP_LABEL_KEYS.get(normalizeText(label)) ?? null;
}

/**
 * 解析分组描述中的模型列表，并追加该分组的补充模型（大小写不敏感去重）。
 * 描述不是「Models within this group: ...」格式时返回 null，由调用方显示原文。
 */
export function resolveAntigravityGroupModels(
  label: string,
  description: string | undefined
): string[] | null {
  const match = description?.trim().match(GROUP_MODELS_PATTERN);
  if (!match) return null;

  const models = match[1]
    .split(',')
    .map((model) => model.trim())
    .filter(Boolean);
  const groupKey = resolveAntigravityGroupLabelKey(label);
  const extras = groupKey ? (GROUP_EXTRA_MODELS.get(groupKey) ?? []) : [];
  const seen = new Set(models.map(normalizeText));
  extras.forEach((model) => {
    if (seen.has(normalizeText(model))) return;
    seen.add(normalizeText(model));
    models.push(model);
  });
  return models;
}

export function resolveAntigravityBucketLabelKey(
  label: string,
  window: string | undefined
): string | null {
  const normalized = normalizeText(label);
  const byLabel =
    BUCKET_LABEL_KEYS.get(normalized) ?? BUCKET_LABEL_KEYS.get(stripRemainingSuffix(normalized));
  if (byLabel) return byLabel;
  if (!window) return null;
  return BUCKET_WINDOW_KEYS.get(normalizeText(window)) ?? null;
}

interface AntigravityBucketLike {
  label: string;
  window?: string;
  remainingFraction: number;
}

/**
 * 满额桶是否显示「额度可用」。同组周限额已耗尽时，5 小时限额即使满额也不可用
 * （上游会把它标为 disabled 且 remainingFraction 仍为 1），此时不显示「额度可用」。
 */
export function shouldShowAntigravityQuotaAvailable(
  bucket: AntigravityBucketLike,
  groupBuckets: readonly AntigravityBucketLike[]
): boolean {
  if (bucket.remainingFraction !== 1) return false;
  if (resolveAntigravityBucketLabelKey(bucket.label, bucket.window) !== 'five_hour_limit') {
    return true;
  }
  return !groupBuckets.some(
    (item) =>
      resolveAntigravityBucketLabelKey(item.label, item.window) === 'weekly_limit' &&
      item.remainingFraction <= 0
  );
}
