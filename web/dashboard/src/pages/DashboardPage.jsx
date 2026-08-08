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
  ErrorBanner, EmptyState, KpiCard, KpiSkeleton, CardSkeleton,
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
      <div className="kpi-grid">
        {totals.loading ? (
          <KpiSkeleton />
        ) : (
          <KpiCard
            label="Total Requests (24h)"
            value={reqCount.toLocaleString()}
            hint={`${failedCount.toLocaleString()} failed (${failureRate.toFixed(1)}%)`}
            tone={failureRate > 0 ? 'warn' : 'neutral'}
            icon={<RequestsIcon />}
          />
        )}
        {totals.loading ? (
          <KpiSkeleton />
        ) : (
          <KpiCard
            label="Total Tokens (24h)"
            value={totalTokens.toLocaleString()}
            hint={`≈ $${totalTokens ? (totalCost / (totalTokens || 1) * 1000).toFixed(4) : '0.0000'}/1k tokens`}
            icon={<TokensIcon />}
          />
        )}
        {totals.loading ? (
          <KpiSkeleton />
        ) : (
          <KpiCard
            label="Total Cost (24h)"
            value={`$${totalCost.toFixed(4)}`}
            hint={`avg $${reqCount > 0 ? (totalCost / reqCount).toFixed(4) : '0.0000'}/req`}
            tone="accent"
            icon={<CostIcon />}
          />
        )}
        {unreadAlerts.loading ? (
          <KpiSkeleton />
        ) : (
          <KpiCard
            label="Unread Alerts"
            value={unread.toLocaleString()}
            hint={<Link to="/alerts">open alerts →</Link>}
            tone={unread > 0 ? 'danger' : 'neutral'}
            icon={<AlertsIcon />}
          />
        )}
      </div>

      {/* ── Chart row ────────────────────────────────────────────── */}
      <div className="grid grid--2 dash-row">
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Request volume (24h)</h3>
            <span className="dim" style={{ fontSize: 12 }}>hourly</span>
          </div>
          {ts.loading && <ChartSkeleton />}
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
          {ts.loading && <ChartSkeleton />}
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
      <div className="grid grid--3 dash-row">
        <div className="card" style={{ padding: 0 }}>
          <div className="row row--between" style={{ padding: '14px 16px 8px' }}>
            <h3 className="card__title" style={{ margin: 0 }}>Top models by cost</h3>
            <Link to="/usage" style={{ fontSize: 12 }}>see all →</Link>
          </div>
          {topModels.loading && <CardSkeleton rows={5} />}
          {topModels.error && (
            <div style={{ padding: '0 16px 16px' }}>
              <ErrorBanner error={topModels.error} onRetry={topModels.reload} />
            </div>
          )}
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
          {modelHealth.loading && <CardSkeleton rows={3} />}
          {modelHealth.error && <ErrorBanner error={modelHealth.error} onRetry={modelHealth.reload} />}
          {!modelHealth.loading && !modelHealth.error && (
            <>
              {healthSnapshots.length === 0 ? (
                <EmptyState title="No health checks yet" hint="Latest status appears once the sweep records its first probe." />
              ) : (
                <>
                  <div className="kpi-grid" style={{ gridTemplateColumns: 'repeat(3, 1fr)', marginBottom: 12 }}>
                    <KpiMini label="Operational" value={operationalCount} tone="ok" />
                    <KpiMini label="Degraded" value={degradedCount} tone="warn" />
                    <KpiMini label="Unavailable" value={unavailableCount} tone="bad" />
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
          {activeAlerts.loading && <CardSkeleton rows={3} />}
          {activeAlerts.error && <ErrorBanner error={activeAlerts.error} onRetry={activeAlerts.reload} />}
          {!activeAlerts.loading && !activeAlerts.error && (
            <AlertMiniList alerts={activeAlerts.data?.alerts || []} />
          )}
        </div>
      </div>

      {/* ── Third row: catalog + cooldowns ───────────────────────── */}
      <div className="grid grid--2 dash-row">
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Models catalog</h3>
            <Link to="/models" style={{ fontSize: 12 }}>open catalog →</Link>
          </div>
          {catalog.loading && <CardSkeleton rows={2} />}
          {catalog.error && <ErrorBanner error={catalog.error} onRetry={catalog.reload} />}
          {!catalog.loading && !catalog.error && (
            <div className="kpi-grid" style={{ gridTemplateColumns: 'repeat(4, 1fr)' }}>
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
          {cooldowns.loading && <CardSkeleton rows={3} />}
          {cooldowns.error && <ErrorBanner error={cooldowns.error} onRetry={cooldowns.reload} />}
          {!cooldowns.loading && !cooldowns.error && (
            cooldownRecords.length === 0 ? (
              <EmptyState title="No providers in cooldown" hint="Round-robin routing is healthy." />
            ) : (
              <ul className="dash-list">
                {cooldownRecords.slice(0, 6).map((r, i) => (
                  <li key={`${r.provider}-${r.model}-${i}`} className="dash-list__item">
                    <span className="dash-list__code">{r.provider}</span>
                    {r.model && <span className="dash-list__msg">{r.model}</span>}
                  </li>
                ))}
                {cooldownRecords.length > 6 && (
                  <li className="dash-list__item">
                    <span className="dim" style={{ fontSize: 12 }}>… and {cooldownRecords.length - 6} more</span>
                  </li>
                )}
              </ul>
            )
          )}
        </div>
      </div>
    </>
  );
}

// ChartSkeleton fills the chart area with a shimmer bar instead of a centered
// spinner, preserving the card height while the time series loads.
function ChartSkeleton() {
  return (
    <div className="chart-skeleton">
      <div className="skeleton-line chart-skeleton__bar" />
    </div>
  );
}

// TopModelsList renders a rank-ordered leaderboard with a cost value beside
// each bar so the ranking reads without scanning the Cost column.
function TopModelsList({ entries }) {
  const max = Math.max(...entries.map((e) => Number(e.cost_usd) || 0), 1);
  return (
    <ul className="dash-list" style={{ padding: '4px 0' }}>
      {entries.map((e, i) => (
        <li key={`${e.key}-${i}`} className="lb-row">
          <span className={`lb-row__rank${i < 3 ? ' lb-row__rank--top' : ''}`}>{i + 1}</span>
          <span className="lb-row__model">{e.key || '—'}</span>
          <span className="lb-row__bar">
            <span className="lb-row__fill" style={{ width: `${((Number(e.cost_usd) || 0) / max * 100)}%` }} />
          </span>
          <span className="lb-row__cost">${(Number(e.cost_usd) || 0).toFixed(4)}</span>
        </li>
      ))}
    </ul>
  );
}

// KpiMini renders a compact labeled count used inside dashboard stat grids.
function KpiMini({ label, value, tone = 'neutral' }) {
  const toneStyle = tone === 'ok'
    ? { color: 'var(--success)' }
    : tone === 'warn' ? { color: 'var(--warning)' }
    : tone === 'bad' ? { color: 'var(--danger, #ef4444)' }
    : undefined;
  return (
    <div className="kpi-mini">
      <div className="kpi-mini__value" style={toneStyle}>{Number(value || 0).toLocaleString()}</div>
      <div className="kpi-mini__label">{label}</div>
    </div>
  );
}

// MiniStat renders a small labeled count used inside the catalog card.
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
    <ul className="dash-list">
      {alerts.slice(0, 6).map((a) => (
        <li key={a.id} className="dash-list__item" style={{ flexDirection: 'column', gap: 3 }}>
          <div className="row gap-sm row--between" style={{ width: '100%' }}>
            <SeverityBadge severity={a.severity} />
            <span className="dash-list__time">{formatTime(a.created_at || a.detected_at)}</span>
          </div>
          <div className="dash-list__msg">{a.message || a.alert_type || 'alert'}</div>
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

// --- Inline KPI icons (dependency-free, matching the sidebar icon style) ---

const ITEM_PROPS = {
  viewBox: '0 0 16 16',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: '1.5',
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
};

function RequestsIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <path d="M2 14V2M2 14h12" />
      <path d="M12 6l-4 4-2.5-2.5L3 10" />
    </svg>
  );
}

function TokensIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <circle cx="5" cy="8" r="3" />
      <path d="M5 5V3.5M5 12.5V11M3 6.5L1.8 5.8M7 6.5l1.2-.7M3 9.5l-1.2.7M7 9.5l1.2.7" />
      <path d="M9 8h5M9 6.5h3.5M9 9.5h3.5M13 11l1.5 1.5M14.5 11L13 12.5" />
    </svg>
  );
}

function CostIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <path d="M2 4.5h9v7H2z" />
      <path d="M4.5 4.5V3.5h8v7h-1.2" />
      <circle cx="6.5" cy="8" r="1.3" />
    </svg>
  );
}

function AlertsIcon() {
  return (
    <svg {...ITEM_PROPS}>
      <path d="M8 1.5l6 12.5H2L8 1.5z" />
      <path d="M8 6.5v3M8 11h0.01" />
    </svg>
  );
}
