export const HTTP_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS', 'HEAD'] as const;
export type HttpMethod = (typeof HTTP_METHODS)[number];

export const STATUS_GROUPS = ['2xx', '3xx', '4xx', '5xx'] as const;
export type StatusGroup = (typeof STATUS_GROUPS)[number];

export type LogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal';

export type LogState = {
  buffer: string[];
  visibleFrom: number;
};

export type ParsedLogLine = {
  raw: string;
  timestamp?: string;
  level?: LogLevel;
  source?: string;
  requestId?: string;
  statusCode?: number;
  latency?: string;
  ip?: string;
  method?: HttpMethod;
  path?: string;
  message: string;
};

export const resolveStatusGroup = (statusCode?: number): StatusGroup | undefined => {
  if (typeof statusCode !== 'number') return undefined;
  if (statusCode >= 200 && statusCode < 300) return '2xx';
  if (statusCode >= 300 && statusCode < 400) return '3xx';
  if (statusCode >= 400 && statusCode < 500) return '4xx';
  if (statusCode >= 500 && statusCode < 600) return '5xx';
  return undefined;
};
