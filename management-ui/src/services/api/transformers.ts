import type {
  ApiKeyEntry,
  CloakConfig,
  GeminiKeyConfig,
  ModelAlias,
  OpenAIProviderConfig,
  ProviderKeyConfig,
} from '@/types';
import type { Config } from '@/types/config';
import type { ProviderRuntimePolicy, RequestScopedErrorRule } from '@/types/provider';
import { buildHeaderObject } from '@/utils/headers';
import { isRecord } from '@/utils/helpers';
import { readCredentialWeight } from '@/utils/credentialWeight';
import { normalizeModelOptions, normalizeModelThinking } from './providerModels';

const normalizeBoolean = (value: unknown): boolean | undefined =>
  typeof value === 'boolean' ? value : undefined;

const normalizeModelAliases = (models: unknown): ModelAlias[] => {
  if (!Array.isArray(models)) return [];
  return models
    .map((item, sourceIndex) => {
      if (item === undefined || item === null) return null;
      if (typeof item === 'string') {
        const trimmed = item.trim();
        return trimmed ? ({ name: trimmed, sourceIndex } satisfies ModelAlias) : null;
      }
      if (!isRecord(item)) return null;

      const name = item.name;
      if (!name) return null;
      const alias = item.alias;
      const priority = item.priority;
      const image = normalizeBoolean(item.image);
      const thinking = normalizeModelThinking(item.thinking);
      const entry: ModelAlias = { name: String(name), sourceIndex, ...normalizeModelOptions(item) };
      if (alias) {
        entry.alias = String(alias);
      }
      if (priority !== undefined) {
        const parsed = Number(priority);
        if (Number.isFinite(parsed)) {
          entry.priority = parsed;
        }
      }
      if (image !== undefined) {
        entry.image = image;
      }
      if (thinking) {
        entry.thinking = thinking;
      }
      return entry;
    })
    .filter(Boolean) as ModelAlias[];
};

const normalizeHeaders = (headers: unknown) => {
  if (!headers || typeof headers !== 'object') return undefined;
  const normalized = buildHeaderObject(
    Array.isArray(headers)
      ? (headers as Array<{ key: string; value: string }>)
      : (headers as Record<string, string | undefined | null>)
  );
  return Object.keys(normalized).length ? normalized : undefined;
};

const normalizeExcludedModels = (input: unknown): string[] => {
  const rawList = Array.isArray(input)
    ? input
    : typeof input === 'string'
      ? input.split(/[\n,]/)
      : [];
  const seen = new Set<string>();
  const normalized: string[] = [];

  rawList.forEach((item) => {
    const trimmed = String(item ?? '').trim();
    if (!trimmed) return;
    const key = trimmed.toLowerCase();
    if (seen.has(key)) return;
    seen.add(key);
    normalized.push(trimmed);
  });

  return normalized;
};

const normalizePrefix = (value: unknown): string | undefined => {
  if (value === undefined || value === null) return undefined;
  const trimmed = String(value).trim();
  return trimmed ? trimmed : undefined;
};

const normalizeAuthIndex = (value: unknown): string | undefined => {
  if (value === undefined || value === null) return undefined;
  const trimmed = String(value).trim();
  return trimmed ? trimmed : undefined;
};

const normalizeApiKeyEntry = (entry: unknown): ApiKeyEntry | null => {
  if (entry === undefined || entry === null) return null;
  const record = isRecord(entry) ? entry : null;
  const apiKey = record?.['api-key'] ?? (typeof entry === 'string' ? entry : '');
  const trimmed = String(apiKey || '').trim();
  if (!trimmed) return null;

  const proxyUrl = record?.['proxy-url'];
  const weight = readCredentialWeight(record?.weight);
  const authIndex = normalizeAuthIndex(record?.['auth-index']);

  const result: ApiKeyEntry = {
    apiKey: trimmed,
    proxyUrl: proxyUrl ? String(proxyUrl) : undefined,
  };
  if (weight !== undefined) result.weight = weight;
  if (authIndex) result.authIndex = authIndex;
  return result;
};

