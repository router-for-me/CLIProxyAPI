import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  getAlerts,
  getActiveAlerts,
  getUnreadAlertCount,
  markAllAlertsRead,
  dismissAlert,
  clearAlerts,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { Spinner, ErrorBanner, EmptyState, Modal } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import { useToast } from '../components/Toast.jsx';

// AlertsPage surfaces the notification feed produced by the background alert
// sweep (max-spend, error-rate, provider cooldown, model-health). It combines
// a live "Active" feed (currently-firing conditions) with a full paged/filtered
// history table, an unread-badge, and a Settings modal to tune the sweep.
//
// The page mirrors the Model Health page (live snapshot + autorefresh + settings
// modal) for the active feed and the paged-history pattern for the full log.

const AUTO_REFRESH_INTERVAL_MS = 15 * 1000;
const UNREAD_POLL_MS = 30 * 1000;
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.alertsAutorefresh';
const PAGE_SIZE = 50;

const CATEGORY_OPTIONS = [
  { value: '', label: 'All types' },
  { value: 'user_budget', label: 'Internal user max spend' },
  { value: 'api_key_budget', label: 'API key max spend' },
  { value: 'error_rate', label: 'Error rate' },
  { value: 'provider_cooldown', label: 'Provider cooldown' },
  { value: 'model_substitution', label: 'Model substitution' },
];

const SEVERITY_OPTIONS = [
  { value: '', label: 'All severities' },
  { value: 'critical', label: 'Critical' },
  { value: 'warning', label: 'Warning' },
];

const STATUS_OPTIONS = [
  { value: '', label: 'All statuses' },
  { value: 'active', label: 'Active' },
  { value: 'dismissed', label: 'Dismissed' },
];

function readAutoRefresh() {
  try { return localStorage.getItem(AUTOREFRESH_STORAGE) !== '0'; }
  catch { return true; }
}

