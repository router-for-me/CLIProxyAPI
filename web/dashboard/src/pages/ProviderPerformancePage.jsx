import React, { useState, useMemo, useCallback } from 'react';
import { getProviderPerformance, getUsageFilterOptions } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { Spinner, ErrorBanner, EmptyState } from '../components/Primitives.jsx';
import { useToast } from '../components/Toast.jsx';
import { PRESETS, presetToRange, FilterSelect } from './usageShared.jsx';

const AUTO_REFRESH_INTERVAL_MS = 60 * 1000;

function fmtMs(v) {
  const n = Number(v || 0);
  return n >= 1000 ? `${(n / 1000).toFixed(2)}s` : `${Math.round(n)}ms`;
}

function fmtNum(v) {
  return Number(v || 0).toLocaleString();
}

function fmtRate(v) {
  return Number(v || 0).toFixed(1);
}

function fmtPct(v) {
  return `${(Number(v || 0) * 100).toFixed(1)}%`;
}

function fmtUSD(v) {
  return `$${Number(v || 0).toFixed(4)}`;
}

// ProviderPerformancePage surfaces per-upstream-provider latency and
// throughput derived from usage_events. The default view groups by provider;
// clicking a row drills down into that provider's served models. Metrics are
// requested with a bounded time window (default Last 24h) so the underlying
// read never competes with the proxy's request path.
export default function ProviderPerformancePage() {
  const toast = useToast();
  const [presetIdx, setPresetIdx] = useState(3); // "Last 24h"
  const [groupBy, setGroupBy] = useState('provider');
  const [drillProvider, setDrillProvider] = useState('');
  const [filter, setFilter] = useState({ provider: '', model: '' });
  const [autoRefresh, setAutoRefresh] = useState(true);

  const rangeParams = useMemo(() => presetToRange(PRESETS[presetIdx]), [presetIdx]);

  const baseFilter = useMemo(() => ({
    provider: drillProvider || filter.provider || undefined,
    model: filter.model || undefined,
    from: rangeParams.from,
    to: rangeParams.to,
  }), [drillProvider, filter.provider, filter.model, rangeParams.from, rangeParams.to]);

  const perf = useAsync(
    () => getProviderPerformance({ ...baseFilter, groupBy, limit: 200 }),
    [JSON.stringify(baseFilter), groupBy],
  );

  const filterOptions = useAsync(
    () => getUsageFilterOptions({ from: rangeParams.from, to: rangeParams.to }),
    [rangeParams.from, rangeParams.to],
  );

  const reload = useCallback(() => {
    perf.reload();
    filterOptions.reload();
  }, [perf, filterOptions]);

  useAutoRefresh(reload, AUTO_REFRESH_INTERVAL_MS, autoRefresh);

  const entries = perf.data?.entries || [];

  const summary = useMemo(() => {
    let totalReq = 0;
    let totalErr = 0;
    let totalOut = 0;
    let totalCost = 0;
    for (const e of entries) {
      totalReq += Number(e.request_count || 0);
      totalErr += Number(e.error_count || 0);
      totalOut += Number(e.output_tokens || 0);
      totalCost += Number(e.cost_usd || 0);
    }
    const attempts = totalReq + totalErr;
    return {
      totalReq,
      totalErr,
      totalOut,
      totalCost,
      errorRate: attempts > 0 ? totalErr / attempts : 0,
    };
  }, [entries]);

  function handleRowClick(e) {
    if (groupBy !== 'provider') return;
    setDrillProvider(e.provider);
    setGroupBy('model');
    setFilter((f) => ({ ...f, model: '' }));
  }

  function backToProviders() {
    setDrillProvider('');
    setGroupBy('provider');
  }

  function handleRefresh() {
    reload();
    toast.info('Provider performance refreshed');
  }

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Provider Performance</h1>
          <div className="main__subtitle">
            Average and p50/p95 request (latency) and response (TTFT) times,
            token generation speed (tokens/sec), request throughput (req/sec),
            and error rate per upstream provider. Click a provider to break its
            numbers down by served model.
          </div>
        </div>
        <div className="row gap-sm">
          <button
            className={`autorefresh-chip ${autoRefresh ? '' : 'autorefresh-chip--off'}`}
            onClick={() => setAutoRefresh((v) => !v)}
            title={autoRefresh ? 'Auto-refresh every 60s — click to pause' : 'Auto-refresh paused — click to resume'}
          >
            <span className="autorefresh-chip__dot" />
            {autoRefresh ? 'Live' : 'Paused'}
          </button>
          <button onClick={handleRefresh}>Refresh</button>
        </div>
      </div>

      <div className="card">
        <div className="usage-toolbar">
          <div className="seg-group" role="tablist" aria-label="Time window">
            {PRESETS.map((p, i) => (
              <button
                key={p.label}
                type="button"
                className={`seg-btn ${i === presetIdx ? 'seg-btn--active' : ''}`}
                onClick={() => setPresetIdx(i)}
              >
                {p.label}
              </button>
            ))}
          </div>
          <div className="catalog-toolbar__spacer" />
          {drillProvider ? (
            <button type="button" className="btn btn--ghost" onClick={backToProviders}>
              ← All providers
            </button>
          ) : null}
        </div>
        <div className="grid grid--3" style={{ marginTop: 8 }}>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Provider</label>
            <FilterSelect
              loading={filterOptions.loading}
              options={(filterOptions.data?.providers || []).map((p) => ({ value: p, label: p }))}
              value={drillProvider || filter.provider}
              onChange={(v) => {
                if (drillProvider) {
                  setDrillProvider(v);
                } else {
                  setFilter((f) => ({ ...f, provider: v }));
                }
              }}
              anyLabel="any provider"
            />
          </div>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Model</label>
            <FilterSelect
              loading={filterOptions.loading}
              options={(filterOptions.data?.models || []).map((m) => ({ value: m, label: m }))}
              value={filter.model}
              onChange={(v) => setFilter((f) => ({ ...f, model: v }))}
              anyLabel="any model"
            />
          </div>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Group by</label>
            <select value={groupBy} onChange={(e) => setGroupBy(e.target.value)}>
              <option value="provider">provider</option>
              <option value="model">model</option>
            </select>
          </div>
        </div>
      </div>

      <div className="stats-grid">
        <div className="stat-card">
          <div className="stat-card__label">Requests</div>
          <div className="stat-card__value">{fmtNum(summary.totalReq)}</div>
          <div className="stat-card__hint">
            {fmtNum(summary.totalErr)} failed ({fmtPct(summary.errorRate)})
          </div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Output Tokens</div>
          <div className="stat-card__value">{fmtNum(summary.totalOut)}</div>
          <div className="stat-card__hint">{entries.length} {groupBy === 'provider' ? 'providers' : 'models'}</div>
        </div>
        <div className="stat-card">
          <div className="stat-card__label">Total Cost</div>
          <div className="stat-card__value">{fmtUSD(summary.totalCost)}</div>
          <div className="stat-card__hint">
            avg {fmtUSD(summary.totalReq > 0 ? summary.totalCost / summary.totalReq : 0)}/req
          </div>
        </div>
      </div>

      {perf.error && <ErrorBanner error={perf.error} onRetry={perf.reload} />}

      <div className="card" style={{ padding: 0 }}>
        <div className="row row--between" style={{ padding: '14px 16px 8px' }}>
          <h3 className="card__title" style={{ margin: 0 }}>
            {drillProvider ? `${drillProvider} — by model` : 'By provider'}
          </h3>
          <span className="dim" style={{ fontSize: 12 }}>
            {groupBy === 'provider' ? 'click a row to drill into models' : 'request/response times and throughput'}
          </span>
        </div>
        {perf.loading && <Spinner label="Loading…" />}
        {!perf.loading && !perf.error && entries.length === 0 && (
          <EmptyState title="No requests in this window" hint="Try widening the time range or clearing filters." />
        )}
        {!perf.loading && !perf.error && entries.length > 0 && (
          <div style={{ overflowX: 'auto' }}>
            <table className="table">
              <thead>
                <tr>
                  <th>{groupBy === 'provider' ? 'Provider' : 'Model'}</th>
                  <th style={{ textAlign: 'right' }}>Requests</th>
                  <th style={{ textAlign: 'right' }}>Errors</th>
                  <th style={{ textAlign: 'right' }}>Avg lat</th>
                  <th style={{ textAlign: 'right' }}>p50 lat</th>
                  <th style={{ textAlign: 'right' }}>p95 lat</th>
                  <th style={{ textAlign: 'right' }}>Avg TTFT</th>
                  <th style={{ textAlign: 'right' }}>p95 TTFT</th>
                  <th style={{ textAlign: 'right' }}>Tok/s</th>
                  <th style={{ textAlign: 'right' }}>Req/s</th>
                  <th style={{ textAlign: 'right' }}>Cost</th>
                </tr>
              </thead>
              <tbody>
                {entries.map((e, i) => (
                  <tr
                    key={`${e.provider || e.model}-${i}`}
                    onClick={() => handleRowClick(e)}
                    style={groupBy === 'provider' ? { cursor: 'pointer' } : undefined}
                    title={groupBy === 'provider' ? 'Click to drill into models' : undefined}
                  >
                    <td className="mono">{e.provider || e.model || '—'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtNum(e.request_count)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>
                      {fmtNum(e.error_count)} {e.error_count > 0 ? <span className="dim">({fmtPct(e.error_rate)})</span> : null}
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtMs(e.avg_latency_ms)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtMs(e.p50_latency_ms)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtMs(e.p95_latency_ms)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtMs(e.avg_ttft_ms)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtMs(e.p95_ttft_ms)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtRate(e.tokens_per_second)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtRate(e.requests_per_second)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtUSD(e.cost_usd)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}