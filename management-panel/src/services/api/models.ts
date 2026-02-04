/**
 * 可用模型获取
 */

import axios from 'axios';
import { normalizeModelList } from '@/utils/models';
import { apiCallApi, getApiCallErrorMessage } from './apiCall';

const normalizeBaseUrl = (baseUrl: string): string => {
  let normalized = String(baseUrl || '').trim();
  if (!normalized) return '';
  normalized = normalized.replace(/\/?v0\/management\/?$/i, '');
  normalized = normalized.replace(/\/+$/g, '');
  if (!/^https?:\/\//i.test(normalized)) {
    normalized = `http://${normalized}`;
  }
  return normalized;
};

const buildModelsEndpoint = (baseUrl: string): string => {
  const normalized = normalizeBaseUrl(baseUrl);
  if (!normalized) return '';
  return `${normalized}/models`;
};

const buildV1ModelsEndpoint = (baseUrl: string): string => {
  const normalized = normalizeBaseUrl(baseUrl);
  if (!normalized) return '';
  return `${normalized}/v1/models`;
};

export const modelsApi = {
  /**
   * Fetch available models from /v1/models endpoint (for system info page)
   */
  async fetchModels(baseUrl: string, apiKey?: string, headers: Record<string, string> = {}) {
    const endpoint = buildV1ModelsEndpoint(baseUrl);
    if (!endpoint) {
      throw new Error('Invalid base url');
    }

    const resolvedHeaders = { ...headers };
    if (apiKey) {
      resolvedHeaders.Authorization = `Bearer ${apiKey}`;
    }

    const response = await axios.get(endpoint, {
      headers: Object.keys(resolvedHeaders).length ? resolvedHeaders : undefined
    });
    const payload = response.data?.data ?? response.data?.models ?? response.data;
    return normalizeModelList(payload, { dedupe: true });
  },

  /**
   * Fetch models from /models endpoint via api-call (for OpenAI provider discovery)
   */
  async fetchModelsViaApiCall(
    baseUrl: string,
    apiKey?: string,
    headers: Record<string, string> = {}
  ) {
    const endpoint = buildModelsEndpoint(baseUrl);
    if (!endpoint) {
      throw new Error('Invalid base url');
    }

    const resolvedHeaders = { ...headers };
    const hasAuthHeader = Boolean(resolvedHeaders.Authorization || resolvedHeaders.authorization);
    if (apiKey && !hasAuthHeader) {
      resolvedHeaders.Authorization = `Bearer ${apiKey}`;
    }

    const result = await apiCallApi.request({
      method: 'GET',
      url: endpoint,
      header: Object.keys(resolvedHeaders).length ? resolvedHeaders : undefined
    });

    if (result.statusCode < 200 || result.statusCode >= 300) {
      throw new Error(getApiCallErrorMessage(result));
    }

    const payload = result.body ?? result.bodyText;
    return normalizeModelList(payload, { dedupe: true });
  }
};
