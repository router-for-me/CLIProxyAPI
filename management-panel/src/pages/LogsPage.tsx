import { useDeferredValue, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { PointerEvent as ReactPointerEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconDownload,
  IconCode,
  IconEyeOff,
  IconRefreshCw,
  IconSearch,
  IconTimer,
  IconTrash2,
  IconX,
} from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import { logsApi } from '@/services/api/logs';
import { MANAGEMENT_API_PREFIX } from '@/utils/constants';
import { formatUnixTimestamp } from '@/utils/format';
import styles from './LogsPage.module.scss';

interface ErrorLogItem {
  name: string;
  size?: number;
  modified?: number;
}

type LogLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error' | 'fatal';

type LogState = {
  buffer: string[];
  visibleFrom: number;
};

// 初始只渲染最近 100 行，滚动到顶部再逐步加载更多（避免一次性渲染过多导致卡顿）
const INITIAL_DISPLAY_LINES = 100;
const LOAD_MORE_LINES = 200;
const MAX_BUFFER_LINES = 10000;
const LOAD_MORE_THRESHOLD_PX = 72;
const LONG_PRESS_MS = 650;
const LONG_PRESS_MOVE_THRESHOLD = 10;

const HTTP_METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS', 'HEAD'] as const;
type HttpMethod = (typeof HTTP_METHODS)[number];
const HTTP_METHOD_REGEX = new RegExp(`\\b(${HTTP_METHODS.join('|')})\\b`);

const LOG_TIMESTAMP_REGEX = /^\[?(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:\.\d{1,3})?)\]?/;
const LOG_LEVEL_REGEX = /^\[?(trace|debug|info|warn|warning|error|fatal)\s*\]?(?=\s|\[|$)\s*/i;
const LOG_SOURCE_REGEX = /^\[([^\]]+)\]/;
const LOG_LATENCY_REGEX =
  /\b(?:\d+(?:\.\d+)?\s*(?:µs|us|ms|s|m))(?:\s*\d+(?:\.\d+)?\s*(?:µs|us|ms|s|m))*\b/i;
const LOG_IPV4_REGEX = /\b(?:\d{1,3}\.){3}\d{1,3}\b/;
const LOG_IPV6_REGEX = /\b(?:[a-f0-9]{0,4}:){2,7}[a-f0-9]{0,4}\b/i;
const LOG_REQUEST_ID_REGEX = /^([a-f0-9]{8}|--------)$/i;
const LOG_TIME_OF_DAY_REGEX = /^\d{1,2}:\d{2}:\d{2}(?:\.\d{1,3})?$/;
const GIN_TIMESTAMP_SEGMENT_REGEX =
  /^\[GIN\]\s+(\d{4})\/(\d{2})\/(\d{2})\s*-\s*(\d{2}:\d{2}:\d{2}(?:\.\d{1,3})?)\s*$/;

const HTTP_STATUS_PATTERNS: RegExp[] = [
  /\|\s*([1-5]\d{2})\s*\|/,
  /\b([1-5]\d{2})\s*-/,
  new RegExp(`\\b(?:${HTTP_METHODS.join('|')})\\s+\\S+\\s+([1-5]\\d{2})\\b`),
  /\b(?:status|code|http)[:\s]+([1-5]\d{2})\b/i,
  /\b([1-5]\d{2})\s+(?:OK|Created|Accepted|No Content|Moved|Found|Bad Request|Unauthorized|Forbidden|Not Found|Method Not Allowed|Internal Server Error|Bad Gateway|Service Unavailable|Gateway Timeout)\b/i,
];

const detectHttpStatusCode = (text: string): number | undefined => {
  for (const pattern of HTTP_STATUS_PATTERNS) {
    const match = text.match(pattern);
    if (!match) continue;
    const code = Number.parseInt(match[1], 10);
    if (!Number.isFinite(code)) continue;
    if (code >= 100 && code <= 599) return code;
  }
  return undefined;
};

const extractIp = (text: string): string | undefined => {
  const ipv4Match = text.match(LOG_IPV4_REGEX);
  if (ipv4Match) return ipv4Match[0];

  const ipv6Match = text.match(LOG_IPV6_REGEX);
  if (!ipv6Match) return undefined;

  const candidate = ipv6Match[0];

  // Avoid treating time strings like "12:34:56" as IPv6 addresses.
  if (LOG_TIME_OF_DAY_REGEX.test(candidate)) return undefined;

  // If no compression marker is present, a valid IPv6 address must contain 8 hextets.
  if (!candidate.includes('::') && candidate.split(':').length !== 8) return undefined;

  return candidate;
};

