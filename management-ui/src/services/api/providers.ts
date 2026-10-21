import { useAuthStore } from '@/stores/useAuthStore';
import { apiClient } from './client';
import { guardConfigConnection } from './configValue';
import { isRecord } from '@/utils/helpers';
import { normalizeApiKeyEntry, normalizeProviderGroups } from './transformers';
import { serializeModelOptions } from './providerModels';
import type {
  GeminiKeyConfig,
  OpenAIProviderConfig,
  ProviderKeyConfig,
  ApiKeyEntry,
  ModelAlias,
} from '@/types';
import type { ProviderSource, ProviderRuntimePolicy } from '@/types/provider';

const serializeHeaders = (headers?: Record<string, string>) =>
  headers && Object.keys(headers).length ? headers : undefined;
const serializeModelAliases = (models?: ModelAlias[], includeOpenAIFields = false) =>
  Array.isArray(models)
    ? models
        .map((model) => {
          if (!model?.name) return null;
          const payload: Record<string, unknown> = {
            name: model.name,
            ...serializeModelOptions(model, includeOpenAIFields),
          };
          if (model.alias) {
            payload.alias = model.alias;
          }
          if (model.priority !== undefined) {
            payload.priority = model.priority;
          }
          if (includeOpenAIFields && model.image) {
            payload.image = true;
          }
          return payload;
        })
        .filter(Boolean)
    : undefined;

const serializeApiKeyEntry = (entry: ApiKeyEntry) => {
  const payload: Record<string, unknown> = { 'api-key': entry.apiKey };
  if (entry.proxyUrl) payload['proxy-url'] = entry.proxyUrl;
  if (entry.weight !== undefined) payload.weight = entry.weight;
  return payload;
};

const serializeRuntimePolicy = (config: ProviderRuntimePolicy, supportsErrors = true) => {
  const payload: Record<string, unknown> = {};
  if (config.requestRetry !== undefined) payload['request-retry'] = config.requestRetry;
  if (supportsErrors && config.requestScopedErrors !== undefined) {
    payload['request-scoped-errors'] = config.requestScopedErrors.map((rule) => ({
      ...(rule.status !== undefined ? { status: rule.status } : {}),
      ...(rule.match !== undefined ? { match: rule.match } : {}),
      ...(rule.matchRegex !== undefined ? { 'match-regexr': rule.matchRegex } : {}),
      ...(rule.action !== undefined ? { action: rule.action } : {}),
    }));
  }
  return payload;
};

/** Restore parent inheritance without turning untouched nulls into persisted defaults. */
const applyPolicyIntent = (
  next: Record<string, unknown>,
  raw: Record<string, unknown>,
  config: ProviderRuntimePolicy,
  after: Record<string, unknown>
) => {
  if (!config.inheritFields) return;
  for (const field of ['disable-cooling', 'request-retry', 'request-scoped-errors'] as const) {
    if (config.inheritFields.includes(field)) {
      if (raw[field] === null) next[field] = null;
      else delete next[field];
    } else if (after[field] !== undefined && raw[field] == null) {
      // An explicit override equal to the inherited effective value is still a change.
      next[field] = after[field];
    }
  }
};

