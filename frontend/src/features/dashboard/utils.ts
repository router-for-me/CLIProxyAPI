import { TRAFFIC_BUCKET_MINUTES } from './types';

/** Provider brands are proper nouns and are not localized. */
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
  kiro: 'Kiro',
  kimi: 'Kimi',
  'kimi-ai': 'Kimi.ai',
  qoder: 'Qoder',
  'qoder-cn': 'Qoder CN',
  zcode: 'ZCode',
  iflow: 'iFlow',
  antigravity: 'Antigravity',
};

export function providerLabel(id: string, unknownLabel: string): string {
  if (id === 'unknown' || !id) return unknownLabel;
  return PROVIDER_LABELS[id] ?? id.charAt(0).toUpperCase() + id.slice(1);
}

export interface WindowParts {
  hours: number;
  minutes: number;
}

export function splitWindowMinutes(totalMinutes: number): WindowParts {
  const safe = Math.max(0, Math.round(totalMinutes));
  return { hours: Math.floor(safe / 60), minutes: safe % 60 };
}

export function bucketsToMinutes(bucketCount: number): number {
  return bucketCount * TRAFFIC_BUCKET_MINUTES;
}

export type MeterTone = 'good' | 'warning' | 'critical' | 'idle';

/** Color is supplementary; the numeric success rate remains visible. */
export function toneForSuccessRate(rate: number | null): MeterTone {
  if (rate === null) return 'idle';
  if (rate >= 95) return 'good';
  if (rate >= 80) return 'warning';
  return 'critical';
}

/** Finer steps keep the axis close to the observed peak. */
const STEP_LADDER = [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10] as const;

export function niceCeil(value: number): number {
  if (value <= 0) return 1;
  const magnitude = 10 ** Math.floor(Math.log10(value));
  const normalized = value / magnitude;
  const step = STEP_LADDER.find((candidate) => normalized <= candidate) ?? 10;
  return step * magnitude;
}

/** Round the step before multiplying so grid lines stay on integers. */
export function axisMax(peak: number, intervals: number): number {
  if (peak <= 0 || intervals <= 0) return Math.max(1, intervals);
  const step = Math.max(1, Math.ceil(niceCeil(peak / intervals)));
  return step * intervals;
}