// formatTime renders an ISO timestamp as local "YYYY-MM-DD HH:MM:SS".
function formatTime(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const p = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

export default function AlertsPage() {
  const toast = useToast();
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());

  // Live feed + unread badge.
  const active = useAsync(() => getActiveAlerts(), []);
  const unread = useAsync(() => getUnreadAlertCount(), []);

  // History table (filter + pager).
  const [page, setPage] = useState(1);
  const [typeFilter, setTypeFilter] = useState('');
  const [severityFilter, setSeverityFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState(''); // 'active' | 'dismissed' | ''
  const [selectedAlert, setSelectedAlert] = useState(null);
  const [clearing, setClearing] = useState(false);

  const filterKey = JSON.stringify({ typeFilter, severityFilter, statusFilter });

  const listParams = useMemo(() => ({
    alert_type: typeFilter,
    severity: severityFilter,
    ...(statusFilter ? { dismissed: statusFilter === 'dismissed' ? 'true' : 'false' } : {}),
    page,
    page_size: PAGE_SIZE,
  }), [typeFilter, severityFilter, statusFilter, page]);

  const list = useAsync(() => getAlerts(listParams), [JSON.stringify(listParams)]);

  // Broadcast helper: busts the sidebar bell badge across the whole app after
  // any read/dismiss action, so every open view converges without a full poll.
  const broadcastAlerts = useCallback(() => {
    window.dispatchEvent(new CustomEvent('nixllm:alerts-changed'));
  }, []);

  const reloadAll = useCallback(() => {
    active.reload();
    unread.reload();
    list.reload();
  }, [active, unread, list]);

  useAutoRefresh(reloadAll, AUTO_REFRESH_INTERVAL_MS, autoRefresh);
  // The bell badge polls on an independent, slower cadence.
  useAutoRefresh(() => unread.reload(), UNREAD_POLL_MS, true);
  // Listen for alert mutations elsewhere so the badge stays fresh immediately.
  useEffect(() => {
    window.addEventListener('nixllm:alerts-changed', () => unread.reload());
    return () => window.removeEventListener('nixllm:alerts-changed', () => unread.reload());
  }, [unread]);

  // Reset to page 1 when a filter changes.
  useEffect(() => { setPage(1); }, [filterKey]);

  function toggleAutoRefresh() {
    setAutoRefresh((v) => {
      const next = !v;
      try { localStorage.setItem(AUTOREFRESH_STORAGE, next ? '1' : '0'); } catch { /* ignore */ }
      return next;
    });
  }

  function handleRefresh() {
    reloadAll();
    toast.info('Alerts refreshed');
  }

  async function handleMarkAllRead() {
    try {
      const res = await markAllAlertsRead();
      toast.success(`Marked ${Number(res?.affected || 0).toLocaleString()} alert(s) as read`);
      broadcastAlerts();
      unread.reload();
      list.reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to mark alerts as read');
    }
  }

  async function handleDismiss(id) {
    try {
      await dismissAlert(id);
      toast.success('Alert dismissed');
      broadcastAlerts();
      reloadAll();
    } catch (err) {
      toast.error(err?.message || 'Failed to dismiss alert');
    }
  }

  async function handleClear() {
    if (!window.confirm('Clear ALL alert rows (history + active feed)? This cannot be undone.')) return;
    setClearing(true);
    try {
      const res = await clearAlerts();
      toast.success(`Cleared ${Number(res?.deleted || 0).toLocaleString()} alert(s)`);
      broadcastAlerts();
      setPage(1);
      reloadAll();
    } catch (err) {
      toast.error(err?.message || 'Failed to clear alerts');
    } finally {
      setClearing(false);
    }
  }

  const activeRows = active.data?.alerts || [];
  const unreadCount = Number(unread.data?.unread || 0);
  const total = list.data?.total || 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const criticalActive = activeRows.filter((a) => a.severity === 'critical').length;

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Alerts</h1>
          <div className="main__subtitle">
            Notifications from the background alert sweep: internal-user &amp;
            API-key max spend, elevated error rate, provider cooldowns, and model
            health degredations. Source:{' '}
            <code>alerts</code> + <code>alert_settings</code> tables (30-day
            retention). Auto-refreshes every 15s.
          </div>
        </div>
        <div className="row gap-sm">
          <button
            className={`autorefresh-chip ${autoRefresh ? '' : 'autorefresh-chip--off'}`}
            onClick={toggleAutoRefresh}
            title={autoRefresh ? 'Auto-refresh every 15s — click to pause' : 'Auto-refresh paused — click to resume'}
          >
            <span className="autorefresh-chip__dot" />
            {autoRefresh ? 'Live' : 'Paused'}
          </button>
          <button onClick={handleMarkAllRead} disabled={unreadCount === 0}>
            {unreadCount > 0 ? `Mark all read (${unreadCount})` : 'Mark all read'}
          </button>
          <button onClick={handleRefresh}>Refresh</button>
        </div>
      </div>

      {/* KPI strip */}
      <div className="stats-grid">
        <div className="stat-card">
          <div className="stat-card__label">Unread</div>
          <div className="stat-card__value">{unreadCount.toLocaleString()}</div>
          <div className="stat-card__hint">not yet acknowledged</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Active</div>
          <div className="stat-card__value">{activeRows.length.toLocaleString()}</div>
          <div className="stat-card__hint">currently firing</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Critical active</div>
          <div className="stat-card__value">{criticalActive.toLocaleString()}</div>
          <div className="stat-card__hint">budget / unavailable</div>
        </div>
      </div>

      {active.error && <ErrorBanner error={active.error} onRetry={reloadAll} />}

      {/* Active feed */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Active alerts</h3>
          <span className="mono" style={{ fontSize: 12, color: 'var(--text-dim)' }}>
            {activeRows.length} currently firing
          </span>
        </div>
        <ActiveFeedBody
          loading={active.loading}
          error={active.error}
          rows={activeRows}
          onDismiss={handleDismiss}
          onRowClick={setSelectedAlert}
        />
      </div>

      {/* History filter bar */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row gap-sm" style={{ flexWrap: 'wrap', alignItems: 'center' }}>
          <label className="filter-label">
            Type
            <select value={typeFilter} onChange={(e) => setTypeFilter(e.target.value)}>
              {CATEGORY_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </label>
          <label className="filter-label">
            Severity
            <select value={severityFilter} onChange={(e) => setSeverityFilter(e.target.value)}>
              {SEVERITY_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </label>
          <label className="filter-label">
            Status
            <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
              {STATUS_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </label>
          <button
            onClick={handleClear}
            disabled={clearing || total === 0}
            style={{ marginLeft: 'auto' }}
          >
            {clearing ? 'Clearing…' : 'Clear history'}
          </button>
        </div>
      </div>

      {/* History table */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <h3 className="card__title" style={{ margin: 0 }}>History</h3>
          <span className="mono" style={{ fontSize: 12, color: 'var(--text-dim)' }}>
            {total.toLocaleString()} alert(s) matching filter
          </span>
        </div>
        <HistoryTableBody
          loading={list.loading}
          error={list.error}
          rows={list.data?.alerts || []}
          onRowClick={setSelectedAlert}
          onDismiss={handleDismiss}
        />
        <Pager
          page={page}
          totalPages={totalPages}
          total={total}
          pageSize={PAGE_SIZE}
          onPageChange={setPage}
        />
      </div>

      {selectedAlert && (
        <AlertDetailModal
          alert={selectedAlert}
          onClose={() => setSelectedAlert(null)}
          onDismiss={(a) => { handleDismiss(a.id); setSelectedAlert(null); }}
        />
      )}
    </>
  );
}

// ActiveFeedBody renders the live "currently firing" feed as a compact list.
function ActiveFeedBody({ loading, error, rows, onDismiss, onRowClick }) {
  if (loading) return <Spinner label="Loading active alerts…" />;
  if (error) return <ErrorBanner error={error} />;
  if (rows.length === 0) {
    return <EmptyState title="No active alerts" hint="No conditions are currently firing." />;
  }
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Type</th>
            <th>Severity</th>
            <th>Title / message</th>
            <th>Occurrences</th>
            <th style={{ textAlign: 'right' }}>Actions</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((a) => (
            <tr key={a.id} className="row-link" onClick={() => onRowClick(a.id)}>
              <td className="mono" style={{ whiteSpace: 'nowrap' }}>{formatTime(a.created_at)}</td>
              <td className="mono">{a.alert_type || '—'}</td>
              <td><SeverityBadge severity={a.severity} /></td>
              <td style={{ maxWidth: 420 }}>
                <div style={{ fontWeight: 600 }}>{a.title || '—'}</div>
                <div className="dim" style={{ fontSize: 12, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {a.message || ''}
                </div>
              </td>
              <td className="mono" style={{ textAlign: 'center' }}>{a.occurrences || 1}</td>
              <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                <button
                  type="button"
                  onClick={(e) => { e.stopPropagation(); onDismiss(a.id); }}
                  title="Dismiss this alert"
                >
                  Dismiss
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// HistoryTableBody renders the full paged alert history. The row doubles as a
// click target for the detail modal; a trailing Dismiss action stops propagation.
function HistoryTableBody({ loading, error, rows, onRowClick, onDismiss }) {
  if (loading) {
    return (
      <TableShell>
        {Array.from({ length: 4 }).map((_, i) => (
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
    <TableShell>
      {rows.length === 0 && (
        <tr>
          <td colSpan={7} style={{ textAlign: 'center', padding: '24px 8px' }}>
            No alerts match this filter.
          </td>
        </tr>
      )}
      {rows.map((a) => (
        <tr key={a.id} className="row-link" onClick={() => onRowClick(a.id)}>
          <td className="mono" style={{ whiteSpace: 'nowrap' }}>{formatTime(a.created_at)}</td>
          <td className="mono">{a.alert_type || '—'}</td>
          <td><SeverityBadge severity={a.severity} /></td>
          <td style={{ maxWidth: 360, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={a.title}>
            {a.title || '—'}
          </td>
          <td className="mono">{a.entity_name || a.entity_id || '—'}</td>
          <td>
            {a.dismissed ? <StatusBadge label="Dismissed" cls="badge--muted" />
              : a.read ? <StatusBadge label="Read" cls="badge--info" />
                : <StatusBadge label="Unread" cls="badge--active" />}
          </td>
          <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
            <button
              type="button"
              onClick={(e) => { e.stopPropagation(); onDismiss(a.id); }}
              disabled={a.dismissed}
              title={a.dismissed ? 'Already dismissed' : 'Dismiss this alert'}
            >
              Dismiss
            </button>
          </td>
        </tr>
      ))}
    </TableShell>
  );
}

// AlertDetailModal renders a single alert row with its full message and context
// JSON. The alert is passed in from the active feed or history table.
function AlertDetailModal({ alert, onClose, onDismiss }) {
  const a = alert;
  return (
    <Modal title={`Alert #${a.id}`} onClose={onClose} size="lg">
      <div className="grid grid--2" style={{ gap: '6px 24px' }}>
        <DetailRow label="Created" value={formatTime(a.created_at)} />
        <DetailRow label="Type" value={a.alert_type || '—'} mono />
        <DetailRow label="Severity" value={a.severity || '—'} />
        <DetailRow label="Status" value={a.dismissed ? 'Dismissed' : a.read ? 'Read' : 'Unread'} />
        <DetailRow label="Entity" value={a.entity_name || a.entity_id || '—'} />
        <DetailRow label="Model" value={a.model || '—'} mono />
        <DetailRow label="Provider" value={a.provider || '—'} />
        <DetailRow label="Occurrences" value={String(a.occurrences || 1)} mono />
        <DetailRow label="Value" value={a.value != null ? a.value.toFixed(4) : '—'} mono />
        <DetailRow label="Limit" value={a.limit_value != null ? a.limit_value.toFixed(2) : '—'} mono />
        <div className="detail-row__block">
          <div className="detail-row__block-label">Message</div>
          <div className="mono" style={{ fontSize: 13, whiteSpace: 'pre-wrap', wordBreak: 'break-all', marginTop: 4 }}>
            {a.message || '—'}
          </div>
        </div>
        {a.data && Object.keys(a.data).length > 0 && (
          <div className="detail-row__block">
            <div className="detail-row__block-label">Context</div>
            <pre className="mono" style={{ fontSize: 12, margin: '4px 0 0', maxHeight: 240, overflow: 'auto', background: 'var(--bg-elevated)', padding: 8, borderRadius: 6 }}>
              {JSON.stringify(a.data, null, 2)}
            </pre>
          </div>
        )}
      </div>
      <div className="modal__footer" style={{ display: 'flex', justifyContent: 'flex-end' }}>
        <button onClick={() => onDismiss(a)} disabled={a.dismissed}>
          {a.dismissed ? 'Dismissed' : 'Dismiss'}
        </button>
      </div>
    </Modal>
  );
}

function DetailRow({ label, value, mono }) {
  return (
    <div className="detail-row">
      <span className="detail-row__label">{label}</span>
      <span className={mono ? 'mono' : ''} style={{ wordBreak: 'break-all' }}>{value || '—'}</span>
    </div>
  );
}

function SeverityBadge({ severity }) {
  const cls = severity === 'critical' ? 'badge--revoked' : 'badge--info';
  return <span className={`badge ${cls}`}>{severity || 'warning'}</span>;
}

function StatusBadge({ label, cls = 'badge--muted' }) {
  return <span className={`badge ${cls}`}>{label}</span>;
}

function TableShell({ children }) {
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Type</th>
            <th>Severity</th>
            <th>Title</th>
            <th>Entity</th>
            <th>Status</th>
            <th style={{ textAlign: 'right' }}>Actions</th>
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

function ToggleRow({ label, checked, onChange }) {
  return (
    <label className="row gap-sm" style={{ cursor: 'pointer' }}>
      <input type="checkbox" checked={!!checked} onChange={(e) => onChange(e.target.checked)} style={{ width: 'auto' }} />
      <span className="form__label" style={{ margin: 0 }}>{label}</span>
    </label>
  );
}
