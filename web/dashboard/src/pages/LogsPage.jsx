import React, { useState, useMemo, useCallback, useEffect, useRef } from 'react';
import { getLogs, clearLogs, ApiError } from '../api/client.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { Spinner } from '../components/Primitives.jsx';
import { Modal } from '../components/Primitives.jsx';
import { useToast } from '../components/Toast.jsx';
import EventsLive from './EventsLive.jsx';

// LogsPage — combined "Logs" hub: server log lines (Static) + live
// structured event stream (Live). Round-2 (docs/plans/2026-09-17-...)
// added the Live tab via the new /v0/management/events endpoint + SSE
// stream.

const POLL_INTERVAL_MS = 3 * 1000;
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.logsAutorefresh';
const MAX_LINES = 10000;
const INITIAL_TAIL = 1000;

// Tab keys for the Logs hub. 'static' is the original logrus-line viewer;
// 'live' is the round-2 SSE-driven event feed.
const TAB_STATIC = 'static';
const TAB_LIVE = 'live';

const LEVELS = ['info', 'warn', 'error', 'debug', 'panic', 'fatal'];

// Matches the LogFormatter output: `[ts] [reqID] [level] [file:line] msg`
// (the caller segment is optional). Level is padded to 5 chars, hence the
// trailing \s* inside the level group.
const LOG_LINE_RE = /^\[([^\]]+)\] \[([^\]]*)\] \[([a-z]+)\s*\] (?:\[([^\]]+)\] )?([\s\S]*)$/;

// parseLogLine splits one raw log line into its parts. Lines that do not
// match the formatter (e.g. panics written by the runtime, multi-line
// stack-trace continuations) come back with level 'other' and the raw text
// as the message so they are never lost.
export function parseLogLine(raw) {
  const m = LOG_LINE_RE.exec(raw);
  if (!m) return { ts: '', reqId: '', level: 'other', caller: '', msg: raw };
  return { ts: m[1], reqId: m[2], level: m[3], caller: m[4] || '', msg: m[5] };
}

// matchesFilter applies the level dropdown + case-insensitive substring
// search to one raw line. A single-select level filter of '' means all;
// 'other' catches unparseable lines.
export function matchesFilter(raw, levelFilter, search) {
  if (levelFilter) {
    const { level } = parseLogLine(raw);
    if (level !== levelFilter) return false;
  }
  if (search) {
    if (!raw.toLowerCase().includes(search.toLowerCase())) return false;
  }
  return true;
}

function readAutoRefresh() {
  try { return localStorage.getItem(AUTOREFRESH_STORAGE) === '1'; } catch { return false; }
}

function writeAutoRefresh(on) {
  try { localStorage.setItem(AUTOREFRESH_STORAGE, on ? '1' : '0'); } catch { /* ignore */ }
}

