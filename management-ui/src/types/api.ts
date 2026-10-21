/**
 * API 相关类型定义
 * 基于原项目 src/core/api-client.js 和各模块 API
 */

// API 客户端配置
export interface ApiClientConfig {
  apiBase: string;
  managementKey: string;
  timeout?: number;
}

// API 错误
export type ApiError = Error & {
  status?: number;
  /** Axios/network error code, such as ERR_NETWORK. */
  code?: string;
  /** Machine-readable error code returned by the Management API. */
  apiCode?: string;
  details?: unknown;
  data?: unknown;
};