const normalizeRuntimePolicy = (record: Record<string, unknown> | null): ProviderRuntimePolicy => {
  const policy: ProviderRuntimePolicy = {};
  const retry = record?.['request-retry'];
  if (typeof retry === 'number' && Number.isSafeInteger(retry)) policy.requestRetry = retry;
  const rules = record?.['request-scoped-errors'];
  if (Array.isArray(rules)) {
    policy.requestScopedErrors = rules.filter(isRecord).map((rule) => ({
      ...(typeof rule.status === 'number' ? { status: rule.status } : {}),
      ...(Array.isArray(rule.match)
        ? { match: rule.match.filter((v): v is string => typeof v === 'string') }
        : {}),
      ...(Array.isArray(rule['match-regexr'])
        ? { matchRegex: rule['match-regexr'].filter((v): v is string => typeof v === 'string') }
        : {}),
      ...(typeof rule.action === 'string'
        ? { action: rule.action as RequestScopedErrorRule['action'] }
        : {}),
    }));
  }
  return policy;
};

const normalizeProviderKeyConfig = (item: unknown): ProviderKeyConfig | null => {
  if (item === undefined || item === null) return null;
  const record = isRecord(item) ? item : null;
  const apiKey = record?.['api-key'] ?? (typeof item === 'string' ? item : '');
  const trimmed = String(apiKey || '').trim();
  if (!trimmed) return null;

  const config: ProviderKeyConfig = { apiKey: trimmed, ...normalizeRuntimePolicy(record) };
  for (const [key, wire] of [
    ['alphaSearch', 'alpha-search'],
    ['disableCodexCloaking', 'disable-codex-cloaking'],
    ['rebuildMidSystemMessage', 'rebuild-mid-system-message'],
  ] as const) {
    const value = normalizeBoolean(record?.[wire]);
    if (value !== undefined) config[key] = value;
  }
  const weight = readCredentialWeight(record?.weight);
  if (weight !== undefined) config.weight = weight;
  const priority = record?.priority;
  if (priority !== undefined && priority !== null && String(priority).trim() !== '') {
    const parsed = Number(priority);
    if (Number.isFinite(parsed)) {
      config.priority = parsed;
    }
  }
  const prefix = normalizePrefix(record?.prefix);
  if (prefix) config.prefix = prefix;
  const baseUrl = record?.['base-url'];
  const proxyUrl = record?.['proxy-url'];
  if (baseUrl) config.baseUrl = String(baseUrl);
  const websockets = normalizeBoolean(record?.websockets);
  if (websockets !== undefined) config.websockets = websockets;
  if (proxyUrl) config.proxyUrl = String(proxyUrl);
  const disableCooling = normalizeBoolean(record?.['disable-cooling']);
  if (disableCooling !== undefined) config.disableCooling = disableCooling;
  const headers = normalizeHeaders(record?.headers);
  if (headers) config.headers = headers;
  const models = normalizeModelAliases(record?.models);
  if (models.length) config.models = models;
  const excludedModels = normalizeExcludedModels(record?.['excluded-models']);
  if (excludedModels.length) config.excludedModels = excludedModels;
  const authIndex = normalizeAuthIndex(record?.['auth-index']);
  if (authIndex) config.authIndex = authIndex;

  const cloakRaw = record?.cloak;
  if (isRecord(cloakRaw)) {
    const cloak: CloakConfig = {};
    const mode = cloakRaw.mode;
    if (typeof mode === 'string' && mode.trim()) {
      cloak.mode = mode.trim();
    }
    const strictMode = normalizeBoolean(cloakRaw['strict-mode']);
    if (strictMode !== undefined) {
      cloak.strictMode = strictMode;
    }
    const sensitiveWords = normalizeExcludedModels(cloakRaw['sensitive-words']);
    if (sensitiveWords.length) {
      cloak.sensitiveWords = sensitiveWords;
    }
    const cacheUserId = normalizeBoolean(cloakRaw['cache-user-id']);
    if (cacheUserId !== undefined) {
      cloak.cacheUserId = cacheUserId;
    }
    if (Object.keys(cloak).length) {
      config.cloak = cloak;
    }
  }
  const fingerprintProfile = record?.['fingerprint-profile'];
  if (typeof fingerprintProfile === 'string' && fingerprintProfile.trim()) {
    config.fingerprintProfile = fingerprintProfile.trim();
  }

  return config;
};

