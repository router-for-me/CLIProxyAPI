import React, { useState, useCallback, useEffect, useMemo } from 'react';
import { getSessionAffinity, revokeSessionAffinity } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { Spinner, ErrorBanner, EmptyState } from '../components/Primitives.jsx';
import { useToast } from '../components/Toast.jsx';

// SessionAffinityPage surfaces the live in-memory session→auth bindings held
// by the sticky-routing cache (the SessionAffinitySelector). Unlike Usage
// Stats / Errors (which read persisted PG tables), this page reads a runtime
// snapshot from /v0/management/session-affinity and auto-polls ~5s so an
// operator can watch pins form and drain. The per-row "Revoke" button routes
// to POST /v0/management/session-affinity/revoke to drop every binding for
// that session, forcing the next request in it to re-select a healthy account
// via round-robin — the escape hatch for sessions pinned to a stuck/erroring
// auth the lazy failover has not yet migrated.
//
// When routing.session-affinity is disabled in config.yaml, the backend
// reports affinity_enabled: false and the page renders an explanatory banner
// instead of an empty binding list (an empty list would be ambiguous: it
// could mean "no sessions active" or "feature off").

const AUTO_REFRESH_INTERVAL_MS = 5 * 1000;
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.sessionAffinityAutorefresh';
const FILTER_UNAVAILABLE_STORAGE = 'nixllm.dashboard.sessionAffinityOnlyUnavail';

function readAutoRefresh() {
  try { return localStorage.getItem(AUTOREFRESH_STORAGE) !== '0'; }
  catch { return true; }
}
function readOnlyUnavail() {
  try { return localStorage.getItem(FILTER_UNAVAILABLE_STORAGE) === '1'; }
  catch { return false; }
}