const normalizeTimestampToSeconds = (value: string): string => {
  const trimmed = value.trim();
  const match = trimmed.match(/^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})/);
  if (!match) return trimmed;
  return `${match[1]} ${match[2]}`;
};

const extractLatency = (text: string): string | undefined => {
  const match = text.match(LOG_LATENCY_REGEX);
  if (!match) return undefined;
  return match[0].replace(/\s+/g, '');
};

type ParsedLogLine = {
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

const extractLogLevel = (value: string): LogLevel | undefined => {
  const normalized = value.trim().toLowerCase();
  if (normalized === 'warning') return 'warn';
  if (normalized === 'warn') return 'warn';
  if (normalized === 'info') return 'info';
  if (normalized === 'error') return 'error';
  if (normalized === 'fatal') return 'fatal';
  if (normalized === 'debug') return 'debug';
  if (normalized === 'trace') return 'trace';
  return undefined;
};

const inferLogLevel = (line: string): LogLevel | undefined => {
  const lowered = line.toLowerCase();
  if (/\bfatal\b/.test(lowered)) return 'fatal';
  if (/\berror\b/.test(lowered)) return 'error';
  if (/\bwarn(?:ing)?\b/.test(lowered) || line.includes('警告')) return 'warn';
  if (/\binfo\b/.test(lowered)) return 'info';
  if (/\bdebug\b/.test(lowered)) return 'debug';
  if (/\btrace\b/.test(lowered)) return 'trace';
  return undefined;
};

const extractHttpMethodAndPath = (text: string): { method?: HttpMethod; path?: string } => {
  const match = text.match(HTTP_METHOD_REGEX);
  if (!match) return {};

  const method = match[1] as HttpMethod;
  const index = match.index ?? 0;
  const after = text.slice(index + match[0].length).trim();
  const path = after ? after.split(/\s+/)[0] : undefined;
  return { method, path };
};

const parseLogLine = (raw: string): ParsedLogLine => {
  let remaining = raw.trim();

  let timestamp: string | undefined;
  const tsMatch = remaining.match(LOG_TIMESTAMP_REGEX);
  if (tsMatch) {
    timestamp = tsMatch[1];
    remaining = remaining.slice(tsMatch[0].length).trim();
  }

  let requestId: string | undefined;
  const requestIdMatch = remaining.match(/^\[([a-f0-9]{8}|--------)\]\s*/i);
  if (requestIdMatch) {
    const id = requestIdMatch[1];
    if (!/^-+$/.test(id)) {
      requestId = id;
    }
    remaining = remaining.slice(requestIdMatch[0].length).trim();
  }

  let level: LogLevel | undefined;
  const lvlMatch = remaining.match(LOG_LEVEL_REGEX);
  if (lvlMatch) {
    level = extractLogLevel(lvlMatch[1]);
    remaining = remaining.slice(lvlMatch[0].length).trim();
  }

  let source: string | undefined;
  const sourceMatch = remaining.match(LOG_SOURCE_REGEX);
  if (sourceMatch) {
    source = sourceMatch[1];
    remaining = remaining.slice(sourceMatch[0].length).trim();
  }

  let statusCode: number | undefined;
  let latency: string | undefined;
  let ip: string | undefined;
  let method: HttpMethod | undefined;
  let path: string | undefined;
  let message = remaining;

  if (remaining.includes('|')) {
    const segments = remaining
      .split('|')
      .map((segment) => segment.trim())
      .filter(Boolean);
    const consumed = new Set<number>();

    const ginIndex = segments.findIndex((segment) => GIN_TIMESTAMP_SEGMENT_REGEX.test(segment));
    if (ginIndex >= 0) {
      const match = segments[ginIndex].match(GIN_TIMESTAMP_SEGMENT_REGEX);
      if (match) {
        const ginTimestamp = `${match[1]}-${match[2]}-${match[3]} ${match[4]}`;
        const normalizedGin = normalizeTimestampToSeconds(ginTimestamp);
        const normalizedParsed = timestamp ? normalizeTimestampToSeconds(timestamp) : undefined;

        if (!timestamp) {
          timestamp = ginTimestamp;
          consumed.add(ginIndex);
        } else if (normalizedParsed === normalizedGin) {
          consumed.add(ginIndex);
        }
      }
    }

    // request id (8-char hex or dashes)
    const requestIdIndex = segments.findIndex((segment) => LOG_REQUEST_ID_REGEX.test(segment));
    if (requestIdIndex >= 0) {
      const match = segments[requestIdIndex].match(LOG_REQUEST_ID_REGEX);
      if (match) {
        const id = match[1];
        if (!/^-+$/.test(id)) {
          requestId = id;
        }
        consumed.add(requestIdIndex);
      }
    }

    // status code
    const statusIndex = segments.findIndex((segment) => /^\d{3}$/.test(segment));
    if (statusIndex >= 0) {
      const match = segments[statusIndex].match(/^(\d{3})$/);
      if (match) {
        const code = Number.parseInt(match[1], 10);
        if (code >= 100 && code <= 599) {
          statusCode = code;
          consumed.add(statusIndex);
        }
      }
    }

    // latency
    const latencyIndex = segments.findIndex((segment) => LOG_LATENCY_REGEX.test(segment));
    if (latencyIndex >= 0) {
      const extracted = extractLatency(segments[latencyIndex]);
      if (extracted) {
        latency = extracted;
        consumed.add(latencyIndex);
      }
    }

    // ip
    const ipIndex = segments.findIndex((segment) => Boolean(extractIp(segment)));
    if (ipIndex >= 0) {
      const extracted = extractIp(segments[ipIndex]);
      if (extracted) {
        ip = extracted;
        consumed.add(ipIndex);
      }
    }

    // method + path
    const methodIndex = segments.findIndex((segment) => {
      const { method: parsedMethod } = extractHttpMethodAndPath(segment);
      return Boolean(parsedMethod);
    });
    if (methodIndex >= 0) {
      const parsed = extractHttpMethodAndPath(segments[methodIndex]);
      method = parsed.method;
      path = parsed.path;
      consumed.add(methodIndex);
    }

    // source (e.g. [gin_logger.go:94])
    const sourceIndex = segments.findIndex((segment) => LOG_SOURCE_REGEX.test(segment));
    if (sourceIndex >= 0) {
      const match = segments[sourceIndex].match(LOG_SOURCE_REGEX);
      if (match) {
        source = match[1];
        consumed.add(sourceIndex);
      }
    }

    message = segments.filter((_, index) => !consumed.has(index)).join(' | ');
  } else {
    statusCode = detectHttpStatusCode(remaining);

    const extracted = extractLatency(remaining);
    if (extracted) latency = extracted;

    ip = extractIp(remaining);

    const parsed = extractHttpMethodAndPath(remaining);
    method = parsed.method;
    path = parsed.path;
  }

  if (!level) level = inferLogLevel(raw);

  if (message) {
    const match = message.match(GIN_TIMESTAMP_SEGMENT_REGEX);
    if (match) {
      const ginTimestamp = `${match[1]}-${match[2]}-${match[3]} ${match[4]}`;
      if (!timestamp) timestamp = ginTimestamp;
      if (normalizeTimestampToSeconds(timestamp) === normalizeTimestampToSeconds(ginTimestamp)) {
        message = '';
      }
    }
  }

  return {
    raw,
    timestamp,
    level,
    source,
    requestId,
    statusCode,
    latency,
    ip,
    method,
    path,
    message,
  };
};

const getErrorMessage = (err: unknown): string => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  if (typeof err !== 'object' || err === null) return '';
  if (!('message' in err)) return '';

  const message = (err as { message?: unknown }).message;
  return typeof message === 'string' ? message : '';
};

