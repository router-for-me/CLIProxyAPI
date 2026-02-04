import type {
  ApiKeyEntry,
  GeminiKeyConfig,
  ModelAlias,
  OpenAIProviderConfig,
  ProviderKeyConfig,
  AmpcodeConfig,
  AmpcodeModelMapping
} from '@/types';
import type { Config } from '@/types/config';
import { buildHeaderObject } from '@/utils/headers';

const normalizeBoolean = (value: any): boolean | undefined => {
  if (value === undefined || value === null) return undefined;
  if (typeof value === 'boolean') return value;
  if (typeof value === 'number') return value !== 0;
  if (typeof value === 'string') {
    const trimmed = value.trim().toLowerCase();
    if (['true', '1', 'yes', 'y', 'on'].includes(trimmed)) return true;
    if (['false', '0', 'no', 'n', 'off'].includes(trimmed)) return false;
  }
  return Boolean(value);
};

const normalizeModelAliases = (models: any): ModelAlias[] => {
  if (!Array.isArray(models)) return [];
  return models
    .map((item) => {
      if (!item) return null;
      const name = item.name || item.id || item.model;
      if (!name) return null;
      const alias = item.alias || item.display_name || item.displayName;
      const priority = item.priority ?? item['priority'];
      const testModel = item['test-model'] ?? item.testModel;
      const entry: ModelAlias = { name: String(name) };
      if (alias && alias !== name) {
        entry.alias = String(alias);
      }
      if (priority !== undefined) {
        entry.priority = Number(priority);
      }
      if (testModel) {
        entry.testModel = String(testModel);
      }
      return entry;
    })
    .filter(Boolean) as ModelAlias[];
};

const normalizeHeaders = (headers: any) => {
  if (!headers || typeof headers !== 'object') return undefined;
  const normalized = buildHeaderObject(headers as Record<string, string>);
  return Object.keys(normalized).length ? normalized : undefined;
};

