/**
 * AI 提供商相关 API
 */

import { apiClient } from './client';
import {
  normalizeGeminiKeyConfig,
  normalizeOpenAIProvider,
  normalizeProviderKeyConfig
} from './transformers';
import type {
  GeminiKeyConfig,
  OpenAIProviderConfig,
  ProviderKeyConfig,
  ApiKeyEntry,
  ModelAlias
} from '@/types';

const serializeHeaders = (headers?: Record<string, string>) => (headers && Object.keys(headers).length ? headers : undefined);

const serializeModelAliases = (models?: ModelAlias[]) =>
  Array.isArray(models)
    ? models
        .map((model) => {
          if (!model?.name) return null;
          const payload: Record<string, any> = { name: model.name };
          if (model.alias && model.alias !== model.name) {
            payload.alias = model.alias;
          }
          if (model.priority !== undefined) {
            payload.priority = model.priority;
          }
          if (model.testModel) {
            payload['test-model'] = model.testModel;
          }
          return payload;
        })
        .filter(Boolean)
    : undefined;

const serializeApiKeyEntry = (entry: ApiKeyEntry) => {
  const payload: Record<string, any> = { 'api-key': entry.apiKey };
  if (entry.proxyUrl) payload['proxy-url'] = entry.proxyUrl;
  const headers = serializeHeaders(entry.headers);
  if (headers) payload.headers = headers;
  return payload;
};

const serializeProviderKey = (config: ProviderKeyConfig) => {
  const payload: Record<string, any> = { 'api-key': config.apiKey };
  if (config.prefix?.trim()) payload.prefix = config.prefix.trim();
  if (config.baseUrl) payload['base-url'] = config.baseUrl;
  if (config.proxyUrl) payload['proxy-url'] = config.proxyUrl;
  const headers = serializeHeaders(config.headers);
  if (headers) payload.headers = headers;
  const models = serializeModelAliases(config.models);
  if (models && models.length) payload.models = models;
  if (config.excludedModels && config.excludedModels.length) {
    payload['excluded-models'] = config.excludedModels;
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
          return { name, alias };
        })
        .filter(Boolean)
    : undefined;

const serializeVertexKey = (config: ProviderKeyConfig) => {
  const payload: Record<string, any> = { 'api-key': config.apiKey };
  if (config.prefix?.trim()) payload.prefix = config.prefix.trim();
  if (config.baseUrl) payload['base-url'] = config.baseUrl;
  if (config.proxyUrl) payload['proxy-url'] = config.proxyUrl;
  const headers = serializeHeaders(config.headers);
  if (headers) payload.headers = headers;
  const models = serializeVertexModelAliases(config.models);
  if (models && models.length) payload.models = models;
  return payload;
};

const serializeGeminiKey = (config: GeminiKeyConfig) => {
  const payload: Record<string, any> = { 'api-key': config.apiKey };
  if (config.prefix?.trim()) payload.prefix = config.prefix.trim();
  if (config.baseUrl) payload['base-url'] = config.baseUrl;
  const headers = serializeHeaders(config.headers);
  if (headers) payload.headers = headers;
  if (config.excludedModels && config.excludedModels.length) {
    payload['excluded-models'] = config.excludedModels;
  }
  return payload;
};

const serializeOpenAIProvider = (provider: OpenAIProviderConfig) => {
  const payload: Record<string, any> = {
    name: provider.name,
    'base-url': provider.baseUrl,
    'api-key-entries': Array.isArray(provider.apiKeyEntries)
      ? provider.apiKeyEntries.map((entry) => serializeApiKeyEntry(entry))
      : []
  };
  if (provider.prefix?.trim()) payload.prefix = provider.prefix.trim();
  const headers = serializeHeaders(provider.headers);
  if (headers) payload.headers = headers;
  const models = serializeModelAliases(provider.models);
  if (models && models.length) payload.models = models;
  if (provider.priority !== undefined) payload.priority = provider.priority;
  if (provider.testModel) payload['test-model'] = provider.testModel;
  return payload;
};