export default function LogsPage() {
  const toast = useToast();
  const [activeTab, setActiveTab] = useState(TAB_STATIC);
  const [lines, setLines] = useState([]);
  const [capped, setCapped] = useState(false);
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());
  const [viewMode, setViewMode] = useState('raw'); // 'raw' | 'table'
  const [levelFilter, setLevelFilter] = useState('');
  const [search, setSearch] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [disabledReason, setDisabledReason] = useState(null);
  const [lastUpdated, setLastUpdated] = useState(null);
  const [confirmClear, setConfirmClear] = useState(false);
  const [clearing, setClearing] = useState(false);
  const [rotated, setRotated] = useState(false);

  // Cursor + error-consecutive counter live in refs: they change on every
  // poll but must never trigger re-renders or reset the auto-refresh
  // interval.
  const cursorRef = useRef('');
  const consecutiveErrorsRef = useRef(0);
  const [pollStalled, setPollStalled] = useState(false);

  const scrollRef = useRef(null);
  const pinnedToBottomRef = useRef(true);

  const applyTail = useCallback((data) => {
    const fresh = Array.isArray(data?.lines) ? data.lines : [];
    cursorRef.current = data?.['next-cursor'] || '';
    setLines(fresh);
    setCapped(false);
    pinnedToBottomRef.current = true;
  }, []);

  const fetchTail = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const data = await getLogs({ limit: INITIAL_TAIL });
      setDisabledReason(null);
      applyTail(data);
      setLastUpdated(Date.now());
      consecutiveErrorsRef.current = 0;
      setPollStalled(false);
    } catch (err) {
      if (err instanceof ApiError && err.status === 400) {
        setDisabledReason(err.message || 'logging to file disabled');
      } else {
        setError(err?.message || 'Failed to load logs');
      }
    } finally {
      setLoading(false);
    }
  }, [applyTail]);

  const pollOnce = useCallback(async () => {
    try {
      const data = await getLogs({ cursor: cursorRef.current, limit: 0 });
      if (data?.['cursor-reset']) {
        // The cursor was invalidated (log rotation): re-tail instead of
        // trying to replay from a dead offset.
        const tail = await getLogs({ limit: INITIAL_TAIL });
        applyTail(tail);
        setRotated(true);
      } else {
        const fresh = Array.isArray(data?.lines) ? data.lines : [];
        cursorRef.current = data?.['next-cursor'] || cursorRef.current;
        if (fresh.length > 0) {
          setLines((prev) => {
            const next = prev.concat(fresh);
            if (next.length > MAX_LINES) {
              setCapped(true);
              return next.slice(next.length - MAX_LINES);
            }
            return next;
          });
        }
        setRotated(false);
      }
      setLastUpdated(Date.now());
      consecutiveErrorsRef.current = 0;
      setPollStalled(false);
    } catch (err) {
      if (err instanceof ApiError && err.status === 400) {
        // Logging-to-file was turned off mid-session: stop polling.
        setDisabledReason(err.message || 'logging to file disabled');
        setAutoRefresh(false);
        return;
      }
      consecutiveErrorsRef.current += 1;
      if (consecutiveErrorsRef.current >= 2) {
        // Two failures in a row (server restarting, network down): pause and
        // let the operator resume manually instead of hammering.
        setPollStalled(true);
      }
    }
  }, [applyTail]);

  useAutoRefresh(pollOnce, POLL_INTERVAL_MS, autoRefresh && !pollStalled && !disabledReason);

  useEffect(() => { fetchTail(); }, [fetchTail]);

  const toggleAutoRefresh = useCallback(() => {
    setAutoRefresh((v) => {
      const next = !v;
      writeAutoRefresh(next);
      if (next) {
        consecutiveErrorsRef.current = 0;
        setPollStalled(false);
      }
      return next;
    });
  }, []);

  // Auto-scroll: follow new output only while the operator is already near
  // the bottom; never steal scroll position while reading upward.
  const onScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    pinnedToBottomRef.current = el.scrollHeight - el.scrollTop - el.clientHeight < 50;
  }, []);

  useEffect(() => {
    const el = scrollRef.current;
    if (el && pinnedToBottomRef.current) {
      el.scrollTop = el.scrollHeight;
    }
  }, [lines, viewMode]);

  const handleClear = useCallback(async () => {
    setClearing(true);
    try {
      await clearLogs();
      toast.success('Server logs cleared');
      setConfirmClear(false);
      await fetchTail();
    } catch (err) {
      toast.error(err?.message || 'Failed to clear logs');
    } finally {
      setClearing(false);
    }
  }, [fetchTail, toast]);

  const visible = useMemo(
    () => lines.filter((l) => matchesFilter(l, levelFilter, search.trim())),
    [lines, levelFilter, search],
  );

  const headerSubtitle = activeTab === TAB_LIVE
    ? 'Live structured event stream from /v0/management/events. SSE with a 10s polling fallback.'
    : disabledReason
      ? 'Server log file access is disabled.'
      : 'Server logs (logrus). Manual refresh by default; auto-poll every 3s when live. Last 1,000 lines on reload, newest appended at the bottom.';

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Server Logs</h1>
          <div className="main__subtitle">{headerSubtitle}</div>
        </div>
        <div className="row gap-sm">
          {activeTab === TAB_STATIC && !disabledReason && (
            <>
              <button
                className={`autorefresh-chip ${autoRefresh && !pollStalled ? '' : 'autorefresh-chip--off'}`}
                onClick={toggleAutoRefresh}
                title={pollStalled ? 'Polling paused after repeated errors — click to resume' : autoRefresh ? 'Auto-refresh every 3s — click to pause' : 'Auto-refresh paused — click to resume'}
              >
                <span className="autorefresh-chip__dot" />
                {pollStalled ? 'Retry…' : autoRefresh ? 'Live' : 'Paused'}
              </button>
              <button onClick={fetchTail} disabled={loading}>Refresh</button>
              <button
                className="danger"
                onClick={() => setConfirmClear(true)}
                disabled={lines.length === 0 && !capped}
              >
                Clear…
              </button>
            </>
          )}
        </div>
      </div>

      {/* Tabs */}
      <div className="card logs-view-toggle" role="group" aria-label="Logs view" style={{ marginTop: 16 }}>
        <button
          className={activeTab === TAB_STATIC ? 'is-active' : ''}
          onClick={() => setActiveTab(TAB_STATIC)}
          aria-pressed={activeTab === TAB_STATIC}
        >
          Static
        </button>
        <button
          className={activeTab === TAB_LIVE ? 'is-active' : ''}
          onClick={() => setActiveTab(TAB_LIVE)}
          aria-pressed={activeTab === TAB_LIVE}
        >
          Live
        </button>
      </div>

      {activeTab === TAB_LIVE && <EventsLive />}

      {activeTab === TAB_STATIC && disabledReason && (
        <div className="card logs-disabled-banner" style={{ marginTop: 16 }}>
          <strong>Logging to file is disabled.</strong>{' '}
          The server answered: <code>{disabledReason}</code>. Enable{' '}
          <code>logging-to-file: true</code> in <code>config.yaml</code> and
          restart to browse logs here.
        </div>
      )}

      {activeTab === TAB_STATIC && !disabledReason && (
        <>
          {/* Toolbar */}
          <div className="card" style={{ marginTop: 16 }}>
            <div className="row gap-sm" style={{ flexWrap: 'wrap', alignItems: 'center' }}>
              <label className="filter-label">
                Level
                <select
                  value={levelFilter}
                  onChange={(e) => setLevelFilter(e.target.value)}
                >
                  <option value="">All levels</option>
                  {LEVELS.map((l) => (
                    <option key={l} value={l}>{l.toUpperCase()}</option>
                  ))}
                  <option value="other">OTHER</option>
                </select>
              </label>
              <label className="filter-label">
                Search
                <input
                  type="search"
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  placeholder="Filter lines…"
                  style={{ minWidth: 220 }}
                />
              </label>
              <div className="logs-view-toggle" role="group" aria-label="View mode">
                <button
                  className={viewMode === 'raw' ? 'is-active' : ''}
                  onClick={() => setViewMode('raw')}
                >
                  Raw
                </button>
                <button
                  className={viewMode === 'table' ? 'is-active' : ''}
                  onClick={() => setViewMode('table')}
                >
                  Table
                </button>
              </div>
              <span className="logs-counter mono">
                {visible.length.toLocaleString()} of {lines.length.toLocaleString()} lines{capped ? ` (capped at ${MAX_LINES.toLocaleString()})` : ''}
              </span>
            </div>
          </div>

          {(error || rotated) && (
            <div className="logs-status-row">
              {error && <span className="logs-status-error">{error}</span>}
              {rotated && <span className="logs-status-note">Log rotated — view reset to latest tail.</span>}
            </div>
          )}

          {/* Log area */}
          <div className="card logs-panel" style={{ marginTop: 16 }}>
            {loading && lines.length === 0 ? (
              <div className="logs-panel__empty"><Spinner label="Loading logs…" /></div>
            ) : visible.length === 0 ? (
              <div className="logs-panel__empty">
                {lines.length === 0 ? 'No log lines loaded. Use Refresh to load the latest tail.' : 'No lines match the current filter.'}
              </div>
            ) : viewMode === 'raw' ? (
              <div className="logs-view logs-view--raw" ref={scrollRef} onScroll={onScroll}>
                {visible.map((line, i) => {
                  const { level } = parseLogLine(line);
                  return (
                    <div key={i} className={`logs-line logs-line--${level}`}>
                      {line}
                    </div>
                  );
                })}
              </div>
            ) : (
              <div className="logs-view" ref={scrollRef} onScroll={onScroll}>
                <table className="logs-table">
                  <thead>
                    <tr>
                      <th>Timestamp</th>
                      <th>Level</th>
                      <th>Message</th>
                    </tr>
                  </thead>
                  <tbody>
                    {visible.map((line, i) => {
                      const { ts, level, msg } = parseLogLine(line);
                      return (
                        <tr key={i} className={`logs-line--${level}`}>
                          <td className="mono logs-table__ts">{ts || '—'}</td>
                          <td className="mono logs-table__level">{level.toUpperCase()}</td>
                          <td className="mono logs-table__msg">{msg}</td>
                        </tr>
                      );
                    })}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          {/* Status bar */}
          <div className="logs-status-bar">
            <span>
              {lastUpdated
                ? `Last updated ${new Date(lastUpdated).toLocaleTimeString()}`
                : 'Not loaded yet'}
            </span>
            <span>
              {pollStalled
                ? 'Auto-refresh paused after repeated errors — click Live to resume.'
                : autoRefresh
                  ? `Auto-refreshing every ${POLL_INTERVAL_MS / 1000}s`
                  : 'Auto-refresh off'}
            </span>
          </div>
        </>
      )}

      {confirmClear && (
        <Modal
          title="Clear server logs"
          onClose={() => setConfirmClear(false)}
          size="sm"
          footer={(
            <div className="row gap-sm" style={{ justifyContent: 'flex-end' }}>
              <button onClick={() => setConfirmClear(false)}>Cancel</button>
              <button className="danger" onClick={handleClear} disabled={clearing}>
                {clearing ? 'Clearing…' : 'Clear logs'}
              </button>
            </div>
          )}
        >
          <p>
            This removes all rotated log files and truncates the active log
            file on the server. This cannot be undone.
          </p>
        </Modal>
      )}
    </>
  );
}
