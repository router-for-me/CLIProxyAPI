import React, { useState, useMemo, useEffect, useCallback } from 'react';
import {
  getUsageTotals, getUsageTimeSeries, getUsageTop, getUsageStats,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import {
  Spinner, ErrorBanner, EmptyState, Stat,
} from '../components/Primitives.jsx';
import { Sparkline, BarChart, MultiBarChart } from '../components/Charts.jsx';

// Preset time windows the dashboard offers at the top of the page. Each
// preset computes from/to in UTC using relative offsets from now.
const PRESETS = [
  { label: 'Last 15m', duration: 15, unit: 'minute', interval: 'minute' },
  { label: 'Last 1h', duration: 1, unit: 'hour', interval: 'minute' },
  { label: 'Last 6h', duration: 6, unit: 'hour', interval: 'hour' },
  { label: 'Last 24h', duration: 24, unit: 'hour', interval: 'hour' },
  { label: 'Last 7d', duration: 7, unit: 'day', interval: 'day' },
  { label: 'Last 30d', duration: 30, unit: 'day', interval: 'day' },
];

function presetToRange(preset) {
  const now = new Date();
  const from = new Date(now);
  if (preset.unit === 'minute') from.setMinutes(from.getMinutes() - preset.duration);
  if (preset.unit === 'hour') from.setHours(from.getHours() - preset.duration);
  if (preset.unit === 'day') from.setDate(from.getDate() - preset.duration);
  return { from: from.toISOString(), to: now.toISOString(), interval: preset.interval };
}

export default function UsageStatsPage() {
  const [presetIdx, setPresetIdx] = useState(3); // "Last 24h" default
  const [filter, setFilter] = useState({
    api_key_id: '',
    provider: '',
    model: '',
    // from/to/interval are auto-derived from the preset unless the user
    // types custom values.
    customFrom: '',
    customTo: '',
    useCustomRange: false,
  });

  const rangeParams = useMemo(() => {
    if (filter.useCustomRange && (filter.customFrom || filter.customTo)) {
      return {
        from: filter.customFrom ? toUTC(filter.customFrom) : undefined,
        to: filter.customTo ? toUTC(filter.customTo) : undefined,
        interval: 'hour',
      };
    }
    return presetToRange(PRESETS[presetIdx]);
  }, [presetIdx, filter.useCustomRange, filter.customFrom, filter.customTo]);

  const baseFilter = useMemo(() => ({
    api_key_id: filter.api_key_id || undefined,
    provider: filter.provider || undefined,
    model: filter.model || undefined,
    from: rangeParams.from,
    to: rangeParams.to,
  }), [filter.api_key_id, filter.provider, filter.model, rangeParams.from, rangeParams.to]);

  const totals = useAsync(() => getUsageTotals(baseFilter), [JSON.stringify(baseFilter)]);
  const ts = useAsync(() => getUsageTimeSeries(baseFilter, rangeParams.interval), [JSON.stringify(baseFilter), rangeParams.interval]);
  const topModels = useAsync(() => getUsageTop({ dimension: 'model', metric: 'cost_usd', limit: 10, ...baseFilter }), [JSON.stringify(baseFilter)]);
  const topKeys = useAsync(() => getUsageTop({ dimension: 'api_key_id', metric: 'request_count', limit: 10, ...baseFilter }), [JSON.stringify(baseFilter)]);
  const topProviders = useAsync(() => getUsageTop({ dimension: 'provider', metric: 'request_count', limit: 10, ...baseFilter }), [JSON.stringify(baseFilter)]);

  const reloadAll = useCallback(() => {
    totals.reload();
    ts.reload();
    topModels.reload();
    topKeys.reload();
    topProviders.reload();
  }, [totals, ts, topModels, topKeys, topProviders]);

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

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Usage Statistics</h1>
          <div className="main__subtitle">
            Aggregate request volume, tokens, cost, and per-key / per-model
            leaderboards from the PG usage_events table.
          </div>
        </div>
        <button onClick={reloadAll}>Refresh</button>
      </div>

      {/* Preset + filter bar */}
      <div className="card">
        <div className="row gap-sm" style={{ flexWrap: 'wrap', marginBottom: 12 }}>
          {PRESETS.map((p, i) => (
            <button
              key={p.label}
              onClick={() => { setPresetIdx(i); updateFilter({ useCustomRange: false }); }}
              className={i === presetIdx && !filter.useCustomRange ? 'primary' : ''}
              style={{ padding: '4px 10px', fontSize: 12 }}
            >
              {p.label}
            </button>
          ))}
          <label className="row gap-sm" style={{ cursor: 'pointer', marginLeft: 'auto' }}>
            <input
              type="checkbox"
              checked={filter.useCustomRange}
              onChange={(e) => updateFilter({ useCustomRange: e.target.checked })}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>Custom range</span>
          </label>
        </div>
        <div className="grid grid--4">
          <div className="form__row">
            <label className="form__label">API Key ID</label>
            <input type="text" value={filter.api_key_id} onChange={(e) => updateFilter({ api_key_id: e.target.value })} placeholder="any" />
          </div>
          <div className="form__row">
            <label className="form__label">Provider</label>
            <input type="text" value={filter.provider} onChange={(e) => updateFilter({ provider: e.target.value })} placeholder="any" />
          </div>
          <div className="form__row">
            <label className="form__label">Model</label>
            <input type="text" value={filter.model} onChange={(e) => updateFilter({ model: e.target.value })} placeholder="any" />
          </div>
          <div className="form__row">
            <label className="form__label">Interval</label>
            <select
              value={rangeParams.interval}
              onChange={(e) => {/* interval tied to preset; allow override via custom range */}}
              disabled={!filter.useCustomRange}
            >
              <option value="minute">minute</option>
              <option value="hour">hour</option>
              <option value="day">day</option>
            </select>
          </div>
        </div>
        {filter.useCustomRange && (
          <div className="grid grid--2" style={{ marginTop: 8 }}>
            <div className="form__row">
              <label className="form__label">From (UTC)</label>
              <input type="datetime-local" value={filter.customFrom} onChange={(e) => updateFilter({ customFrom: e.target.value })} />
            </div>
            <div className="form__row">
              <label className="form__label">To (UTC)</label>
              <input type="datetime-local" value={filter.customTo} onChange={(e) => updateFilter({ customTo: e.target.value })} />
            </div>
          </div>
        )}
      </div>

      {/* KPI cards */}
      <div className="grid grid--4">
        <Stat
          label="Total Requests"
          value={(totals.data?.totals?.request_count || 0).toLocaleString()}
          delta={`${(totals.data?.totals?.failed_count || 0).toLocaleString()} failed (${failureRate.toFixed(1)}%)`}
        />
        <Stat
          label="Total Tokens"
          value={totalTokens.toLocaleString()}
          delta={`in ${(totals.data?.totals?.input_tokens || 0).toLocaleString()} · out ${(totals.data?.totals?.output_tokens || 0).toLocaleString()}`}
        />
        <Stat
          label="Total Cost"
          value={`$${(totals.data?.totals?.cost_usd || 0).toFixed(4)}`}
          delta={`avg $${(totals.data?.totals?.request_count || 0) > 0 ? ((totals.data?.totals?.cost_usd || 0) / (totals.data?.totals?.request_count || 1)).toFixed(4) : '0.0000'}/req`}
        />
        <Stat
          label="Active Models"
          value={String(topModels.data?.entries?.length || 0)}
          delta={`${topProviders.data?.entries?.length || 0} providers`}
        />
      </div>

      {totals.error && <ErrorBanner error={totals.error} onRetry={totals.reload} />}

      {/* Time-series charts */}
      <div className="grid grid--2" style={{ marginTop: 16 }}>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Request volume</h3>
            <span className="dim" style={{ fontSize: 12 }}>{rangeParams.interval}</span>
          </div>
          {ts.loading && <Spinner label="Loading…" />}
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
            <span className="dim row gap-sm" style={{ fontSize: 11 }}>
              <span style={{ width: 10, height: 10, background: 'var(--accent)', display: 'inline-block', borderRadius: 2 }} /> input
              <span style={{ width: 10, height: 10, background: 'var(--warning)', display: 'inline-block', borderRadius: 2, marginLeft: 6 }} /> output
            </span>
          </div>
          {ts.loading && <Spinner label="Loading…" />}
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
        {ts.loading && <Spinner label="Loading…" />}
        {!ts.loading && tsData.length > 0 && (
          <Sparkline values={tsData.map((d) => d.cost)} />
        )}
        {!ts.loading && tsData.length === 0 && (
          <EmptyState title="No cost data for this window" />
        )}
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
  return (
    <div className="card" style={{ padding: 0 }}>
      <div className="row row--between" style={{ padding: '14px 16px 8px' }}>
        <h3 className="card__title" style={{ margin: 0 }}>{title}</h3>
        <button onClick={onReload} style={{ padding: '2px 8px', fontSize: 12 }}>↻</button>
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
                <td className="mono" style={{ textAlign: 'right' }}>{fmt(e[metric === 'cost_usd' ? 'cost_usd' : metric])}</td>
                <td>
                  <div style={{
                    height: 8,
                    background: 'var(--bg)',
                    borderRadius: 4,
                    overflow: 'hidden',
                  }}>
                    <div style={{
                      width: `${(e[metric === 'cost_usd' ? 'cost_usd' : metric] / max * 100)}%`,
                      height: '100%',
                      background: 'var(--accent)',
                      opacity: 0.7,
                    }} />
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

function toUTC(localValue) {
  if (!localValue) return '';
  // datetime-local input produces "2026-07-19T15:30"; assume the user means
  // local time. Convert to ISO UTC.
  const d = new Date(localValue);
  if (isNaN(d.getTime())) return '';
  return d.toISOString();
}
