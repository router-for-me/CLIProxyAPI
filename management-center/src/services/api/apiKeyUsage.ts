import { apiClient } from './client';
import type { ApiKeyUsageResponse } from '@/utils/recentRequests';

const API_KEY_USAGE_TIMEOUT_MS = 15 * 1000;

export const apiKeyUsageApi = {
  getUsage: () =>
    apiClient.get<ApiKeyUsageResponse>('/api-key-usage', {
      timeout: API_KEY_USAGE_TIMEOUT_MS,
    }),
};
