/**
 * 模型列表状态管理（带缓存）
 */

import { create } from 'zustand';
import { modelsApi } from '@/services/api/models';
import { CACHE_EXPIRY_MS } from '@/utils/constants';
import type { ModelInfo } from '@/utils/models';

interface ModelsCache {
  data: ModelInfo[];
  timestamp: number;
  apiBase: string;
}

interface ModelsState {
  models: ModelInfo[];
  loading: boolean;
  error: string | null;
  cache: ModelsCache | null;

  fetchModels: (apiBase: string, apiKey?: string, forceRefresh?: boolean) => Promise<ModelInfo[]>;
  clearCache: () => void;
  isCacheValid: (apiBase: string) => boolean;
}

export const useModelsStore = create<ModelsState>((set, get) => ({
  models: [],
  loading: false,
  error: null,
  cache: null,

  fetchModels: async (apiBase, apiKey, forceRefresh = false) => {
    const { cache, isCacheValid } = get();

    // 检查缓存
    if (!forceRefresh && isCacheValid(apiBase) && cache) {
      set({ models: cache.data, error: null });
      return cache.data;
    }

    set({ loading: true, error: null });

    try {
      const list = await modelsApi.fetchModels(apiBase, apiKey);
      const now = Date.now();

      set({
        models: list,
        loading: false,
        cache: { data: list, timestamp: now, apiBase }
      });

      return list;
    } catch (error: any) {
      const message = error?.message || 'Failed to fetch models';
      set({
        error: message,
        loading: false,
        models: []
      });
      throw error;
    }
  },

  clearCache: () => {
    set({ cache: null, models: [] });
  },

  isCacheValid: (apiBase) => {
    const { cache } = get();
    if (!cache) return false;
    if (cache.apiBase !== apiBase) return false;
    return Date.now() - cache.timestamp < CACHE_EXPIRY_MS;
  }
}));