const copyToClipboard = async (text: string) => {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    try {
      const textarea = document.createElement('textarea');
      textarea.value = text;
      textarea.style.position = 'fixed';
      textarea.style.opacity = '0';
      textarea.style.left = '-9999px';
      textarea.style.top = '0';
      document.body.appendChild(textarea);
      textarea.focus();
      textarea.select();
      const ok = document.execCommand('copy');
      document.body.removeChild(textarea);
      return ok;
    } catch {
      return false;
    }
  }
};

type TabType = 'logs' | 'errors';

export function LogsPage() {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const requestLogEnabled = useConfigStore((state) => state.config?.requestLog ?? false);

  const [activeTab, setActiveTab] = useState<TabType>('logs');
  const [logState, setLogState] = useState<LogState>({ buffer: [], visibleFrom: 0 });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [autoRefresh, setAutoRefresh] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const deferredSearchQuery = useDeferredValue(searchQuery);
  const [hideManagementLogs, setHideManagementLogs] = useState(true);
  const [showRawLogs, setShowRawLogs] = useState(false);
  const [errorLogs, setErrorLogs] = useState<ErrorLogItem[]>([]);
  const [loadingErrors, setLoadingErrors] = useState(false);
  const [errorLogsError, setErrorLogsError] = useState('');
  const [requestLogId, setRequestLogId] = useState<string | null>(null);
  const [requestLogDownloading, setRequestLogDownloading] = useState(false);

  const logViewerRef = useRef<HTMLDivElement | null>(null);
  const pendingScrollToBottomRef = useRef(false);
  const pendingPrependScrollRef = useRef<{ scrollHeight: number; scrollTop: number } | null>(null);
  const longPressRef = useRef<{
    timer: number | null;
    startX: number;
    startY: number;
    fired: boolean;
  } | null>(null);

  // 保存最新时间戳用于增量获取
  const latestTimestampRef = useRef<number>(0);

  const disableControls = connectionStatus !== 'connected';

  const isNearBottom = (node: HTMLDivElement | null) => {
    if (!node) return true;
    const threshold = 24;
    return node.scrollHeight - node.scrollTop - node.clientHeight <= threshold;
  };

  const scrollToBottom = () => {
    const node = logViewerRef.current;
    if (!node) return;
    node.scrollTop = node.scrollHeight;
  };

  const loadLogs = async (incremental = false) => {
    if (connectionStatus !== 'connected') {
      setLoading(false);
      return;
    }

    if (!incremental) {
      setLoading(true);
    }
    setError('');

    try {
      pendingScrollToBottomRef.current = !incremental || isNearBottom(logViewerRef.current);

      const params =
        incremental && latestTimestampRef.current > 0 ? { after: latestTimestampRef.current } : {};
      const data = await logsApi.fetchLogs(params);

      // 更新时间戳
      if (data['latest-timestamp']) {
        latestTimestampRef.current = data['latest-timestamp'];
      }

      const newLines = Array.isArray(data.lines) ? data.lines : [];

      if (incremental && newLines.length > 0) {
        // 增量更新：追加新日志并限制缓冲区大小（避免内存与渲染膨胀）
        setLogState((prev) => {
          const prevRenderedCount = prev.buffer.length - prev.visibleFrom;
          const combined = [...prev.buffer, ...newLines];
          const dropCount = Math.max(combined.length - MAX_BUFFER_LINES, 0);
          const buffer = dropCount > 0 ? combined.slice(dropCount) : combined;
          let visibleFrom = Math.max(prev.visibleFrom - dropCount, 0);

          // 若用户停留在底部（跟随最新日志），则保持“渲染窗口”大小不变，避免无限增长
          if (pendingScrollToBottomRef.current) {
            visibleFrom = Math.max(buffer.length - prevRenderedCount, 0);
          }

          return { buffer, visibleFrom };
        });
      } else if (!incremental) {
        // 全量加载：默认只渲染最后 100 行，向上滚动再展开更多
        const buffer = newLines.slice(-MAX_BUFFER_LINES);
        const visibleFrom = Math.max(buffer.length - INITIAL_DISPLAY_LINES, 0);
        setLogState({ buffer, visibleFrom });
      }
    } catch (err: unknown) {
      console.error('Failed to load logs:', err);
      if (!incremental) {
        setError(getErrorMessage(err) || t('logs.load_error'));
      }
    } finally {
      if (!incremental) {
        setLoading(false);
      }
    }
  };

  useHeaderRefresh(() => loadLogs(false));

  const clearLogs = async () => {
    showConfirmation({
      title: t('logs.clear_confirm_title', { defaultValue: 'Clear Logs' }),
      message: t('logs.clear_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        try {
          await logsApi.clearLogs();
          setLogState({ buffer: [], visibleFrom: 0 });
          latestTimestampRef.current = 0;
          showNotification(t('logs.clear_success'), 'success');
        } catch (err: unknown) {
          const message = getErrorMessage(err);
          showNotification(
            `${t('notification.delete_failed')}${message ? `: ${message}` : ''}`,
            'error'
          );
        }
      },
    });
  };

  const downloadLogs = () => {
    const text = logState.buffer.join('\n');
    const blob = new Blob([text], { type: 'text/plain' });
    const url = window.URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'logs.txt';
    a.click();
    window.URL.revokeObjectURL(url);
    showNotification(t('logs.download_success'), 'success');
  };

  const loadErrorLogs = async () => {
    if (connectionStatus !== 'connected') {
      setLoadingErrors(false);
      return;
    }

    setLoadingErrors(true);
    setErrorLogsError('');
    try {
      const res = await logsApi.fetchErrorLogs();
      // API 返回 { files: [...] }
      setErrorLogs(Array.isArray(res.files) ? res.files : []);
    } catch (err: unknown) {
      console.error('Failed to load error logs:', err);
      setErrorLogs([]);
      const message = getErrorMessage(err);
      setErrorLogsError(
        message ? `${t('logs.error_logs_load_error')}: ${message}` : t('logs.error_logs_load_error')
      );
    } finally {
      setLoadingErrors(false);
    }
  };

  const downloadErrorLog = async (name: string) => {
    try {
      const response = await logsApi.downloadErrorLog(name);
      const blob = new Blob([response.data], { type: 'text/plain' });
      const url = window.URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = name;
      a.click();
      window.URL.revokeObjectURL(url);
      showNotification(t('logs.error_log_download_success'), 'success');
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      showNotification(
        `${t('notification.download_failed')}${message ? `: ${message}` : ''}`,
        'error'
      );
    }
  };

  useEffect(() => {
    if (connectionStatus === 'connected') {
      latestTimestampRef.current = 0;
      loadLogs(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connectionStatus]);

  useEffect(() => {
    if (activeTab !== 'errors') return;
    if (connectionStatus !== 'connected') return;
    void loadErrorLogs();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeTab, connectionStatus, requestLogEnabled]);

  useEffect(() => {
    if (!autoRefresh || connectionStatus !== 'connected') {
      return;
    }
    const id = window.setInterval(() => {
      loadLogs(true);
    }, 8000);
    return () => window.clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoRefresh, connectionStatus]);

  useEffect(() => {
    if (!pendingScrollToBottomRef.current) return;
    if (loading) return;
    if (!logViewerRef.current) return;

    scrollToBottom();
    pendingScrollToBottomRef.current = false;
  }, [loading, logState.buffer, logState.visibleFrom]);

  const visibleLines = useMemo(
    () => logState.buffer.slice(logState.visibleFrom),
    [logState.buffer, logState.visibleFrom]
  );

  const trimmedSearchQuery = deferredSearchQuery.trim();
  const isSearching = trimmedSearchQuery.length > 0;
  const baseLines = isSearching ? logState.buffer : visibleLines;

  const { filteredLines, removedCount } = useMemo(() => {
    let working = baseLines;
    let removed = 0;

    if (hideManagementLogs) {
      const next: string[] = [];
      for (const line of working) {
        if (line.includes(MANAGEMENT_API_PREFIX)) {
          removed += 1;
        } else {
          next.push(line);
        }
      }
      working = next;
    }

    if (trimmedSearchQuery) {
      const queryLowered = trimmedSearchQuery.toLowerCase();
      const next: string[] = [];
      for (const line of working) {
        if (line.toLowerCase().includes(queryLowered)) {
          next.push(line);
        } else {
          removed += 1;
        }
      }
      working = next;
    }

    return { filteredLines: working, removedCount: removed };
  }, [baseLines, hideManagementLogs, trimmedSearchQuery]);

  const parsedVisibleLines = useMemo(() => {
    if (showRawLogs) return [];
    return filteredLines.map((line) => parseLogLine(line));
  }, [filteredLines, showRawLogs]);

  const rawVisibleText = useMemo(() => filteredLines.join('\n'), [filteredLines]);

  const canLoadMore = !isSearching && logState.visibleFrom > 0;

  const handleLogScroll = () => {
    const node = logViewerRef.current;
    if (!node) return;
    if (isSearching) return;
    if (!canLoadMore) return;
    if (pendingPrependScrollRef.current) return;
    if (node.scrollTop > LOAD_MORE_THRESHOLD_PX) return;

    pendingPrependScrollRef.current = {
      scrollHeight: node.scrollHeight,
      scrollTop: node.scrollTop,
    };
    setLogState((prev) => ({
      ...prev,
      visibleFrom: Math.max(prev.visibleFrom - LOAD_MORE_LINES, 0),
    }));
  };

  useLayoutEffect(() => {
    const node = logViewerRef.current;
    const pending = pendingPrependScrollRef.current;
    if (!node || !pending) return;

    const delta = node.scrollHeight - pending.scrollHeight;
    node.scrollTop = pending.scrollTop + delta;
    pendingPrependScrollRef.current = null;
  }, [logState.visibleFrom]);

  const copyLogLine = async (raw: string) => {
    const ok = await copyToClipboard(raw);
    if (ok) {
      showNotification(t('logs.copy_success', { defaultValue: 'Copied to clipboard' }), 'success');
    } else {
      showNotification(t('logs.copy_failed', { defaultValue: 'Copy failed' }), 'error');
    }
  };

  const clearLongPressTimer = () => {
    if (longPressRef.current?.timer) {
      window.clearTimeout(longPressRef.current.timer);
      longPressRef.current.timer = null;
    }
  };

  const startLongPress = (event: ReactPointerEvent<HTMLDivElement>, id?: string) => {
    if (!requestLogEnabled) return;
    if (!id) return;
    if (requestLogId) return;
    clearLongPressTimer();
    longPressRef.current = {
      timer: window.setTimeout(() => {
        setRequestLogId(id);
        if (longPressRef.current) {
          longPressRef.current.fired = true;
          longPressRef.current.timer = null;
        }
      }, LONG_PRESS_MS),
      startX: event.clientX,
      startY: event.clientY,
      fired: false,
    };
  };

  const cancelLongPress = () => {
    clearLongPressTimer();
    longPressRef.current = null;
  };

  const handleLongPressMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const current = longPressRef.current;
    if (!current || current.timer === null || current.fired) return;
    const deltaX = Math.abs(event.clientX - current.startX);
    const deltaY = Math.abs(event.clientY - current.startY);
    if (deltaX > LONG_PRESS_MOVE_THRESHOLD || deltaY > LONG_PRESS_MOVE_THRESHOLD) {
      cancelLongPress();
    }
  };

  const closeRequestLogModal = () => {
    if (requestLogDownloading) return;
    setRequestLogId(null);
  };

  const downloadRequestLog = async (id: string) => {
    setRequestLogDownloading(true);
    try {
      const response = await logsApi.downloadRequestLogById(id);
      const blob = new Blob([response.data], { type: 'text/plain' });
      const url = window.URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `request-${id}.log`;
      a.click();
      window.URL.revokeObjectURL(url);
      showNotification(t('logs.request_log_download_success'), 'success');
      setRequestLogId(null);
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      showNotification(
        `${t('notification.download_failed')}${message ? `: ${message}` : ''}`,
        'error'
      );
    } finally {
      setRequestLogDownloading(false);
    }
  };

  useEffect(() => {
    return () => {
      if (longPressRef.current?.timer) {
        window.clearTimeout(longPressRef.current.timer);
        longPressRef.current.timer = null;
      }
    };
  }, []);

  return (
    <div className={styles.container}>
      <h1 className={styles.pageTitle}>{t('logs.title')}</h1>

      <div className={styles.tabBar}>
        <button
          type="button"
          className={`${styles.tabItem} ${activeTab === 'logs' ? styles.tabActive : ''}`}
          onClick={() => setActiveTab('logs')}
        >
          {t('logs.log_content')}
        </button>
        <button
          type="button"
          className={`${styles.tabItem} ${activeTab === 'errors' ? styles.tabActive : ''}`}
          onClick={() => setActiveTab('errors')}
        >
          {t('logs.error_logs_modal_title')}
        </button>
      </div>

      <div className={styles.content}>
        {activeTab === 'logs' && (
          <Card className={styles.logCard}>
            {error && <div className="error-box">{error}</div>}

            <div className={styles.filters}>
              <div className={styles.searchWrapper}>
                <Input
                  value={searchQuery}
                  onChange={(e) => setSearchQuery(e.target.value)}
                  placeholder={t('logs.search_placeholder')}
                  className={styles.searchInput}
                  rightElement={
                    searchQuery ? (
                      <button
                        type="button"
                        className={styles.searchClear}
                        onClick={() => setSearchQuery('')}
                        title="Clear"
                        aria-label="Clear"
                      >
                        <IconX size={16} />
                      </button>
                    ) : (
                      <IconSearch size={16} className={styles.searchIcon} />
                    )
                  }
                />
              </div>

              <ToggleSwitch
                checked={hideManagementLogs}
                onChange={setHideManagementLogs}
                label={
                  <span className={styles.switchLabel}>
                    <IconEyeOff size={16} />
                    {t('logs.hide_management_logs', { prefix: MANAGEMENT_API_PREFIX })}
                  </span>
                }
              />

              <ToggleSwitch
                checked={showRawLogs}
                onChange={setShowRawLogs}
                label={
                  <span
                    className={styles.switchLabel}
                    title={t('logs.show_raw_logs_hint', {
                      defaultValue: 'Show original log text for easier multi-line copy',
                    })}
                  >
                    <IconCode size={16} />
                    {t('logs.show_raw_logs', { defaultValue: 'Show raw logs' })}
                  </span>
                }
              />

              <div className={styles.toolbar}>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => loadLogs(false)}
                  disabled={disableControls || loading}
                  className={styles.actionButton}
                >
                  <span className={styles.buttonContent}>
                    <IconRefreshCw size={16} />
                    {t('logs.refresh_button')}
                  </span>
                </Button>
                <ToggleSwitch
                  checked={autoRefresh}
                  onChange={(value) => setAutoRefresh(value)}
                  disabled={disableControls}
                  label={
                    <span className={styles.switchLabel}>
                      <IconTimer size={16} />
                      {t('logs.auto_refresh')}
                    </span>
                  }
                />
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={downloadLogs}
                  disabled={logState.buffer.length === 0}
                  className={styles.actionButton}
                >
                  <span className={styles.buttonContent}>
                    <IconDownload size={16} />
                    {t('logs.download_button')}
                  </span>
                </Button>
                <Button
                  variant="danger"
                  size="sm"
                  onClick={clearLogs}
                  disabled={disableControls}
                  className={styles.actionButton}
                >
                  <span className={styles.buttonContent}>
                    <IconTrash2 size={16} />
                    {t('logs.clear_button')}
                  </span>
                </Button>
              </div>
            </div>

            {loading ? (
              <div className="hint">{t('logs.loading')}</div>
            ) : logState.buffer.length > 0 && filteredLines.length > 0 ? (
              <div ref={logViewerRef} className={styles.logPanel} onScroll={handleLogScroll}>
                {canLoadMore && (
                  <div className={styles.loadMoreBanner}>
                    <span>{t('logs.load_more_hint')}</span>
                    <div className={styles.loadMoreStats}>
                      <span>
                        {t('logs.loaded_lines', { count: filteredLines.length })}
                      </span>
                      {removedCount > 0 && (
                        <span className={styles.loadMoreCount}>
                          {t('logs.filtered_lines', { count: removedCount })}
                        </span>
                      )}
                      <span className={styles.loadMoreCount}>
                        {t('logs.hidden_lines', { count: logState.visibleFrom })}
                      </span>
                    </div>
                  </div>
                )}
                {showRawLogs ? (
                  <pre className={styles.rawLog} spellCheck={false}>
                    {rawVisibleText}
                  </pre>
                ) : (
                  <div className={styles.logList}>
                    {parsedVisibleLines.map((line, index) => {
                      const rowClassNames = [styles.logRow];
                      if (line.level === 'warn') rowClassNames.push(styles.rowWarn);
                      if (line.level === 'error' || line.level === 'fatal')
                        rowClassNames.push(styles.rowError);
                      return (
                        <div
                          key={`${logState.visibleFrom + index}-${line.raw}`}
                          className={rowClassNames.join(' ')}
                          onDoubleClick={() => {
                            void copyLogLine(line.raw);
                          }}
                          onPointerDown={(event) => startLongPress(event, line.requestId)}
                          onPointerUp={cancelLongPress}
                          onPointerLeave={cancelLongPress}
                          onPointerCancel={cancelLongPress}
                          onPointerMove={handleLongPressMove}
                          title={t('logs.double_click_copy_hint', {
                            defaultValue: 'Double-click to copy',
                          })}
                        >
                          <div className={styles.timestamp}>{line.timestamp || ''}</div>
                          <div className={styles.rowMain}>
                            {line.level && (
                              <span
                                className={[
                                  styles.badge,
                                  line.level === 'info' ? styles.levelInfo : '',
                                  line.level === 'warn' ? styles.levelWarn : '',
                                  line.level === 'error' || line.level === 'fatal'
                                    ? styles.levelError
                                    : '',
                                  line.level === 'debug' ? styles.levelDebug : '',
                                  line.level === 'trace' ? styles.levelTrace : '',
                                ]
                                  .filter(Boolean)
                                  .join(' ')}
                              >
                                {line.level.toUpperCase()}
                              </span>
                            )}

                            {line.source && (
                              <span className={styles.source} title={line.source}>
                                {line.source}
                              </span>
                            )}

                            {line.requestId && (
                              <span
                                className={[styles.badge, styles.requestIdBadge].join(' ')}
                                title={line.requestId}
                              >
                                {line.requestId}
                              </span>
                            )}

                            {typeof line.statusCode === 'number' && (
                              <span
                                className={[
                                  styles.badge,
                                  styles.statusBadge,
                                  line.statusCode >= 200 && line.statusCode < 300
                                    ? styles.statusSuccess
                                    : line.statusCode >= 300 && line.statusCode < 400
                                      ? styles.statusInfo
                                      : line.statusCode >= 400 && line.statusCode < 500
                                        ? styles.statusWarn
                                        : styles.statusError,
                                ].join(' ')}
                              >
                                {line.statusCode}
                              </span>
                            )}

                            {line.latency && <span className={styles.pill}>{line.latency}</span>}
                            {line.ip && <span className={styles.pill}>{line.ip}</span>}

                            {line.method && (
                              <span className={[styles.badge, styles.methodBadge].join(' ')}>
                                {line.method}
                              </span>
                            )}

                            {line.path && (
                              <span className={styles.path} title={line.path}>
                                {line.path}
                              </span>
                            )}

                            {line.message && <span className={styles.message}>{line.message}</span>}
                          </div>
                        </div>
                      );
                    })}
                  </div>
                )}
              </div>
            ) : logState.buffer.length > 0 ? (
              <EmptyState
                title={t('logs.search_empty_title')}
                description={t('logs.search_empty_desc')}
              />
            ) : (
              <EmptyState title={t('logs.empty_title')} description={t('logs.empty_desc')} />
            )}
          </Card>
        )}

        {activeTab === 'errors' && (
          <Card
            extra={
              <Button
                variant="secondary"
                size="sm"
                onClick={loadErrorLogs}
                loading={loadingErrors}
                disabled={disableControls}
              >
                {t('common.refresh')}
              </Button>
            }
          >
            <div className="stack">
              <div className="hint">{t('logs.error_logs_description')}</div>

              {requestLogEnabled && (
                <div>
                  <div className="status-badge warning">{t('logs.error_logs_request_log_enabled')}</div>
                </div>
              )}

              {errorLogsError && <div className="error-box">{errorLogsError}</div>}

              <div className={styles.errorPanel}>
                {loadingErrors ? (
                  <div className="hint">{t('common.loading')}</div>
                ) : errorLogs.length === 0 ? (
                  <div className="hint">{t('logs.error_logs_empty')}</div>
                ) : (
                  <div className="item-list">
                    {errorLogs.map((item) => (
                      <div key={item.name} className="item-row">
                        <div className="item-meta">
                          <div className="item-title">{item.name}</div>
                          <div className="item-subtitle">
                            {item.size ? `${(item.size / 1024).toFixed(1)} KB` : ''}{' '}
                            {item.modified ? formatUnixTimestamp(item.modified) : ''}
                          </div>
                        </div>
                        <div className="item-actions">
                          <Button
                            variant="secondary"
                            size="sm"
                            onClick={() => downloadErrorLog(item.name)}
                            disabled={disableControls}
                          >
                            {t('logs.error_logs_download')}
                          </Button>
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>
          </Card>
        )}
      </div>

      <Modal
        open={Boolean(requestLogId)}
        onClose={closeRequestLogModal}
        title={t('logs.request_log_download_title')}
        footer={
          <>
            <Button variant="secondary" onClick={closeRequestLogModal} disabled={requestLogDownloading}>
              {t('common.cancel')}
            </Button>
            <Button
              onClick={() => {
                if (requestLogId) {
                  void downloadRequestLog(requestLogId);
                }
              }}
              loading={requestLogDownloading}
              disabled={!requestLogId}
            >
              {t('common.confirm')}
            </Button>
          </>
        }
      >
        {requestLogId ? t('logs.request_log_download_confirm', { id: requestLogId }) : null}
      </Modal>
    </div>
  );
}
