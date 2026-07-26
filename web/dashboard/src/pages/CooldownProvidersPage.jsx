import React, { useState, useCallback, useEffect } from 'react';
import { getCooldownProviders, resetCooldownProvider } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { Spinner, ErrorBanner, EmptyState } from '../components/Primitives.jsx';
import { useToast } from '../components/Toast.jsx';

// CooldownProvidersPage surfaces the live in-memory cooldown state of every
// upstream auth / model pair the auth manager has temporarily blocked for
// retries. Unlike Usage Stats / Errors (which read persisted PG tables), this
// page reads a runtime snapshot from /v0/management/cooldown-providers and
// auto-polls ~5s so an operator can watch cooldowns drain. The per-row
// "Reset" button routes to the existing POST /v0/management/reset-quota
// endpoint (auth_index → Manager.ResetQuota) to clear the cooldown on demand.

const AUTO_REFRESH_INTERVAL_MS = 5 * 1000;
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.cooldownAutorefresh';

function readAutoRefresh() {
  try { return localStorage.getItem(AUTOREFRESH_STORAGE) !== '0'; }
  catch { return true; }
}

export default function CooldownProvidersPage() {
  const toast = useToast();
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());
  const [resettingIndex, setResettingIndex] = useState(null);
  const snapshot = useAsync(() => getCooldownProviders(), []);

  const reload = useCallback(() => { snapshot.reload(); }, [snapshot]);
  useAutoRefresh(reload, AUTO_REFRESH_INTERVAL_MS, autoRefresh);

  function handleRefresh() {
    reload();
    toast.info('Cooldown snapshot refreshed');
  }

  function toggleAutoRefresh() {
    setAutoRefresh((v) => {
      const next = !v;
      try { localStorage.setItem(AUTOREFRESH_STORAGE, next ? '1' : '0'); } catch { /* ignore */ }
      return next;
    });
  }

  async function handleReset(authIndex) {
    if (!authIndex) {
      toast.error('Missing auth_index — cannot reset this row');
      return;
    }
    setResettingIndex(authIndex);
    try {
      const res = await resetCooldownProvider(authIndex);
      const cleared = Array.isArray(res?.models) ? res.models.length : 0;
      toast.success(`Cooldown cleared for ${authIndex}${cleared ? ` (${cleared} model${cleared === 1 ? '' : 's'} restored)` : ''}`);
      reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to reset cooldown');
    } finally {
      setResettingIndex(null);
    }
  }

  const records = snapshot.data?.records || [];
  const coolingCount = records.length;

  // Counter used in the subtitle; updated from the snapshot so the header
  // reflects the last poll rather than a stale mount-time value.
  const [nowTick, setNowTick] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNowTick(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Cooldown Providers</h1>
          <div className="main__subtitle">
            Live snapshot of upstream auth/model pairs the scheduler has
            temporarily blocked for retries (quota exhaustion, transient 5xx,
            backoff). Source: in-memory auth manager — not a persisted table.
            Use a row&rsquo;s <strong>Reset</strong> button to force-clear
            cooldown routing state for that auth.
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

      {/* KPI strip */}
      <div className="stats-grid">
        <div className="stat-card">
          <div className="stat-card__label">In Cooldown</div>
          <div className="stat-card__value">{coolingCount.toLocaleString()}</div>
          <div className="stat-card__hint">auth/model pairs blocked now</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Auth-level Cooldowns</div>
          <div className="stat-card__value">{records.filter((r) => r.level === 'auth').length.toLocaleString()}</div>
          <div className="stat-card__hint">whole credential unavailable</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Model-level Cooldowns</div>
          <div className="stat-card__value">{records.filter((r) => r.level === 'model').length.toLocaleString()}</div>
          <div className="stat-card__hint">specific model under an auth</div>
        </div>
      </div>

      {snapshot.error && <ErrorBanner error={snapshot.error} onRetry={reload} />}

      {/* Cooldown table */}
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Cooldowns</h3>
          <span className="mono" style={{ fontSize: 12, color: 'var(--text-dim)' }}>
            polled {autoRefresh ? '5s ago' : 'on demand'}
          </span>
        </div>
        <CooldownTableBody
          loading={snapshot.loading}
          error={snapshot.error}
          records={records}
          nowTick={nowTick}
          onReset={handleReset}
          resettingIndex={resettingIndex}
        />
      </div>
    </>
  );
}

// CooldownTableBody renders the live cooldown snapshot. Headers are always
// shown (even when empty) per the page's design — a steady shape helps an
// operator scan the table across polls. When a row is missing its
// auth_index (auth racing removal), the Reset button is disabled.
function CooldownTableBody({ loading, error, records, nowTick, onReset, resettingIndex }) {
  if (loading) {
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
          <td colSpan={8} style={{ textAlign: 'center', padding: '24px 8px' }}>
            No providers in cooldown — all upstream auths / models are available.
          </td>
        </tr>
      )}
      {records.map((r, i) => {
        const key = `${r.auth_id}|${r.model || ''}|${r.provider || ''}|${i}`;
        const nextRetry = r.next_retry_after ? new Date(r.next_retry_after) : null;
        const secsLeft = nextRetry ? Math.max(0, Math.round((nextRetry.getTime() - nowTick) / 1000)) : null;
        const isResetting = resettingIndex === r.auth_index;
        return (
          <tr key={key}>
            <td className="mono">{r.provider || '—'}</td>
            <td className="mono" style={{ whiteSpace: 'nowrap' }}>
              {r.auth_index || r.auth_id || '—'}
            </td>
            <td className="mono">{r.model ? r.model : <span style={{ color: 'var(--text-dim)' }}>— (auth-level)</span>}</td>
            <td>
              <span className="badge badge--muted">{r.status || 'cooling'}</span>
            </td>
            <td style={{ maxWidth: 320, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {r.reason || <span style={{ color: 'var(--text-dim)' }}>—</span>}
            </td>
            <td className="mono" style={{ whiteSpace: 'nowrap' }} title={nextRetry ? nextRetry.toISOString() : ''}>
              {nextRetry ? `${secsLeft}s (${formatTime(nextRetry)})` : '—'}
            </td>
            <td className="mono" style={{ maxWidth: 280, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {r.last_error ? `${r.last_error.http_status || r.last_error.code || ''} ${r.last_error.message || ''}`.trim() : '—'}
            </td>
            <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
              <button
                type="button"
                onClick={() => onReset(r.auth_index)}
                disabled={!r.auth_index || isResetting}
                title={r.auth_index ? `Reset quota/cooldown for ${r.auth_index}` : 'No auth_index available for this row'}
              >
                {isResetting ? 'Resetting…' : 'Reset'}
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
            <th>Provider</th>
            <th>Auth Index</th>
            <th>Model</th>
            <th>Status</th>
            <th>Reason</th>
            <th>Next retry</th>
            <th>Last error</th>
            <th style={{ textAlign: 'right' }}>Actions</th>
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

// formatTime renders HH:MM:SS UTC for compact display in the Next retry
// column so an operator can eyeball the wall-clock recovery time without
// parsing a full ISO timestamp from the tooltip.
function formatTime(d) {
  const p = (n) => String(n).padStart(2, '0');
  return `${p(d.getUTCHours())}:${p(d.getUTCMinutes())}:${p(d.getUTCSeconds())}Z`;
}