export const providersApi = {
  async getGeminiKeys(): Promise<GeminiKeyConfig[]> {
    const data = await apiClient.get('/gemini-api-key');
    const list = (data && (data['gemini-api-key'] ?? data.items ?? data)) as any;
    if (!Array.isArray(list)) return [];
    return list.map((item) => normalizeGeminiKeyConfig(item)).filter(Boolean) as GeminiKeyConfig[];
  },

  saveGeminiKeys: (configs: GeminiKeyConfig[]) =>
    apiClient.put('/gemini-api-key', configs.map((item) => serializeGeminiKey(item))),

  updateGeminiKey: (index: number, value: GeminiKeyConfig) =>
    apiClient.patch('/gemini-api-key', { index, value: serializeGeminiKey(value) }),

  deleteGeminiKey: (apiKey: string) =>
    apiClient.delete(`/gemini-api-key?api-key=${encodeURIComponent(apiKey)}`),

  async getCodexConfigs(): Promise<ProviderKeyConfig[]> {
    const data = await apiClient.get('/codex-api-key');
    const list = (data && (data['codex-api-key'] ?? data.items ?? data)) as any;
    if (!Array.isArray(list)) return [];
    return list.map((item) => normalizeProviderKeyConfig(item)).filter(Boolean) as ProviderKeyConfig[];
  },

  saveCodexConfigs: (configs: ProviderKeyConfig[]) =>
    apiClient.put('/codex-api-key', configs.map((item) => serializeProviderKey(item))),

  updateCodexConfig: (index: number, value: ProviderKeyConfig) =>
    apiClient.patch('/codex-api-key', { index, value: serializeProviderKey(value) }),

  deleteCodexConfig: (apiKey: string) =>
    apiClient.delete(`/codex-api-key?api-key=${encodeURIComponent(apiKey)}`),

  async getClaudeConfigs(): Promise<ProviderKeyConfig[]> {
    const data = await apiClient.get('/claude-api-key');
    const list = (data && (data['claude-api-key'] ?? data.items ?? data)) as any;
    if (!Array.isArray(list)) return [];
    return list.map((item) => normalizeProviderKeyConfig(item)).filter(Boolean) as ProviderKeyConfig[];
  },

  saveClaudeConfigs: (configs: ProviderKeyConfig[]) =>
    apiClient.put('/claude-api-key', configs.map((item) => serializeProviderKey(item))),

  updateClaudeConfig: (index: number, value: ProviderKeyConfig) =>
    apiClient.patch('/claude-api-key', { index, value: serializeProviderKey(value) }),

  deleteClaudeConfig: (apiKey: string) =>
    apiClient.delete(`/claude-api-key?api-key=${encodeURIComponent(apiKey)}`),

  async getVertexConfigs(): Promise<ProviderKeyConfig[]> {
    const data = await apiClient.get('/vertex-api-key');
    const list = (data && (data['vertex-api-key'] ?? data.items ?? data)) as any;
    if (!Array.isArray(list)) return [];
    return list.map((item) => normalizeProviderKeyConfig(item)).filter(Boolean) as ProviderKeyConfig[];
  },

  saveVertexConfigs: (configs: ProviderKeyConfig[]) =>
    apiClient.put('/vertex-api-key', configs.map((item) => serializeVertexKey(item))),

  updateVertexConfig: (index: number, value: ProviderKeyConfig) =>
    apiClient.patch('/vertex-api-key', { index, value: serializeVertexKey(value) }),

  deleteVertexConfig: (apiKey: string) =>
    apiClient.delete(`/vertex-api-key?api-key=${encodeURIComponent(apiKey)}`),

  async getOpenAIProviders(): Promise<OpenAIProviderConfig[]> {
    const data = await apiClient.get('/openai-compatibility');
    const list = (data && (data['openai-compatibility'] ?? data.items ?? data)) as any;
    if (!Array.isArray(list)) return [];
    return list.map((item) => normalizeOpenAIProvider(item)).filter(Boolean) as OpenAIProviderConfig[];
  },

  saveOpenAIProviders: (providers: OpenAIProviderConfig[]) =>
    apiClient.put('/openai-compatibility', providers.map((item) => serializeOpenAIProvider(item))),

  updateOpenAIProvider: (index: number, value: OpenAIProviderConfig) =>
    apiClient.patch('/openai-compatibility', { index, value: serializeOpenAIProvider(value) }),

  deleteOpenAIProvider: (name: string) =>
    apiClient.delete(`/openai-compatibility?name=${encodeURIComponent(name)}`)
};
