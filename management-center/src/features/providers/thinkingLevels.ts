export const THINKING_LEVELS = [
  'none',
  'minimal',
  'low',
  'medium',
  'high',
  'xhigh',
  'max',
  'auto',
] as const;

export type ThinkingLevel = (typeof THINKING_LEVELS)[number];

const THINKING_LEVEL_SET = new Set<string>(THINKING_LEVELS);
const SERIALIZED_LEVEL_ORDER: readonly ThinkingLevel[] = [
  'minimal',
  'low',
  'medium',
  'high',
  'xhigh',
  'max',
  'none',
  'auto',
];

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

export const readThinkingLevels = (value: unknown): ThinkingLevel[] => {
  if (!isRecord(value)) return [];

  const selected = new Set<ThinkingLevel>();
  if (Array.isArray(value.levels)) {
    value.levels.forEach((rawLevel) => {
      if (typeof rawLevel !== 'string') return;
      const level = rawLevel.trim().toLowerCase();
      if (THINKING_LEVEL_SET.has(level)) selected.add(level as ThinkingLevel);
    });
  }
  if (value.zero_allowed === true) selected.add('none');
  if (value.dynamic_allowed === true) selected.add('auto');

  return THINKING_LEVELS.filter((level) => selected.has(level));
};

export const buildThinkingFromLevels = (
  levels: readonly ThinkingLevel[] | undefined
): Record<string, unknown> | undefined => {
  if (!levels?.length) return undefined;
  const selected = new Set(levels);
  return {
    levels: SERIALIZED_LEVEL_ORDER.filter((level) => selected.has(level)),
  };
};
