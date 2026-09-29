import type {
  ApiKeyEntry,
  CloakConfig,
  GeminiKeyConfig,
  ModelAlias,
  OpenAIProviderConfig,
  ProviderKeyConfig,
} from '@/types';
import type { Config } from '@/types/config';
import { buildHeaderObject } from '@/utils/headers';
import { isRecord } from '@/utils/helpers';
import { readCredentialWeight } from '@/utils/credentialWeight';

const normalizeBoolean = (value: unknown): boolean | undefined =>
  typeof value === 'boolean' ? value : undefined;

const normalizeRecord = (value: unknown): Record<string, unknown> | undefined =>
  isRecord(value) ? value : undefined;

const normalizeModelAliases = (models: unknown): ModelAlias[] => {
  if (!Array.isArray(models)) return [];
  return models
    .map((item) => {
      if (item === undefined || item === null) return null;
      if (typeof item === 'string') {
        const trimmed = item.trim();
        return trimmed ? ({ name: trimmed } satisfies ModelAlias) : null;
      }
      if (!isRecord(item)) return null;

      const name = item.name;
      if (!name) return null;
      const alias = item.alias;
      const priority = item.priority;
      const testModel = item['test-model'];
      const image = normalizeBoolean(item.image);
      const thinking = normalizeRecord(item.thinking);
      const entry: ModelAlias = { name: String(name) };
      if (alias && alias !== name) {
        entry.alias = String(alias);
      }
      if (priority !== undefined) {
        const parsed = Number(priority);
        if (Number.isFinite(parsed)) {
          entry.priority = parsed;
        }
      }
      if (testModel) {
        entry.testModel = String(testModel);
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

const normalizeProviderKeyConfig = (item: unknown): ProviderKeyConfig | null => {
  if (item === undefined || item === null) return null;
  const record = isRecord(item) ? item : null;
  const apiKey = record?.['api-key'] ?? (typeof item === 'string' ? item : '');
  const trimmed = String(apiKey || '').trim();
  if (!trimmed) return null;

  const config: ProviderKeyConfig = { apiKey: trimmed };
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

  const config: GeminiKeyConfig = { apiKey: trimmed };
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

  const apiKeyEntries = Array.isArray(provider['api-key-entries'])
    ? (provider['api-key-entries']
        .map((entry) => normalizeApiKeyEntry(entry))
        .filter(Boolean) as ApiKeyEntry[])
    : [];

  const headers = normalizeHeaders(provider.headers);
  const models = normalizeModelAliases(provider.models);
  const priority = provider.priority;
  const testModel = provider['test-model'];

  const result: OpenAIProviderConfig = {
    name: String(name),
    baseUrl: String(baseUrl),
    apiKeyEntries,
  };

  const disabled = normalizeBoolean(provider.disabled);
  if (disabled !== undefined) result.disabled = disabled;
  const disableCooling = normalizeBoolean(provider['disable-cooling']);
  if (disableCooling !== undefined) result.disableCooling = disableCooling;
  const prefix = normalizePrefix(provider.prefix);
  if (prefix) result.prefix = prefix;
  if (headers) result.headers = headers;
  if (models.length) result.models = models;
  if (priority !== undefined) result.priority = Number(priority);
  if (testModel) result.testModel = String(testModel);
  const authIndex = normalizeAuthIndex(provider['auth-index']);
  if (authIndex) result.authIndex = authIndex;
  if (sourceIndex !== undefined) result.sourceIndex = sourceIndex;
  return result;
};

const normalizeOauthExcluded = (payload: unknown): Record<string, string[]> | undefined => {
  if (!isRecord(payload)) return undefined;
  const source = payload['oauth-excluded-models'] ?? payload.items ?? payload;
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

/**
 * 规范化 /config 返回值
 */
export const normalizeConfigResponse = (raw: unknown): Config => {
  const config: Config = { raw: isRecord(raw) ? raw : {} };
  if (!isRecord(raw)) {
    return config;
  }

  config.debug = normalizeBoolean(raw.debug);
  const proxyUrl = raw['proxy-url'];
  config.proxyUrl =
    typeof proxyUrl === 'string'
      ? proxyUrl
      : proxyUrl === undefined || proxyUrl === null
        ? undefined
        : String(proxyUrl);
  const requestRetry = raw['request-retry'];
  if (typeof requestRetry === 'number' && Number.isFinite(requestRetry)) {
    config.requestRetry = requestRetry;
  } else if (typeof requestRetry === 'string' && requestRetry.trim() !== '') {
    const parsed = Number(requestRetry);
    if (Number.isFinite(parsed)) {
      config.requestRetry = parsed;
    }
  }

  const quota = raw['quota-exceeded'];
  if (isRecord(quota)) {
    config.quotaExceeded = {
      switchProject: normalizeBoolean(quota['switch-project']),
      switchPreviewModel: normalizeBoolean(quota['switch-preview-model']),
      antigravityCredits: normalizeBoolean(quota['antigravity-credits']),
    };
  }

  config.requestLog = normalizeBoolean(raw['request-log']);
  config.loggingToFile = normalizeBoolean(raw['logging-to-file']);
  const logsMaxTotalSizeMb = raw['logs-max-total-size-mb'];
  if (typeof logsMaxTotalSizeMb === 'number' && Number.isFinite(logsMaxTotalSizeMb)) {
    config.logsMaxTotalSizeMb = logsMaxTotalSizeMb;
  } else if (typeof logsMaxTotalSizeMb === 'string' && logsMaxTotalSizeMb.trim() !== '') {
    const parsed = Number(logsMaxTotalSizeMb);
    if (Number.isFinite(parsed)) {
      config.logsMaxTotalSizeMb = parsed;
    }
  }
  config.wsAuth = normalizeBoolean(raw['ws-auth']);
  config.forceModelPrefix = normalizeBoolean(raw['force-model-prefix']);
  const routing = raw.routing;
  const strategyRaw = isRecord(routing) ? routing.strategy : undefined;
  if (strategyRaw !== undefined && strategyRaw !== null) {
    config.routingStrategy = String(strategyRaw);
  }
  const apiKeysRaw = raw['api-keys'];
  if (Array.isArray(apiKeysRaw)) {
    config.apiKeys = apiKeysRaw.map((key) => String(key)).filter((key) => key.trim() !== '');
  }

  const geminiList = raw['gemini-api-key'];
  if (Array.isArray(geminiList)) {
    config.geminiApiKeys = geminiList
      .map((item) => normalizeGeminiKeyConfig(item))
      .filter(Boolean) as GeminiKeyConfig[];
  }

  const interactionsList = raw['interactions-api-key'];
  if (Array.isArray(interactionsList)) {
    config.interactionsApiKeys = interactionsList
      .map((item) => normalizeGeminiKeyConfig(item))
      .filter(Boolean) as GeminiKeyConfig[];
  }

  const codexList = raw['codex-api-key'];
  if (Array.isArray(codexList)) {
    config.codexApiKeys = codexList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const metaList = raw['meta-api-key'];
  if (Array.isArray(metaList)) {
    config.metaApiKeys = metaList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const xaiList = raw['xai-api-key'];
  if (Array.isArray(xaiList)) {
    config.xaiApiKeys = xaiList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const claudeList = raw['claude-api-key'];
  if (Array.isArray(claudeList)) {
    config.claudeApiKeys = claudeList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const mirasimList = raw['mirasim-api-key'];
  if (Array.isArray(mirasimList)) {
    config.mirasimApiKeys = mirasimList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const vertexList = raw['vertex-api-key'];
  if (Array.isArray(vertexList)) {
    config.vertexApiKeys = vertexList
      .map((item) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const openaiList = raw['openai-compatibility'];
  if (Array.isArray(openaiList)) {
    config.openaiCompatibility = openaiList
      .map((item, index) => normalizeOpenAIProvider(item, index))
      .filter(Boolean) as OpenAIProviderConfig[];
  }

  const oauthExcluded = normalizeOauthExcluded(raw['oauth-excluded-models']);
  if (oauthExcluded) {
    config.oauthExcludedModels = oauthExcluded;
  }

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
