import type { ModelAlias } from '@/types';
import type { ModelEntryInput } from './types';
import { buildThinkingFromLevels, readThinkingLevels, type ThinkingLevel } from './thinkingLevels';

export interface ModelOptionsInput {
  displayName?: string;
  maxContextLength?: string;
  forceMapping?: boolean;
  isCompat?: boolean;
  supportConfigurationUpdate?: boolean;
  inputModalitiesText?: string;
  outputModalitiesText?: string;
  originalInputModalities?: string[];
  originalOutputModalities?: string[];
  inputModalitiesTouched?: boolean;
  outputModalitiesTouched?: boolean;
  useMaxCompletionTokens?: boolean;
  thinkingEnabled?: boolean;
  thinkingMin?: string;
  thinkingMax?: string;
  thinkingZeroAllowed?: boolean;
  thinkingDynamicAllowed?: boolean;
  thinkingBudgetTouched?: boolean;
}

export function readModelOptions(model: ModelAlias): ModelOptionsInput & {
  thinkingLevels: ThinkingLevel[];
} {
  const thinking = model.thinking;
  return {
    displayName: model.displayName,
    maxContextLength: model.maxContextLength?.toString(),
    forceMapping: model.forceMapping,
    isCompat: model.isCompat,
    supportConfigurationUpdate: model.supportConfigurationUpdate,
    inputModalitiesText: model.inputModalities?.join(', '),
    outputModalitiesText: model.outputModalities?.join(', '),
    originalInputModalities: model.inputModalities,
    originalOutputModalities: model.outputModalities,
    useMaxCompletionTokens: model.useMaxCompletionTokens,
    thinkingEnabled: thinking !== undefined,
    thinkingMin: typeof thinking?.min === 'number' ? String(thinking.min) : undefined,
    thinkingMax: typeof thinking?.max === 'number' ? String(thinking.max) : undefined,
    thinkingZeroAllowed:
      typeof thinking?.zero_allowed === 'boolean' ? thinking.zero_allowed : undefined,
    thinkingDynamicAllowed:
      typeof thinking?.dynamic_allowed === 'boolean' ? thinking.dynamic_allowed : undefined,
    // Budget allowances are not discrete effort levels.
    thinkingLevels: readThinkingLevels({ levels: thinking?.levels }),
  };
}

function parseThinking(raw: string | undefined): Record<string, unknown> | undefined {
  if (!raw?.trim()) return undefined;
  const value: unknown = JSON.parse(raw);
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error('Invalid thinking object');
  }
  return value as Record<string, unknown>;
}

const numberValue = (value: string | undefined) =>
  value?.trim() ? Number(value.trim()) : undefined;
const modalities = (value: string | undefined) => {
  const parts = value
    ?.trim()
    .split(/[,\s]+/)
    .filter(Boolean);
  return parts?.length ? parts : undefined;
};

export function buildModelOptions(entry: ModelEntryInput): Partial<ModelAlias> {
  let thinking: Record<string, unknown> | undefined;
  if (entry.thinkingEnabled !== false) {
    thinking = parseThinking(entry.thinkingJson);
    if (
      entry.thinkingEnabled === true ||
      entry.thinkingLevelsTouched ||
      entry.thinkingBudgetTouched
    ) {
      thinking = { ...thinking };
      if (entry.thinkingBudgetTouched || !entry.thinkingJson?.trim()) {
        const budget = {
          min: numberValue(entry.thinkingMin),
          max: numberValue(entry.thinkingMax),
          zero_allowed: entry.thinkingZeroAllowed,
          dynamic_allowed: entry.thinkingDynamicAllowed,
        };
        for (const [key, value] of Object.entries(budget)) {
          if (value === undefined) delete thinking[key];
          else thinking[key] = value;
        }
      }
      if (entry.thinkingLevelsTouched) {
        const levels = buildThinkingFromLevels(entry.thinkingLevels)?.levels;
        if (levels === undefined) delete thinking.levels;
        else thinking.levels = levels;
      }
    }
  }
  return {
    displayName: entry.displayName?.trim() || undefined,
    maxContextLength: numberValue(entry.maxContextLength),
    forceMapping: entry.forceMapping,
    isCompat: entry.isCompat,
    supportConfigurationUpdate: entry.supportConfigurationUpdate,
    inputModalities:
      !entry.inputModalitiesTouched && entry.originalInputModalities !== undefined
        ? entry.originalInputModalities
        : modalities(entry.inputModalitiesText),
    outputModalities:
      !entry.outputModalitiesTouched && entry.originalOutputModalities !== undefined
        ? entry.originalOutputModalities
        : modalities(entry.outputModalitiesText),
    useMaxCompletionTokens: entry.useMaxCompletionTokens,
    thinking,
  };
}

const validInteger = (value: string | undefined) => {
  if (!value?.trim()) return true;
  return /^\d+$/.test(value.trim()) && Number.isSafeInteger(Number(value));
};

export function validateModelOptions(entries: ModelEntryInput[]): string | null {
  for (const entry of entries) {
    if (!entry.name.trim()) continue;
    if (!validInteger(entry.maxContextLength)) return 'providersPage.modelOptions.invalidContext';
    if (entry.thinkingEnabled === false) continue;
    let thinking: Record<string, unknown> | undefined;
    try {
      thinking = buildModelOptions(entry).thinking;
    } catch {
      return 'providersPage.modelOptions.invalidThinking';
    }
    if (entry.thinkingBudgetTouched || !entry.thinkingJson?.trim()) {
      if (!validInteger(entry.thinkingMin) || !validInteger(entry.thinkingMax)) {
        return 'providersPage.modelOptions.invalidBudget';
      }
    }
    const min = thinking?.min;
    const max = thinking?.max;
    if (
      [min, max].some(
        (value) =>
          value != null && (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0)
      )
    )
      return 'providersPage.modelOptions.invalidBudget';
    // Go uses zero for omitted bounds. Only min=max=0 means no numeric range;
    // a zero maximum with a positive minimum is not an unlimited budget.
    // Keep pre-existing configurations editable when their budget was not touched.
    if (
      (entry.thinkingBudgetTouched || !entry.thinkingJson?.trim()) &&
      Number(min ?? 0) > Number(max ?? 0)
    ) {
      return 'providersPage.modelOptions.invalidRange';
    }
  }
  return null;
}
