/**
 * 日志相关类型
 * 基于原项目 src/modules/logs.js
 */

// 日志级别
export type LogLevel = 'info' | 'warn' | 'error' | 'debug';

// 日志条目
export interface LogEntry {
  timestamp: string;
  level: LogLevel;
  message: string;
  details?: any;
}

// 日志筛选
export interface LogFilter {
  level?: LogLevel;
  searchQuery: string;
  startTime?: Date;
  endTime?: Date;
}
