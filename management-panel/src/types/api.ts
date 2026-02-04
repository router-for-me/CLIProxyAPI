/**
 * API 相关类型定义
 * 基于原项目 src/core/api-client.js 和各模块 API
 */

// HTTP 方法
export type HttpMethod = 'GET' | 'POST' | 'PUT' | 'DELETE';

// API 客户端配置
export interface ApiClientConfig {
  apiBase: string;
  managementKey: string;
  timeout?: number;
}

// 请求选项
export interface RequestOptions {
  method?: HttpMethod;
  headers?: Record<string, string>;
  params?: Record<string, any>;
  data?: any;
}

// 服务器版本信息
export interface ServerVersion {
  version: string;
  buildDate?: string;
}

// API 错误
export type ApiError = Error & {
  status?: number;
  code?: string;
  details?: any;
  data?: any;
};
