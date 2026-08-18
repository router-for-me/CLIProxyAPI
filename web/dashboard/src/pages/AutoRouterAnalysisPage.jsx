import React, { useState, useMemo } from 'react';
import { listAutoRouters, getAutoRouterStats, getUsageFilterOptions } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState } from '../components/Primitives.jsx';
import { loadTimezone } from './usageShared.jsx';

// Time window options offered on this page (subset of the Usage Stats presets).
const RANGE_OPTIONS = [
  { label: 'Last 7d', days: 7 },
  { label: 'Last 30d', days: 30 },
];

const DEFAULT_LIMIT = 100;

// Canonical tier order — the backend always returns all 4 tiers, but keeping a
// fixed order here makes the cards render in the same sequence regardless of
// response ordering and lets us surface any tier the backend ever omits.
const TIERS = [
  { key: 'simple', label: 'Simple' },
  { key: 'medium', label: 'Medium' },
  { key: 'complex', label: 'Complex' },
  { key: 'reasoning', label: 'Reasoning' },
];

// Range state: selected preset + optional custom from/to. from/to sent to the
// server are always RFC3339 UTC (toISOString); the datetime-local inputs stay
// wall-clock local time.
function useRange(initialDays = 7) {
  const [preset, setPreset] = useState(initialDays);
  const [customFrom, setCustomFrom] = useState('');
  const [customTo, setCustomTo] = useState('');
  const [useCustomRange, setUseCustomRange] = useState(false);

  const range = useMemo(() => {
    if (useCustomRange) {
      const from = customFrom ? new Date(customFrom).toISOString() : '';
      const to = customTo ? new Date(customTo).toISOString() : '';
      if (from || to) return { from, to };
    }
    const now = new Date();
    const from = new Date(now);
    from.setDate(from.getDate() - preset);
    return { from: from.toISOString(), to: now.toISOString() };
  }, [preset, customFrom, customTo, useCustomRange]);

  return {
    range,
    preset,
    setPreset,
    customFrom,
    setCustomFrom,
    customTo,
    setCustomTo,
    useCustomRange,
    setUseCustomRange,
  };
}