const normalizeGeminiKeyConfig = (item: unknown): GeminiKeyConfig | null => {
  if (item === undefined || item === null) return null;
  const record = isRecord(item) ? item : null;
  let apiKey = record?.['api-key'];
  if (!apiKey && typeof item === 'string') {
    apiKey = item;
  }
  const trimmed = String(apiKey || '').trim();
  if (!trimmed) return null;

  const config: GeminiKeyConfig = { apiKey: trimmed, ...normalizeRuntimePolicy(record) };
  const weight = readCredentialWeight(record?.weight);
  if (weight !== undefined) config.weight = weight;
  const priority = record?.priority;
  if (priority !== undefined && priority !== null && String(priority).trim() !== '') {
    const parsed = Number(priority);
    if (Number.isFinite(parsed)) {
      config.priority = parsed;
    }
  }
  const prefix = normalizePrefix(record?.prefix);
  if (prefix) config.prefix = prefix;
  const baseUrl = record?.['base-url'];
  if (baseUrl) config.baseUrl = String(baseUrl);
  const proxyUrl = record?.['proxy-url'];
  if (proxyUrl) config.proxyUrl = String(proxyUrl);
  const disableCooling = normalizeBoolean(record?.['disable-cooling']);
  if (disableCooling !== undefined) config.disableCooling = disableCooling;
  const models = normalizeModelAliases(record?.models);
  if (models.length) config.models = models;
  const headers = normalizeHeaders(record?.headers);
  if (headers) config.headers = headers;
  const excludedModels = normalizeExcludedModels(record?.['excluded-models']);
  if (excludedModels.length) config.excludedModels = excludedModels;
  const authIndex = normalizeAuthIndex(record?.['auth-index']);
  if (authIndex) config.authIndex = authIndex;
  return config;
};

const normalizeOpenAIProvider = (
  provider: unknown,
  sourceIndex?: number
): OpenAIProviderConfig | null => {
  if (!isRecord(provider)) return null;
  const name = provider.name;
  const baseUrl = provider['base-url'];
  if (!name || !baseUrl) return null;

  const apiKeyEntries = Array.isArray(provider.keys)
    ? (provider.keys
        .map((entry, sourceIndex) => {
          const normalized = normalizeApiKeyEntry(entry);
          return normalized ? { ...normalized, sourceIndex } : null;
        })
        .filter(Boolean) as ApiKeyEntry[])
    : [];

  const headers = normalizeHeaders(provider.headers);
  const models = normalizeModelAliases(provider.models);
  const priority = provider.priority;

  const result: OpenAIProviderConfig = {
    name: String(name),
    baseUrl: String(baseUrl),
    apiKeyEntries,
    ...normalizeRuntimePolicy(provider),
  };

  const supportPromptCacheKey = normalizeBoolean(provider['support-prompt-cache-key']);
  if (supportPromptCacheKey !== undefined) result.supportPromptCacheKey = supportPromptCacheKey;
  const disabled = normalizeBoolean(provider.disabled);
  if (disabled !== undefined) result.disabled = disabled;
  const disableCooling = normalizeBoolean(provider['disable-cooling']);
  if (disableCooling !== undefined) result.disableCooling = disableCooling;
  const prefix = normalizePrefix(provider.prefix);
  if (prefix) result.prefix = prefix;
  if (headers) result.headers = headers;
  if (models.length) result.models = models;
  if (priority !== undefined) result.priority = Number(priority);
  const authIndex = normalizeAuthIndex(provider['auth-index']);
  if (authIndex) result.authIndex = authIndex;
  if (sourceIndex !== undefined) result.sourceIndex = sourceIndex;
  return result;
};

const normalizeOauthExcluded = (payload: unknown): Record<string, string[]> | undefined => {
  if (!isRecord(payload)) return undefined;
  const source = payload;
  if (!isRecord(source)) return undefined;
  const map: Record<string, string[]> = {};
  Object.entries(source).forEach(([provider, models]) => {
    const key = String(provider || '').trim();
    if (!key) return;
    const normalized = normalizeExcludedModels(models);
    map[key.toLowerCase()] = normalized;
  });
  return map;
};