const serializeProviderKey = (config: ProviderKeyConfig, family: ProviderFamily) => {
  const payload: Record<string, unknown> = {
    'api-key': config.apiKey,
    ...serializeRuntimePolicy(config),
  };
  if (config.priority !== undefined) payload.priority = config.priority;
  if (config.weight !== undefined) payload.weight = config.weight;
  if (config.prefix?.trim()) payload.prefix = config.prefix.trim();
  if (config.baseUrl) payload['base-url'] = config.baseUrl;
  if (config.websockets !== undefined) payload.websockets = config.websockets;
  if (family === 'codex') {
    if (config.alphaSearch !== undefined) payload['alpha-search'] = config.alphaSearch;
    if (config.disableCodexCloaking !== undefined)
      payload['disable-codex-cloaking'] = config.disableCodexCloaking;
  }
  if (family === 'claude' && config.rebuildMidSystemMessage !== undefined) {
    payload['rebuild-mid-system-message'] = config.rebuildMidSystemMessage;
  }
  if (config.proxyUrl) payload['proxy-url'] = config.proxyUrl;
  if (config.disableCooling !== undefined) payload['disable-cooling'] = config.disableCooling;
  const headers = serializeHeaders(config.headers);
  if (headers) payload.headers = headers;
  const models = serializeModelAliases(config.models);
  if (models && models.length) payload.models = models;
  if (config.excludedModels && config.excludedModels.length) {
    payload['excluded-models'] = config.excludedModels;
  }
  if (config.cloak) {
    const cloakPayload: Record<string, unknown> = {};
    const mode = config.cloak.mode?.trim();
    if (mode) cloakPayload.mode = mode;
    if (config.cloak.strictMode !== undefined)
      cloakPayload['strict-mode'] = config.cloak.strictMode;
    if (config.cloak.sensitiveWords && config.cloak.sensitiveWords.length) {
      cloakPayload['sensitive-words'] = config.cloak.sensitiveWords;
    }
    if (config.cloak.cacheUserId) {
      cloakPayload['cache-user-id'] = true;
    }
    if (Object.keys(cloakPayload).length) {
      payload.cloak = cloakPayload;
    }
  }
  if (config.fingerprintProfile?.trim()) {
    payload['fingerprint-profile'] = config.fingerprintProfile.trim();
  }
  return payload;
};

const serializeVertexModelAliases = (models?: ModelAlias[]) =>
  Array.isArray(models)
    ? models
        .map((model) => {
          const name = typeof model?.name === 'string' ? model.name.trim() : '';
          const alias = typeof model?.alias === 'string' ? model.alias.trim() : '';
          if (!name || !alias) return null;
          return {
            name,
            alias,
            ...serializeModelOptions(model, false, true),
          };
        })
        .filter(Boolean)
    : undefined;

const serializeVertexKey = (config: ProviderKeyConfig) => {
  const payload: Record<string, unknown> = {
    'api-key': config.apiKey,
    ...serializeRuntimePolicy(config, false),
  };
  if (config.disableCooling !== undefined) payload['disable-cooling'] = config.disableCooling;
  if (config.priority !== undefined) payload.priority = config.priority;
  if (config.weight !== undefined) payload.weight = config.weight;
  if (config.prefix?.trim()) payload.prefix = config.prefix.trim();
  if (config.baseUrl) payload['base-url'] = config.baseUrl;
  if (config.proxyUrl) payload['proxy-url'] = config.proxyUrl;
  const headers = serializeHeaders(config.headers);
  if (headers) payload.headers = headers;
  const models = serializeVertexModelAliases(config.models);
  if (models && models.length) payload.models = models;
  if (config.excludedModels && config.excludedModels.length) {
    payload['excluded-models'] = config.excludedModels;
  }
  return payload;
};

const serializeGeminiKey = (config: GeminiKeyConfig) => {
  const payload: Record<string, unknown> = {
    'api-key': config.apiKey,
    ...serializeRuntimePolicy(config),
  };
  if (config.priority !== undefined) payload.priority = config.priority;
  if (config.weight !== undefined) payload.weight = config.weight;
  if (config.prefix?.trim()) payload.prefix = config.prefix.trim();
  if (config.baseUrl) payload['base-url'] = config.baseUrl;
  if (config.proxyUrl) payload['proxy-url'] = config.proxyUrl;
  if (config.disableCooling !== undefined) payload['disable-cooling'] = config.disableCooling;
  const headers = serializeHeaders(config.headers);
  if (headers) payload.headers = headers;
  const models = serializeModelAliases(config.models);
  if (models && models.length) payload.models = models;
  if (config.excludedModels && config.excludedModels.length) {
    payload['excluded-models'] = config.excludedModels;
  }
  return payload;
};