export default function SessionAffinityPage() {
  const toast = useToast();
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());
  const [onlyUnavail, setOnlyUnavail] = useState(() => readOnlyUnavail());
  const [revokingSession, setRevokingSession] = useState(null);
  const snapshot = useAsync(() => getSessionAffinity(), []);

  const reload = useCallback(() => { snapshot.reload(); }, [snapshot]);
  useAutoRefresh(reload, AUTO_REFRESH_INTERVAL_MS, autoRefresh);

  function handleRefresh() {
    reload();
    toast.info('Affinity snapshot refreshed');
  }

  function toggleAutoRefresh() {
    setAutoRefresh((v) => {
      const next = !v;
      try { localStorage.setItem(AUTOREFRESH_STORAGE, next ? '1' : '0'); } catch { /* ignore */ }
      return next;
    });
  }

  function toggleOnlyUnavail() {
    setOnlyUnavail((v) => {
      const next = !v;
      try { localStorage.setItem(FILTER_UNAVAILABLE_STORAGE, next ? '1' : '0'); } catch { /* ignore */ }
      return next;
    });
  }

  async function handleRevoke(sessionID) {
    if (!sessionID) {
      toast.error('Missing session_id — cannot revoke this row');
      return;
    }
    const ok = window.confirm(
      `Revoke the session affinity binding for\n\n  ${sessionID}\n\n` +
      `The next request in this session will re-select a credential via round-robin ` +
      `(or whatever routing strategy is configured). This does not disable the bound auth.`
    );
    if (!ok) return;
    setRevokingSession(sessionID);
    try {
      const res = await revokeSessionAffinity(sessionID);
      const remaining = typeof res?.remaining === 'number' ? res.remaining : null;
      toast.success(
        `Revoked session ${shortSession(sessionID)}` +
        (remaining != null ? ` — ${remaining} binding${remaining === 1 ? '' : 's'} remaining` : '')
      );
      reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to revoke session');
    } finally {
      setRevokingSession(null);
    }
  }

  const affinityEnabled = snapshot.data?.affinity_enabled !== false;
  const records = snapshot.data?.records || [];

  const filteredRecords = useMemo(
    () => (onlyUnavail ? records.filter((r) => !r.available) : records),
    [records, onlyUnavail]
  );

  // Distinct-count KPIs reflect the unfiltered snapshot so the operator sees
  // the true totals even when the only-unavailable filter is on.
  const totalBindings = records.reduce((n, r) => n + (r.bindings || 1), 0);
  const distinctSessions = records.length;
  const unavailableSessions = records.filter((r) => !r.available).length;

  // Subtitle counter; updated each second so the "expires in Nm" countdowns
  // tick without waiting for the 5s backend poll.
  const [nowTick, setNowTick] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNowTick(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Session Affinity</h1>
          <div className="main__subtitle">
            Live snapshot of the sticky-routing cache that pins each session to
            an upstream credential (so a conversation keeps hitting the same
            account). Source: in-memory auth manager — per-process and not
            persisted; a restart drops every binding. Use a row&rsquo;s
            <strong> Revoke</strong> button to unpin a session whose requests
            keep erroring against a stuck credential, forcing the next request
            to re-select a healthy account.
          </div>
        </div>
        <div className="row gap-sm">
          <button
            className={`autorefresh-chip ${autoRefresh ? '' : 'autorefresh-chip--off'}`}
            onClick={toggleAutoRefresh}
            title={autoRefresh ? 'Auto-refresh every 5s — click to pause' : 'Auto-refresh paused — click to resume'}
          >
            <span className="autorefresh-chip__dot" />
            {autoRefresh ? 'Live' : 'Paused'}
          </button>
          <button onClick={handleRefresh}>Refresh</button>
        </div>
      </div>

      {!affinityEnabled && (
        <div className="card" style={{ marginTop: 16 }}>
          <EmptyState
            title="Session affinity is disabled"
            hint="Sticky session→auth routing is off, so no bindings exist to list or revoke. Enable it in config.yaml under routing.session-affinity: true (see config.example.yaml) and reload the server."
          />
        </div>
      )}

      {affinityEnabled && (
        <>
          {/* KPI strip */}
          <div className="stats-grid">
            <div className="stat-card">
              <div className="stat-card__label">Active Bindings</div>
              <div className="stat-card__value">{totalBindings.toLocaleString()}</div>
              <div className="stat-card__hint">provider::session::model entries cached</div>
            </div>
            <div className="stat-card">
              <div className="stat-card__label">Distinct Sessions</div>
              <div className="stat-card__value">{distinctSessions.toLocaleString()}</div>
              <div className="stat-card__hint">conversations pinned to a credential</div>
            </div>
            <div className="stat-card">
              <div className="stat-card__label">Pinned to Unavailable</div>
              <div className="stat-card__value">{unavailableSessions.toLocaleString()}</div>
              <div className="stat-card__hint">bound auth disabled / in cooldown — revoke to recover</div>
            </div>
          </div>

          {snapshot.error && <ErrorBanner error={snapshot.error} onRetry={reload} />}

          {/* Affinity table */}
          <div className="card" style={{ marginTop: 16 }}>
            <div className="row row--between" style={{ marginBottom: 12, flexWrap: 'wrap', gap: 12 }}>
              <h3 className="card__title" style={{ margin: 0 }}>Session Bindings</h3>
              <label
                className="row gap-sm"
                style={{ fontSize: 13, color: 'var(--text-dim)', cursor: 'pointer', userSelect: 'none' }}
                title="Show only sessions whose pinned auth is currently disabled or in cooldown"
              >
                <input
                  type="checkbox"
                  checked={onlyUnavail}
                  onChange={toggleOnlyUnavail}
                  style={{ cursor: 'pointer' }}
                />
                Only show sessions bound to an unavailable auth
              </label>
            </div>
            <AffinityTableBody
              loading={snapshot.loading}
              error={snapshot.error}
              records={filteredRecords}
              nowTick={nowTick}
              onRevoke={handleRevoke}
              revokingSession={revokingSession}
            />
          </div>
        </>
      )}

      {/* Spinner used during the initial load before the first snapshot lands.
          Render a slim full-width placeholder so the layout doesn't jump. */}
      {snapshot.loading && !snapshot.data && (
        <div className="card" style={{ marginTop: 16, padding: 24, textAlign: 'center' }}>
          <Spinner /> <span style={{ marginLeft: 8, color: 'var(--text-dim)' }}>Loading affinity snapshot…</span>
        </div>
      )}
    </>
  );
}

