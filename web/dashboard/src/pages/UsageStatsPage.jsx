import React, { useState, useMemo, useCallback } from 'react';
import {
  getUsageTotals, getUsageTimeSeries, getUsageTop, getUsageFilterOptions,
  getProviderPerformance,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import {
  Spinner, ErrorBanner, EmptyState,
} from '../components/Primitives.jsx';
import { Sparkline, BarChart, MultiBarChart, DonutChart, HorizontalBarChart } from '../components/Charts.jsx';
import { useToast } from '../components/Toast.jsx';
import {
  PRESETS, presetToRange, toUTCFromTZ,
  TIMEZONES, loadTimezone, saveTimezone,
  ChartSkeleton,
  FilterSelect,
} from './usageShared.jsx';

const AUTO_REFRESH_INTERVAL_MS = 60 * 1000;
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.usageAutorefresh';

// Palette for the donut chart segments (cost by model). Cycles for models
// beyond the first 8 entries.
const MODEL_COLORS = [
  'var(--accent)',
  '#a78bfa',
  '#38bdf8',
  '#f472b6',
  '#34d399',
  '#fb923c',
  '#e879f9',
  '#22d3ee',
];

// Provider color map for the cost-by-provider bars. Falls back to var(--accent)
// for providers not in this list.
const PROVIDER_COLORS = {
  openai: '#10a37f',
  anthropic: '#d97706',
  google: '#4285f4',
  gemini: '#4285f4',
  deepseek: '#4f46e5',
  groq: '#f97316',
  mistral: '#e11d48',
  cohere: '#0891b2',
};

function readAutoRefresh() {
  try { return localStorage.getItem(AUTOREFRESH_STORAGE) !== '0'; }
  catch { return true; }
}

export default function UsageStatsPage() {
  const toast = useToast();
  const [presetIdx, setPresetIdx] = useState(3); // "Last 24h" default
  const [filter, setFilter] = useState({
    api_key_id: '',
    provider: '',
    model: '',
    request_id: '',
    // from/to/interval are auto-derived from the preset unless the user
    // types custom values.
    customFrom: '',
    customTo: '',
    useCustomRange: false,
    interval: 'hour',
  });
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());
  // Selected display timezone. Defaults to Asia/Jakarta (see usageShared) and
  // persists across reloads + both Usage Stats and Errors pages. The server
  // always receives UTC from/to; this only affects how wall-clock columns and
  // the custom-range datetime-local inputs are interpreted/displayed.
  const [timezone, setTimezone] = useState(() => loadTimezone());

  function changeTimezone(tz) {
    setTimezone(tz);
    saveTimezone(tz);
  }

  const rangeParams = useMemo(() => {
    if (filter.useCustomRange && (filter.customFrom || filter.customTo)) {
      return {
        from: filter.customFrom ? toUTCFromTZ(filter.customFrom, timezone) : undefined,
        to: filter.customTo ? toUTCFromTZ(filter.customTo, timezone) : undefined,
        interval: filter.interval || 'hour',
      };
    }
    return presetToRange(PRESETS[presetIdx]);
  }, [presetIdx, filter.useCustomRange, filter.customFrom, filter.customTo, filter.interval, timezone]);

  const baseFilter = useMemo(() => ({
    api_key_id: filter.api_key_id || undefined,
    provider: filter.provider || undefined,
    model: filter.model || undefined,
    request_id: filter.request_id.trim() || undefined,
    from: rangeParams.from,
    to: rangeParams.to,
  }), [filter.api_key_id, filter.provider, filter.model, filter.request_id, rangeParams.from, rangeParams.to]);

  const totals = useAsync(() => getUsageTotals(baseFilter), [JSON.stringify(baseFilter)]);
  const ts = useAsync(() => getUsageTimeSeries(baseFilter, rangeParams.interval), [JSON.stringify(baseFilter), rangeParams.interval]);
  const topModels = useAsync(() => getUsageTop({ dimension: 'model', metric: 'cost_usd', limit: 10, ...baseFilter }), [JSON.stringify(baseFilter)]);
  const topKeys = useAsync(() => getUsageTop({ dimension: 'api_key_id', metric: 'request_count', limit: 10, ...baseFilter }), [JSON.stringify(baseFilter)]);
  const topProviders = useAsync(() => getUsageTop({ dimension: 'provider', metric: 'request_count', limit: 10, ...baseFilter }), [JSON.stringify(baseFilter)]);
  const topCostProviders = useAsync(() => getUsageTop({ dimension: 'provider', metric: 'cost_usd', limit: 10, ...baseFilter }), [JSON.stringify(baseFilter)]);
  const providerPerf = useAsync(() => getProviderPerformance(baseFilter), [JSON.stringify(baseFilter)]);

  // Filter dropdown options. Refetched whenever the time range changes so the
  // operator sees only values that actually appear in the window.
  const filterOptions = useAsync(
    () => getUsageFilterOptions(baseFilter),
    [JSON.stringify({ from: baseFilter.from, to: baseFilter.to })],
  );

  const reloadAll = useCallback(() => {
    totals.reload();
    ts.reload();
    topModels.reload();
    topKeys.reload();
    topProviders.reload();
    topCostProviders.reload();
    providerPerf.reload();
    filterOptions.reload();
  }, [totals, ts, topModels, topKeys, topProviders, topCostProviders, providerPerf, filterOptions]);

  // Auto-refresh: re-fetch every metric every 60s while the page is visible
  // and the toggle is on. Mirrors the Models Catalog auto-refresh pattern.
  useAutoRefresh(reloadAll, AUTO_REFRESH_INTERVAL_MS, autoRefresh);

  function handleRefresh() {
    reloadAll();
    toast.info('Stats refreshed');
  }

  function toggleAutoRefresh() {
    setAutoRefresh((v) => {
      const next = !v;
      try { localStorage.setItem(AUTOREFRESH_STORAGE, next ? '1' : '0'); } catch { /* ignore */ }
      return next;
    });
  }

  function updateFilter(partial) {
    setFilter((f) => ({ ...f, ...partial }));
  }

  const tsPoints = ts.data?.points || [];
  const tsData = useMemo(() => tsPoints.map((p) => ({
    label: p.bucket,
    value: p.request_count,
    a: p.input_tokens,
    b: p.output_tokens,
    cost: p.cost_usd,
  })), [tsPoints]);

  const totalTokens = totals.data?.totals?.total_tokens || 0;
  const failureRate = totals.data?.failure_rate ?? 0;
  const reqCount = totals.data?.totals?.request_count || 0;

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Usage Statistics</h1>
          <div className="main__subtitle">
            Aggregate request volume, tokens, cost, and per-key / per-model
            leaderboards scoped by dropdown-driven filters from the PG
            usage_events table. Per-request drill-down is on the Recent Events
            page.
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

      {/* Preset + filter bar */}
      <div className="card">
        <div className="usage-toolbar">
          <div className="seg-group" role="tablist" aria-label="Time window">
            {PRESETS.map((p, i) => (
              <button
                key={p.label}
                type="button"
                className={`seg-btn ${i === presetIdx && !filter.useCustomRange ? 'seg-btn--active' : ''}`}
                onClick={() => { setPresetIdx(i); updateFilter({ useCustomRange: false }); }}
              >
                {p.label}
              </button>
            ))}
          </div>
          <div className="catalog-toolbar__spacer" />
          <label className="row gap-sm" style={{ cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={filter.useCustomRange}
              onChange={(e) => updateFilter({ useCustomRange: e.target.checked })}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>Custom range</span>
          </label>
          <label className="row gap-sm" style={{ alignItems: 'center' }} title="Display timezone for the Time column and custom range inputs">
            <span className="form__label" style={{ margin: 0 }}>Timezone</span>
            <select
              value={timezone}
              onChange={(e) => changeTimezone(e.target.value)}
              style={{ width: 'auto' }}
            >
              {TIMEZONES.map((tz) => (
                <option key={tz} value={tz}>{tz}</option>
              ))}
            </select>
          </label>
        </div>
        <div className="grid grid--4">
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">API Key</label>
            <FilterSelect
              loading={filterOptions.loading}
              options={(filterOptions.data?.api_keys || []).map((k) => ({ value: k.id, label: k.alias || k.id }))}
              value={filter.api_key_id}
              onChange={(v) => updateFilter({ api_key_id: v })}
              anyLabel="any key"
            />
          </div>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Provider</label>
            <FilterSelect
              loading={filterOptions.loading}
              options={(filterOptions.data?.providers || []).map((p) => ({ value: p, label: p }))}
              value={filter.provider}
              onChange={(v) => updateFilter({ provider: v })}
              anyLabel="any provider"
            />
          </div>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Model</label>
            <FilterSelect
              loading={filterOptions.loading}
              options={(filterOptions.data?.models || []).map((m) => ({ value: m, label: m }))}
              value={filter.model}
              onChange={(v) => updateFilter({ model: v })}
              anyLabel="any model"
            />
          </div>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Interval</label>
            <select
              value={rangeParams.interval}
              onChange={(e) => updateFilter({ interval: e.target.value })}
              disabled={!filter.useCustomRange}
            >
              <option value="minute">minute</option>
              <option value="hour">hour</option>
              <option value="day">day</option>
            </select>
          </div>
        </div>
        <div className="form__row" style={{ marginBottom: 0, marginTop: 8 }}>
          <label className="form__label">Request ID</label>
          <input
            type="text"
            className="search-input"
            placeholder="filter by request id (exact match)"
            value={filter.request_id}
            onChange={(e) => updateFilter({ request_id: e.target.value })}
          />
        </div>
        {filter.useCustomRange && (
          <div className="usage-toolbar__range">
            <div className="row gap-sm">
              <span className="usage-toolbar__range-label">From ({timezone})</span>
              <input type="datetime-local" value={filter.customFrom} onChange={(e) => updateFilter({ customFrom: e.target.value })} />
            </div>
            <div className="row gap-sm">
              <span className="usage-toolbar__range-label">To ({timezone})</span>
              <input type="datetime-local" value={filter.customTo} onChange={(e) => updateFilter({ customTo: e.target.value })} />
            </div>
          </div>
        )}
      </div>

      {/* KPI cards */}
      <div className="stats-grid">
        <div className="stat-card">
          <div className="stat-card__label">Total Requests</div>
          <div className="stat-card__value">{reqCount.toLocaleString()}</div>
          <div className="stat-card__hint">
            {(totals.data?.totals?.failed_count || 0).toLocaleString()} failed ({failureRate.toFixed(1)}%)
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Total Tokens</div>
          <div className="stat-card__value">{totalTokens.toLocaleString()}</div>
          <div className="stat-card__hint">
            in {(totals.data?.totals?.input_tokens || 0).toLocaleString()} · out {(totals.data?.totals?.output_tokens || 0).toLocaleString()}
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Total Cost</div>
          <div className="stat-card__value">${(totals.data?.totals?.cost_usd || 0).toFixed(4)}</div>
          <div className="stat-card__hint">
            avg ${reqCount > 0 ? ((totals.data?.totals?.cost_usd || 0) / reqCount).toFixed(4) : '0.0000'}/req
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Active Models</div>
          <div className="stat-card__value">{String(topModels.data?.entries?.length || 0)}</div>
          <div className="stat-card__hint">{topProviders.data?.entries?.length || 0} providers</div>
        </div>
      </div>

      {totals.error && <ErrorBanner error={totals.error} onRetry={totals.reload} />}

      {/* Time-series charts */}
      <div className="grid grid--2" style={{ marginTop: 16 }}>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Request volume</h3>
            <span className="dim" style={{ fontSize: 12 }}>{rangeParams.interval}</span>
          </div>
          {ts.loading && <ChartSkeleton />}
          {!ts.loading && tsData.length > 0 && (
            <BarChart data={tsData} />
          )}
          {!ts.loading && tsData.length === 0 && (
            <EmptyState title="No data for this window" />
          )}
        </div>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Token usage</h3>
            <span className="row gap-sm" style={{ fontSize: 11 }}>
              <span className="tokbar__legend-dot" style={{ background: 'var(--accent)' }} /> input
              <span className="tokbar__legend-dot" style={{ background: 'var(--warning)', marginLeft: 6 }} /> output
            </span>
          </div>
          {ts.loading && <ChartSkeleton />}
          {!ts.loading && tsData.length > 0 && (
            <MultiBarChart data={tsData} />
          )}
          {!ts.loading && tsData.length === 0 && (
            <EmptyState title="No data for this window" />
          )}
        </div>
      </div>

      <div className="card">
        <div className="row row--between" style={{ marginBottom: 12 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Cost trend</h3>
          <span className="dim" style={{ fontSize: 12 }}>{rangeParams.interval}</span>
        </div>
        {ts.loading && <ChartSkeleton />}
        {!ts.loading && tsData.length > 0 && (
          <Sparkline values={tsData.map((d) => d.cost)} />
        )}
        {!ts.loading && tsData.length === 0 && (
          <EmptyState title="No cost data for this window" />
        )}
      </div>

      {/* Advanced charts row 1: donut + latency */}
      <div className="grid grid--2" style={{ marginTop: 16 }}>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Cost by model</h3>
          </div>
          {topModels.loading && <ChartSkeleton />}
          {!topModels.loading && topModels.error && <ErrorBanner error={topModels.error} />}
          {!topModels.loading && !topModels.error && (
            <DonutChart
              segments={(topModels.data?.entries || []).map((e, i) => ({
                label: e.key || '—',
                value: Number(e.cost_usd || 0),
                color: MODEL_COLORS[i % MODEL_COLORS.length],
              }))}
            />
          )}
        </div>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Provider latency</h3>
          </div>
          {providerPerf.loading && <ChartSkeleton />}
          {!providerPerf.loading && providerPerf.error && <ErrorBanner error={providerPerf.error} />}
          {!providerPerf.loading && !providerPerf.error && providerPerf.data && providerPerf.data.length > 0 && (
            <HorizontalBarChart
              data={providerPerf.data.map((p) => ({
                label: p.official_provider || p.provider,
                value: p.avg_latency_ms,
                color: 'var(--warning)',
              }))}
            />
          )}
          {!providerPerf.loading && !providerPerf.error && (!providerPerf.data || providerPerf.data.length === 0) && (
            <EmptyState title="No latency data" />
          )}
          {providerPerf.data && providerPerf.data.length > 0 && (
            <div className="dim" style={{ fontSize: 11, marginTop: 8, textAlign: 'right' }}>
              avg latency ms
            </div>
          )}
        </div>
      </div>

      {/* Advanced charts row 2: cost by provider + daily cost */}
      <div className="grid grid--2" style={{ marginTop: 16 }}>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Cost by provider</h3>
          </div>
          {topCostProviders.loading && <ChartSkeleton />}
          {!topCostProviders.loading && topCostProviders.error && <ErrorBanner error={topCostProviders.error} />}
          {!topCostProviders.loading && !topCostProviders.error && topCostProviders.data?.entries?.length > 0 && (
            <HorizontalBarChart
              data={topCostProviders.data.entries.map((e) => ({
                label: e.key || '—',
                value: Number(e.cost_usd || 0),
                color: PROVIDER_COLORS[e.key] || 'var(--accent)',
              }))}
            />
          )}
          {!topCostProviders.loading && !topCostProviders.error && (!topCostProviders.data?.entries || topCostProviders.data.entries.length === 0) && (
            <EmptyState title="No cost data for this window" />
          )}
        </div>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Daily cost</h3>
            <span className="dim" style={{ fontSize: 12 }}>day</span>
          </div>
          {ts.loading && <ChartSkeleton />}
          {!ts.loading && tsData.length > 0 && (
            <BarChart data={tsData.map((d) => ({ label: d.label, value: d.cost }))} color="var(--success)" />
          )}
          {!ts.loading && tsData.length === 0 && (
            <EmptyState title="No cost data" />
          )}
        </div>
      </div>

      {/* Leaderboards */}
      <div className="grid grid--3" style={{ marginTop: 16 }}>
        <LeaderboardCard
          title="Top models by cost"
          entries={topModels.data?.entries}
          loading={topModels.loading}
          error={topModels.error}
          onReload={topModels.reload}
          metric="cost_usd"
        />
        <LeaderboardCard
          title="Top API keys by requests"
          entries={topKeys.data?.entries}
          loading={topKeys.loading}
          error={topKeys.error}
          onReload={topKeys.reload}
          metric="request_count"
        />
        <LeaderboardCard
          title="Top providers"
          entries={topProviders.data?.entries}
          loading={topProviders.loading}
          error={topProviders.error}
          onReload={topProviders.reload}
          metric="request_count"
        />
      </div>

      {/* The per-request event log now lives on its own Recent Events page
          (sidebar: Analysis → Recent Events). Failed attempts are on the
          Errors page. */}
    </>
  );
}

function LeaderboardCard({ title, entries, loading, error, onReload, metric }) {
  const fmt = (v) => {
    if (metric === 'cost_usd') return `$${Number(v).toFixed(4)}`;
    return Number(v).toLocaleString();
  };
  const max = useMemo(() => {
    if (!entries || entries.length === 0) return 1;
    return Math.max(...entries.map((e) => e[metric === 'cost_usd' ? 'cost_usd' : metric]), 1);
  }, [entries, metric]);
  const valueKey = metric === 'cost_usd' ? 'cost_usd' : metric;
  return (
    <div className="card" style={{ padding: 0 }}>
      <div className="row row--between" style={{ padding: '14px 16px 8px' }}>
        <h3 className="card__title" style={{ margin: 0 }}>{title}</h3>
        <button onClick={onReload} style={{ padding: '3px 8px', fontSize: 12 }} title="Reload">↻</button>
      </div>
      {loading && <Spinner label="Loading…" />}
      {error && <ErrorBanner error={error} />}
      {!loading && !error && entries && entries.length === 0 && (
        <EmptyState title="No data" />
      )}
      {!loading && !error && entries && entries.length > 0 && (
        <table className="table">
          <thead>
            <tr>
              <th>Key</th>
              <th style={{ textAlign: 'right' }}>{metricLabel(metric)}</th>
              <th style={{ width: '40%' }}></th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e, i) => (
              <tr key={`${e.key}-${i}`}>
                <td className="mono">{e.key || '—'}</td>
                <td className="mono" style={{ textAlign: 'right' }}>{fmt(e[valueKey])}</td>
                <td>
                  <div className="lb-bar">
                    <div
                      className="lb-bar__fill"
                      style={{ width: `${(e[valueKey] / max * 100)}%` }}
                    />
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function metricLabel(metric) {
  if (metric === 'cost_usd') return 'Cost (USD)';
  if (metric === 'total_tokens') return 'Tokens';
  return 'Requests';
}
