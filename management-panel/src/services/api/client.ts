/**
 * Axios API 客户端
 * 替代原项目 src/core/api-client.js
 */

import axios, { AxiosInstance, AxiosRequestConfig, AxiosResponse } from 'axios';
import type { ApiClientConfig, ApiError } from '@/types';
import {
  BUILD_DATE_HEADER_KEYS,
  MANAGEMENT_API_PREFIX,
  REQUEST_TIMEOUT_MS,
  VERSION_HEADER_KEYS
} from '@/utils/constants';

class ApiClient {
  private instance: AxiosInstance;
  private apiBase: string = '';
  private managementKey: string = '';

  constructor() {
    this.instance = axios.create({
      timeout: REQUEST_TIMEOUT_MS,
      headers: {
        'Content-Type': 'application/json'
      }
    });

    this.setupInterceptors();
  }

  /**
   * 设置 API 配置
   */
  setConfig(config: ApiClientConfig): void {
    this.apiBase = this.normalizeApiBase(config.apiBase);
    this.managementKey = config.managementKey;

    if (config.timeout) {
      this.instance.defaults.timeout = config.timeout;
    } else {
      this.instance.defaults.timeout = REQUEST_TIMEOUT_MS;
    }
  }

  /**
   * 规范化 API Base URL
   */
  private normalizeApiBase(base: string): string {
    let normalized = base.trim();

    // 移除尾部的 /v0/management
    normalized = normalized.replace(/\/?v0\/management\/?$/i, '');

    // 移除尾部斜杠
    normalized = normalized.replace(/\/+$/, '');

    // 添加协议
    if (!/^https?:\/\//i.test(normalized)) {
      normalized = `http://${normalized}`;
    }

    return `${normalized}${MANAGEMENT_API_PREFIX}`;
  }

  private readHeader(headers: Record<string, any> | undefined, keys: string[]): string | null {
    if (!headers) return null;

    const normalizeValue = (value: unknown): string | null => {
      if (value === undefined || value === null) return null;
      if (Array.isArray(value)) {
        const first = value.find((entry) => entry !== undefined && entry !== null && String(entry).trim());
        return first !== undefined ? String(first) : null;
      }
      const text = String(value);
      return text ? text : null;
    };

    const headerGetter = (headers as { get?: (name: string) => any }).get;
    if (typeof headerGetter === 'function') {
      for (const key of keys) {
        const match = normalizeValue(headerGetter.call(headers, key));
        if (match) return match;
      }
    }

    const entries =
      typeof (headers as { entries?: () => Iterable<[string, any]> }).entries === 'function'
        ? Array.from((headers as { entries: () => Iterable<[string, any]> }).entries())
        : Object.entries(headers);

    const normalized = Object.fromEntries(
      entries.map(([key, value]) => [String(key).toLowerCase(), value])
    );
    for (const key of keys) {
      const match = normalizeValue(normalized[key.toLowerCase()]);
      if (match) return match;
    }
    return null;
  }

  /**
   * 设置请求/响应拦截器
   */
  private setupInterceptors(): void {
    // 请求拦截器
    this.instance.interceptors.request.use(
      (config) => {
        // 设置 baseURL
        config.baseURL = this.apiBase;
        if (config.url) {
          // Normalize deprecated Gemini endpoint to the current path.
          config.url = config.url.replace(/\/generative-language-api-key\b/g, '/gemini-api-key');
        }

        // 添加认证头
        if (this.managementKey) {
          config.headers.Authorization = `Bearer ${this.managementKey}`;
        }

        return config;
      },
      (error) => Promise.reject(this.handleError(error))
    );

    // 响应拦截器
    this.instance.interceptors.response.use(
      (response) => {
        const headers = response.headers as Record<string, string | undefined>;
        const version = this.readHeader(headers, VERSION_HEADER_KEYS);
        const buildDate = this.readHeader(headers, BUILD_DATE_HEADER_KEYS);

        // 触发版本更新事件（后续通过 store 处理）
        if (version || buildDate) {
          window.dispatchEvent(
            new CustomEvent('server-version-update', {
              detail: { version: version || null, buildDate: buildDate || null }
            })
          );
        }

        return response;
      },
      (error) => Promise.reject(this.handleError(error))
    );
  }

  /**
   * 错误处理
   */
  private handleError(error: any): ApiError {
    if (axios.isAxiosError(error)) {
      const responseData = error.response?.data as any;
      const message = responseData?.error || responseData?.message || error.message || 'Request failed';
      const apiError = new Error(message) as ApiError;
      apiError.name = 'ApiError';
      apiError.status = error.response?.status;
      apiError.code = error.code;
      apiError.details = responseData;
      apiError.data = responseData;

      // 401 未授权 - 触发登出事件
      if (error.response?.status === 401) {
        window.dispatchEvent(new Event('unauthorized'));
      }

      return apiError;
    }

    const fallback = new Error(error?.message || 'Unknown error occurred') as ApiError;
    fallback.name = 'ApiError';
    return fallback;
  }

  /**
   * GET 请求
   */
  async get<T = any>(url: string, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.get<T>(url, config);
    return response.data;
  }

  /**
   * POST 请求
   */
  async post<T = any>(url: string, data?: any, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.post<T>(url, data, config);
    return response.data;
  }

  /**
   * PUT 请求
   */
  async put<T = any>(url: string, data?: any, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.put<T>(url, data, config);
    return response.data;
  }

  /**
   * PATCH 请求
   */
  async patch<T = any>(url: string, data?: any, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.patch<T>(url, data, config);
    return response.data;
  }

  /**
   * DELETE 请求
   */
  async delete<T = any>(url: string, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.delete<T>(url, config);
    return response.data;
  }

  /**
   * 获取原始响应（用于下载等场景）
   */
  async getRaw(url: string, config?: AxiosRequestConfig): Promise<AxiosResponse> {
    return this.instance.get(url, config);
  }

  /**
   * 发送 FormData
   */
  async postForm<T = any>(url: string, formData: FormData, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.post<T>(url, formData, {
      ...config,
      headers: {
        ...(config?.headers || {}),
        'Content-Type': 'multipart/form-data'
      }
    });
    return response.data;
  }

  /**
   * 保留对 axios.request 的访问，便于下载等场景
   */
  async requestRaw(config: AxiosRequestConfig): Promise<AxiosResponse> {
    return this.instance.request(config);
  }
}

// 导出单例
export const apiClient = new ApiClient();
