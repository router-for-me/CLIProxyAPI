/**
 * 版本相关 API
 */

import { apiClient } from './client';

export const versionApi = {
  checkLatest: () => apiClient.get('/latest-version')
};
