import type { AmpcodeConfig, AmpcodeModelMapping, ApiKeyEntry } from '@/types';
import { buildCandidateUsageSourceIds, type KeyStatBucket, type KeyStats } from '@/utils/usage';
import type { AmpcodeFormState, ModelEntry } from './types';

export const DISABLE_ALL_MODELS_RULE = '*';

export const hasDisableAllModelsRule = (models?: string[]) =>
  Array.isArray(models) &&
  models.some((model) => String(model ?? '').trim() === DISABLE_ALL_MODELS_RULE);

export const stripDisableAllModelsRule = (models?: string[]) =>
  Array.isArray(models)
    ? models.filter((model) => String(model ?? '').trim() !== DISABLE_ALL_MODELS_RULE)
    : [];

export const withDisableAllModelsRule = (models?: string[]) => {
  const base = stripDisableAllModelsRule(models);
  return [...base, DISABLE_ALL_MODELS_RULE];
};

export const withoutDisableAllModelsRule = (models?: string[]) => {
  const base = stripDisableAllModelsRule(models);
  return base;
};

export const parseExcludedModels = (text: string): string[] =>
  text
    .split(/[\n,]+/)
    .map((item) => item.trim())
    .filter(Boolean);

export const excludedModelsToText = (models?: string[]) =>
  Array.isArray(models) ? models.join('\n') : '';

export const normalizeOpenAIBaseUrl = (baseUrl: string): string => {
  let trimmed = String(baseUrl || '').trim();
  if (!trimmed) return '';
  trimmed = trimmed.replace(/\/?v0\/management\/?$/i, '');
  trimmed = trimmed.replace(/\/+$/g, '');
  if (!/^https?:\/\//i.test(trimmed)) {
    trimmed = `http://${trimmed}`;
  }
  return trimmed;
};

export const buildOpenAIModelsEndpoint = (baseUrl: string): string => {
  const trimmed = normalizeOpenAIBaseUrl(baseUrl);
  if (!trimmed) return '';
  return `${trimmed}/models`;
};

export const buildOpenAIChatCompletionsEndpoint = (baseUrl: string): string => {
  const trimmed = normalizeOpenAIBaseUrl(baseUrl);
  if (!trimmed) return '';
  if (trimmed.endsWith('/chat/completions')) {
    return trimmed;
  }
  return `${trimmed}/chat/completions`;
};

// 根据 source (apiKey) 获取统计数据 - 与旧版逻辑一致
export const getStatsBySource = (
  apiKey: string,
  keyStats: KeyStats,
  prefix?: string
): KeyStatBucket => {
  const bySource = keyStats.bySource ?? {};
  const candidates = buildCandidateUsageSourceIds({ apiKey, prefix });
  if (!candidates.length) {
    return { success: 0, failure: 0 };
  }

  let success = 0;
  let failure = 0;
  candidates.forEach((candidate) => {
    const stats = bySource[candidate];
    if (!stats) return;
    success += stats.success;
    failure += stats.failure;
  });

  return { success, failure };
};

// 对于 OpenAI 提供商，汇总所有 apiKeyEntries 的统计 - 与旧版逻辑一致
export const getOpenAIProviderStats = (
  apiKeyEntries: ApiKeyEntry[] | undefined,
  keyStats: KeyStats,
  providerPrefix?: string
): KeyStatBucket => {
  const bySource = keyStats.bySource ?? {};

  const sourceIds = new Set<string>();
  buildCandidateUsageSourceIds({ prefix: providerPrefix }).forEach((id) => sourceIds.add(id));
  (apiKeyEntries || []).forEach((entry) => {
    buildCandidateUsageSourceIds({ apiKey: entry?.apiKey }).forEach((id) => sourceIds.add(id));
  });

  let success = 0;
  let failure = 0;
  sourceIds.forEach((id) => {
    const stats = bySource[id];
    if (!stats) return;
    success += stats.success;
    failure += stats.failure;
  });

  return { success, failure };
};

export const buildApiKeyEntry = (input?: Partial<ApiKeyEntry>): ApiKeyEntry => ({
  apiKey: input?.apiKey ?? '',
  proxyUrl: input?.proxyUrl ?? '',
  headers: input?.headers ?? {},
});

export const ampcodeMappingsToEntries = (mappings?: AmpcodeModelMapping[]): ModelEntry[] => {
  if (!Array.isArray(mappings) || mappings.length === 0) {
    return [{ name: '', alias: '' }];
  }
  return mappings.map((mapping) => ({
    name: mapping.from ?? '',
    alias: mapping.to ?? '',
  }));
};

export const entriesToAmpcodeMappings = (entries: ModelEntry[]): AmpcodeModelMapping[] => {
  const seen = new Set<string>();
  const mappings: AmpcodeModelMapping[] = [];

  entries.forEach((entry) => {
    const from = entry.name.trim();
    const to = entry.alias.trim();
    if (!from || !to) return;
    const key = from.toLowerCase();
    if (seen.has(key)) return;
    seen.add(key);
    mappings.push({ from, to });
  });

  return mappings;
};

export const buildAmpcodeFormState = (ampcode?: AmpcodeConfig | null): AmpcodeFormState => ({
  upstreamUrl: ampcode?.upstreamUrl ?? '',
  upstreamApiKey: '',
  forceModelMappings: ampcode?.forceModelMappings ?? false,
  mappingEntries: ampcodeMappingsToEntries(ampcode?.modelMappings),
});
