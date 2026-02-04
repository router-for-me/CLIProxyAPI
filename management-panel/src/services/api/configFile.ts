/**
 * 配置文件相关 API（/config.yaml）
 */

import { apiClient } from './client';

export const configFileApi = {
  async fetchConfigYaml(): Promise<string> {
    const response = await apiClient.getRaw('/config.yaml', {
      responseType: 'text',
      headers: { Accept: 'application/yaml, text/yaml, text/plain' }
    });
    const data = response.data as any;
    if (typeof data === 'string') return data;
    if (data === undefined || data === null) return '';
    return String(data);
  },

  async saveConfigYaml(content: string): Promise<void> {
    await apiClient.put('/config.yaml', content, {
      headers: {
        'Content-Type': 'application/yaml',
        Accept: 'application/json, text/plain, */*'
      }
    });
  }
};
