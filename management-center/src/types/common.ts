/**
 * 通用类型定义
 */

export type Theme = 'light' | 'white' | 'dark' | 'auto';

export type Language = 'zh-CN' | 'zh-TW' | 'en' | 'ru';

export type NotificationType = 'info' | 'success' | 'warning' | 'error';

export interface Notification {
  id: string;
  message: string;
  type: NotificationType;
  duration?: number;
}
