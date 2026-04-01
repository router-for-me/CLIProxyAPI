/**
 * Amp CLI Integration (ampcode) 相关 API
 */

import { apiClient } from './client';
import {
  normalizeAmpcodeConfig,
  normalizeAmpcodeModelMappings,
  normalizeAmpcodeUpstreamApiKeys,
} from './transformers';
import type { AmpcodeConfig, AmpcodeModelMapping, AmpcodeUpstreamApiKeyMapping } from '@/types';

const serializeUpstreamApiKeyMappings = (mappings: AmpcodeUpstreamApiKeyMapping[]) =>
  mappings.map((mapping) => ({
    'upstream-api-key': mapping.upstreamApiKey,
    'api-keys': mapping.apiKeys,
  }));

export const ampcodeApi = {
  async getAmpcode(): Promise<AmpcodeConfig> {
    const data = await apiClient.get('/ampcode');
    return normalizeAmpcodeConfig(data) ?? {};
  },

  updateUpstreamUrl: (url: string) => apiClient.put('/ampcode/upstream-url', { value: url }),
  clearUpstreamUrl: () => apiClient.delete('/ampcode/upstream-url'),

  updateUpstreamApiKey: (apiKey: string) => apiClient.put('/ampcode/upstream-api-key', { value: apiKey }),
  clearUpstreamApiKey: () => apiClient.delete('/ampcode/upstream-api-key'),

  async getUpstreamApiKeys(): Promise<AmpcodeUpstreamApiKeyMapping[]> {
    const data = await apiClient.get<Record<string, unknown>>('/ampcode/upstream-api-keys');
    const list = data?.['upstream-api-keys'] ?? data?.upstreamApiKeys ?? data?.items ?? data;
    return normalizeAmpcodeUpstreamApiKeys(list);
  },

  saveUpstreamApiKeys: (mappings: AmpcodeUpstreamApiKeyMapping[]) =>
    apiClient.put('/ampcode/upstream-api-keys', { value: serializeUpstreamApiKeyMappings(mappings) }),
  patchUpstreamApiKeys: (mappings: AmpcodeUpstreamApiKeyMapping[]) =>
    apiClient.patch('/ampcode/upstream-api-keys', { value: serializeUpstreamApiKeyMappings(mappings) }),
  deleteUpstreamApiKeys: (upstreamApiKeys: string[]) =>
    apiClient.delete('/ampcode/upstream-api-keys', { data: { value: upstreamApiKeys } }),

  async getModelMappings(): Promise<AmpcodeModelMapping[]> {
    const data = await apiClient.get<Record<string, unknown>>('/ampcode/model-mappings');
    const list = data?.['model-mappings'] ?? data?.modelMappings ?? data?.items ?? data;
    return normalizeAmpcodeModelMappings(list);
  },

  saveModelMappings: (mappings: AmpcodeModelMapping[]) =>
    apiClient.put('/ampcode/model-mappings', { value: mappings }),
  patchModelMappings: (mappings: AmpcodeModelMapping[]) =>
    apiClient.patch('/ampcode/model-mappings', { value: mappings }),
  clearModelMappings: () => apiClient.delete('/ampcode/model-mappings'),
  deleteModelMappings: (fromList: string[]) =>
    apiClient.delete('/ampcode/model-mappings', { data: { value: fromList } }),

  updateForceModelMappings: (enabled: boolean) => apiClient.put('/ampcode/force-model-mappings', { value: enabled })
};