export default function AutoRouterAnalysisPage() {
  const [routerId, setRouterId] = useState('');
  const [apiKeyId, setApiKeyId] = useState('');
  const rng = useRange(7);
  // Display timezone for the custom-range labels (shared with Usage Stats via
  // localStorage), so operators see that the inputs are local wall-clock.
  const [timezone] = useState(() => loadTimezone());

  // Router dropdown options (all auto routers; the CRUD list endpoint returns
  // id + name + display_name + model_id per router).
  const routers = useAsync(() => listAutoRouters({ page: 1, pageSize: 500 }), []);

  // API-key dropdown options — same source as the Usage Stats page so the
  // operator sees the same key identifiers (id + alias) in both surfaces.
  const filterOptions = useAsync(() => getUsageFilterOptions({}), []);

  // Shared query params for both stat requests.
  const params = useMemo(() => ({
    router_id: routerId || undefined,
    api_key_id: apiKeyId || undefined,
    from: rng.range.from,
    to: rng.range.to,
  }), [routerId, apiKeyId, rng.range.from, rng.range.to]);

  // Fetch both views only once a router is selected. The backend guarantees
  // tier stats always carry the 4 canonical tiers (zero-valued when empty);
  // model stats are ordered by cost descending server-side.
  const key = JSON.stringify(params);
  const tiers = useAsync(
    () => getAutoRouterStats({ ...params, top: 'tier' }),
    [key],
  );
  const models = useAsync(
    () => getAutoRouterStats({ ...params, top: 'model', limit: DEFAULT_LIMIT }),
    [key],
  );

  const routerOptions = routers.data?.auto_routers || [];
  const keyOptions = filterOptions.data?.api_keys || [];
  const tierRows = tiers.data?.tiers || [];
  const modelRows = models.data?.models || [];

  const hasSelection = routerId !== '';
  const totalRequests = tierRows.reduce((a, t) => a + Number(t.request_count || 0), 0);
  const totalCost = tierRows.reduce((a, t) => a + Number(t.cost_usd || 0), 0);
  // The backend always returns all 4 tier rows (zero-valued when a tier has no
  // requests), so "no data" is detected from the actual totals rather than the
  // row count: no requests across any tier and no model rows = empty range.
  const noData = hasSelection && totalRequests === 0 && modelRows.length === 0;

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Auto Router Analysis</h1>
          <div className="main__subtitle">
            Breakdown of requests routed through an auto router — volume, tokens,
            and cost per complexity tier, plus a cost-ranked view of the target
            models each tier forwarded to.
          </div>
        </div>
      </div>

      {/* Controls: router + API key + time range */}
      <div className="card">
        <div className="grid grid--3">
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Auto Router</label>
            <select
              value={routerId}
              onChange={(e) => setRouterId(e.target.value)}
              disabled={routers.loading}
            >
              <option value="">Select a router…</option>
              {routerOptions.map((r) => (
                <option key={r.id} value={r.model_id}>
                  {r.display_name || r.name} ({r.model_id})
                </option>
              ))}
            </select>
          </div>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">API Key</label>
            <select
              value={apiKeyId}
              onChange={(e) => setApiKeyId(e.target.value)}
              disabled={filterOptions.loading}
            >
              <option value="">All keys</option>
              {keyOptions.map((k) => (
                <option key={k.id} value={k.id}>{k.alias || k.id}</option>
              ))}
            </select>
          </div>
          <div className="form__row" style={{ marginBottom: 0 }}>
            <label className="form__label">Time range</label>
            <div className="row gap-sm" style={{ alignItems: 'center' }}>
              <select
                value={rng.preset}
                onChange={(e) => { rng.setPreset(Number(e.target.value)); rng.setUseCustomRange(false); }}
                disabled={rng.useCustomRange}
              >
                {RANGE_OPTIONS.map((r) => (
                  <option key={r.days} value={r.days}>{r.label}</option>
                ))}
              </select>
              <label className="row gap-sm" style={{ cursor: 'pointer' }}>
                <input
                  type="checkbox"
                  checked={rng.useCustomRange}
                  onChange={(e) => rng.setUseCustomRange(e.target.checked)}
                  style={{ width: 'auto' }}
                />
                <span className="form__label" style={{ margin: 0 }}>Custom</span>
              </label>
            </div>
            {rng.useCustomRange && (
              <div className="usage-toolbar__range" style={{ marginTop: 6 }}>
                <div className="row gap-sm">
                  <span className="usage-toolbar__range-label">From ({timezone})</span>
                  <input
                    type="datetime-local"
                    value={rng.customFrom}
                    onChange={(e) => rng.setCustomFrom(e.target.value)}
                  />
                </div>
                <div className="row gap-sm">
                  <span className="usage-toolbar__range-label">To ({timezone})</span>
                  <input
                    type="datetime-local"
                    value={rng.customTo}
                    onChange={(e) => rng.setCustomTo(e.target.value)}
                  />
                </div>
              </div>
            )}
          </div>
        </div>
      </div>

      {/* No router selected yet */}
      {!hasSelection && (
        <EmptyState
          title="Select a router and API key"
          hint="Choose an auto router above to see its per-tier request breakdown and per-model cost ranking."
        />
      )}

      {/* Range returned no data */}
      {hasSelection && !tiers.loading && !tiers.error && !models.loading && !models.error && noData && (
        <EmptyState title="No data for this range" />
      )}

      {/* View 1 — Requests per tier */}
      {hasSelection && !noData && (
        <div className="card" style={{ marginTop: 16 }}>
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Requests per tier</h3>
            <span className="dim" style={{ fontSize: 12 }}>
              {totalRequests.toLocaleString()} req · ${totalCost.toFixed(4)}
            </span>
          </div>
          {tiers.loading && <Spinner label="Loading tier stats…" />}
          {tiers.error && <ErrorBanner error={tiers.error} onRetry={tiers.reload} />}
          {!tiers.loading && !tiers.error && (
            <div className="stats-grid">
              {TIERS.map((t) => {
                const row = tierRows.find((r) => r.tier === t.key) || { request_count: 0, total_tokens: 0, cost_usd: 0 };
                return (
                  <div key={t.key} className="stat-card">
                    <div className="stat-card__label">{t.label}</div>
                    <div className="stat-card__value">{Number(row.request_count || 0).toLocaleString()}</div>
                    <div className="stat-card__hint">
                      {Number(row.total_tokens || 0).toLocaleString()} tokens · ${Number(row.cost_usd || 0).toFixed(4)}
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </div>
      )}

      {/* View 2 — Cost per target model */}
      {hasSelection && !noData && (
        <div className="card" style={{ marginTop: 16, padding: 0 }}>
          <div className="row row--between" style={{ padding: '14px 16px 8px' }}>
            <h3 className="card__title" style={{ margin: 0 }}>Cost per target model</h3>
            <span className="dim" style={{ fontSize: 12 }}>top {DEFAULT_LIMIT} by cost</span>
          </div>
          {models.loading && <Spinner label="Loading model stats…" />}
          {models.error && <ErrorBanner error={models.error} onRetry={models.reload} />}
          {!models.loading && !models.error && modelRows.length === 0 && (
            <EmptyState title="No data for this range" />
          )}
          {!models.loading && !models.error && modelRows.length > 0 && (
            <div style={{ overflowX: 'auto' }}>
              <table className="table">
                <thead>
                  <tr>
                    <th>Target model</th>
                    <th style={{ textAlign: 'right' }}>Requests</th>
                    <th style={{ textAlign: 'right' }}>Total tokens</th>
                    <th style={{ textAlign: 'right' }}>Cost (USD)</th>
                    <th style={{ textAlign: 'right' }}>Avg cost / request</th>
                  </tr>
                </thead>
                <tbody>
                  {modelRows.map((m, i) => (
                    <tr key={`${m.model}-${i}`}>
                      <td className="mono">{m.model || '—'}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>{Number(m.request_count || 0).toLocaleString()}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>{Number(m.total_tokens || 0).toLocaleString()}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>${Number(m.cost_usd || 0).toFixed(4)}</td>
                      <td className="mono" style={{ textAlign: 'right' }}>${Number(m.avg_cost_per_request || 0).toFixed(4)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </>
  );
}
