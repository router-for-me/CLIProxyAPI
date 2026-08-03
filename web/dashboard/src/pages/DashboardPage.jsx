import React, { useCallback, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  getUsageTotals,
  getUsageTimeSeries,
  getUsageTop,
  getActiveAlerts,
  getUnreadAlertCount,
  getModelHealth,
  getModelsCatalogSummary,
  getCooldownProviders,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import {
  Spinner, ErrorBanner, EmptyState,
} from '../components/Primitives.jsx';
import { BarChart, Sparkline } from '../components/Charts.jsx';
import { useToast } from '../components/Toast.jsx';
import { PRESETS, presetToRange } from './usageShared.jsx';

const AUTO_REFRESH_INTERVAL_MS = 60 * 1000;
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.homeAutorefresh';

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

function formatAgo(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const secs = Math.max(0, Math.floor((Date.now() - d.getTime()) / 1000));
  if (secs < 60) return `${secs}s ago`;
  const mins = Math.floor(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

// DashboardPage is the authenticated landing page (/). It aggregates the
// highest-signal operational metrics into a single at-a-glance overview:
// usage KPIs, a request-volume chart, top spenders, model health, catalog
// liveness, active alerts, and cooldown state.
//
// Every block is an independent useAsync call so a partial failure (e.g. a
// PG-backed route returning 503 when Postgres is not configured) renders its
// own inline banner without taking down the whole page.
export default function DashboardPage() {
  const toast = useToast();
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());

  // Usage window: fixed to "Last 24h" for the home overview. Operators can
  // drill into arbitrary windows on Usage Stats.
  const range = useMemo(
    () => presetToRange(PRESETS[3]), // Last 24h
    [],
  );
  const baseFilter = useMemo(
    () => ({ from: range.from, to: range.to }),
    [range],
  );

  const totals = useAsync(() => getUsageTotals(baseFilter), [JSON.stringify(baseFilter)]);
  const ts = useAsync(() => getUsageTimeSeries(baseFilter, 'hour'), [JSON.stringify(baseFilter), 'hour']);
  const topModels = useAsync(() => getUsageTop({ dimension: 'model', metric: 'cost_usd', limit: 5, ...baseFilter }), [JSON.stringify(baseFilter)]);

  const activeAlerts = useAsync(() => getActiveAlerts(), []);
  const unreadAlerts = useAsync(() => getUnreadAlertCount(), []);
  const modelHealth = useAsync(() => getModelHealth(), []);
  const catalog = useAsync(() => getModelsCatalogSummary(), []);
  const cooldowns = useAsync(() => getCooldownProviders(), []);

  const reloadAll = useCallback(() => {
    totals.reload();
    ts.reload();
    topModels.reload();
    activeAlerts.reload();
    unreadAlerts.reload();
    modelHealth.reload();
    catalog.reload();
    cooldowns.reload();
  }, [totals, ts, topModels, activeAlerts, unreadAlerts, modelHealth, catalog, cooldowns]);

  useAutoRefresh(reloadAll, AUTO_REFRESH_INTERVAL_MS, autoRefresh);

  function handleRefresh() {
    reloadAll();
    toast.info('Dashboard refreshed');
  }

  function toggleAutoRefresh() {
    setAutoRefresh((v) => {
      const next = !v;
      try { localStorage.setItem(AUTOREFRESH_STORAGE, next ? '1' : '0'); } catch { /* ignore */ }
      return next;
    });
  }

  const tsPoints = ts.data?.points || [];
  const tsData = useMemo(
    () => tsPoints.map((p) => ({
      label: p.bucket,
      value: p.request_count,
      cost: p.cost_usd,
    })),
    [tsPoints],
  );

  const reqCount = totals.data?.totals?.request_count || 0;
  const failedCount = totals.data?.totals?.failed_count || 0;
  const failureRate = totals.data?.failure_rate ?? 0;
  const totalTokens = totals.data?.totals?.total_tokens || 0;
  const totalCost = totals.data?.totals?.cost_usd || 0;
  const unread = unreadAlerts.data?.unread || 0;

  const healthSnapshots = modelHealth.data?.snapshots || [];
  const healthSettings = modelHealth.data?.settings || {};
  const operationalCount = healthSnapshots.filter((s) => s.status === 'operational').length;
  const degradedCount = healthSnapshots.filter((s) => s.status === 'degraded').length;
  const unavailableCount = healthSnapshots.filter((s) => s.status === 'unavailable').length;

  const cooldownRecords = cooldowns.data?.records || [];

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Dashboard</h1>
          <div className="main__subtitle">
            At-a-glance operational overview of the proxy — usage, top spenders,
            model health, catalog liveness, and live alerts across the past 24h.
          </div>
        </div>
        <div className="row gap-sm">
          <button
            className={`autorefresh-chip ${autoRefresh ? '' : 'autorefresh-chip--off'}`}
            onClick={toggleAutoRefresh}
            title={autoRefresh ? 'Auto-refresh every 60s — click to pause' : 'Auto-refresh paused — click to resume'}
          >
            <span className="autorefresh-chip__dot" />
            {autoRefresh ? 'Live' : 'Paused'}
          </button>
          <button onClick={handleRefresh}>Refresh</button>
        </div>
      </div>

      {/* ── KPI cards ─────────────────────────────────────────────── */}
      <div className="stats-grid">
        <div className="stat-card">
          <div className="stat-card__label">Total Requests (24h)</div>
          <div className="stat-card__value">{reqCount.toLocaleString()}</div>
          <div className="stat-card__hint">
            {failedCount.toLocaleString()} failed ({failureRate.toFixed(1)}%)
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Total Tokens (24h)</div>
          <div className="stat-card__value">{totalTokens.toLocaleString()}</div>
          <div className="stat-card__hint">
            ≈ ${totalTokens ? (totalCost / (totalTokens || 1) * 1000).toFixed(4) : '0.0000'}/1k tokens
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Total Cost (24h)</div>
          <div className="stat-card__value">${totalCost.toFixed(4)}</div>
          <div className="stat-card__hint">
            avg ${reqCount > 0 ? (totalCost / reqCount).toFixed(4) : '0.0000'}/req
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Unread Alerts</div>
          <div className="stat-card__value">{unread.toLocaleString()}</div>
          <div className="stat-card__hint">
            <Link to="/alerts">open alerts →</Link>
          </div>
        </div>
      </div>

      {/* ── Chart row ────────────────────────────────────────────── */}
      <div className="grid grid--2" style={{ marginTop: 16 }}>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Request volume (24h)</h3>
            <span className="dim" style={{ fontSize: 12 }}>hourly</span>
          </div>
          {ts.loading && <Spinner label="Loading…" />}
          {ts.error && <ErrorBanner error={ts.error} onRetry={ts.reload} />}
          {!ts.loading && !ts.error && tsData.length > 0 && <BarChart data={tsData} />}
          {!ts.loading && !ts.error && tsData.length === 0 && (
            <EmptyState title="No data for this window" />
          )}
        </div>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Cost trend (24h)</h3>
            <span className="dim" style={{ fontSize: 12 }}>hourly</span>
          </div>
          {ts.loading && <Spinner label="Loading…" />}
          {ts.error && <ErrorBanner error={ts.error} onRetry={ts.reload} />}
          {!ts.loading && !ts.error && tsData.length > 0 && (
            <Sparkline values={tsData.map((d) => d.cost)} />
          )}
          {!ts.loading && !ts.error && tsData.length === 0 && (
            <EmptyState title="No cost data for this window" />
          )}
        </div>
      </div>

      {/* ── Second row: top models + model health + alerts ────────── */}
      <div className="grid grid--3" style={{ marginTop: 16 }}>
        <div className="card" style={{ padding: 0 }}>
          <div className="row row--between" style={{ padding: '14px 16px 8px' }}>
            <h3 className="card__title" style={{ margin: 0 }}>Top models by cost</h3>
            <Link to="/usage" style={{ fontSize: 12 }}>see all →</Link>
          </div>
          {topModels.loading && <Spinner label="Loading…" />}
          {topModels.error && <ErrorBanner error={topModels.error} onRetry={topModels.reload} />}
          {!topModels.loading && !topModels.error && (!topModels.data?.entries || topModels.data.entries.length === 0) && (
            <EmptyState title="No data" />
          )}
          {!topModels.loading && !topModels.error && topModels.data?.entries?.length > 0 && (
            <TopModelsList entries={topModels.data.entries} />
          )}
        </div>

        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Model health</h3>
            <Link to="/model-health" style={{ fontSize: 12 }}>details →</Link>
          </div>
          {modelHealth.loading && <Spinner label="Loading…" />}
          {modelHealth.error && <ErrorBanner error={modelHealth.error} onRetry={modelHealth.reload} />}
          {!modelHealth.loading && !modelHealth.error && (
            <>
              {healthSnapshots.length === 0 ? (
                <EmptyState title="No health checks yet" hint="Latest status appears once the sweep records its first probe." />
              ) : (
                <>
                  <div className="stats-grid" style={{ gridTemplateColumns: 'repeat(3, 1fr)', marginBottom: 12 }}>
                    <MiniStat label="Operational" value={operationalCount} tone="ok" />
                    <MiniStat label="Degraded" value={degradedCount} tone="warn" />
                    <MiniStat label="Unavailable" value={unavailableCount} tone="bad" />
                  </div>
                  <div className="dim" style={{ fontSize: 12 }}>
                    {healthSnapshots.length} model(s) · interval{' '}
                    {Math.round((healthSettings.interval_seconds || 900) / 60)}m · last run{' '}
                    {formatAgo(modelHealth.data?.last_run_at)}
                  </div>
                </>
              )}
            </>
          )}
        </div>

        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Active alerts</h3>
            <Link to="/alerts" style={{ fontSize: 12 }}>all alerts →</Link>
          </div>
          {activeAlerts.loading && <Spinner label="Loading…" />}
          {activeAlerts.error && <ErrorBanner error={activeAlerts.error} onRetry={activeAlerts.reload} />}
          {!activeAlerts.loading && !activeAlerts.error && (
            <AlertMiniList alerts={activeAlerts.data?.alerts || []} />
          )}
        </div>
      </div>

      {/* ── Third row: catalog + cooldowns ───────────────────────── */}
      <div className="grid grid--2" style={{ marginTop: 16 }}>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Models catalog</h3>
            <Link to="/models" style={{ fontSize: 12 }}>open catalog →</Link>
          </div>
          {catalog.loading && <Spinner label="Loading…" />}
          {catalog.error && <ErrorBanner error={catalog.error} onRetry={catalog.reload} />}
          {!catalog.loading && !catalog.error && (
            <div className="stats-grid" style={{ gridTemplateColumns: 'repeat(4, 1fr)' }}>
              <MiniStat label="Total" value={catalog.data?.total ?? 0} tone="neutral" />
              <MiniStat label="Live" value={catalog.data?.live ?? 0} tone="ok" />
              <MiniStat label="Stale" value={catalog.data?.stale ?? 0} tone="warn" />
              <MiniStat label="Priced" value={catalog.data?.priced ?? 0} tone="neutral" />
            </div>
          )}
        </div>

        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Cooldown providers</h3>
            <Link to="/cooldown-providers" style={{ fontSize: 12 }}>details →</Link>
          </div>
          {cooldowns.loading && <Spinner label="Loading…" />}
          {cooldowns.error && <ErrorBanner error={cooldowns.error} onRetry={cooldowns.reload} />}
          {!cooldowns.loading && !cooldowns.error && (
            cooldownRecords.length === 0 ? (
              <EmptyState title="No providers in cooldown" hint="Round-robin routing is healthy." />
            ) : (
              <ul className="cooldown-list">
                {cooldownRecords.slice(0, 6).map((r, i) => (
                  <li key={`${r.provider}-${r.model}-${i}`}>
                    <span className="mono">{r.provider}</span>
                    {r.model && <span className="dim"> · {r.model}</span>}
                  </li>
                ))}
                {cooldownRecords.length > 6 && (
                  <li className="dim">… and {cooldownRecords.length - 6} more</li>
                )}
              </ul>
            )
          )}
        </div>
      </div>
    </>
  );
}

// TopModelsList renders a compact leaderboard bar list (top N by cost).
function TopModelsList({ entries }) {
  const max = Math.max(...entries.map((e) => Number(e.cost_usd) || 0), 1);
  return (
    <table className="table">
      <thead>
        <tr>
          <th>Model</th>
          <th style={{ textAlign: 'right' }}>Cost</th>
        </tr>
      </thead>
      <tbody>
        {entries.map((e, i) => (
          <tr key={`${e.key}-${i}`}>
            <td className="mono" style={{ maxWidth: 180, overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {e.key || '—'}
            </td>
            <td style={{ width: '45%' }}>
              <div className="lb-bar">
                <div className="lb-bar__fill" style={{ width: `${((Number(e.cost_usd) || 0) / max * 100)}%` }} />
              </div>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// MiniStat renders a small labeled count used inside dashboard stat grids.
function MiniStat({ label, value, tone = 'neutral' }) {
  const toneStyle = tone === 'ok'
    ? { color: 'var(--success)' }
    : tone === 'warn' ? { color: 'var(--warning)' }
    : tone === 'bad' ? { color: 'var(--danger, #ef4444)' }
    : undefined;
  return (
    <div>
      <div className="stat-card__value" style={{ fontSize: 22, ...toneStyle }}>{Number(value || 0).toLocaleString()}</div>
      <div className="stat-card__label" style={{ fontSize: 11 }}>{label}</div>
    </div>
  );
}

// AlertMiniList renders the most recent active alerts inline.
function AlertMiniList({ alerts }) {
  if (alerts.length === 0) {
    return <EmptyState title="No active alerts" hint="Nothing is currently firing." />;
  }
  return (
    <ul className="cooldown-list">
      {alerts.slice(0, 6).map((a) => (
        <li key={a.id}>
          <SeverityBadge severity={a.severity} />
          <span className="dim" style={{ marginLeft: 6, fontSize: 12 }}>{formatTime(a.created_at || a.detected_at)}</span>
          <div className="mono" style={{ fontSize: 12, marginTop: 2 }}>{a.message || a.alert_type || 'alert'}</div>
        </li>
      ))}
    </ul>
  );
}

// SeverityBadge mirrors AlertsPage's badge: critical reads as revoked/danger,
// everything else reads as a neutral info badge.
function SeverityBadge({ severity }) {
  const cls = severity === 'critical' ? 'badge--revoked' : 'badge--info';
  return <span className={`badge ${cls}`}>{severity || 'warning'}</span>;
}
