import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocalStorage } from '@/hooks/useLocalStorage';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import { logsApi } from '@/services/api/logs';
import { getErrorMessage } from '@/utils/helpers';
import {
  applyLogPage,
  buildLogsQuery,
  emptyLogBuffer,
  shouldCatchUp,
  INITIAL_VISIBLE_LINES,
} from '../model/logBuffer';
import { createLogRequestGuard, createLogRequestQueue } from '../model/logRequests';

const INITIAL_DISPLAY_LINES = INITIAL_VISIBLE_LINES;

const getErrorPayloadText = (err: unknown): string => {
  if (typeof err !== 'object' || err === null) return '';
  const payloads = [
    (err as { data?: unknown }).data,
    (err as { details?: unknown }).details,
  ].filter((payload) => payload !== undefined);
  return payloads
    .map((payload) => {
      if (typeof payload === 'string') return payload;
      try {
        return JSON.stringify(payload);
      } catch {
        return '';
      }
    })
    .join(' ');
};

const isLoggingToFileDisabledError = (err: unknown): boolean => {
  const text = `${getErrorMessage(err)} ${getErrorPayloadText(err)}`.toLowerCase();
  return text.includes('logging to file disabled');
};

type LogStreamOptions = {
  active: boolean;
  isFollowing: () => boolean;
  onFollow: () => void;
};

