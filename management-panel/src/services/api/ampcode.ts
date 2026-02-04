/**
 * Amp CLI Integration (ampcode) 相关 API
 */

import { apiClient } from './client';
import { normalizeAmpcodeConfig, normalizeAmpcodeModelMappings } from './transformers';
import type { AmpcodeConfig, AmpcodeModelMapping } from '@/types';

export const ampcodeApi = {
  async getAmpcode(): Promise<AmpcodeConfig> {
    const data = await apiClient.get('/ampcode');
    return normalizeAmpcodeConfig(data) ?? {};
  },

  updateUpstreamUrl: (url: string) => apiClient.put('/ampcode/upstream-url', { value: url }),
  clearUpstreamUrl: () => apiClient.delete('/ampcode/upstream-url'),

  updateUpstreamApiKey: (apiKey: string) => apiClient.put('/ampcode/upstream-api-key', { value: apiKey }),
  clearUpstreamApiKey: () => apiClient.delete('/ampcode/upstream-api-key'),

  async getModelMappings(): Promise<AmpcodeModelMapping[]> {
    const data = await apiClient.get('/ampcode/model-mappings');
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

