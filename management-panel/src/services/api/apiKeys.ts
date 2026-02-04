/**
 * API 密钥管理
 */

import { apiClient } from './client';

export const apiKeysApi = {
  async list(): Promise<string[]> {
    const data = await apiClient.get('/api-keys');
    const keys = (data && (data['api-keys'] ?? data.apiKeys)) as unknown;
    return Array.isArray(keys) ? (keys as string[]) : [];
  },

  replace: (keys: string[]) => apiClient.put('/api-keys', keys),

  update: (index: number, value: string) => apiClient.patch('/api-keys', { index, value }),

  delete: (index: number) => apiClient.delete(`/api-keys?index=${index}`)
};