const normalizeExcludedModels = (input: any): string[] => {
  const rawList = Array.isArray(input) ? input : typeof input === 'string' ? input.split(/[\n,]/) : [];
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

const normalizePrefix = (value: any): string | undefined => {
  if (value === undefined || value === null) return undefined;
  const trimmed = String(value).trim();
  return trimmed ? trimmed : undefined;
};

const normalizeApiKeyEntry = (entry: any): ApiKeyEntry | null => {
  if (!entry) return null;
  const apiKey = entry['api-key'] ?? entry.apiKey ?? entry.key ?? (typeof entry === 'string' ? entry : '');
  const trimmed = String(apiKey || '').trim();
  if (!trimmed) return null;

  const proxyUrl = entry['proxy-url'] ?? entry.proxyUrl;
  const headers = normalizeHeaders(entry.headers);

  return {
    apiKey: trimmed,
    proxyUrl: proxyUrl ? String(proxyUrl) : undefined,
    headers
  };
};

const normalizeProviderKeyConfig = (item: any): ProviderKeyConfig | null => {
  if (!item) return null;
  const apiKey = item['api-key'] ?? item.apiKey ?? (typeof item === 'string' ? item : '');
  const trimmed = String(apiKey || '').trim();
  if (!trimmed) return null;

  const config: ProviderKeyConfig = { apiKey: trimmed };
  const prefix = normalizePrefix(item.prefix ?? item['prefix']);
  if (prefix) config.prefix = prefix;
  const baseUrl = item['base-url'] ?? item.baseUrl;
  const proxyUrl = item['proxy-url'] ?? item.proxyUrl;
  if (baseUrl) config.baseUrl = String(baseUrl);
  if (proxyUrl) config.proxyUrl = String(proxyUrl);
  const headers = normalizeHeaders(item.headers);
  if (headers) config.headers = headers;
  const models = normalizeModelAliases(item.models);
  if (models.length) config.models = models;
  const excludedModels = normalizeExcludedModels(
    item['excluded-models'] ?? item.excludedModels ?? item['excluded_models'] ?? item.excluded_models
  );
  if (excludedModels.length) config.excludedModels = excludedModels;
  return config;
};

const normalizeGeminiKeyConfig = (item: any): GeminiKeyConfig | null => {
  if (!item) return null;
  let apiKey = item['api-key'] ?? item.apiKey;
  if (!apiKey && typeof item === 'string') {
    apiKey = item;
  }
  const trimmed = String(apiKey || '').trim();
  if (!trimmed) return null;

  const config: GeminiKeyConfig = { apiKey: trimmed };
  const prefix = normalizePrefix(item.prefix ?? item['prefix']);
  if (prefix) config.prefix = prefix;
  const baseUrl = item['base-url'] ?? item.baseUrl ?? item['base_url'];
  if (baseUrl) config.baseUrl = String(baseUrl);
  const headers = normalizeHeaders(item.headers);
  if (headers) config.headers = headers;
  const excludedModels = normalizeExcludedModels(item['excluded-models'] ?? item.excludedModels);
  if (excludedModels.length) config.excludedModels = excludedModels;
  return config;
};

const normalizeOpenAIProvider = (provider: any): OpenAIProviderConfig | null => {
  if (!provider || typeof provider !== 'object') return null;
  const name = provider.name || provider.id;
  const baseUrl = provider['base-url'] ?? provider.baseUrl;
  if (!name || !baseUrl) return null;

  let apiKeyEntries: ApiKeyEntry[] = [];
  if (Array.isArray(provider['api-key-entries'])) {
    apiKeyEntries = provider['api-key-entries']
      .map((entry: any) => normalizeApiKeyEntry(entry))
      .filter(Boolean) as ApiKeyEntry[];
  } else if (Array.isArray(provider['api-keys'])) {
    apiKeyEntries = provider['api-keys']
      .map((key: any) => normalizeApiKeyEntry({ 'api-key': key }))
      .filter(Boolean) as ApiKeyEntry[];
  }

  const headers = normalizeHeaders(provider.headers);
  const models = normalizeModelAliases(provider.models);
  const priority = provider.priority ?? provider['priority'];
  const testModel = provider['test-model'] ?? provider.testModel;

  const result: OpenAIProviderConfig = {
    name: String(name),
    baseUrl: String(baseUrl),
    apiKeyEntries
  };

  const prefix = normalizePrefix(provider.prefix ?? provider['prefix']);
  if (prefix) result.prefix = prefix;
  if (headers) result.headers = headers;
  if (models.length) result.models = models;
  if (priority !== undefined) result.priority = Number(priority);
  if (testModel) result.testModel = String(testModel);
  return result;
};

const normalizeOauthExcluded = (payload: any): Record<string, string[]> | undefined => {
  if (!payload || typeof payload !== 'object') return undefined;
  const source = payload['oauth-excluded-models'] ?? payload.items ?? payload;
  if (!source || typeof source !== 'object') return undefined;
  const map: Record<string, string[]> = {};
  Object.entries(source).forEach(([provider, models]) => {
    const key = String(provider || '').trim();
    if (!key) return;
    const normalized = normalizeExcludedModels(models);
    map[key.toLowerCase()] = normalized;
  });
  return map;
};

const normalizeAmpcodeModelMappings = (input: any): AmpcodeModelMapping[] => {
  if (!Array.isArray(input)) return [];
  const seen = new Set<string>();
  const mappings: AmpcodeModelMapping[] = [];

  input.forEach((entry) => {
    if (!entry || typeof entry !== 'object') return;
    const from = String(entry.from ?? entry['from'] ?? '').trim();
    const to = String(entry.to ?? entry['to'] ?? '').trim();
    if (!from || !to) return;
    const key = from.toLowerCase();
    if (seen.has(key)) return;
    seen.add(key);
    mappings.push({ from, to });
  });

  return mappings;
};

const normalizeAmpcodeConfig = (payload: any): AmpcodeConfig | undefined => {
  const source = payload?.ampcode ?? payload;
  if (!source || typeof source !== 'object') return undefined;

  const config: AmpcodeConfig = {};
  const upstreamUrl = source['upstream-url'] ?? source.upstreamUrl ?? source['upstream_url'];
  if (upstreamUrl) config.upstreamUrl = String(upstreamUrl);
  const upstreamApiKey = source['upstream-api-key'] ?? source.upstreamApiKey ?? source['upstream_api_key'];
  if (upstreamApiKey) config.upstreamApiKey = String(upstreamApiKey);

  const forceModelMappings = normalizeBoolean(
    source['force-model-mappings'] ?? source.forceModelMappings ?? source['force_model_mappings']
  );
  if (forceModelMappings !== undefined) {
    config.forceModelMappings = forceModelMappings;
  }

  const modelMappings = normalizeAmpcodeModelMappings(
    source['model-mappings'] ?? source.modelMappings ?? source['model_mappings']
  );
  if (modelMappings.length) {
    config.modelMappings = modelMappings;
  }

  return config;
};

/**
 * 规范化 /config 返回值
 */
export const normalizeConfigResponse = (raw: any): Config => {
  const config: Config = { raw: raw || {} };
  if (!raw || typeof raw !== 'object') {
    return config;
  }

  config.debug = raw.debug;
  config.proxyUrl = raw['proxy-url'] ?? raw.proxyUrl;
  config.requestRetry = raw['request-retry'] ?? raw.requestRetry;

  const quota = raw['quota-exceeded'] ?? raw.quotaExceeded;
  if (quota && typeof quota === 'object') {
    config.quotaExceeded = {
      switchProject: quota['switch-project'] ?? quota.switchProject,
      switchPreviewModel: quota['switch-preview-model'] ?? quota.switchPreviewModel
    };
  }

  config.usageStatisticsEnabled = raw['usage-statistics-enabled'] ?? raw.usageStatisticsEnabled;
  config.requestLog = raw['request-log'] ?? raw.requestLog;
  config.loggingToFile = raw['logging-to-file'] ?? raw.loggingToFile;
  config.logsMaxTotalSizeMb = raw['logs-max-total-size-mb'] ?? raw.logsMaxTotalSizeMb;
  config.wsAuth = raw['ws-auth'] ?? raw.wsAuth;
  config.forceModelPrefix = raw['force-model-prefix'] ?? raw.forceModelPrefix;
  const routing = raw.routing;
  if (routing && typeof routing === 'object') {
    config.routingStrategy = routing.strategy ?? routing['strategy'];
  } else {
    config.routingStrategy = raw['routing-strategy'] ?? raw.routingStrategy;
  }
  config.apiKeys = Array.isArray(raw['api-keys']) ? raw['api-keys'].slice() : raw.apiKeys;

  const geminiList = raw['gemini-api-key'] ?? raw.geminiApiKey ?? raw.geminiApiKeys;
  if (Array.isArray(geminiList)) {
    config.geminiApiKeys = geminiList
      .map((item: any) => normalizeGeminiKeyConfig(item))
      .filter(Boolean) as GeminiKeyConfig[];
  }

  const codexList = raw['codex-api-key'] ?? raw.codexApiKey ?? raw.codexApiKeys;
  if (Array.isArray(codexList)) {
    config.codexApiKeys = codexList
      .map((item: any) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const claudeList = raw['claude-api-key'] ?? raw.claudeApiKey ?? raw.claudeApiKeys;
  if (Array.isArray(claudeList)) {
    config.claudeApiKeys = claudeList
      .map((item: any) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const vertexList = raw['vertex-api-key'] ?? raw.vertexApiKey ?? raw.vertexApiKeys;
  if (Array.isArray(vertexList)) {
    config.vertexApiKeys = vertexList
      .map((item: any) => normalizeProviderKeyConfig(item))
      .filter(Boolean) as ProviderKeyConfig[];
  }

  const openaiList = raw['openai-compatibility'] ?? raw.openaiCompatibility ?? raw.openAICompatibility;
  if (Array.isArray(openaiList)) {
    config.openaiCompatibility = openaiList
      .map((item: any) => normalizeOpenAIProvider(item))
      .filter(Boolean) as OpenAIProviderConfig[];
  }

  const ampcode = normalizeAmpcodeConfig(raw.ampcode);
  if (ampcode) {
    config.ampcode = ampcode;
  }

  const oauthExcluded = normalizeOauthExcluded(raw['oauth-excluded-models'] ?? raw.oauthExcludedModels);
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
  normalizeAmpcodeConfig,
  normalizeAmpcodeModelMappings
};