const serializeOpenAIProvider = (provider: OpenAIProviderConfig) => {
  const payload: Record<string, unknown> = {
    name: provider.name,
    'base-url': provider.baseUrl,
    ...serializeRuntimePolicy(provider),
    keys: Array.isArray(provider.apiKeyEntries)
      ? provider.apiKeyEntries.map((entry) => serializeApiKeyEntry(entry))
      : [],
  };
  if (provider.prefix?.trim()) payload.prefix = provider.prefix.trim();
  if (provider.disabled !== undefined) payload.disabled = provider.disabled;
  if (provider.supportPromptCacheKey !== undefined)
    payload['support-prompt-cache-key'] = provider.supportPromptCacheKey;
  const headers = serializeHeaders(provider.headers);
  if (headers) payload.headers = headers;
  const models = serializeModelAliases(provider.models, true);
  if (models && models.length) payload.models = models;
  if (provider.priority !== undefined) payload.priority = provider.priority;
  if (provider.disableCooling !== undefined) payload['disable-cooling'] = provider.disableCooling;
  return payload;
};

export type ProviderFamily =
  | 'gemini'
  | 'interactions'
  | 'codex'
  | 'meta'
  | 'xai'
  | 'claude'
  | 'vertex'
  | 'openai-compatibility';
type KeyConfig = ProviderKeyConfig | GeminiKeyConfig;
const equal = (a: unknown, b: unknown): boolean => {
  if (a === b) return true;
  if (Array.isArray(a) && Array.isArray(b))
    return a.length === b.length && a.every((v, i) => equal(v, b[i]));
  if (isRecord(a) && isRecord(b)) {
    const keys = Object.keys(a).filter((k) => a[k] !== undefined);
    return (
      keys.length === Object.keys(b).filter((k) => b[k] !== undefined).length &&
      keys.every((k) => equal(a[k], b[k]))
    );
  }
  return false;
};
const conflict = () =>
  new Error('Provider configuration changed or is ambiguous; refresh and try again.');

export const readProviderGroups = (
  raw: unknown,
  family: ProviderFamily
): Record<string, unknown>[] => {
  if (!isRecord(raw) || !isRecord(raw['api-keys'])) return [];
  const groups = raw['api-keys'][family];
  if (groups === undefined) return [];
  if (!Array.isArray(groups) || !groups.every((g) => isRecord(g) && Array.isArray(g.keys)))
    throw conflict();
  return groups;
};
const getGroups = async (family: ProviderFamily) => {
  const assertConnection = guardConfigConnection();
  const session = useAuthStore.getState();
  const raw = await apiClient.get('/config');
  assertConnection();
  const current = useAuthStore.getState();
  if (
    session.apiBase !== current.apiBase ||
    session.managementKey !== current.managementKey ||
    session.isAuthenticated !== current.isAuthenticated
  )
    throw conflict();
  return readProviderGroups(raw, family);
};
// Remove the old UI-only field at known config locations, not inside opaque
// user maps such as headers. Raw snapshots otherwise preserve unknown fields.
const removeTestModel = (value: Record<string, unknown>) => {
  const next = { ...value };
  delete next['test-model'];
  return next;
};
const cleanModelTestFields = (value: Record<string, unknown>) => {
  if (!Array.isArray(value.models)) return value;
  return {
    ...value,
    models: value.models.map((model) => (isRecord(model) ? removeTestModel(model) : model)),
  };
};
const putGroups = (family: ProviderFamily, groups: Record<string, unknown>[]) =>
  apiClient.put(
    `/config/api-keys/${family}`,
    groups.map((group) => ({
      ...cleanModelTestFields(family === 'openai-compatibility' ? removeTestModel(group) : group),
      keys: (group.keys as unknown[]).map((key) =>
        isRecord(key) ? cleanModelTestFields(key) : key
      ),
    }))
  );

