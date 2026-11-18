import { TRAFFIC_BUCKET_MINUTES } from './types';

/** 供应商展示名。均为专有名词，不进入 i18n。 */
const PROVIDER_LABELS: Record<string, string> = {
  gemini: 'Gemini',
  'gemini-interactions': 'Interactions API',
  aistudio: 'AI Studio',
  codex: 'Codex',
  claude: 'Claude',
  devin: 'Devin',
  meta: 'Muse (Meta)',
  xai: 'xAI',
  vertex: 'Vertex AI',
  openai: 'OpenAI Compatible',
  'openai-compatibility': 'OpenAI Compatible',
  qwen: 'Qwen',
  kimi: 'Kimi',
  iflow: 'iFlow',
  antigravity: 'Antigravity',
};

/**
 * 解析供应商展示名；未知 id 首字母大写兜底，`unknown` 交给调用方本地化。
 */
export function providerLabel(id: string, unknownLabel: string): string {
  if (id === 'unknown' || !id) return unknownLabel;
  return PROVIDER_LABELS[id] ?? id.charAt(0).toUpperCase() + id.slice(1);
}

export interface WindowParts {
  hours: number;
  minutes: number;
}

/** 把窗口分钟数拆成 时/分，供 i18n 插值 */
export function splitWindowMinutes(totalMinutes: number): WindowParts {
  const safe = Math.max(0, Math.round(totalMinutes));
  return { hours: Math.floor(safe / 60), minutes: safe % 60 };
}

/** 桶数 → 覆盖分钟数 */
export function bucketsToMinutes(bucketCount: number): number {
  return bucketCount * TRAFFIC_BUCKET_MINUTES;
}

export type MeterTone = 'good' | 'warning' | 'critical' | 'idle';

/** 成功率 → 严重度。数值本身始终可见，颜色只是辅助通道。 */
export function toneForSuccessRate(rate: number | null): MeterTone {
  if (rate === null) return 'idle';
  if (rate >= 95) return 'good';
  if (rate >= 80) return 'warning';
  return 'critical';
}

/** 刻度阶梯。比 1/2/5 更细，避免峰值 112 被抬到 200 这种浪费半张图的情况。 */
const STEP_LADDER = [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10] as const;

/** 把数值向上取整到易读刻度（阶梯 × 10^n） */
export function niceCeil(value: number): number {
  if (value <= 0) return 1;
  const magnitude = 10 ** Math.floor(Math.log10(value));
  const normalized = value / magnitude;
  const step = STEP_LADDER.find((candidate) => normalized <= candidate) ?? 10;
  return step * magnitude;
}

/**
 * 纵轴上限：先把「刻度间距」取整，再乘以间隔数。
 * 这样每条网格线都落在整数上，且上限不会远高于峰值。
 */
export function axisMax(peak: number, intervals: number): number {
  if (peak <= 0 || intervals <= 0) return Math.max(1, intervals);
  const step = Math.max(1, Math.ceil(niceCeil(peak / intervals)));
  return step * intervals;
}
