import { useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';
import type { PointerEvent as ReactPointerEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconChevronDown,
  IconChevronUp,
  IconCode,
  IconDownload,
  IconEyeOff,
  IconRefreshCw,
  IconSearch,
  IconSlidersHorizontal,
  IconTimer,
  IconTrash2,
  IconX,
} from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useLocalStorage } from '@/hooks/useLocalStorage';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import { logsApi } from '@/services/api/logs';
import { copyToClipboard } from '@/utils/clipboard';
import { downloadBlob } from '@/utils/download';
import { MANAGEMENT_API_PREFIX } from '@/utils/constants';
import { formatUnixTimestamp } from '@/utils/format';
import {
  HTTP_METHODS,
  STATUS_GROUPS,
  resolveStatusGroup,
  type LogState,
} from './hooks/logTypes';
import { parseLogLine } from './hooks/logParsing';
import { useLogFilters } from './hooks/useLogFilters';
import { isNearBottom, useLogScroller } from './hooks/useLogScroller';
import { isTraceableRequestPath, useTraceResolver } from './hooks/useTraceResolver';
import styles from './LogsPage.module.scss';

interface ErrorLogItem {
  name: string;
  size?: number;
  modified?: number;
}

// 初始只渲染最近 100 行，滚动到顶部再逐步加载更多（避免一次性渲染过多导致卡顿）
const INITIAL_DISPLAY_LINES = 100;
const MAX_BUFFER_LINES = 10000;
const LONG_PRESS_MS = 650;
const LONG_PRESS_MOVE_THRESHOLD = 10;

const getErrorMessage = (err: unknown): string => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  if (typeof err !== 'object' || err === null) return '';
  if (!('message' in err)) return '';

  const message = (err as { message?: unknown }).message;
  return typeof message === 'string' ? message : '';
};

type TabType = 'logs' | 'errors';