/** Locate by persisted snapshot, never by flattened row index. Refuse ambiguous duplicates. */
export const locateProviderGroup = (
  groups: Record<string, unknown>[],
  source: ProviderSource
): number => {
  if (
    source.groups &&
    equal(groups, source.groups) &&
    equal(groups[source.groupIndex], source.group)
  )
    return source.groupIndex;
  const matches = groups.flatMap((group, index) => {
    if (source.keyIndex === undefined) return equal(group, source.group) ? [index] : [];
    const { keys: oldKeys, ...oldPolicy } = source.group;
    const { keys, ...policy } = group;
    if (!equal(policy, oldPolicy) || !Array.isArray(oldKeys) || !Array.isArray(keys)) return [];
    return keys.some((key) => equal(key, oldKeys[source.keyIndex!])) ? [index] : [];
  });
  if (matches.length !== 1) throw conflict();
  return matches[0];
};
const keySerializer = (family: ProviderFamily) =>
  family === 'vertex'
    ? serializeVertexKey
    : family === 'gemini' || family === 'interactions'
      ? serializeGeminiKey
      : (config: ProviderKeyConfig) => serializeProviderKey(config, family);
const emptyOverride = (field: string): unknown => {
  if (['models', 'excluded-models', 'request-scoped-errors'].includes(field)) return [];
  if (field === 'request-retry') return -1;
  if (['headers', 'cloak'].includes(field)) return {};
  if (['disable-cooling', 'websockets', 'disabled'].includes(field)) return false;
  if (['priority', 'weight'].includes(field)) return 0;
  return '';
};
/** Only changed effective fields become overrides. Unchanged null/missing values retain inheritance. */
export const applyProviderChanges = (
  raw: Record<string, unknown>,
  before: Record<string, unknown>,
  after: Record<string, unknown>,
  inherited?: Record<string, unknown>
) => {
  const next = { ...raw };
  for (const field of new Set([...Object.keys(before), ...Object.keys(after)])) {
    if (equal(before[field], after[field])) continue;
    // The row form displays an omitted WebSocket flag as off. Do not materialize
    // that default when editing another field (preserve missing/null on disk).
    if (field === 'websockets' && before[field] === undefined && after[field] === false) continue;
    if (after[field] !== undefined) {
      const fieldRaw = raw[field] ?? inherited?.[field];
      if (isRecord(fieldRaw) && isRecord(before[field]) && isRecord(after[field])) {
        next[field] = applyProviderChanges(fieldRaw, before[field], after[field]);
      } else if (
        Array.isArray(fieldRaw) &&
        Array.isArray(before[field]) &&
        Array.isArray(after[field])
      ) {
        const rawItems = fieldRaw as unknown[];
        const beforeItems = before[field] as unknown[];
        const used = new Set<number>();
        next[field] = (after[field] as unknown[]).map((item) => {
          if (!isRecord(item)) return item;
          const identity = item.name ?? item['api-key'];
          const i = beforeItems.findIndex(
            (old, index) =>
              !used.has(index) && isRecord(old) && (old.name ?? old['api-key']) === identity
          );
          if (i < 0 || !isRecord(rawItems[i]) || !isRecord(beforeItems[i])) return item;
          used.add(i);
          return applyProviderChanges(
            rawItems[i] as Record<string, unknown>,
            beforeItems[i] as Record<string, unknown>,
            item
          );
        });
      } else next[field] = after[field];
    } else if (inherited?.[field] !== undefined && inherited[field] !== null)
      next[field] = emptyOverride(field);
    else delete next[field];
  }
  return next;
};
/** Form row identity survives renames, deletion and reordering; it is never serialized. */
const preserveModelMetadata = (
  next: Record<string, unknown>,
  raw: unknown,
  before: unknown,
  after: unknown,
  original: ModelAlias[] | undefined,
  desired: ModelAlias[] | undefined,
  serialize: (models?: ModelAlias[]) => unknown
) => {
  if (!Array.isArray(after) || !Array.isArray(raw)) return;
  const sameRows = equal(
    original?.map((model) => model.sourceIndex),
    desired?.map((model) => model.sourceIndex)
  );
  if (equal(before, after) && sameRows) return;
  const used = new Set<number>();
  const reserved = new Set(desired?.flatMap((model) => model.sourceIndex ?? []) ?? []);
  next.models = (desired ?? []).flatMap((model) => {
    const serialized = serialize([model]);
    if (!Array.isArray(serialized) || !isRecord(serialized[0])) return [];
    const value = serialized[0];
    if (model.sourceIndex === null) return [value];
    let old: ModelAlias | undefined;
    if (model.sourceIndex !== undefined) {
      old = original?.find((entry) => entry.sourceIndex === model.sourceIndex);
      if (!old || used.has(model.sourceIndex)) throw conflict();
    } else {
      // Preserve compatibility with callers without row identities, but never guess
      // between multiple aliases of the same upstream model or steal an indexed row.
      const candidates = (original ?? []).filter(
        (entry) =>
          typeof entry.sourceIndex === 'number' &&
          !used.has(entry.sourceIndex) &&
          !reserved.has(entry.sourceIndex) &&
          entry.name === model.name
      );
      const exact = candidates.filter((entry) => entry.alias === model.alias);
      old = exact.length === 1 ? exact[0] : candidates.length === 1 ? candidates[0] : undefined;
    }
    const index = old?.sourceIndex;
    if (!old || typeof index !== 'number') return [value];
    if (!isRecord(raw[index])) throw conflict();
    used.add(index);
    const previous = serialize([old]);
    if (!Array.isArray(previous) || !isRecord(previous[0])) throw conflict();
    return [applyProviderChanges(raw[index], previous[0], value)];
  });
};