/** Resolve effective values for display only; retain the complete persisted group for writes. */
export const normalizeProviderGroups = (groups: unknown, openai = false) => {
  if (!Array.isArray(groups)) return [];
  return groups.flatMap<ProviderKeyConfig | OpenAIProviderConfig>((group, groupIndex) => {
    if (!isRecord(group) || !Array.isArray(group.keys)) return [];
    if (openai) {
      const config = normalizeOpenAIProvider(group, groupIndex);
      return config ? [{ ...config, source: { groupIndex, group, groups } }] : [];
    }
    return group.keys.flatMap((key, keyIndex) => {
      if (!isRecord(key)) return [];
      const effective = { ...group };
      delete effective.keys;
      delete effective.name;
      Object.entries(key).forEach(([field, value]) => {
        if (value !== null) effective[field] = value;
      });
      const config = normalizeProviderKeyConfig(effective);
      return config ? [{ ...config, source: { groupIndex, keyIndex, group, groups } }] : [];
    });
  });
};

export const normalizeConfigResponse = (raw: unknown): Config => {
  const config: Config = { raw: isRecord(raw) ? raw : {} };
  if (!isRecord(raw)) return config;
  const at = (path: string): unknown =>
    path
      .split('.')
      .reduce<unknown>((value, key) => (isRecord(value) ? value[key] : undefined), raw);
  config.debug = normalizeBoolean(at('observability.logs.debug'));
  config.requestLog = normalizeBoolean(at('observability.logs.request-log'));
  config.loggingToFile = normalizeBoolean(at('observability.logs.logging-to-file'));
  const size = at('observability.logs.logs-max-total-size-mb');
  if (typeof size === 'number') config.logsMaxTotalSizeMb = size;
  const proxy = at('requests.proxy-url');
  if (typeof proxy === 'string') config.proxyUrl = proxy;
  const retry = at('routing.retry.request-retry');
  if (typeof retry === 'number') config.requestRetry = retry;
  config.wsAuth = normalizeBoolean(at('oauth.providers.aistudio.ws-auth')) ?? true;
  config.forceModelPrefix = normalizeBoolean(at('routing.force-model-prefix'));
  const strategy = at('routing.strategy');
  if (typeof strategy === 'string') config.routingStrategy = strategy;
  const keys = at('access.api-keys');
  config.apiKeys = Array.isArray(keys)
    ? keys.filter((key): key is string => typeof key === 'string')
    : [];
  config.quotaExceeded = {
    antigravityCredits:
      normalizeBoolean(at('oauth.providers.antigravity.antigravity-credits')) ?? false,
  };
  config.providerGroups = isRecord(raw['api-keys']) ? raw['api-keys'] : {};
  config.geminiApiKeys = normalizeProviderGroups(at('api-keys.gemini')) as ProviderKeyConfig[];
  config.interactionsApiKeys = normalizeProviderGroups(
    at('api-keys.interactions')
  ) as ProviderKeyConfig[];
  config.codexApiKeys = normalizeProviderGroups(at('api-keys.codex')) as ProviderKeyConfig[];
  config.metaApiKeys = normalizeProviderGroups(at('api-keys.meta')) as ProviderKeyConfig[];
  config.xaiApiKeys = normalizeProviderGroups(at('api-keys.xai')) as ProviderKeyConfig[];
  config.claudeApiKeys = normalizeProviderGroups(at('api-keys.claude')) as ProviderKeyConfig[];
  config.vertexApiKeys = normalizeProviderGroups(at('api-keys.vertex')) as ProviderKeyConfig[];
  config.openaiCompatibility = normalizeProviderGroups(
    at('api-keys.openai-compatibility'),
    true
  ) as OpenAIProviderConfig[];
  config.oauthExcludedModels = normalizeOauthExcluded(at('oauth.excluded-models'));
  return config;
};

export {
  normalizeApiKeyEntry,
  normalizeGeminiKeyConfig,
  normalizeModelAliases,
  normalizeOpenAIProvider,
  normalizeProviderKeyConfig,
  normalizeHeaders,
  normalizeExcludedModels,
};
