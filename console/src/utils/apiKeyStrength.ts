/**
 * API Key 强度评估。
 *
 * 模型：字符集熵 × 可预测性折扣 → 四档。纯函数，UI 只消费 tier/segments，
 * 因此评分口径的调整不会牵动组件。
 */

export type ApiKeyStrengthTier = 'weak' | 'fair' | 'good' | 'strong';

export interface ApiKeyStrength {
  tier: ApiKeyStrengthTier;
  /** 点亮的段数（0–4）；0 表示空输入 */
  segments: number;
  /** 估算熵，向下取整到 bit */
  bits: number;
}

/** 档位由弱到强，索引即点亮段数 - 1 */
const TIER_ORDER: readonly ApiKeyStrengthTier[] = ['weak', 'fair', 'good', 'strong'];

export const API_KEY_STRENGTH_SEGMENTS = TIER_ORDER.length;

// 字符集大小：与 isValidApiKeyCharset 允许的 ASCII 可见字符对齐（0x21–0x7E 共 94 个）
const CHARSET_CLASSES: readonly { pattern: RegExp; size: number }[] = [
  { pattern: /[a-z]/, size: 26 },
  { pattern: /[A-Z]/, size: 26 },
  { pattern: /[0-9]/, size: 10 },
  { pattern: /[^a-zA-Z0-9]/, size: 32 },
];

/** 一眼可猜的口令片段，命中即大幅折价 */
const GUESSABLE_TOKENS: readonly string[] = [
  'password',
  'passwd',
  '123456',
  'qwerty',
  'admin',
  'secret',
  'apikey',
  'api-key',
  'letmein',
  'changeme',
  'iloveyou',
  'default',
  'test',
  'demo',
];

// 重复字符与顺序字符几乎不贡献猜测成本，按残值计入有效长度
const REPEAT_WEIGHT = 0.25;
const SEQUENCE_WEIGHT = 0.35;
const GUESSABLE_FACTOR = 0.4;

// 熵阈值（bit）。48 位随机 base62 ≈ 285 bit，32 位十六进制 = 128 bit。
const BITS_FOR_FAIR = 40;
const BITS_FOR_GOOD = 64;
const BITS_FOR_STRONG = 96;

// 长度封顶：熵再高也挡不住短串被离线爆破，短于阈值就锁在对应档
const LENGTH_CAPS: readonly { below: number; tier: ApiKeyStrengthTier }[] = [
  { below: 8, tier: 'weak' },
  { below: 16, tier: 'fair' },
  { below: 24, tier: 'good' },
];

/** 字符种类过少时，长度带来的熵是假的（如 32 个 a） */
const MIN_UNIQUE_FOR_FAIR = 5;

/**
 * 有效长度：与前一字符相同、或延续升/降序列的字符只按残值计入。
 * 随机串几乎不触发折扣，规律串会被显著压缩。
 */
function effectiveLength(key: string): number {
  let total = 0;
  let sequenceRun = 1;

  for (let index = 0; index < key.length; index += 1) {
    const code = key.charCodeAt(index);
    const previous = index > 0 ? key.charCodeAt(index - 1) : Number.NaN;

    if (code === previous) {
      total += REPEAT_WEIGHT;
      sequenceRun = 1;
      continue;
    }

    const delta = code - previous;
    if (delta === 1 || delta === -1) {
      sequenceRun += 1;
      // 前两个字符仍算新信息，第三个起才是可预测的顺序
      total += sequenceRun >= 3 ? SEQUENCE_WEIGHT : 1;
      continue;
    }

    sequenceRun = 1;
    total += 1;
  }

  return total;
}

/**
 * 最小周期长度：`deadbeefdeadbeef` → 8，`abcabca` → 3，无周期则返回原长。
 * 用 (s + s).indexOf(s, 1) 求解，尾部不完整的周期同样能识别。
 */
function smallestPeriod(key: string): number {
  const period = `${key}${key}`.indexOf(key, 1);
  return period > 0 && period < key.length ? period : key.length;
}

function charsetSize(key: string): number {
  return CHARSET_CLASSES.reduce(
    (size, charClass) => (charClass.pattern.test(key) ? size + charClass.size : size),
    0
  );
}

function tierForBits(bits: number): ApiKeyStrengthTier {
  if (bits >= BITS_FOR_STRONG) return 'strong';
  if (bits >= BITS_FOR_GOOD) return 'good';
  if (bits >= BITS_FOR_FAIR) return 'fair';
  return 'weak';
}

function capTier(tier: ApiKeyStrengthTier, cap: ApiKeyStrengthTier): ApiKeyStrengthTier {
  return TIER_ORDER.indexOf(tier) <= TIER_ORDER.indexOf(cap) ? tier : cap;
}

/**
 * 评估用户自拟 API Key 的强度。仅供参考，不参与保存校验。
 */
export function evaluateApiKeyStrength(rawKey: string): ApiKeyStrength {
  const key = rawKey.trim();
  if (!key) return { tier: 'weak', segments: 0, bits: 0 };

  const pool = charsetSize(key);
  const lowerCased = key.toLowerCase();
  const guessable = GUESSABLE_TOKENS.some((token) => lowerCased.includes(token));
  // 周期串的猜测成本只等于一个周期，重复部分按残值计入
  const period = smallestPeriod(key);
  const length = effectiveLength(key.slice(0, period)) + (key.length - period) * REPEAT_WEIGHT;
  const bits = Math.floor(length * Math.log2(pool) * (guessable ? GUESSABLE_FACTOR : 1));

  let tier = tierForBits(bits);
  for (const { below, tier: cap } of LENGTH_CAPS) {
    if (key.length < below) tier = capTier(tier, cap);
  }
  if (new Set(key).size < MIN_UNIQUE_FOR_FAIR) tier = capTier(tier, 'weak');

  return { tier, segments: TIER_ORDER.indexOf(tier) + 1, bits };
}
