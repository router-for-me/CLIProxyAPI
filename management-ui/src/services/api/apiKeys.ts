/** Client access keys are a direct config list, not upstream provider groups. */
import { apiClient } from './client';
import { getConfigValue, guardConfigConnection } from './configValue';

const PATH = '/config/access/api-keys';

const assertIndex = (keys: string[], index: number): void => {
  if (!Number.isInteger(index) || index < 0 || index >= keys.length) {
    throw new RangeError('API key index out of range');
  }
};

export const apiKeysApi = {
  async list(): Promise<string[]> {
    const data = await getConfigValue<unknown>(PATH, []);
    return Array.isArray(data) ? data.map((key) => String(key)) : [];
  },

  replace: (keys: string[]) => apiClient.put(PATH, keys),

  async update(index: number, value: string) {
    const assertConnection = guardConfigConnection();
    const keys = await apiKeysApi.list();
    assertConnection();
    assertIndex(keys, index);
    keys[index] = value;
    return apiKeysApi.replace(keys);
  },

  async delete(index: number) {
    const assertConnection = guardConfigConnection();
    const keys = await apiKeysApi.list();
    assertConnection();
    assertIndex(keys, index);
    keys.splice(index, 1);
    return apiKeysApi.replace(keys);
  },
};
