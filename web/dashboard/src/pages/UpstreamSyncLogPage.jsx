import React, { useState, useMemo, useCallback, useEffect } from 'react';
import {
  getUpstreamSyncLog,
  getUpstreamSyncLogEvent,
  getUpstreamSyncLogProviders,
  clearUpstreamSyncLog,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { Spinner, ErrorBanner } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import { useToast } from '../components/Toast.jsx';
import { TIMEZONES, loadTimezone } from './usageShared.jsx';

// UpstreamSyncLogPage surfaces the persisted outcome of every upstream
// OAuth/auth token refresh performed by the auth manager. Unlike the Cooldown
// Providers page (a live in-memory snapshot), this reads the upstream_sync_log
// PG table so an operator can audit refresh history (success + failure) across
// the 30-day retention window. Filters let you narrow by provider, trigger
// (auto / on_demand / unauthorized_retry), and outcome. Returns 503 when PG is
// not configured.

const AUTO_REFRESH_INTERVAL_MS = 10 * 1000;
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.upstreamSyncLogAutorefresh';
const PAGE_SIZE = 50;

const TRIGGER_OPTIONS = [
  { value: '', label: 'All triggers' },
  { value: 'auto', label: 'Auto (background)' },
  { value: 'on_demand', label: 'On demand' },
  { value: 'unauthorized_retry', label: 'Unauthorized retry' },
];

const SUCCESS_OPTIONS = [
  { value: '', label: 'All outcomes' },
  { value: 'true', label: 'Success only' },
  { value: 'false', label: 'Failures only' },
];

function readAutoRefresh() {
  try { return localStorage.getItem(AUTOREFRESH_STORAGE) !== '0'; } catch { return true; }
}

export default function UpstreamSyncLogPage() {
  const toast = useToast();
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());
  const [page, setPage] = useState(1);
  const [provider, setProvider] = useState('');
  const [trigger, setTrigger] = useState('');
  const [success, setSuccess] = useState('');
  const [selectedId, setSelectedId] = useState(null);
  const [clearing, setClearing] = useState(false);
  const tz = useMemo(loadTimezone, []);

  const filterKey = JSON.stringify({ provider, trigger, success });
  const providersReq = useAsync(() => getUpstreamSyncLogProviders(), []);

  const listParams = useMemo(() => ({
    provider, trigger, success,
    page, page_size: PAGE_SIZE,
  }), [provider, trigger, success, page]);

  const list = useAsync(() => getUpstreamSyncLog(listParams), [
    JSON.stringify(listParams),
  ]);

  const reload = useCallback(() => { list.reload(); }, [list]);
  useAutoRefresh(reload, AUTO_REFRESH_INTERVAL_MS, autoRefresh);

  // Reset to page 1 whenever a filter changes so operators never stare at an
  // empty out-of-range page after tightening a filter.
  useEffect(() => { setPage(1); }, [filterKey]);

  function toggleAutoRefresh() {
    setAutoRefresh((v) => {
      const next = !v;
      try { localStorage.setItem(AUTOREFRESH_STORAGE, next ? '1' : '0'); } catch { /* ignore */ }
      return next;
    });
  }

  function handleRefresh() {
    reload();
    toast.info('Sync log refreshed');
  }

  async function handleClear() {
    if (!window.confirm('Clear all upstream sync log rows? This cannot be undone.')) return;
    setClearing(true);
    try {
      const res = await clearUpstreamSyncLog();
      toast.success(`Cleared ${Number(res?.deleted || 0).toLocaleString()} sync log row(s)`);
      setPage(1);
      providersReq.reload();
      reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to clear sync log');
    } finally {
      setClearing(false);
    }
  }

  const events = list.data?.events || [];
  const total = list.data?.total || 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  // KPIs: compute from the current page (the backend returns total + per-page
  // rows, not aggregates). For a true total-success/failure count the operator
  // can filter, but the strip gives a quick at-a-glance read on the loaded rows.
  const loadedSuccess = events.filter((e) => e.success).length;
  const loadedFailure = events.length - loadedSuccess;

  const providerOptions = (providersReq.data?.providers || []).map((p) => ({ value: p, label: p }));

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Upstream Providers</h1>
          <div className="main__subtitle">
            Persisted outcome of every upstream OAuth/auth token refresh
            (success + failure) recorded by the auth manager, including the
            trigger that caused it. Source: <code>upstream_sync_log</code> table
            (30-day retention). Auto-refreshes every 10s.
          </div>
        </div>
        <div className="row gap-sm">
          <button
            className={`autorefresh-chip ${autoRefresh ? '' : 'autorefresh-chip--off'}`}
            onClick={toggleAutoRefresh}
            title={autoRefresh ? 'Auto-refresh every 10s — click to pause' : 'Auto-refresh paused — click to resume'}
          >
            <span className="autorefresh-chip__dot" />
            {autoRefresh ? 'Live' : 'Paused'}
          </button>
          <button onClick={handleRefresh}>Refresh</button>
          <button onClick={handleClear} disabled={clearing || total === 0}>
            {clearing ? 'Clearing…' : 'Clear log'}
          </button>
        </div>
      </div>

      {/* KPI strip */}
      <div className="stats-grid">
        <div className="stat-card">
          <div className="stat-card__label">Total (matching filter)</div>
          <div className="stat-card__value">{total.toLocaleString()}</div>
          <div className="stat-card__hint">refresh outcomes in window</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Success (this page)</div>
          <div className="stat-card__value">{loadedSuccess.toLocaleString()}</div>
          <div className="stat-card__hint">of {events.length} loaded rows</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Failures (this page)</div>
          <div className="stat-card__value">{loadedFailure.toLocaleString()}</div>
          <div className="stat-card__hint">of {events.length} loaded rows</div>
        </div>
      </div>

      {list.error && <ErrorBanner error={list.error} onRetry={reload} />}

      {/* Filter bar */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row gap-sm" style={{ flexWrap: 'wrap', alignItems: 'center' }}>
          <label className="filter-label">
            Provider
            <select value={provider} onChange={(e) => setProvider(e.target.value)}>
              <option value="">All providers</option>
              {providerOptions.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </label>
          <label className="filter-label">
            Trigger
            <select value={trigger} onChange={(e) => setTrigger(e.target.value)}>
              {TRIGGER_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </label>
          <label className="filter-label">
            Outcome
            <select value={success} onChange={(e) => setSuccess(e.target.value)}>
              {SUCCESS_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </label>
          <span className="dim" style={{ fontSize: 12, marginLeft: 'auto' }}>
            times in {tz}
          </span>
        </div>
      </div>

      {/* Table */}
      <div className="card" style={{ marginTop: 16 }}>
        <SyncLogTableBody
          loading={list.loading}
          error={list.error}
          events={events}
          tz={tz}
          onRowClick={setSelectedId}
        />
        <Pager
          page={page}
          totalPages={totalPages}
          total={total}
          pageSize={PAGE_SIZE}
          onPageChange={setPage}
        />
      </div>

      {selectedId != null && (
        <SyncLogDetailModal id={selectedId} onClose={() => setSelectedId(null)} tz={tz} />
      )}
    </>
  );
}

// SyncLogTableBody renders the refresh outcome table. Headers are always shown.
function SyncLogTableBody({ loading, error, events, tz, onRowClick }) {
  if (loading) {
    return (
      <TableShell tzLabel={tz}>
        {Array.from({ length: 5 }).map((_, i) => (
          <tr key={i} className="skeleton-row">
            {Array.from({ length: 7 }).map((__, j) => (
              <td key={j}><span className="skeleton-line" /></td>
            ))}
          </tr>
        ))}
      </TableShell>
    );
  }
  if (error) return <ErrorBanner error={error} />;
  return (
    <TableShell tzLabel={tz}>
      {events.length === 0 && (
        <tr>
          <td colSpan={7} style={{ textAlign: 'center', padding: '24px 8px' }}>
            No refresh outcomes recorded in this window.
          </td>
        </tr>
      )}
      {events.map((e) => (
        <tr
          key={e.id}
          className="table__row"
          style={{ cursor: 'pointer' }}
          onClick={() => onRowClick?.(e.id)}
          title="Click for details"
        >
          <td className="mono" style={{ whiteSpace: 'nowrap' }}>
            {formatInTz(e.occurred_at, tz)}
          </td>
          <td className="mono">{e.auth_id || '—'}</td>
          <td className="mono">{e.provider || '—'}</td>
          <td>
            <span className="badge badge--muted">{formatTrigger(e.trigger)}</span>
          </td>
          <td style={{ textAlign: 'right' }} className="mono">{e.duration_ms ?? 0}ms</td>
          <td>
            {e.success ? (
              <span className="badge badge--ok">Success</span>
            ) : (
              <span className="badge badge--err">Failed</span>
            )}
          </td>
          <td
            className="mono"
            style={{ maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
            title={e.error_message || ''}
          >
            {e.error_message || <span style={{ color: 'var(--text-dim)' }}>—</span>}
          </td>
        </tr>
      ))}
    </TableShell>
  );
}

function TableShell({ children, tzLabel }) {
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="table">
        <thead>
          <tr>
            <th>Time{tzLabel ? ` (${tzLabel})` : ''}</th>
            <th>Auth ID</th>
            <th>Provider</th>
            <th>Trigger</th>
            <th style={{ textAlign: 'right' }}>Duration</th>
            <th>Status</th>
            <th>Error</th>
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

// SyncLogDetailModal fetches a single outcome and shows the full error_message
// + raw fields. Mirrors the EventDetailModal pattern from usageShared.
function SyncLogDetailModal({ id, onClose, tz }) {
  const detail = useAsync(() => getUpstreamSyncLogEvent(id), [id]);
  return (
    <div className="modal-overlay" onClick={onClose}>
      <div className="modal modal--wide" onClick={(ev) => ev.stopPropagation()}>
        <div className="modal__header">
          <h3 className="modal__title">Refresh outcome #{id}</h3>
          <button className="modal__close" onClick={onClose}>×</button>
        </div>
        <div className="modal__body">
          {detail.loading && <Spinner />}
          {detail.error && <ErrorBanner error={detail.error} onRetry={detail.reload} />}
          {detail.data && (
            <div className="detail-rows">
              <DetailRow label="Time" value={formatInTz(detail.data.occurred_at, tz)} mono />
              <DetailRow label="Time (UTC)" value={detail.data.occurred_at} mono />
              <DetailRow label="Auth ID" value={detail.data.auth_id || '—'} mono />
              <DetailRow label="Provider" value={detail.data.provider || '—'} mono />
              <DetailRow label="Trigger" value={formatTrigger(detail.data.trigger)} />
              <DetailRow
                label="Status"
                value={detail.data.success ? 'Success' : 'Failed'}
              />
              <DetailRow
                label="Duration"
                value={`${detail.data.duration_ms ?? 0} ms`}
                mono
              />
              <DetailRow
                label="Error message"
                value={detail.data.error_message || '—'}
                mono
              />
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function DetailRow({ label, value, mono }) {
  return (
    <div className="detail-row">
      <div className="detail-row__label">{label}</div>
      <div className={`detail-row__value ${mono ? 'mono' : ''}`} style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
        {value}
      </div>
    </div>
  );
}

// formatInTz renders an ISO timestamp in the operator's preferred timezone
// (default UTC). Falls back to the raw value on parse failure.
function formatInTz(iso, tz) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return String(iso);
  try {
    return new Intl.DateTimeFormat('en-US', {
      timeZone: tz,
      year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
      hour12: false,
    }).format(d);
  } catch {
    return d.toISOString();
  }
}

function formatTrigger(trigger) {
  switch (trigger) {
    case 'auto': return 'auto';
    case 'on_demand': return 'on demand';
    case 'unauthorized_retry': return 'unauthorized retry';
    default: return trigger || '—';
  }
}