export function LogsPage() {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const traceScopeKey = `${apiBase}::${managementKey}`;
  const config = useConfigStore((state) => state.config);
  const requestLogEnabled = config?.requestLog ?? false;

  const [activeTab, setActiveTab] = useState<TabType>('logs');
  const [logState, setLogState] = useState<LogState>({ buffer: [], visibleFrom: 0 });
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [autoRefresh, setAutoRefresh] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const deferredSearchQuery = useDeferredValue(searchQuery);
  const [hideManagementLogs, setHideManagementLogs] = useState(true);
  const [showRawLogs, setShowRawLogs] = useState(false);
  const [structuredFiltersExpanded, setStructuredFiltersExpanded] = useLocalStorage(
    'logsPage.structuredFiltersExpanded',
    true
  );
  const [errorLogs, setErrorLogs] = useState<ErrorLogItem[]>([]);
  const [loadingErrors, setLoadingErrors] = useState(false);
  const [errorLogsError, setErrorLogsError] = useState('');
  const [requestLogId, setRequestLogId] = useState<string | null>(null);
  const [requestLogDownloading, setRequestLogDownloading] = useState(false);

  const trace = useTraceResolver({
    traceScopeKey,
    connectionStatus,
    config,
    requestLogDownloading
  });

  const logScrollerRef = useRef<ReturnType<typeof useLogScroller> | null>(null);
  const longPressRef = useRef<{
    timer: number | null;
    startX: number;
    startY: number;
    fired: boolean;
  } | null>(null);
  const logRequestInFlightRef = useRef(false);
  const pendingFullReloadRef = useRef(false);

  // 保存最新时间戳用于增量获取
  const latestTimestampRef = useRef<number>(0);

  const disableControls = connectionStatus !== 'connected';

  const loadLogs = async (incremental = false) => {
    if (connectionStatus !== 'connected') {
      setLoading(false);
      return;
    }

    if (logRequestInFlightRef.current) {
      if (!incremental) {
        pendingFullReloadRef.current = true;
      }
      return;
    }

    logRequestInFlightRef.current = true;

    if (!incremental) {
      setLoading(true);
    }
    setError('');

    try {
      const scrollerInstance = logScrollerRef.current;
      const stickToBottom =
        !incremental || isNearBottom(scrollerInstance?.logViewerRef.current ?? null);
      if (stickToBottom) {
        scrollerInstance?.requestScrollToBottom();
      }

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
          if (stickToBottom) {
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
      logRequestInFlightRef.current = false;
      if (pendingFullReloadRef.current) {
        pendingFullReloadRef.current = false;
        void loadLogs(false);
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
    downloadBlob({ filename: 'logs.txt', blob: new Blob([text], { type: 'text/plain' }) });
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
      downloadBlob({ filename: name, blob: new Blob([response.data], { type: 'text/plain' }) });
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

  const visibleLines = useMemo(
    () => logState.buffer.slice(logState.visibleFrom),
    [logState.buffer, logState.visibleFrom]
  );

  const trimmedSearchQuery = deferredSearchQuery.trim();
  const isSearching = trimmedSearchQuery.length > 0;
  const baseLines = isSearching ? logState.buffer : visibleLines;

  const parsedSearchLines = useMemo(() => {
    let working = baseLines;

    if (hideManagementLogs) {
      working = working.filter((line) => !line.includes(MANAGEMENT_API_PREFIX));
    }

    if (trimmedSearchQuery) {
      const queryLowered = trimmedSearchQuery.toLowerCase();
      working = working.filter((line) => line.toLowerCase().includes(queryLowered));
    }

    return working.map((line) => parseLogLine(line));
  }, [baseLines, hideManagementLogs, trimmedSearchQuery]);

  const filters = useLogFilters({ parsedLines: parsedSearchLines });
  const structuredFiltersPanelId = 'logs-structured-filters';
  const structuredFilterCount =
    filters.methodFilters.length + filters.statusFilters.length + filters.pathFilters.length;

  const { filteredParsedLines, filteredLines, removedCount } = useMemo(() => {
    const filteredParsed = parsedSearchLines.filter((line) => {
      if (
        filters.methodFilterSet.size > 0 &&
        (!line.method || !filters.methodFilterSet.has(line.method))
      ) {
        return false;
      }

      const statusGroup = resolveStatusGroup(line.statusCode);
      if (
        filters.statusFilterSet.size > 0 &&
        (!statusGroup || !filters.statusFilterSet.has(statusGroup))
      ) {
        return false;
      }

      if (filters.pathFilterSet.size > 0 && (!line.path || !filters.pathFilterSet.has(line.path))) {
        return false;
      }

      return true;
    });

    return {
      filteredParsedLines: filteredParsed,
      filteredLines: filteredParsed.map((line) => line.raw),
      removedCount: Math.max(baseLines.length - filteredParsed.length, 0)
    };
  }, [
    baseLines,
    filters.methodFilterSet,
    filters.pathFilterSet,
    filters.statusFilterSet,
    parsedSearchLines
  ]);

  const parsedVisibleLines = useMemo(
    () => (showRawLogs ? [] : filteredParsedLines),
    [filteredParsedLines, showRawLogs]
  );

  const rawVisibleText = useMemo(() => filteredLines.join('\n'), [filteredLines]);

  const scroller = useLogScroller({
    logState,
    setLogState,
    loading,
    isSearching,
    filteredLineCount: filteredLines.length,
    hasStructuredFilters: filters.hasStructuredFilters,
    showRawLogs
  });

  logScrollerRef.current = scroller;

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
      downloadBlob({
        filename: `request-${id}.log`,
        blob: new Blob([response.data], { type: 'text/plain' })
      });
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

              <div className={styles.filterPanelHeader}>
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  className={styles.filterPanelToggle}
                  onClick={() => setStructuredFiltersExpanded((prev) => !prev)}
                  aria-expanded={structuredFiltersExpanded}
                  aria-controls={structuredFiltersPanelId}
                  title={
                    structuredFiltersExpanded
                      ? t('logs.filter_panel_collapse')
                      : t('logs.filter_panel_expand')
                  }
                >
                  <span className={styles.filterPanelButtonContent}>
                    <IconSlidersHorizontal size={16} />
                    <span>{t('logs.filter_panel_title')}</span>
                    {structuredFilterCount > 0 && (
                      <span className={styles.filterPanelCount}>
                        {t('logs.filter_panel_active_count', { count: structuredFilterCount })}
                      </span>
                    )}
                    {structuredFiltersExpanded ? (
                      <IconChevronUp size={16} />
                    ) : (
                      <IconChevronDown size={16} />
                    )}
                  </span>
                </Button>
              </div>

              {structuredFiltersExpanded && (
                <div id={structuredFiltersPanelId} className={styles.structuredFilters}>
                  <div className={styles.filterChipGroup}>
                    <span className={styles.filterChipLabel}>{t('logs.filter_method')}</span>
                    <div className={styles.filterChipList}>
                      {HTTP_METHODS.map((method) => {
                        const active = filters.methodFilters.includes(method);
                        const count = filters.methodCounts[method] ?? 0;
                        return (
                          <button
                            key={method}
                            type="button"
                            className={`${styles.filterChip} ${active ? styles.filterChipActive : ''}`}
                            onClick={() => filters.toggleMethodFilter(method)}
                            disabled={count === 0 && !active}
                            aria-pressed={active}
                          >
                            {method} ({count})
                          </button>
                        );
                      })}
                    </div>
                  </div>

                  <div className={styles.filterChipGroup}>
                    <span className={styles.filterChipLabel}>{t('logs.filter_status')}</span>
                    <div className={styles.filterChipList}>
                      {STATUS_GROUPS.map((statusGroup) => {
                        const active = filters.statusFilters.includes(statusGroup);
                        const count = filters.statusCounts[statusGroup] ?? 0;
                        return (
                          <button
                            key={statusGroup}
                            type="button"
                            className={`${styles.filterChip} ${active ? styles.filterChipActive : ''}`}
                            onClick={() => filters.toggleStatusFilter(statusGroup)}
                            disabled={count === 0 && !active}
                            aria-pressed={active}
                          >
                            {t(`logs.filter_status_${statusGroup}`)} ({count})
                          </button>
                        );
                      })}
                    </div>
                  </div>

                  <div className={styles.filterChipGroup}>
                    <span className={styles.filterChipLabel}>{t('logs.filter_path')}</span>
                    <div className={styles.filterChipList}>
                      {filters.pathOptions.length === 0 ? (
                        <span className={styles.filterChipHint}>{t('logs.filter_path_empty')}</span>
                      ) : (
                        filters.pathOptions.map(({ path, count }) => {
                          const active = filters.pathFilters.includes(path);
                          return (
                            <button
                              key={path}
                              type="button"
                              className={`${styles.filterChip} ${active ? styles.filterChipActive : ''}`}
                              onClick={() => filters.togglePathFilter(path)}
                              aria-pressed={active}
                              title={path}
                            >
                              {path} ({count})
                            </button>
                          );
                        })
                      )}
                    </div>
                  </div>

                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={filters.clearStructuredFilters}
                    disabled={!filters.hasStructuredFilters}
                  >
                    {t('logs.clear_filters')}
                  </Button>
                </div>
              )}

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
              <div
                ref={scroller.logViewerRef}
                className={styles.logPanel}
                onScroll={scroller.handleLogScroll}
              >
                {scroller.canLoadMore && (
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
                      const canTraceRequest = isTraceableRequestPath(line.path);
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

                            {canTraceRequest && (
                              <button
                                type="button"
                                className={styles.traceButton}
                                onClick={(event) => {
                                  event.stopPropagation();
                                  cancelLongPress();
                                  trace.openTraceModal(line);
                                }}
                                title={t('logs.trace_button')}
                              >
                                {t('logs.trace_button')}
                              </button>
                            )}
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
        open={Boolean(trace.traceLogLine)}
        onClose={trace.closeTraceModal}
        title={t('logs.trace_title')}
        footer={
          <>
            {trace.traceLogLine?.requestId && (
              <Button
                variant="secondary"
                onClick={() => {
                  if (trace.traceLogLine?.requestId) {
                    void downloadRequestLog(trace.traceLogLine.requestId);
                  }
                }}
                loading={requestLogDownloading}
              >
                {t('logs.trace_download_request_log')}
              </Button>
            )}
            <Button
              variant="secondary"
              onClick={trace.closeTraceModal}
              disabled={requestLogDownloading}
            >
              {t('common.close')}
            </Button>
          </>
        }
      >
        {trace.traceLogLine && (
          <div className={styles.tracePanel}>
            <div className={styles.traceNotice}>{t('logs.trace_notice')}</div>

            <h3 className={styles.traceSectionTitle}>{t('logs.trace_log_info')}</h3>
            <div className={styles.traceInfoGrid}>
              <div className={styles.traceInfoItem}>
                <span className={styles.traceInfoLabel}>{t('logs.trace_request_id')}</span>
                <span className={styles.traceInfoValue}>{trace.traceLogLine.requestId || '-'}</span>
              </div>
              <div className={styles.traceInfoItem}>
                <span className={styles.traceInfoLabel}>{t('logs.trace_method')}</span>
                <span className={styles.traceInfoValue}>{trace.traceLogLine.method || '-'}</span>
              </div>
              <div className={styles.traceInfoItem}>
                <span className={styles.traceInfoLabel}>{t('logs.trace_path')}</span>
                <span className={styles.traceInfoValue}>{trace.traceLogLine.path || '-'}</span>
              </div>
              <div className={styles.traceInfoItem}>
                <span className={styles.traceInfoLabel}>{t('logs.trace_status_code')}</span>
                <span className={styles.traceInfoValue}>
                  {typeof trace.traceLogLine.statusCode === 'number'
                    ? trace.traceLogLine.statusCode
                    : '-'}
                </span>
              </div>
              <div className={styles.traceInfoItem}>
                <span className={styles.traceInfoLabel}>{t('logs.trace_latency')}</span>
                <span className={styles.traceInfoValue}>{trace.traceLogLine.latency || '-'}</span>
              </div>
              <div className={styles.traceInfoItem}>
                <span className={styles.traceInfoLabel}>{t('logs.trace_ip')}</span>
                <span className={styles.traceInfoValue}>{trace.traceLogLine.ip || '-'}</span>
              </div>
              <div className={styles.traceInfoItem}>
                <span className={styles.traceInfoLabel}>{t('logs.trace_timestamp')}</span>
                <span className={styles.traceInfoValue}>{trace.traceLogLine.timestamp || '-'}</span>
              </div>
              <div className={`${styles.traceInfoItem} ${styles.traceInfoItemWide}`}>
                <span className={styles.traceInfoLabel}>{t('logs.trace_message')}</span>
                <span className={styles.traceInfoValue}>{trace.traceLogLine.message || '-'}</span>
              </div>
            </div>

            <div className={styles.traceCandidatesHeader}>
              <h3 className={styles.traceSectionTitle}>{t('logs.trace_candidates_title')}</h3>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => {
                  void trace.refreshTraceUsageDetails().catch(() => {});
                }}
                loading={trace.traceLoading}
                disabled={requestLogDownloading}
              >
                {t('common.refresh')}
              </Button>
            </div>
            {trace.traceLoading ? (
              <div className="hint">{t('logs.trace_loading')}</div>
            ) : trace.traceError ? (
              <div className="error-box">{trace.traceError}</div>
            ) : trace.traceCandidates.length === 0 ? (
              <div className="hint">{t('logs.trace_no_match')}</div>
            ) : (
              <div className={styles.traceCandidates}>
                {trace.traceCandidates.map((candidate) => {
                  const sourceInfo = trace.resolveTraceSourceInfo(
                    String(candidate.detail.source ?? ''),
                    candidate.detail.auth_index
                  );
                  return (
                    <div
                      key={`${candidate.detail.__endpoint}-${candidate.detail.__modelName}-${candidate.detail.timestamp}-${candidate.detail.source}`}
                      className={styles.traceCandidate}
                    >
                      <div className={styles.traceCandidateHeader}>
                        {candidate.modelMatched && (
                          <span className={styles.traceModelBadge}>
                            {t('logs.trace_model_matched')}
                          </span>
                        )}
                        {candidate.timeDeltaMs !== null && (
                          <span className={styles.traceDelta}>
                            {t('logs.trace_delta_seconds', {
                              seconds: (candidate.timeDeltaMs / 1000).toFixed(2)
                            })}
                          </span>
                        )}
                      </div>
                      <div className={styles.traceCandidateGrid}>
                        <div className={styles.traceInfoItem}>
                          <span className={styles.traceInfoLabel}>{t('logs.trace_endpoint')}</span>
                          <span className={styles.traceInfoValue}>{candidate.detail.__endpoint}</span>
                        </div>
                        <div className={styles.traceInfoItem}>
                          <span className={styles.traceInfoLabel}>{t('logs.trace_model')}</span>
                          <span className={styles.traceInfoValue}>{candidate.detail.__modelName || '-'}</span>
                        </div>
                        <div className={styles.traceInfoItem}>
                          <span className={styles.traceInfoLabel}>{t('logs.trace_source')}</span>
                          <span
                            className={styles.traceInfoValue}
                            title={String(candidate.detail.source || '-')}
                          >
                            <span>{sourceInfo.displayName}</span>
                            {sourceInfo.type && (
                              <span className={styles.traceSourceType}>{sourceInfo.type}</span>
                            )}
                          </span>
                        </div>
                        <div className={styles.traceInfoItem}>
                          <span className={styles.traceInfoLabel}>{t('logs.trace_auth_index')}</span>
                          <span className={styles.traceInfoValue}>
                            {candidate.detail.auth_index ?? '-'}
                          </span>
                        </div>
                        <div className={styles.traceInfoItem}>
                          <span className={styles.traceInfoLabel}>{t('logs.trace_timestamp')}</span>
                          <span className={styles.traceInfoValue}>
                            {candidate.detail.timestamp || '-'}
                          </span>
                        </div>
                        <div className={styles.traceInfoItem}>
                          <span className={styles.traceInfoLabel}>{t('logs.trace_result')}</span>
                          <span className={styles.traceInfoValue}>
                            {candidate.detail.failed ? t('stats.failure') : t('stats.success')}
                          </span>
                        </div>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        )}
      </Modal>

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
