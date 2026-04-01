/**
 * 通用类型定义
 */

export type Theme = 'light' | 'white' | 'dark' | 'auto';

export type Language = 'zh-CN' | 'en' | 'ru';

export type NotificationType = 'info' | 'success' | 'warning' | 'error';

export interface Notification {
  id: string;
  message: string;
  type: NotificationType;
  duration?: number;
}

export interface ApiResponse<T = unknown> {
  data?: T;
  error?: string;
  message?: string;
}

export interface PaginationState {
  currentPage: number;
  pageSize: number;
  totalPages: number;
  totalItems?: number;
}

export interface LoadingState {
  isLoading: boolean;
  error: Error | null;
}

// 泛型异步状态
export interface AsyncState<T> extends LoadingState {
  data: T | null;
}