// AffinityTableBody renders one row per session (grouped across models).
// Headers are always shown (even when empty) so the shape stays stable across
// polls. A session bound to an unavailable auth renders a danger badge on the
// pinned-auth cell and surfaces the reason in the Status column — that's the
// triage signal that motivates a revoke.
function AffinityTableBody({ loading, error, records, nowTick, onRevoke, revokingSession }) {
  if (loading && records.length === 0) {
    return (
      <TableShell>
        {Array.from({ length: 4 }).map((_, i) => (
          <tr key={i} className="skeleton-row">
            <td><span className="skeleton-line" /></td>
            <td><span className="skeleton-line" /></td>
            <td><span className="skeleton-line" /></td>
            <td><span className="skeleton-line" /></td>
            <td><span className="skeleton-line" /></td>
            <td><span className="skeleton-line" /></td>
            <td><span className="skeleton-line" /></td>
          </tr>
        ))}
      </TableShell>
    );
  }
  if (error) return <ErrorBanner error={error} />;
  return (
    <TableShell>
      {records.length === 0 && (
        <tr>
          <td colSpan={7} style={{ textAlign: 'center', padding: '24px 8px' }}>
            No active session bindings — the affinity cache is empty (sessions
            expire on TTL and re-form on the next request).
          </td>
        </tr>
      )}
      {records.map((r, i) => {
        const key = `${r.session_id}|${r.auth_id || ''}|${i}`;
        const expires = r.expires_at ? new Date(r.expires_at) : null;
        const secsLeft = expires ? Math.max(0, Math.round((expires.getTime() - nowTick) / 1000)) : null;
        const isRevoking = revokingSession === r.session_id;
        const modelsLabel = r.models && r.models.length
          ? (r.models.length === 1 ? r.models[0] : `${r.models.length} models`)
          : (r.bindings > 1 ? `${r.bindings} bindings` : <span style={{ color: 'var(--text-dim)' }}>—</span>);
        return (
          <tr key={key}>
            <td className="mono" style={{ maxWidth: 280, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={r.session_id}>
              {r.session_id || '—'}
            </td>
            <td className="mono">{r.provider || <span style={{ color: 'var(--text-dim)' }}>multi</span>}</td>
            <td className="mono" style={{ whiteSpace: 'nowrap' }}>
              {r.available ? (
                (r.auth_index || r.auth_id || '—')
              ) : (
                <span className="badge badge--danger" title={r.status_note || 'pinned auth is unavailable'}>
                  {r.auth_index || r.auth_id || 'stale'}
                </span>
              )}
            </td>
            <td style={{ maxWidth: 240, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={(r.models || []).join(', ')}>
              {modelsLabel}
            </td>
            <td className="mono" style={{ whiteSpace: 'nowrap' }} title={expires ? expires.toISOString() : ''}>
              {expires ? `${formatMins(secsLeft)} (${formatTime(expires)})` : '—'}
            </td>
            <td>
              {r.available
                ? <span className="badge badge--muted">{r.status || 'available'}</span>
                : <span className="badge badge--danger" title={r.status_note}>{r.status || 'unavailable'}</span>}
              {r.status_note && !r.available && (
                <span style={{ display: 'block', fontSize: 11, color: 'var(--text-dim)', marginTop: 2 }}>
                  {r.status_note}
                </span>
              )}
            </td>
            <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
              <button
                type="button"
                onClick={() => onRevoke(r.session_id)}
                disabled={!r.session_id || isRevoking}
                title={r.session_id ? `Revoke the affinity binding for this session` : 'No session_id available for this row'}
              >
                {isRevoking ? 'Revoking…' : 'Revoke'}
              </button>
            </td>
          </tr>
        );
      })}
    </TableShell>
  );
}

function TableShell({ children }) {
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="table">
        <thead>
          <tr>
            <th>Session ID</th>
            <th>Provider</th>
            <th>Pinned Auth</th>
            <th>Models</th>
            <th>Expires</th>
            <th>Status</th>
            <th style={{ textAlign: 'right' }}>Actions</th>
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

// formatTime renders HH:MM:SS UTC for compact display in the Expires column so
// an operator can eyeball the wall-clock drop time without parsing a full ISO
// timestamp from the tooltip. Mirrors CooldownProvidersPage exactly.
function formatTime(d) {
  const p = (n) => String(n).padStart(2, '0');
  return `${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())}Z`;
}

// formatMins renders a countdown like "58m" or "42s" for the Expires column.
function formatMins(secs) {
  if (secs == null) return '—';
  if (secs < 60) return `${secs}s`;
  const m = Math.floor(secs / 60);
  const s = secs % 60;
  if (m < 60) return s ? `${m}m${s}s` : `${m}m`;
  const h = Math.floor(m / 60);
  const mm = m % 60;
  return mm ? `${h}h${mm}m` : `${h}h`;
}

// shortSession truncates a long session ID for toast messages to avoid pushing
// a full conversation identifier into the toast body.
function shortSession(id) {
  if (!id) return '';
  if (id.length <= 24) return id;
  return id.slice(0, 12) + '…' + id.slice(-4);
}
