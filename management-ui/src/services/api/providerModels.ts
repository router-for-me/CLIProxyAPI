import type { ModelAlias } from '@/types/provider';
import { isRecord } from '@/utils/helpers';

const MODEL_BOOLEAN_FIELDS = [
  ['forceMapping', 'force-mapping'],
  ['isCompat', 'is-compat'],
  ['supportConfigurationUpdate', 'support-configuration-update'],
  ['useMaxCompletionTokens', 'use-max-completion-tokens'],
] as const;

export function normalizeModelOptions(raw: Record<string, unknown>): Partial<ModelAlias> {
  const result: Partial<ModelAlias> = {};
  if (typeof raw['display-name'] === 'string') result.displayName = raw['display-name'];
  const context = raw['max-context-length'];
  if (typeof context === 'number' && Number.isSafeInteger(context))
    result.maxContextLength = context;
  for (const [key, wire] of MODEL_BOOLEAN_FIELDS) {
    const value = raw[wire];
    if (typeof value === 'boolean') result[key] = value;
  }
  for (const [key, wire] of [
    ['inputModalities', 'input-modalities'],
    ['outputModalities', 'output-modalities'],
  ] as const) {
    const value = raw[wire];
    if (Array.isArray(value))
      result[key] = value.filter((item): item is string => typeof item === 'string');
  }
  return result;
}

/** Management v8 JSON is decoded as YAML; its field names follow the YAML tags. */
export function normalizeModelThinking(value: unknown): Record<string, unknown> | undefined {
  if (!isRecord(value)) return undefined;
  const result = { ...value };
  for (const [key, wire] of [
    ['zero_allowed', 'zero-allowed'],
    ['dynamic_allowed', 'dynamic-allowed'],
  ] as const) {
    if (wire in result) {
      result[key] = result[wire];
      delete result[wire];
    }
  }
  return result;
}

export function serializeModelThinking(value: Record<string, unknown>): Record<string, unknown> {
  const result = { ...value };
  for (const [key, wire] of [
    ['zero_allowed', 'zero-allowed'],
    ['dynamic_allowed', 'dynamic-allowed'],
  ] as const) {
    if (key in result) {
      result[wire] = result[key];
      delete result[key];
    }
  }
  return result;
}

export function serializeModelOptions(
  model: ModelAlias,
  openai = false,
  vertex = false
): Record<string, unknown> {
  const result: Record<string, unknown> = {};
  if (model.displayName !== undefined) result['display-name'] = model.displayName;
  if (!vertex && model.maxContextLength !== undefined)
    result['max-context-length'] = model.maxContextLength;
  for (const [key, wire] of MODEL_BOOLEAN_FIELDS) {
    if (vertex && key !== 'forceMapping') continue;
    if (!openai && key === 'useMaxCompletionTokens') continue;
    if (model[key] !== undefined) result[wire] = model[key];
  }
  if (openai) {
    if (model.inputModalities !== undefined) result['input-modalities'] = model.inputModalities;
    if (model.outputModalities !== undefined) result['output-modalities'] = model.outputModalities;
  }
  if (model.thinking !== undefined) result.thinking = serializeModelThinking(model.thinking);
  return result;
}
