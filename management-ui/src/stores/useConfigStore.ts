/**
 * 配置状态管理
 * 从原项目 src/core/config-service.js 迁移
 */

import { create } from 'zustand';
import type { Config } from '@/types';
import type { RawConfigSection } from '@/types/config';
import { configApi } from '@/services/api/config';
import { CACHE_EXPIRY_MS } from '@/utils/constants';

interface ConfigCache {
  data: Config;
  timestamp: number;
}

interface ConfigState {
  config: Config | null;

  // 操作
  fetchConfig: (forceRefresh?: boolean) => Promise<Config>;
  updateConfigValue: (section: RawConfigSection, value: unknown) => void;
  clearCache: (section?: RawConfigSection) => void;
  isCacheValid: () => boolean;
}

let configRequestToken = 0;
let inFlightConfigRequest: { id: number; promise: Promise<Config> } | null = null;
let fullConfigCache: ConfigCache | null = null;

const isFullCacheValid = () =>
  fullConfigCache !== null && Date.now() - fullConfigCache.timestamp < CACHE_EXPIRY_MS;

export const useConfigStore = create<ConfigState>((set, get) => ({
  config: null,

  fetchConfig: async (forceRefresh = false) => {
    // 检查缓存
    if (!forceRefresh && fullConfigCache && isFullCacheValid()) {
      return fullConfigCache.data;
    }

    // 同一时刻合并多个 /config 请求（如 StrictMode 或多个页面同时触发）
    if (inFlightConfigRequest) {
      return inFlightConfigRequest.promise;
    }

    const requestId = (configRequestToken += 1);
    try {
      const requestPromise = configApi.getConfig();
      inFlightConfigRequest = { id: requestId, promise: requestPromise };
      const data = await requestPromise;

      // 如果在请求过程中连接已被切换/登出，则忽略旧请求的结果，避免覆盖新会话的状态
      if (requestId !== configRequestToken) {
        return data;
      }

      fullConfigCache = { data, timestamp: Date.now() };
      set({ config: data });
      return data;
    } finally {
      if (inFlightConfigRequest?.id === requestId) {
        inFlightConfigRequest = null;
      }
    }
  },

  updateConfigValue: (section, value) => {
    set((state) => {
      // Optimistic values are UI models, not serialized v8 config nodes (provider
      // rows in particular are not upstream groups). Keep the last server document
      // intact until the invalidated cache is fetched again.
      const nextConfig: Config = { ...(state.config || {}) };

      switch (section) {
        case 'debug':
          nextConfig.debug = value as Config['debug'];
          break;
        case 'proxy-url':
          nextConfig.proxyUrl = value as Config['proxyUrl'];
          break;
        case 'request-retry':
          nextConfig.requestRetry = value as Config['requestRetry'];
          break;
        case 'quota-exceeded':
          nextConfig.quotaExceeded = value as Config['quotaExceeded'];
          break;
        case 'request-log':
          nextConfig.requestLog = value as Config['requestLog'];
          break;
        case 'logging-to-file':
          nextConfig.loggingToFile = value as Config['loggingToFile'];
          break;
        case 'logs-max-total-size-mb':
          nextConfig.logsMaxTotalSizeMb = value as Config['logsMaxTotalSizeMb'];
          break;
        case 'ws-auth':
          nextConfig.wsAuth = value as Config['wsAuth'];
          break;
        case 'force-model-prefix':
          nextConfig.forceModelPrefix = value as Config['forceModelPrefix'];
          break;
        case 'routing/strategy':
          nextConfig.routingStrategy = value as Config['routingStrategy'];
          break;
        case 'api-keys':
          nextConfig.apiKeys = value as Config['apiKeys'];
          break;
        case 'gemini-api-key':
          nextConfig.geminiApiKeys = value as Config['geminiApiKeys'];
          break;
        case 'interactions-api-key':
          nextConfig.interactionsApiKeys = value as Config['interactionsApiKeys'];
          break;
        case 'codex-api-key':
          nextConfig.codexApiKeys = value as Config['codexApiKeys'];
          break;
        case 'meta-api-key':
          nextConfig.metaApiKeys = value as Config['metaApiKeys'];
          break;
        case 'xai-api-key':
          nextConfig.xaiApiKeys = value as Config['xaiApiKeys'];
          break;
        case 'claude-api-key':
          nextConfig.claudeApiKeys = value as Config['claudeApiKeys'];
          break;
        case 'vertex-api-key':
          nextConfig.vertexApiKeys = value as Config['vertexApiKeys'];
          break;
        case 'openai-compatibility':
          nextConfig.openaiCompatibility = value as Config['openaiCompatibility'];
          break;
        case 'oauth-excluded-models':
          nextConfig.oauthExcludedModels = value as Config['oauthExcludedModels'];
          break;
        default:
          break;
      }

      return { config: nextConfig };
    });

    // 使缓存失效（保留当前 config 快照）
    get().clearCache(section);
  },

  clearCache: (section) => {
    fullConfigCache = null;

    // 缓存失效通常伴随乐观写或“切换连接/登出/全量刷新”，需要让 in-flight 的旧请求失效
    configRequestToken += 1;
    inFlightConfigRequest = null;

    // 无 section 代表“切换连接/登出/全量刷新”，连 config 快照一起清除
    if (!section) {
      set({ config: null });
    }
  },

  isCacheValid: () => isFullCacheValid(),
}));