/** Owns application-log requests and invalidates synchronously, including A→B→A switches. */
export function useLogStream({ active, isFollowing, onFollow }: LogStreamOptions) {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const loggingToFileEnabled = useConfigStore((state) => state.config?.loggingToFile ?? false);
  const cpaNeedsFileLogging = !loggingToFileEnabled;
  const [fileLoggingRequired, setFileLoggingRequired] = useState(false);
  const showFileLoggingRequired = cpaNeedsFileLogging || fileLoggingRequired;
  const [requests] = useState(() => ({
    session: createLogRequestGuard(),
    logs: createLogRequestQueue(),
  }));
  const [logBuffer, setLogBuffer] = useState(emptyLogBuffer);
  const logBufferRef = useRef(logBuffer);
  const [visibleCount, setVisibleCount] = useState(INITIAL_DISPLAY_LINES);
  const [lastUpdated, setLastUpdated] = useState<number>();
  const [catchingUp, setCatchingUp] = useState(false);
  const [wasReset, setWasReset] = useState(false);
  const retryDelayRef = useRef(8000);
  const nextReadAtRef = useRef(0);
  const [loading, setLoading] = useState(true);
  const [clearingLogs, setClearingLogs] = useState(false);
  const [error, setError] = useState('');
  const [autoRefresh, setAutoRefresh] = useLocalStorage('logsPage.autoRefresh', false);
  const autoRefreshRef = useRef(autoRefresh);
  useLayoutEffect(() => {
    autoRefreshRef.current = autoRefresh;
  }, [autoRefresh]);
  const resetLogPosition = () => {
    const empty = emptyLogBuffer();
    logBufferRef.current = empty;
    setLogBuffer(empty);
    setVisibleCount(INITIAL_DISPLAY_LINES);
    setLastUpdated(undefined);
    setWasReset(false);
    setCatchingUp(false);
    nextReadAtRef.current = 0;
    retryDelayRef.current = 8000;
  };

  async function loadLogs(incremental = false) {
    // Queued reloads must read live context, not the render that started the old request.
    if (useAuthStore.getState().connectionStatus !== 'connected') {
      setLoading(false);
      return;
    }

    if (!useConfigStore.getState().config?.loggingToFile) {
      if (!incremental) {
        resetLogPosition();
        setFileLoggingRequired(false);
        setError('');
        setLoading(false);
      }
      return;
    }

    const request = requests.logs.startRead(incremental);
    if (request === null) return;

    if (logBufferRef.current.buffer.length === 0) setLoading(true);

    try {
      // One owner drains at most three pages, then yields to the UI and other actions.
      for (let page = 0; page < 3; page++) {
        const cursor = logBufferRef.current.cursor;
        const data = await logsApi.fetchLogs(buildLogsQuery(cursor));
        if (!requests.logs.isCurrent(request)) return;
        const stickToBottom = isFollowing();
        const next = applyLogPage(logBufferRef.current, data, cursor);
        const added = next.nextId - logBufferRef.current.nextId;
        logBufferRef.current = next;
        setLogBuffer(next);
        if (data.cursorReset) {
          setWasReset(true);
          setVisibleCount(INITIAL_DISPLAY_LINES);
        } else if (!stickToBottom) {
          setVisibleCount((count) => Math.min(next.buffer.length, count + added));
        }
        if (stickToBottom) onFollow();
        setFileLoggingRequired(false);
        setError('');
        setLastUpdated(Date.now());
        retryDelayRef.current = 8000;
        const more = shouldCatchUp(data, cursor);
        setCatchingUp(more);
        nextReadAtRef.current = Date.now() + (more ? 1000 : 8000);
        if (!more || (incremental && !autoRefreshRef.current)) break;
      }
    } catch (err: unknown) {
      if (!requests.logs.isCurrent(request)) return;
      setCatchingUp(false);
      if (isLoggingToFileDisabledError(err)) {
        setFileLoggingRequired(true);
        setError('');
        return;
      }
      setError(getErrorMessage(err) || t('logs.load_error'));
      nextReadAtRef.current = Date.now() + retryDelayRef.current;
      retryDelayRef.current = Math.min(retryDelayRef.current * 2, 60000);
    } finally {
      if (requests.logs.isCurrent(request)) {
        setLoading(false);
        if (requests.logs.finish(request)) void loadLogs(false);
      }
    }
  }

  const clearLogs = async () => {
    if (cpaNeedsFileLogging) {
      showNotification(t('logs.cpa_file_logging_required'), 'warning');
      return;
    }
    if (fileLoggingRequired) {
      showNotification(t('logs.file_logging_required'), 'warning');
      return;
    }
    const session = requests.session.capture();
    showConfirmation({
      title: t('logs.clear_confirm_title', { defaultValue: 'Clear Logs' }),
      message: t('logs.clear_application_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        if (!requests.session.isCurrent(session)) return;
        if (useAuthStore.getState().connectionStatus !== 'connected') return;
        if (!useConfigStore.getState().config?.loggingToFile) return;
        const request = requests.logs.startClear();
        if (request === null) return;
        setClearingLogs(true);
        setLoading(false);
        setError('');
        let clearFailed = false;
        try {
          await logsApi.clearLogs();
          if (!requests.logs.isCurrent(request)) return;
          resetLogPosition();
          setFileLoggingRequired(false);
          showNotification(t('logs.clear_success'), 'success');
        } catch (err: unknown) {
          if (!requests.logs.isCurrent(request)) return;
          clearFailed = true;
          const message = getErrorMessage(err);
          showNotification(
            `${t('notification.delete_failed')}${message ? `: ${message}` : ''}`,
            'error'
          );
        } finally {
          if (requests.logs.isCurrent(request)) {
            setClearingLogs(false);
            // Clear superseded the old read; recover it even when deletion fails.
            if (requests.logs.finish(request, clearFailed)) void loadLogs(false);
          }
        }
      },
    });
  };

  useEffect(() => {
    const resetLogs = () => {
      requests.logs.invalidate();
      resetLogPosition();
      setLoading(false);
      setClearingLogs(false);
      setError('');
      setFileLoggingRequired(false);
    };
    const invalidateSession = () => {
      requests.session.invalidate();
      requests.logs.invalidate();
    };
    // Invalidate at store notification time, not after React commits a new render.
    const unsubscribeAuth = useAuthStore.subscribe((next, previous) => {
      if (
        next.apiBase === previous.apiBase &&
        next.managementKey === previous.managementKey &&
        next.connectionStatus === previous.connectionStatus &&
        next.isAuthenticated === previous.isAuthenticated
      )
        return;
      invalidateSession();
      resetLogs();
    });
    const unsubscribeConfig = useConfigStore.subscribe((next, previous) => {
      if (next.config?.loggingToFile !== previous.config?.loggingToFile) resetLogs();
    });
    return () => {
      unsubscribeAuth();
      unsubscribeConfig();
      invalidateSession();
    };
  }, [requests]);

  useEffect(() => {
    if (connectionStatus === 'connected') {
      resetLogPosition();
      setFileLoggingRequired(false);
      loadLogs(false);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connectionStatus, apiBase, managementKey, loggingToFileEnabled]);

  useEffect(() => {
    if (!autoRefresh || !active || connectionStatus !== 'connected' || showFileLoggingRequired) {
      return;
    }
    const id = window.setInterval(() => {
      if (document.visibilityState === 'hidden' || Date.now() < nextReadAtRef.current) return;
      void loadLogs(true);
    }, 1000);
    return () => window.clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoRefresh, active, connectionStatus, showFileLoggingRequired]);

  return {
    logBuffer,
    visibleCount,
    setVisibleCount,
    lastUpdated,
    catchingUp,
    wasReset,
    loading,
    clearingLogs,
    error,
    autoRefresh,
    setAutoRefresh,
    cpaNeedsFileLogging,
    showFileLoggingRequired,
    loadLogs,
    clearLogs,
  };
}