const findKeySource = (
  groups: Record<string, unknown>[],
  apiKey: string,
  baseUrl?: string
): ProviderSource => {
  const matches = (normalizeProviderGroups(groups) as KeyConfig[]).filter(
    (c) => c.apiKey === apiKey && (c.baseUrl ?? '') === (baseUrl ?? '')
  );
  if (matches.length !== 1 || !matches[0].source) throw conflict();
  return matches[0].source;
};
const createKey = async (family: ProviderFamily, config: KeyConfig) => {
  const groups = await getGroups(family);
  const payload = keySerializer(family)(config);
  const baseUrl = payload['base-url'];
  delete payload['base-url'];
  const names = new Set(groups.map((g) => g.name));
  let number = groups.length + 1;
  while (names.has(`${family}-${number}`)) number++;
  await putGroups(family, [
    ...groups,
    { name: `${family}-${number}`, ...(baseUrl ? { 'base-url': baseUrl } : {}), keys: [payload] },
  ]);
};
const updateKey = async (
  family: ProviderFamily,
  apiKey: string,
  baseUrl: string | undefined,
  config: KeyConfig
) => {
  const groups = await getGroups(family);
  const source = config.source ?? findKeySource(groups, apiKey, baseUrl);
  const index = locateProviderGroup(groups, source);
  const group = groups[index];
  const keys = [...(group.keys as Record<string, unknown>[])];
  const originalKeys = source.group.keys;
  if (source.keyIndex === undefined || !Array.isArray(originalKeys)) throw conflict();
  const matches = equal(group, source.group)
    ? [source.keyIndex]
    : keys.flatMap((key, i) => (equal(key, originalKeys[source.keyIndex!]) ? [i] : []));
  if (matches.length !== 1) throw conflict();
  const keyIndex = matches[0];
  const original = (normalizeProviderGroups([group]) as KeyConfig[]).find(
    (c) => c.source?.keyIndex === keyIndex
  );
  if (!original) throw conflict();
  const serialize = keySerializer(family);
  const before = serialize(original);
  const after = serialize(config);
  const nextGroup = { ...group };
  if (!equal(before['base-url'], after['base-url'])) {
    if (after['base-url'] === undefined) delete nextGroup['base-url'];
    else nextGroup['base-url'] = after['base-url'];
  }
  delete before['base-url'];
  delete after['base-url'];
  const rawModels = keys[keyIndex].models ?? group.models;
  const rawKey = keys[keyIndex];
  keys[keyIndex] = applyProviderChanges(rawKey, before, after, group);
  applyPolicyIntent(keys[keyIndex], rawKey, config, after);
  preserveModelMetadata(
    keys[keyIndex],
    rawModels,
    before.models,
    after.models,
    original.models,
    config.models,
    family === 'vertex' ? serializeVertexModelAliases : serializeModelAliases
  );
  // Response metadata belongs to credentials, not arbitrary nested maps such as headers.
  delete keys[keyIndex]['auth-index'];
  groups[index] = { ...nextGroup, keys };
  await putGroups(family, groups);
};
const deleteKey = async (
  family: ProviderFamily,
  apiKey: string,
  baseUrl?: string,
  expected?: ProviderSource
) => {
  const groups = await getGroups(family);
  const source = expected ?? findKeySource(groups, apiKey, baseUrl);
  const index = locateProviderGroup(groups, source);
  const originalKeys = source.group.keys;
  if (source.keyIndex === undefined || !Array.isArray(originalKeys)) throw conflict();
  const keys = groups[index].keys as unknown[];
  const matches = equal(groups[index], source.group)
    ? [source.keyIndex]
    : keys.flatMap((key, i) => (equal(key, originalKeys[source.keyIndex!]) ? [i] : []));
  if (matches.length !== 1) throw conflict();
  groups[index] = { ...groups[index], keys: keys.filter((_, i) => i !== matches[0]) };
  await putGroups(family, groups);
};
const serializeOpenAIGroup = serializeOpenAIProvider;
const findOpenAISource = (groups: Record<string, unknown>[], source?: ProviderSource): number => {
  if (!source) throw conflict();
  return locateProviderGroup(groups, source);
};

export const providersApi = {
  createGeminiKey: (config: GeminiKeyConfig) => createKey('gemini', config),
  updateGeminiKey: (apiKey: string, baseUrl: string | undefined, config: GeminiKeyConfig) =>
    updateKey('gemini', apiKey, baseUrl, config),
  deleteGeminiKey: (apiKey: string, baseUrl?: string, source?: ProviderSource) =>
    deleteKey('gemini', apiKey, baseUrl, source),
  createInteractionsKey: (config: GeminiKeyConfig) => createKey('interactions', config),
  updateInteractionsKey: (apiKey: string, baseUrl: string | undefined, config: GeminiKeyConfig) =>
    updateKey('interactions', apiKey, baseUrl, config),
  deleteInteractionsKey: (apiKey: string, baseUrl?: string, source?: ProviderSource) =>
    deleteKey('interactions', apiKey, baseUrl, source),
  createCodexConfig: (config: ProviderKeyConfig) => createKey('codex', config),
  updateCodexConfig: (apiKey: string, baseUrl: string | undefined, config: ProviderKeyConfig) =>
    updateKey('codex', apiKey, baseUrl, config),
  deleteCodexConfig: (apiKey: string, baseUrl?: string, source?: ProviderSource) =>
    deleteKey('codex', apiKey, baseUrl, source),
  createMetaConfig: (config: ProviderKeyConfig) => createKey('meta', config),
  updateMetaConfig: (apiKey: string, baseUrl: string | undefined, config: ProviderKeyConfig) =>
    updateKey('meta', apiKey, baseUrl, config),
  deleteMetaConfig: (apiKey: string, baseUrl?: string, source?: ProviderSource) =>
    deleteKey('meta', apiKey, baseUrl, source),
  createXAIConfig: (config: ProviderKeyConfig) => createKey('xai', config),
  updateXAIConfig: (apiKey: string, baseUrl: string | undefined, config: ProviderKeyConfig) =>
    updateKey('xai', apiKey, baseUrl, config),
  deleteXAIConfig: (apiKey: string, baseUrl?: string, source?: ProviderSource) =>
    deleteKey('xai', apiKey, baseUrl, source),
  createClaudeConfig: (config: ProviderKeyConfig) => createKey('claude', config),
  updateClaudeConfig: (apiKey: string, baseUrl: string | undefined, config: ProviderKeyConfig) =>
    updateKey('claude', apiKey, baseUrl, config),
  deleteClaudeConfig: (apiKey: string, baseUrl?: string, source?: ProviderSource) =>
    deleteKey('claude', apiKey, baseUrl, source),
  createVertexConfig: (config: ProviderKeyConfig) => createKey('vertex', config),
  updateVertexConfig: (apiKey: string, baseUrl: string | undefined, config: ProviderKeyConfig) =>
    updateKey('vertex', apiKey, baseUrl, config),
  deleteVertexConfig: (apiKey: string, baseUrl?: string, source?: ProviderSource) =>
    deleteKey('vertex', apiKey, baseUrl, source),
  async getMetaConfigs() {
    return normalizeProviderGroups(await getGroups('meta')) as ProviderKeyConfig[];
  },
  async getVertexConfigs() {
    return normalizeProviderGroups(await getGroups('vertex')) as ProviderKeyConfig[];
  },
  async getOpenAIProviders() {
    return normalizeProviderGroups(
      await getGroups('openai-compatibility'),
      true
    ) as OpenAIProviderConfig[];
  },
  async createOpenAIProvider(config: OpenAIProviderConfig) {
    const groups = await getGroups('openai-compatibility');
    await putGroups('openai-compatibility', [...groups, serializeOpenAIGroup(config)]);
  },
  async updateOpenAIProvider(_name: string, _index: number, config: OpenAIProviderConfig) {
    const groups = await getGroups('openai-compatibility');
    const index = findOpenAISource(groups, config.source);
    const original = (normalizeProviderGroups([groups[index]], true) as OpenAIProviderConfig[])[0];
    const before = serializeOpenAIGroup(original);
    const after = serializeOpenAIGroup(config);
    const next = applyProviderChanges(groups[index], before, after);
    applyPolicyIntent(next, groups[index], config, after);
    preserveModelMetadata(
      next,
      groups[index].models,
      before.models,
      after.models,
      original.models,
      config.models,
      (models) => serializeModelAliases(models, true)
    );
    if (!equal(before.keys, after.keys)) {
      const rawKeys = groups[index].keys as Record<string, unknown>[];
      const used = new Set<number>();
      next.keys = config.apiKeyEntries.map((entry) => {
        // Existing form entries retain their source index; its absence means a new credential.
        // API keys alone are not identities: the same key can use different proxies.
        const keyIndex = entry.sourceIndex;
        if (keyIndex === undefined) return serializeApiKeyEntry(entry);
        if (used.has(keyIndex) || !isRecord(rawKeys[keyIndex])) throw conflict();
        used.add(keyIndex);
        const old = normalizeApiKeyEntry(rawKeys[keyIndex]);
        if (!old) throw conflict();
        const key = applyProviderChanges(
          rawKeys[keyIndex],
          serializeApiKeyEntry(old),
          serializeApiKeyEntry(entry)
        );
        delete key['auth-index'];
        return key;
      });
    }
    groups[index] = next;
    await putGroups('openai-compatibility', groups);
  },
  async updateOpenAIProviderDisabled(_index: number, disabled: boolean, source?: ProviderSource) {
    const groups = await getGroups('openai-compatibility');
    const index = findOpenAISource(groups, source);
    groups[index] = { ...groups[index], disabled };
    await putGroups('openai-compatibility', groups);
  },
  async deleteOpenAIProvider(_index: number, source?: ProviderSource) {
    const groups = await getGroups('openai-compatibility');
    const index = findOpenAISource(groups, source);
    await putGroups(
      'openai-compatibility',
      groups.filter((_, i) => i !== index)
    );
  },
};
